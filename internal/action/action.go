// Package action is the platform-neutral side of an Action: the declaration
// an Executor advertises for each Action it offers, and the check of a step's
// arguments against it (ADR 0003 D2). Platform packages declare their Actions
// and are wired in at main; the core never imports them.
package action

import (
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/itsankoff/incident-copilot/internal/signal"
)

// Declaration is what an Executor says about one Action it offers. Adding an
// Action or widening its bounds is a reviewed code change.
type Declaration struct {
	Name          string   `json:"name"`
	Description   string   `json:"description"`
	TargetKinds   []string `json:"targetKinds"`
	Args          []Arg    `json:"args"` // every one is required, and no other is accepted
	Preconditions []string `json:"preconditions"`
	Preview       string   `json:"preview"`
	Check         string   `json:"check"`
	// ByHand is how a human carries the Action out, with {namespace}, {name},
	// and {<argument>} standing for the Target's and the step's values.
	ByHand string `json:"byHand"`
}

// Arg is one argument. Its bounds are hard limits.
type Arg struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Type        string `json:"type"` // integer is the only type so far
	Minimum     int64  `json:"minimum"`
	Maximum     int64  `json:"maximum"`
}

// Accepts reports whether the Action works on a Target of kind.
func (d Declaration) Accepts(kind string) bool { return slices.Contains(d.TargetKinds, kind) }

// CheckArgs returns every reason args fail d's schema: not an object, an
// unknown or missing argument, a value of the wrong type or out of range.
func (d Declaration) CheckArgs(args json.RawMessage) []string {
	var got map[string]json.RawMessage
	if len(args) > 0 {
		if err := json.Unmarshal(args, &got); err != nil {
			return []string{"args are not a JSON object"}
		}
	}
	var out []string
	keys := make([]string, 0, len(got))
	for k := range got {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		if !slices.ContainsFunc(d.Args, func(a Arg) bool { return a.Name == k }) {
			out = append(out, fmt.Sprintf("argument %q is not in the schema of %s", k, d.Name))
		}
	}
	for _, a := range d.Args {
		raw, ok := got[a.Name]
		if !ok {
			out = append(out, fmt.Sprintf("argument %q is missing", a.Name))
			continue
		}
		if a.Type != "integer" {
			// Fail closed on a declaration this version cannot check.
			out = append(out, fmt.Sprintf("argument %q has type %q, which cannot be checked", a.Name, a.Type))
			continue
		}
		n, err := strconv.ParseInt(string(raw), 10, 64)
		if err != nil {
			out = append(out, fmt.Sprintf("argument %q is not an integer", a.Name))
			continue
		}
		if n < a.Minimum || n > a.Maximum {
			out = append(out, fmt.Sprintf("argument %q is %d, outside %d to %d", a.Name, n, a.Minimum, a.Maximum))
		}
	}
	return out
}

// ByHandFor is d.ByHand for one step on t. Call it only for args that pass
// CheckArgs, so every value is a checked integer.
func (d Declaration) ByHandFor(t signal.Target, args json.RawMessage) string {
	pairs := []string{"{namespace}", t.Namespace, "{name}", t.Name}
	var got map[string]json.RawMessage
	_ = json.Unmarshal(args, &got) // checked by CheckArgs
	for _, a := range d.Args {
		pairs = append(pairs, "{"+a.Name+"}", string(got[a.Name]))
	}
	return strings.NewReplacer(pairs...).Replace(d.ByHand)
}
