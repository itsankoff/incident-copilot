package controlplane

import (
	"fmt"
	"slices"
)

// RCA is the thin root-cause analysis: a summary and a confidence, each made
// of Claims. The full schema is #50.
type RCA struct {
	Summary    []Claim    `json:"summary"`
	Confidence Confidence `json:"confidence"`
}

type Confidence struct {
	Level  string  `json:"level"` // low, medium, or high
	Claims []Claim `json:"claims"`
}

type ClaimKind string

const (
	Observed ClaimKind = "observed" // cites Evidence refs by call id
	Inferred ClaimKind = "inferred" // cites other Claims by id
)

type Claim struct {
	ID        string    `json:"id"`
	Statement string    `json:"statement"`
	Kind      ClaimKind `json:"kind"`
	Cites     []string  `json:"cites"`
}

// gateFailure is one reason an RCA fails Gate 1. Claim is empty when the
// reason belongs to the RCA as a whole.
type gateFailure struct {
	Claim  string `json:"claim"`
	Reason string `json:"reason"`
}

// gate1 is the citation check on an RCA (ADR 0003 D4): every cited Evidence
// ref is a query of this Incident that succeeded, and every inferred Claim
// cites existing Claims. A nil RCA has nothing to check.
func gate1(r *RCA, ledger []ledgerEntry) []gateFailure {
	if r == nil {
		return nil
	}
	ok := map[string]bool{}
	for _, e := range ledger {
		ok[e.Ref.CallID] = e.OK
	}
	all := append(append([]Claim{}, r.Summary...), r.Confidence.Claims...)
	ids := map[string]Claim{}
	for _, c := range all {
		if _, dup := ids[c.ID]; !dup {
			ids[c.ID] = c
		}
	}

	var out []gateFailure
	fail := func(c Claim, format string, args ...any) {
		out = append(out, gateFailure{c.ID, fmt.Sprintf(format, args...)})
	}
	checked := map[string]bool{}
	for _, c := range all {
		// A claim repeated as is (say, under both summary and confidence) is
		// one claim, checked once.
		if checked[c.ID] {
			if first := ids[c.ID]; first.Statement != c.Statement || first.Kind != c.Kind || !slices.Equal(first.Cites, c.Cites) {
				fail(c, "claim id %q names two different claims", c.ID)
			}
			continue
		}
		checked[c.ID] = true
		if len(c.Cites) == 0 {
			fail(c, "cites nothing")
		}
		for _, id := range c.Cites {
			switch c.Kind {
			case Observed:
				succeeded, known := ok[id]
				switch {
				case !known:
					fail(c, "cites %s, which is not an Evidence query of this Incident", id)
				case !succeeded:
					fail(c, "cites %s, whose Evidence query failed", id)
				}
			case Inferred:
				if _, exists := ids[id]; !exists || id == c.ID {
					fail(c, "cites %s, which is not another claim", id)
				}
			}
		}
		if c.Kind != Observed && c.Kind != Inferred {
			fail(c, "kind %q is neither observed nor inferred", c.Kind)
		}
	}
	switch r.Confidence.Level {
	case "low", "medium", "high":
	default:
		out = append(out, gateFailure{"", fmt.Sprintf("confidence level %q is not low, medium, or high", r.Confidence.Level)})
	}
	return out
}
