// Package fault is the logic of faultctl: the scenario spec, the kubectl calls
// that inject a scenario into the demo and clear it, and the leakage check.
package fault

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"sigs.k8s.io/yaml"
)

// Scenario is one fault and its ground truth, read from eval/scenarios.
type Scenario struct {
	Name   string `json:"name"`
	Kind   string `json:"kind"` // only "deploy" for now; runtime faults need the admin API
	Inject Inject `json:"inject"`
	Expect Expect `json:"expect"`
}

// Inject is a deploy fault: a rollout of the target Deployment with Patch
// applied to the baseline manifest.
type Inject struct {
	Target      string          `json:"target"`       // Deployment in the demo namespace
	ChangeCause string          `json:"change_cause"` // the rollout's kubernetes.io/change-cause
	Patch       json.RawMessage `json:"patch"`        // strategic-merge patch of the Deployment
}

// Expect is the scenario's ground truth.
type Expect struct {
	Alerts        []string `json:"alerts"`
	AlertWithin   Duration `json:"alert_within"`
	Cause         string   `json:"cause"`       // ground-truth category used for scoring
	CauseFacts    []string `json:"cause_facts"` // facts a correct RCA must mention
	Actions       []string `json:"actions"`     // any of these counts as a correct remediation
	UnsafeActions []string `json:"unsafe_actions"`
}

// Duration is a time.Duration written as a string such as "5m".
type Duration time.Duration

func (d *Duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return err
	}
	*d = Duration(v)
	return nil
}

func (d Duration) String() string { return time.Duration(d).String() }

// ParseScenario parses and validates one scenario spec.
func ParseScenario(data []byte) (Scenario, error) {
	var s Scenario
	if err := yaml.UnmarshalStrict(data, &s); err != nil {
		return Scenario{}, err
	}
	var errs []error
	check := func(ok bool, format string, args ...any) {
		if !ok {
			errs = append(errs, fmt.Errorf(format, args...))
		}
	}
	check(s.Name != "", "name: required")
	check(s.Kind == "deploy", "kind: want deploy, got %q", s.Kind)
	check(s.Inject.Target != "", "inject.target: required")
	check(len(s.Inject.Patch) > 0 && string(s.Inject.Patch) != "null", "inject.patch: required")
	check(len(s.Expect.Alerts) > 0, "expect.alerts: required")
	check(s.Expect.AlertWithin > 0, "expect.alert_within: must be positive")
	check(s.Expect.Cause != "", "expect.cause: required")
	check(len(s.Expect.Actions) > 0, "expect.actions: required")
	for _, a := range s.Expect.Actions {
		check(!slices.Contains(s.Expect.UnsafeActions, a), "expect: %s is both acceptable and unsafe", a)
	}
	// What gets rolled out is readable by the investigation, so it must not
	// give the scenario away.
	if s.Name != "" {
		for _, text := range []string{s.Inject.ChangeCause, string(s.Inject.Patch)} {
			if leaks := Leaks(s.Name, text); len(leaks) > 0 {
				errs = append(errs, fmt.Errorf("inject: gives the scenario away: %s", strings.Join(leaks, ", ")))
			}
		}
	}
	if err := errors.Join(errs...); err != nil {
		return Scenario{}, fmt.Errorf("scenario %s: %w", s.Name, err)
	}
	return s, nil
}

// LoadScenarios parses every *.yaml in dir, sorted by name.
func LoadScenarios(dir string) ([]Scenario, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "*.yaml"))
	if err != nil {
		return nil, err
	}
	var out []Scenario
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			return nil, err
		}
		s, err := ParseScenario(data)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", p, err)
		}
		out = append(out, s)
	}
	slices.SortFunc(out, func(a, b Scenario) int { return strings.Compare(a.Name, b.Name) })
	return out, nil
}
