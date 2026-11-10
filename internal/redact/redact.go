// Package redact is the one redactor (ADR 0003 D8): it replaces secrets in
// text and JSON with Marker. The Sensor runs it on what it fetches, and the
// Control plane runs it again before every LLM call and every audit write, so
// a secret one pass misses the other still catches.
package redact

import (
	"bytes"
	"encoding/json"
	"regexp"
)

// Marker replaces each secret.
const Marker = "[REDACTED]"

// A rule matches a secret. When it has a capturing group, only the first
// group is the secret and the rest of the match is kept.
type rule struct{ re *regexp.Regexp }

// sensitiveName is a key or parameter name whose value is a secret.
const sensitiveName = `[A-Za-z0-9_.-]*(?:password|passwd|pwd|secret|token|api[_-]?key|access[_-]?key|account[_-]?key|private[_-]?key)[A-Za-z0-9_.-]*`

var rules = []rule{
	// Private keys, JWTs, bearer tokens, AWS key ids, Google API keys, GitHub tokens.
	{regexp.MustCompile(`-----BEGIN[A-Z ]*PRIVATE KEY-----(?s:.*?)(?:-----END[A-Z ]*PRIVATE KEY-----|\z)`)},
	{regexp.MustCompile(`\beyJ[A-Za-z0-9_-]+\.eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+`)},
	{regexp.MustCompile(`(?i)\bbearer\s+([A-Za-z0-9._~+/=-]{20,})`)},
	{regexp.MustCompile(`\b(?:AKIA|ASIA|ABIA|ACCA)[0-9A-Z]{16}\b`)},
	{regexp.MustCompile(`\bAIza[0-9A-Za-z_-]{35}\b`)},
	// GitHub tokens are 36 characters after the prefix today, but GitHub says
	// the length may change, so a looser bound misses fewer.
	{regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{30,}\b|\bgithub_pat_[A-Za-z0-9_]{22,}`)},
	// Pre-signed URLs: S3, GCS, Azure SAS (GitHub job-log links), CloudFront.
	{regexp.MustCompile(`(?i)[?&](?:x-amz-signature|x-amz-credential|x-amz-security-token|x-goog-signature|x-goog-credential|signature|sig)=([^&#\s"'<>]+)`)},
	{regexp.MustCompile(`(?i)` + sensitiveName + `\s*[=:]\s*("[^"]*"|'[^']*'|[^\s&,;"']+)`)},
}

var sensitive = regexp.MustCompile(`(?i)^` + sensitiveName + `$`)

// String returns s with every secret replaced by Marker.
func String(s string) string {
	for _, r := range rules {
		s = r.apply(s)
	}
	return s
}

func (r rule) apply(s string) string {
	matches := r.re.FindAllStringSubmatchIndex(s, -1)
	if matches == nil {
		return s
	}
	var b []byte
	last := 0
	for _, m := range matches {
		start, end := m[0], m[1]
		if len(m) > 2 && m[2] >= 0 {
			start, end = m[2], m[3]
		}
		if s[start:end] == Marker { // redacted by an earlier pass
			continue
		}
		b = append(append(b, s[last:start]...), Marker...)
		last = end
	}
	return string(append(b, s[last:]...))
}

// JSON returns raw with every secret in its string values replaced by Marker.
// It works on decoded values, since encoding hides secrets from the patterns
// (json.Marshal writes & as &). A string under a sensitive key, or the
// value of a {"name": <sensitive>, "value": ...} pair such as a Kubernetes
// env var, is replaced whole. raw is returned unchanged when it holds no
// secret, and redacted as text when it is not JSON.
func JSON(raw json.RawMessage) json.RawMessage {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber() // numbers keep their exact text
	var v any
	if err := d.Decode(&v); err != nil {
		return json.RawMessage(String(string(raw)))
	}
	v, changed := walk(v)
	if !changed {
		return raw
	}
	b, _ := json.Marshal(v) // decoded JSON always marshals
	return b
}

func walk(v any) (any, bool) {
	switch v := v.(type) {
	case string:
		s := String(v)
		return s, s != v
	case []any:
		changed := false
		for i, e := range v {
			var c bool
			v[i], c = walk(e)
			changed = changed || c
		}
		return v, changed
	case map[string]any:
		changed := false
		name, _ := v["name"].(string)
		for k, e := range v {
			if s, ok := e.(string); ok && s != Marker && (sensitive.MatchString(k) || k == "value" && sensitive.MatchString(name)) {
				v[k], changed = Marker, true
				continue
			}
			var c bool
			v[k], c = walk(e)
			changed = changed || c
		}
		return v, changed
	}
	return v, false
}
