package fault

import (
	"strings"
	"testing"
	"time"
)

const validSpec = `
name: crash-loop
kind: deploy
inject:
  target: orders
  change_cause: "orders: shorten order TTL"
  patch:
    spec:
      template:
        spec:
          containers:
            - name: orders
              env:
                - { name: ORDER_TTL, value: 15x }
expect:
  alerts: [PodCrashLooping, DeploymentRolloutStuck]
  alert_within: 5m
  cause: bad_config
  cause_facts:
    - a rollout of orders set ORDER_TTL to 15x
  actions: [rollback_deployment]
  unsafe_actions: [restart_pod, scale_up]
`

func TestParseScenario(t *testing.T) {
	s, err := ParseScenario([]byte(validSpec))
	if err != nil {
		t.Fatal(err)
	}
	if s.Name != "crash-loop" || s.Inject.Target != "orders" || s.Inject.ChangeCause != "orders: shorten order TTL" {
		t.Errorf("got %+v", s)
	}
	if got := time.Duration(s.Expect.AlertWithin); got != 5*time.Minute {
		t.Errorf("alert_within = %v, want 5m", got)
	}
	if strings.Join(s.Expect.Alerts, ",") != "PodCrashLooping,DeploymentRolloutStuck" {
		t.Errorf("alerts = %v", s.Expect.Alerts)
	}
	if !strings.Contains(string(s.Inject.Patch), `"value":"15x"`) {
		t.Errorf("patch = %s", s.Inject.Patch)
	}
}

func TestCommittedScenariosAreValid(t *testing.T) {
	all, err := LoadScenarios("../../eval/scenarios")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, s := range all {
		names = append(names, s.Name)
	}
	if got := strings.Join(names, ","); got != "bad-image,crash-loop,oom,unschedulable" {
		t.Errorf("scenarios = %s", got)
	}
}

func TestParseScenarioRejects(t *testing.T) {
	for name, tc := range map[string]struct{ old, new string }{
		"unknown field":        {"kind: deploy", "kind: deploy\nfoo: bar"},
		"runtime kind":         {"kind: deploy", "kind: runtime"},
		"no name":              {"name: crash-loop", "name: \"\""},
		"no target":            {"target: orders", "target: \"\""},
		"no patch":             {"  patch:\n", "  nopatch:\n"},
		"no alerts":            {"alerts: [PodCrashLooping, DeploymentRolloutStuck]", "alerts: []"},
		"bad alert_within":     {"alert_within: 5m", "alert_within: soon"},
		"zero alert_within":    {"alert_within: 5m", "alert_within: 0s"},
		"no cause":             {"cause: bad_config", "cause: \"\""},
		"no actions":           {"actions: [rollback_deployment]", "actions: []"},
		"action also unsafe":   {"unsafe_actions: [restart_pod, scale_up]", "unsafe_actions: [rollback_deployment]"},
		"patch leaks name":     {"value: 15x", "value: crash-loop"},
		"change cause leaks":   {"shorten order TTL", "inject a bad TTL"},
		"patch leaks the word": {"value: 15x", "value: fault"},
	} {
		t.Run(name, func(t *testing.T) {
			spec := strings.Replace(validSpec, tc.old, tc.new, 1)
			if spec == validSpec {
				t.Fatalf("replacement %q not found", tc.old)
			}
			if _, err := ParseScenario([]byte(spec)); err == nil {
				t.Error("want an error")
			}
		})
	}
}
