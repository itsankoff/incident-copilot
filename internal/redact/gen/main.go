// Command gen writes internal/redact/rules_gen.go from betterleaks' default
// config (config/betterleaks.toml) at a pinned version. Run it through
// `make redact-rules`; a version bump is a reviewed change to that file.
//
// It is its own module so its TOML parser stays out of incident-copilot's
// dependencies.
package main

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"go/format"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"
)

const module = "github.com/betterleaks/betterleaks"

// globalFilter names betterleaks' top-level filter in overrides.
const globalFilter = "(global filter)"

// overrides fix upstream: each replaces old, which must occur exactly once,
// with new in a rule's regex or in the global filter. Generation fails when a
// rule or its old text is gone, so a version bump revisits every fix.
var overrides = []struct{ id, old, new, why string }{
	{globalFilter, "`(?i)^true|false|null$`", "`(?i)^(?:true|false|null)$`", `the ungrouped alternation drops every secret holding "false"`},
	{"openai-api-key", `[\s;]`, `[\s;.,]`, "OpenAI's own 401 text puts a '.' right after the key"},
}

func main() {
	version := flag.String("version", "", "betterleaks version, such as v1.10.0")
	commit := flag.String("commit", "", "the upstream commit that version must be")
	out := flag.String("out", "", "file to write")
	flag.Parse()
	if *version == "" || *commit == "" || *out == "" {
		fail(fmt.Errorf("usage: gen -version <betterleaks version> -commit <its commit> -out <rules_gen.go>"))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	zipPath, err := download(ctx, *version, *commit)
	if err != nil {
		fail(err)
	}
	cfg, licence, err := readModule(zipPath, *version)
	if err != nil {
		fail(err)
	}
	src, err := generate(cfg, licence, *version, *commit)
	if err != nil {
		fail(err)
	}
	if err := os.WriteFile(*out, src, 0o644); err != nil {
		fail(err)
	}
}

func fail(err error) {
	slog.Error(err.Error())
	os.Exit(1)
}

// download fetches the module zip through the Go module proxy, which checks
// it against sum.golang.org, checks that it was cut from commit, and returns
// its path. betterleaks needs a newer Go than this repo, so the go command
// refuses to extract the zip, but it has downloaded and verified it by then.
func download(ctx context.Context, version, commit string) (string, error) {
	cmd := exec.CommandContext(ctx, "go", "mod", "download", "-json", module+"@"+version)
	cmd.Env = append(os.Environ(), "GOTOOLCHAIN=local")
	b, _ := cmd.Output() // exits non-zero for the Go version; the JSON says what happened
	var info struct {
		Zip, Error string
		Origin     struct{ Hash string }
	}
	if err := json.Unmarshal(b, &info); err != nil {
		return "", fmt.Errorf("go mod download %s@%s: %v: %s", module, version, err, b)
	}
	if info.Zip == "" {
		return "", fmt.Errorf("go mod download %s@%s: %s", module, version, info.Error)
	}
	if info.Origin.Hash != commit {
		return "", fmt.Errorf("%s@%s is commit %q, want %s", module, version, info.Origin.Hash, commit)
	}
	if _, err := os.Stat(info.Zip); err != nil {
		return "", err
	}
	return info.Zip, nil
}

// readModule returns config/betterleaks.toml and LICENSE from the module zip.
func readModule(zipPath, version string) (cfg, licence []byte, err error) {
	z, err := zip.OpenReader(zipPath)
	if err != nil {
		return nil, nil, err
	}
	defer z.Close()
	read := func(name string) ([]byte, error) {
		f, err := z.Open(module + "@" + version + "/" + name)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		return io.ReadAll(f)
	}
	if cfg, err = read("config/betterleaks.toml"); err != nil {
		return nil, nil, err
	}
	if licence, err = read("LICENSE"); err != nil {
		return nil, nil, err
	}
	return cfg, licence, nil
}

type config struct {
	Filter string `toml:"filter"`
	Rules  []struct {
		ID          string   `toml:"id"`
		Regex       string   `toml:"regex"`
		SecretGroup int      `toml:"secretGroup"`
		Keywords    []string `toml:"keywords"`
		Filter      string   `toml:"filter"`
		SkipReport  bool     `toml:"skipReport"`
		Path        string   `toml:"path"`
	} `toml:"rules"`
}

// filter is the part of a betterleaks filter expression the table keeps. A
// secret must have entropy above minEntropy (at least minEntropy, when
// minInclusive) and match no deny regex.
type filter struct {
	minEntropy   float64
	minInclusive bool
	deny         []string
}

// stats counts what generation kept and dropped, for the reviewer of a bump.
type stats map[string]int

func generate(cfgTOML, licence []byte, version, commit string) ([]byte, error) {
	var cfg config
	if err := toml.Unmarshal(cfgTOML, &cfg); err != nil {
		return nil, err
	}
	st := stats{}
	fixed := map[string]bool{}
	expr, err := override(globalFilter, cfg.Filter, fixed)
	if err != nil {
		return nil, err
	}
	global, _, err := translate(expr, st)
	if err != nil {
		return nil, err
	}
	var lossy []string
	var w bytes.Buffer
	fmt.Fprintf(&w, "// Code generated by internal/redact/gen from %s@%s config/betterleaks.toml; DO NOT EDIT.\n", module, version)
	fmt.Fprintf(&w, "//\n// Upstream commit %s. The rules are derived from betterleaks, whose licence follows.\n//\n", commit)
	for _, l := range strings.Split(strings.TrimSpace(string(licence)), "\n") {
		fmt.Fprintln(&w, strings.TrimRight("// "+l, " "))
	}
	fmt.Fprintf(&w, "\npackage redact\n\n")
	fmt.Fprintf(&w, "// vendoredDeny holds betterleaks' global filter: a secret matching one is a placeholder.\n")
	fmt.Fprintf(&w, "var vendoredDeny = %s\n\n", list(global.deny, true))
	fmt.Fprintf(&w, "var vendoredRules = []vendoredRule{\n")
	n := 0
	for _, r := range cfg.Rules {
		switch {
		case strings.HasPrefix(r.ID, "generic-"):
			st["skipped: generic (our key-name rule covers them)"]++
			continue
		case r.SkipReport:
			st["skipped: skipReport (identifies a service, not a secret)"]++
			continue
		case r.Regex == "":
			st["skipped: no regex (matches file paths only)"]++
			continue
		case r.Path != "":
			// Redacted text has no file path, so the rule runs on all of it.
			st["kept: path-scoped, run without its path"]++
		}
		re, err := override(r.ID, r.Regex, fixed)
		if err != nil {
			return nil, err
		}
		f, ok, err := translate(r.Filter, st)
		if err != nil {
			return nil, fmt.Errorf("rule %s: %v", r.ID, err)
		}
		if !ok {
			lossy = append(lossy, r.ID)
		}
		for _, p := range append([]string{re}, f.deny...) {
			if _, err := regexp.Compile(p); err != nil {
				return nil, fmt.Errorf("rule %s: %v", r.ID, err)
			}
		}
		fmt.Fprintf(&w, "{id: %q, re: %s, group: %d, keywords: %s, minEntropy: %v, minInclusive: %v, deny: %s},\n",
			r.ID, quote(re), r.SecretGroup, list(r.Keywords, false), f.minEntropy, f.minInclusive, list(f.deny, false))
		n++
	}
	fmt.Fprintf(&w, "}\n")
	for _, o := range overrides {
		if !fixed[o.id] {
			return nil, fmt.Errorf("override for %s: no such rule", o.id)
		}
	}
	src, err := format.Source(w.Bytes())
	if err != nil {
		return nil, err
	}
	slog.Info("rules written", "count", n)
	keys := make([]string, 0, len(st))
	for k := range st {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		slog.Info(k, "count", st[k])
	}
	slog.Info("rules with a dropped filter part other than tokenRatio", "ids", lossy)
	return src, nil
}

// override applies the overrides for id to s, and records that they applied.
func override(id, s string, fixed map[string]bool) (string, error) {
	for _, o := range overrides {
		if o.id != id {
			continue
		}
		if strings.Count(s, o.old) != 1 {
			return "", fmt.Errorf("override for %s: %q does not occur once in %s", o.id, o.old, s)
		}
		s = strings.Replace(s, o.old, o.new, 1)
		fixed[o.id] = true
	}
	return s, nil
}

var (
	entropyRe     = regexp.MustCompile(`^(?:filter\.)?entropy\(finding\["secret"\]\)\s*(<=|<)\s*([0-9.]+)$`)
	matchesAnyRe  = regexp.MustCompile(`(?s)^(?:filter\.)?matchesAny\(finding\["secret"\],\s*\[(.*)\]\)$`)
	containsAnyRe = regexp.MustCompile(`(?s)^(?:filter\.)?containsAny\(finding\["secret"\],\s*\[(.*)\]\)$`)
	stringLitRe   = regexp.MustCompile("`[^`]*`|\"(?:[^\"\\\\]|\\\\.)*\"")
)

// translate keeps the parts of a filter expression the table understands:
// entropy thresholds and deny lists on the secret. It drops the rest, such
// as tokenRatio, and reports whether it dropped any part other than
// tokenRatio. A dropped part only ever makes the table redact more.
func translate(expr string, st stats) (f filter, ok bool, err error) {
	ok = true
	for _, p := range orParts(expr) {
		if m := entropyRe.FindStringSubmatch(p); m != nil {
			f.minEntropy, _ = strconv.ParseFloat(m[2], 64)
			f.minInclusive = m[1] == "<"
			st["filter kept: entropy"]++
			continue
		}
		var lits []string
		switch m, c := matchesAnyRe.FindStringSubmatch(p), containsAnyRe.FindStringSubmatch(p); {
		case m != nil:
			lits, err = literals(m[1], false)
			st["filter kept: matchesAny"]++
		case c != nil:
			lits, err = literals(c[1], true)
			st["filter kept: containsAny"]++
		case strings.Contains(p, "tokenRatio("):
			st["filter dropped: tokenRatio"]++
		default:
			st["filter dropped: other expression"]++
			ok = false
		}
		if err != nil {
			return f, ok, err
		}
		f.deny = append(f.deny, lits...)
	}
	return f, ok, nil
}

// orParts splits expr on its top-level "||", unwrapping parentheses that
// enclose a whole part.
func orParts(expr string) []string {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return nil
	}
	var parts []string
	depth, start, firstClose := 0, 0, -1
	scan(expr, func(i int, c byte) {
		switch c {
		case '(', '[':
			depth++
		case ')', ']':
			depth--
			if depth == 0 && firstClose < 0 {
				firstClose = i
			}
		case '|':
			if depth == 0 && i > start && expr[i-1] == '|' {
				parts = append(parts, expr[start:i-1])
				start = i + 1
			}
		}
	})
	if len(parts) == 0 {
		if expr[0] == '(' && firstClose == len(expr)-1 {
			return orParts(expr[1 : len(expr)-1])
		}
		return []string{expr}
	}
	parts = append(parts, expr[start:])
	var all []string
	for _, p := range parts {
		all = append(all, orParts(p)...)
	}
	return all
}

// scan calls f for each byte of expr outside string literals.
func scan(expr string, f func(int, byte)) {
	var quote byte
	for i := 0; i < len(expr); i++ {
		c := expr[i]
		switch {
		case quote == '"' && c == '\\':
			i++
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case c == '`' || c == '"':
			quote = c
		default:
			f(i, c)
		}
	}
}

// literals returns the string literals in a list as regexes: raw strings are
// regexes already, and quoted ones are substrings when quoteMeta is set.
func literals(list string, quoteMeta bool) ([]string, error) {
	var out []string
	for _, m := range stringLitRe.FindAllString(list, -1) {
		s := m[1 : len(m)-1]
		if m[0] == '"' {
			u, err := strconv.Unquote(m)
			if err != nil {
				return nil, fmt.Errorf("literal %s: %v", m, err)
			}
			s = u
		}
		if quoteMeta {
			s = regexp.QuoteMeta(s)
		}
		out = append(out, s)
	}
	return out, nil
}

// list writes ss as a Go []string literal, one string per line when multiline.
func list(ss []string, multiline bool) string {
	if len(ss) == 0 {
		return "nil"
	}
	q := make([]string, len(ss))
	for i, s := range ss {
		q[i] = quote(s)
	}
	if multiline {
		return "[]string{\n" + strings.Join(q, ",\n") + ",\n}"
	}
	return "[]string{" + strings.Join(q, ", ") + "}"
}

// quote writes s as a Go raw string when it can.
func quote(s string) string {
	if !strings.Contains(s, "`") {
		return "`" + s + "`"
	}
	return strconv.Quote(s)
}
