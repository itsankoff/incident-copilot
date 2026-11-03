package fault

import "regexp"

// giveaways matches words that would tell an investigator a change was staged.
// It needs a word start, so "default" does not match "fault".
var giveaways = regexp.MustCompile(`(?i)\b(fault|inject)\w*`)

// Leaks returns every word in text that gives the scenario away: "fault" or
// "inject" in any case and form, or the scenario's own name. The name match is
// case-sensitive on whole words, so Kubernetes' own reasons (OOMKilled,
// Unschedulable) are evidence, not leaks.
func Leaks(scenario, text string) []string {
	found := giveaways.FindAllString(text, -1)
	name := regexp.MustCompile(`\b` + regexp.QuoteMeta(scenario) + `\b`)
	return append(found, name.FindAllString(text, -1)...)
}
