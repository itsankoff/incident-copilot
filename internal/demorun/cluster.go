package demorun

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/itsankoff/incident-copilot/internal/fault"
)

// Pods reports the state of the Target Deployment's pods as it changes. The
// runner reports it because Kubernetes does not log it readably.
type Pods struct {
	Target string // the value of the pods' app.kubernetes.io/name label
	last   map[string]string
}

type podList struct {
	Items []struct {
		Metadata struct {
			Name   string            `json:"name"`
			Labels map[string]string `json:"labels"`
		} `json:"metadata"`
		Status struct {
			Phase      string `json:"phase"`
			Conditions []struct {
				Type, Status, Reason, Message string
			} `json:"conditions"`
			ContainerStatuses []struct {
				Ready        bool `json:"ready"`
				RestartCount int  `json:"restartCount"`
				State        struct {
					Waiting *struct{ Reason string } `json:"waiting"`
				} `json:"state"`
				LastState struct {
					Terminated *struct {
						Reason   string `json:"reason"`
						ExitCode int    `json:"exitCode"`
					} `json:"terminated"`
				} `json:"lastState"`
			} `json:"containerStatuses"`
		} `json:"status"`
	} `json:"items"`
}

// Diff takes a kubectl get pods -o json answer and returns a line for each
// Target pod whose state changed since the last call, sorted by pod name.
func (p *Pods) Diff(podsJSON []byte) ([]string, error) {
	var list podList
	if err := json.Unmarshal(podsJSON, &list); err != nil {
		return nil, fmt.Errorf("pods: %w", err)
	}
	now := map[string]string{}
	for _, it := range list.Items {
		if it.Metadata.Labels["app.kubernetes.io/name"] != p.Target {
			continue
		}
		s := it.Status
		var parts []string
		waiting, restarts := "", 0
		var last string
		for _, c := range s.ContainerStatuses {
			if c.State.Waiting != nil && c.State.Waiting.Reason != "" && waiting == "" {
				waiting = c.State.Waiting.Reason
			}
			restarts += c.RestartCount
			if t := c.LastState.Terminated; t != nil && last == "" {
				last = fmt.Sprintf("last exit %s (code %d)", t.Reason, t.ExitCode)
			}
		}
		switch {
		case waiting != "" && waiting != "ContainerCreating":
			parts = append(parts, waiting)
		default:
			parts = append(parts, s.Phase)
			for _, c := range s.Conditions {
				switch {
				case c.Type == "PodScheduled" && c.Status == "False" && c.Reason == "Unschedulable":
					parts = append(parts, "unschedulable: "+c.Message)
				case c.Type == "Ready" && s.Phase == "Running":
					parts = append(parts, map[bool]string{true: "Ready", false: "not Ready"}[c.Status == "True"])
				}
			}
		}
		if last != "" {
			parts = append(parts, last)
		}
		switch {
		case restarts == 1:
			parts = append(parts, "1 restart")
		case restarts > 1:
			parts = append(parts, fmt.Sprintf("%d restarts", restarts))
		}
		now[it.Metadata.Name] = strings.Join(parts, ", ")
	}
	var lines []string
	for _, name := range slices.Sorted(maps.Keys(now)) {
		if now[name] != p.last[name] {
			lines = append(lines, fmt.Sprintf("pod %s: %s", name, now[name]))
		}
	}
	for _, name := range slices.Sorted(maps.Keys(p.last)) {
		if _, ok := now[name]; !ok {
			lines = append(lines, fmt.Sprintf("pod %s: gone", name))
		}
	}
	p.last = now
	return lines, nil
}

// Alerts reports the demo alerts that start or stop firing in Alertmanager,
// which logs neither readably.
type Alerts struct {
	last map[string]bool
}

// Diff takes the alerts active now and returns a line for each one that
// started or stopped firing since the last call.
func (a *Alerts) Diff(active []fault.Alert) []string {
	now := map[string]bool{}
	for _, al := range active {
		now[alertKey(al)] = true
	}
	var lines []string
	for _, k := range slices.Sorted(maps.Keys(now)) {
		if !a.last[k] {
			lines = append(lines, "Alertmanager: "+strings.Replace(k, " ", " firing for ", 1))
		}
	}
	for _, k := range slices.Sorted(maps.Keys(a.last)) {
		if !now[k] {
			lines = append(lines, "Alertmanager: "+strings.Replace(k, " ", " resolved for ", 1))
		}
	}
	a.last = now
	return lines
}

func alertKey(a fault.Alert) string {
	l := a.Labels
	what := l["deployment"]
	if what == "" {
		what = l["pod"]
	}
	return fmt.Sprintf("%s %s/%s", l["alertname"], l["namespace"], what)
}
