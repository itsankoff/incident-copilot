package redact

import (
	"math"
	"regexp"
	"slices"
	"strings"
)

// vendoredRule is one provider rule from betterleaks (rules_gen.go, written by
// `make redact-rules`).
type vendoredRule struct {
	id       string
	re       string
	group    int      // the capture group holding the secret; 0 means the first non-empty group, else the whole match
	keywords []string // the rule runs only on text holding one, ignoring case
	// A match is not a secret when its entropy is at most minEntropy (below
	// it, when orEqual) or it matches a deny regex.
	minEntropy float64
	orEqual    bool
	deny       []string
}

type compiledRule struct {
	*vendoredRule
	re   *regexp.Regexp
	deny []*regexp.Regexp
}

var (
	rules      []compiledRule
	unkeyed    []int   // rules with no keywords, which always run
	keywordFor [][]int // keyword index to the rules it enables
	keywords   *keywordMatcher
	globalDeny []*regexp.Regexp
)

func init() {
	index := map[string]int{}
	var words []string
	for i := range vendoredRules {
		r := &vendoredRules[i]
		c := compiledRule{vendoredRule: r, re: regexp.MustCompile(r.re)}
		for _, d := range r.deny {
			c.deny = append(c.deny, regexp.MustCompile(d))
		}
		rules = append(rules, c)
		if len(r.keywords) == 0 {
			unkeyed = append(unkeyed, i)
		}
		for _, k := range r.keywords {
			k = strings.ToLower(k)
			j, ok := index[k]
			if !ok {
				j = len(words)
				index[k] = j
				words = append(words, k)
				keywordFor = append(keywordFor, nil)
			}
			keywordFor[j] = append(keywordFor[j], i)
		}
	}
	keywords = newKeywordMatcher(words)
	for _, d := range vendoredDeny {
		globalDeny = append(globalDeny, regexp.MustCompile(d))
	}
}

// redactVendored replaces each secret a vendored rule finds in s. Only rules
// whose keyword occurs in s run.
func redactVendored(s string) string {
	candidates := slices.Clone(unkeyed)
	keywords.each(s, func(k int) { candidates = append(candidates, keywordFor[k]...) })
	if len(candidates) == 0 {
		return s
	}
	slices.Sort(candidates)
	candidates = slices.Compact(candidates)

	type span struct{ start, end int }
	var spans []span
	for _, i := range candidates {
		r := &rules[i]
		for _, m := range r.re.FindAllStringSubmatchIndex(s, -1) {
			start, end := r.secret(m)
			if sec := s[start:end]; sec != "" && sec != Marker && r.isSecret(sec) {
				spans = append(spans, span{start, end})
			}
		}
	}
	if len(spans) == 0 {
		return s
	}
	slices.SortFunc(spans, func(a, b span) int { return a.start - b.start })
	var b strings.Builder
	last := 0
	for _, sp := range spans {
		if sp.end <= last { // inside a span already replaced
			continue
		}
		if sp.start >= last {
			b.WriteString(s[last:sp.start])
			b.WriteString(Marker)
		} // else it overlaps the previous span, which grows to cover it
		last = sp.end
	}
	b.WriteString(s[last:])
	return b.String()
}

// secret returns the bounds of the secret in match m, as betterleaks picks it.
func (r *compiledRule) secret(m []int) (start, end int) {
	if g := r.group; g > 0 {
		if 2*g+1 < len(m) && m[2*g] >= 0 {
			return m[2*g], m[2*g+1]
		}
		return m[0], m[1]
	}
	for g := 1; 2*g+1 < len(m); g++ {
		if m[2*g] >= 0 && m[2*g+1] > m[2*g] {
			return m[2*g], m[2*g+1]
		}
	}
	return m[0], m[1]
}

// isSecret applies the rule's filters, and the global one, to a match.
func (r *compiledRule) isSecret(sec string) bool {
	if r.minEntropy > 0 {
		if e := entropy(sec); e < r.minEntropy || e == r.minEntropy && !r.orEqual {
			return false
		}
	}
	for _, d := range r.deny {
		if d.MatchString(sec) {
			return false
		}
	}
	for _, d := range globalDeny {
		if d.MatchString(sec) {
			return false
		}
	}
	return true
}

// entropy is the Shannon entropy of s's bytes, in bits.
func entropy(s string) float64 {
	var counts [256]int
	for i := 0; i < len(s); i++ {
		counts[s[i]]++
	}
	var h float64
	n := float64(len(s))
	for _, c := range counts {
		if c > 0 {
			p := float64(c) / n
			h -= p * math.Log2(p)
		}
	}
	return h
}

// keywordMatcher finds keywords in text in one pass, ignoring ASCII case: an
// Aho-Corasick automaton over only the bytes keywords use (class 0 stands for
// every other byte), which keeps its table small.
type keywordMatcher struct {
	class   [256]byte
	classes int
	next    []int32 // next[state*classes+class] is the state after a byte
	out     [][]int // the keywords that end at each state
}

func newKeywordMatcher(words []string) *keywordMatcher {
	m := &keywordMatcher{classes: 1}
	for _, w := range words {
		for i := 0; i < len(w); i++ {
			if c := w[i]; m.class[c] == 0 {
				m.class[c] = byte(m.classes)
				m.classes++
			}
		}
	}
	for c := 'a'; c <= 'z'; c++ {
		m.class[c-'a'+'A'] = m.class[c]
	}
	n := int32(m.classes)
	newState := func() int32 {
		for range n {
			m.next = append(m.next, -1)
		}
		m.out = append(m.out, nil)
		return int32(len(m.out) - 1)
	}
	newState() // the root
	for wi, w := range words {
		st := int32(0)
		for i := 0; i < len(w); i++ {
			t := st*n + int32(m.class[w[i]])
			if m.next[t] < 0 {
				m.next[t] = newState()
			}
			st = m.next[t]
		}
		m.out[st] = append(m.out[st], wi)
	}
	// Breadth-first, point each missing transition where the longest suffix
	// would go, and inherit the keywords that end at the fallback state.
	fail := make([]int32, len(m.out))
	var queue []int32
	for c := range n {
		if s := m.next[c]; s <= 0 {
			m.next[c] = 0
		} else {
			queue = append(queue, s)
		}
	}
	for len(queue) > 0 {
		st := queue[0]
		queue = queue[1:]
		for c := range n {
			t := st*n + c
			s := m.next[t]
			if s < 0 {
				m.next[t] = m.next[fail[st]*n+c]
				continue
			}
			fail[s] = m.next[fail[st]*n+c]
			m.out[s] = append(m.out[s], m.out[fail[s]]...)
			queue = append(queue, s)
		}
	}
	return m
}

// each calls f with the index of every keyword occurrence in s.
func (m *keywordMatcher) each(s string, f func(int)) {
	st := int32(0)
	n := int32(m.classes)
	for i := 0; i < len(s); i++ {
		st = m.next[st*n+int32(m.class[s[i]])]
		for _, w := range m.out[st] {
			f(w)
		}
	}
}
