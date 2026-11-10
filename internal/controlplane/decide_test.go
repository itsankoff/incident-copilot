package controlplane

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/itsankoff/incident-copilot/internal/signal"
	"github.com/itsankoff/incident-copilot/internal/triage"
)

var decideTarget = &signal.Target{Platform: "kubernetes", Location: "prod", Namespace: "demo", Kind: "Deployment", Name: "orders"}

func withSeverity(inc triage.Incident, s signal.Severity) triage.Incident {
	inc.Members = []triage.Member{{Key: "am/x", Name: "KubePodCrashLooping", Severity: s}}
	return inc
}

// Decide over every combination of ceiling, kill switch, confidence, guard
// result, and Target (ADR 0003 D3): the level is never above any input, never
// above L2, explained by a cap at that level, and blind to severity.
func TestDecideIsNeverAboveAnyInput(t *testing.T) {
	type ceiling struct {
		name   string
		policy Policy
		env    string
		max    Level // the ceiling this input sets, as the ADR states it
	}
	ceilings := []ceiling{
		{"missing policy", Policy{Problem: "is not configured"}, "staging", L1},
		{"unparseable policy", Policy{Problem: "is unparseable: x"}, "staging", L1},
		{"unknown Environment, production L1", Policy{Ceilings: map[string]Level{"production": L1, "staging": L2}}, "qa", L1},
		{"unknown Environment, no production", Policy{Ceilings: map[string]Level{"staging": L2}}, "qa", L1},
		{"no Location", Policy{Ceilings: map[string]Level{"production": L0}}, "", L0},
	}
	for _, l := range []Level{L0, L1, L2, L3} {
		ceilings = append(ceilings, ceiling{"staging " + l.String(), Policy{Ceilings: map[string]Level{"staging": l, "production": L0}}, "staging", l})
	}
	type kill struct {
		ks  KillSwitch
		max Level
	}
	kills := []kill{{KillSwitch{}, L3}, {KillSwitch{Problem: "store down"}, L1}}
	for _, l := range []Level{L0, L1, L2} {
		kills = append(kills, kill{KillSwitch{Set: true, Level: l}, l})
	}
	confidences := map[string]Level{"high": L2, "medium": L2, "low": L1, "": L1, "certain": L1}
	guardSets := map[string][]Guard{
		"none":   nil,
		"pass":   {{Name: "plan limits", Passed: true}},
		"failed": {{Name: "plan limits", Passed: true}, {Name: "gate1", Reason: "the Advice fails Gate 1"}},
	}

	n := 0
	for _, c := range ceilings {
		for _, k := range kills {
			for conf, confMax := range confidences {
				for gname, guards := range guardSets {
					for _, target := range []*signal.Target{decideTarget, nil} {
						n++
						in := DecideInput{Incident: triage.Incident{Target: target}, Environment: c.env, Policy: c.policy, KillSwitch: k.ks, Confidence: conf, Guards: guards}
						name := fmt.Sprintf("%s/%+v/%s/%s/target=%v", c.name, k.ks, conf, gname, target != nil)
						in.Incident = withSeverity(in.Incident, signal.Critical)
						d := Decide(in)

						bounds := map[string]Level{"ceiling": c.max, "kill switch": k.max, "confidence": confMax, "version": L2}
						if gname == "failed" {
							bounds["guard"] = L1
						}
						if target == nil {
							bounds["target"] = L1
						}
						for what, max := range bounds {
							if d.Level > max {
								t.Errorf("%s: level %s above the %s's %s", name, d.Level, what, max)
							}
						}
						explained := false
						for _, cp := range d.Caps {
							explained = explained || (cp.Level == d.Level && cp.Reason != "")
						}
						if !explained {
							t.Errorf("%s: level %s has no cap explaining it: %+v", name, d.Level, d.Caps)
						}
						for _, s := range []signal.Severity{signal.Warning, signal.Info, signal.Unknown} {
							in.Incident = withSeverity(in.Incident, s)
							if other := Decide(in); !reflect.DeepEqual(other, d) {
								t.Errorf("%s: severity %s changed the decision: %+v, want %+v", name, s, other, d)
							}
						}
					}
				}
			}
		}
	}
	if n != 9*5*5*3*2 {
		t.Fatalf("checked %d combinations", n)
	}
}

func TestDecideWorkedExamples(t *testing.T) {
	staging := Policy{Ceilings: map[string]Level{"staging": L2, "production": L1}}
	for _, tc := range []struct {
		name  string
		in    DecideInput
		level Level
		cap   string // a cap the ticket must show
	}{
		{"all clear", DecideInput{Incident: triage.Incident{Target: decideTarget}, Environment: "staging", Policy: staging, Confidence: "high"}, L2, "Environment staging has a ceiling of L2"},
		{"no Target", DecideInput{Environment: "staging", Policy: staging, Confidence: "high"}, L1, "the Incident has no Target"},
		{"kill switch", DecideInput{Incident: triage.Incident{Target: decideTarget}, Environment: "staging", Policy: staging, Confidence: "high", KillSwitch: KillSwitch{Set: true, Level: L0}}, L0, "the kill switch is set to L0"},
		{"low confidence", DecideInput{Incident: triage.Incident{Target: decideTarget}, Environment: "staging", Policy: staging, Confidence: "low"}, L1, "the RCA's confidence is low"},
		{"guard", DecideInput{Incident: triage.Incident{Target: decideTarget}, Environment: "staging", Policy: staging, Confidence: "high", Guards: []Guard{{Name: "approval", Reason: "approval is not built yet (#58)"}}}, L1, "approval is not built yet (#58)"},
		{"unknown Environment", DecideInput{Incident: triage.Incident{Target: decideTarget}, Environment: "qa", Policy: staging, Confidence: "high"}, L1, "Environment qa is not in the policy, so it is treated as production, which has a ceiling of L1"},
		{"missing policy", DecideInput{Incident: triage.Incident{Target: decideTarget}, Environment: "staging", Policy: Policy{Problem: "is not configured"}, Confidence: "high"}, L1, "the Environment policy is not configured"},
		{"L3 ceiling", DecideInput{Incident: triage.Incident{Target: decideTarget}, Environment: "staging", Policy: Policy{Ceilings: map[string]Level{"staging": L3}}, Confidence: "high"}, L2, "the RCA's confidence is high"},
	} {
		d := Decide(tc.in)
		if d.Level != tc.level {
			t.Errorf("%s: level %s, want %s (caps %+v)", tc.name, d.Level, tc.level, d.Caps)
		}
		found := false
		for _, c := range d.Caps {
			found = found || strings.Contains(c.Reason, tc.cap)
		}
		if !found {
			t.Errorf("%s: no cap %q in %+v", tc.name, tc.cap, d.Caps)
		}
	}
}
