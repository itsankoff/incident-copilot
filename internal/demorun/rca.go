package demorun

import "strings"

const rcaHeading = "## Root-cause analysis"

// RCA returns the RCA section of a ticket in Markdown, up to the next
// second-level heading or comment separator, or "" when it has none.
func RCA(ticket string) string {
	i := strings.Index(ticket, rcaHeading)
	if i < 0 {
		return ""
	}
	lines := strings.Split(ticket[i:], "\n")
	end := len(lines)
	for j, l := range lines[1:] {
		if strings.HasPrefix(l, "## ") || strings.TrimSpace(l) == "---" {
			end = j + 1
			break
		}
	}
	return strings.TrimSpace(strings.Join(lines[:end], "\n"))
}
