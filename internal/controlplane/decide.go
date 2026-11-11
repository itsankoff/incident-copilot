package controlplane

import (
	"fmt"

	"github.com/itsankoff/incident-copilot/internal/triage"
)

// Level is an Autonomy level (ADR 0003 D3).
type Level int

const (
	L0 Level = iota // observe: recorded in the audit log only
	L1              // suggest: shown in the ticket, carried out by hand
	L2              // approve: runs after approval of its exact version
	L3              // autonomous: not reachable in this version
)

func (l Level) String() string { return fmt.Sprintf("L%d", int(l)) }

func (l Level) MarshalText() ([]byte, error) { return []byte(l.String()), nil }

// ParseLevel reads L0 to L3.
func ParseLevel(s string) (Level, error) {
	for l := L0; l <= L3; l++ {
		if s == l.String() {
			return l, nil
		}
	}
	return 0, fmt.Errorf("level %q: want L0, L1, L2, or L3", s)
}

// Policy is the autonomy ceiling of each Environment, from Control plane
// configuration. When it is missing or unparseable, Ceilings is nil, Problem
// says why, and every ceiling is L1.
type Policy struct {
	Ceilings map[string]Level
	Problem  string // completes "the Environment policy …"
}

// KillSwitch is the operator's override, read afresh for every decision.
type KillSwitch struct {
	Set     bool
	Level   Level
	Problem string // set when it could not be read: it then caps at L1
}

// Guard is one guard check on the Advice. A failed guard caps at L1.
type Guard struct {
	Name   string `json:"name"`
	Passed bool   `json:"passed"`
	Reason string `json:"reason,omitempty"` // why it failed
}

// DecideInput is everything Decide reads. Severity is deliberately not part of
// it: Decide reads only the Incident's Target.
type DecideInput struct {
	Incident    triage.Incident
	Environment string // the Environment of the Incident's Location; "" when there is none
	Policy      Policy
	KillSwitch  KillSwitch
	Confidence  string // the RCA's, after Gate 1; "" when there is no RCA
	Guards      []Guard
}

// Cap is one input's limit on the level, with the reason the ticket shows.
type Cap struct {
	Level  Level  `json:"level"`
	Reason string `json:"reason"`
}

// Decision is the Advice's one Autonomy level and every cap that limits it.
type Decision struct {
	Level Level `json:"level"`
	Caps  []Cap `json:"caps"`
}

// Decide computes the Autonomy level: the minimum of the Environment ceiling,
// the kill switch, the confidence cap, and the guard cap, never above L2
// (ADR 0003 D3). It is pure: no I/O and no clock. Every input can only lower
// the level, and the model states none of them.
func Decide(in DecideInput) Decision {
	var caps []Cap
	add := func(l Level, format string, args ...any) {
		caps = append(caps, Cap{l, fmt.Sprintf(format, args...)})
	}

	switch ceiling, known := in.Policy.Ceilings[in.Environment]; {
	case in.Policy.Problem != "":
		add(L1, "the Environment policy %s", in.Policy.Problem)
	case known:
		add(ceiling, "Environment %s has a ceiling of %s", in.Environment, ceiling)
	default:
		// Fail closed: an unknown Environment is production.
		what := "the Incident's Location has no Environment, so it"
		if in.Environment != "" {
			what = fmt.Sprintf("Environment %s is not in the policy, so it", in.Environment)
		}
		if prod, ok := in.Policy.Ceilings["production"]; ok {
			add(prod, "%s is treated as production, which has a ceiling of %s", what, prod)
		} else {
			add(L1, "%s is treated as production, which the policy gives no ceiling", what)
		}
	}

	switch ks := in.KillSwitch; {
	case ks.Problem != "":
		add(L1, "the kill switch could not be read: %s", ks.Problem)
	case ks.Set:
		add(ks.Level, "the kill switch is set to %s", ks.Level)
	}

	// Even high confidence caps at L2, which is what keeps every level at
	// most L2 in this version.
	switch c := in.Confidence; c {
	case "high", "medium":
		add(L2, "the RCA's confidence is %s", c)
	case "low":
		add(L1, "the RCA's confidence is low")
	case "":
		add(L1, "there is no RCA")
	default:
		add(L1, "the RCA's confidence %q is not low, medium, or high", c)
	}

	for _, g := range in.Guards {
		if !g.Passed {
			add(L1, "%s", g.Reason)
		}
	}
	if in.Incident.Target == nil {
		add(L1, "the Incident has no Target")
	}

	level := L3
	for _, c := range caps {
		level = min(level, c.Level)
	}
	return Decision{Level: level, Caps: caps}
}
