package fault

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// Namespace is where the demo workload runs.
const Namespace = "demo"

// Kubectl runs kubectl with args and returns its stdout.
type Kubectl func(ctx context.Context, args ...string) ([]byte, error)

// KubectlFor returns a Kubectl that pins every call to kubeContext.
func KubectlFor(kubeContext string) Kubectl {
	return func(ctx context.Context, args ...string) ([]byte, error) {
		cmd := exec.CommandContext(ctx, "kubectl", append([]string{"--context", kubeContext}, args...)...)
		var stderr strings.Builder
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			return out, fmt.Errorf("kubectl %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
		}
		return out, nil
	}
}

// Demo injects scenarios into, and clears them from, the demo workload.
type Demo struct {
	Kubectl  Kubectl
	Baseline string        // the kustomize dir `make deploy` applied (build/deploy)
	Dir      string        // faultctl's own dir for the overlay and the armed scenario
	Commit   func() string // a synthetic commit SHA for each injected rollout
	Poll     time.Duration // how often Clear re-checks pods and alerts
}

// Inject rolls out s's patch over the baseline.
func (d Demo) Inject(ctx context.Context, s Scenario) error {
	if err := d.checkBaseline(); err != nil {
		return err
	}
	var patch map[string]any
	if err := json.Unmarshal(s.Inject.Patch, &patch); err != nil {
		return fmt.Errorf("inject.patch: %w", err)
	}
	patch["apiVersion"] = "apps/v1"
	patch["kind"] = "Deployment"
	patch["metadata"] = map[string]any{
		"name": s.Inject.Target,
		"annotations": map[string]string{
			"kubernetes.io/change-cause":  s.Inject.ChangeCause,
			"incident-copilot.dev/commit": d.Commit(),
		},
	}
	patchJSON, err := json.Marshal(patch)
	if err != nil {
		return err
	}
	base, err := filepath.Rel(d.Dir, d.Baseline)
	if err != nil {
		return err
	}
	// JSON is valid YAML, so the kustomization is written as JSON.
	kust, err := json.MarshalIndent(map[string]any{
		"apiVersion": "kustomize.config.k8s.io/v1beta1",
		"kind":       "Kustomization",
		"resources":  []string{base},
		"patches": []map[string]any{{
			"target": map[string]string{"kind": "Deployment", "name": s.Inject.Target},
			"patch":  string(patchJSON),
		}},
	}, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(d.Dir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(d.Dir, "kustomization.yaml"), kust, 0o644); err != nil {
		return err
	}
	if _, err := d.Kubectl(ctx, "apply", "-k", d.Dir); err != nil {
		return err
	}
	return os.WriteFile(d.armedFile(), []byte(s.Name), 0o644)
}

// Clear re-applies the baseline and blocks until every demo pod is Ready and
// no alert fires for the demo namespace, or ctx ends. It is safe to run again.
func (d Demo) Clear(ctx context.Context) error {
	if err := d.checkBaseline(); err != nil {
		return err
	}
	if _, err := d.Kubectl(ctx, "apply", "-k", d.Baseline); err != nil {
		return err
	}
	if _, err := d.Kubectl(ctx, "-n", Namespace, "rollout", "status", "deployment", "-l", "app.kubernetes.io/part-of=demo"); err != nil {
		return err
	}
	if err := d.waitFor(ctx, "pods Ready", d.unreadyPods); err != nil {
		return err
	}
	if err := d.waitFor(ctx, "no alerts firing", func(ctx context.Context) ([]string, error) {
		alerts, err := d.FiringAlerts(ctx)
		names := make([]string, len(alerts))
		for i, a := range alerts {
			names[i] = a.String()
		}
		return names, err
	}); err != nil {
		return err
	}
	if err := os.Remove(d.armedFile()); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// waitFor polls pending until it returns nothing, and on timeout names what
// was still pending.
func (d Demo) waitFor(ctx context.Context, goal string, pending func(context.Context) ([]string, error)) error {
	for {
		left, err := pending(ctx)
		if err != nil {
			return err
		}
		if len(left) == 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("waiting for %s: %w; still: %s", goal, ctx.Err(), strings.Join(left, ", "))
		case <-time.After(d.Poll):
		}
	}
}

func (d Demo) unreadyPods(ctx context.Context) ([]string, error) {
	out, err := d.Kubectl(ctx, "-n", Namespace, "get", "pods", "-o", "json")
	if err != nil {
		return nil, err
	}
	var list struct {
		Items []struct {
			Metadata struct {
				Name              string  `json:"name"`
				DeletionTimestamp *string `json:"deletionTimestamp"`
			} `json:"metadata"`
			Status struct {
				Conditions []struct{ Type, Status string } `json:"conditions"`
			} `json:"status"`
		} `json:"items"`
	}
	if err := json.Unmarshal(out, &list); err != nil {
		return nil, fmt.Errorf("pods: %w", err)
	}
	var unready []string
	for _, p := range list.Items {
		ready := p.Metadata.DeletionTimestamp == nil && slices.ContainsFunc(p.Status.Conditions, func(c struct{ Type, Status string }) bool {
			return c.Type == "Ready" && c.Status == "True"
		})
		if !ready {
			unready = append(unready, p.Metadata.Name)
		}
	}
	return unready, nil
}

// Alert is one alert active in Alertmanager.
type Alert struct {
	Labels map[string]string `json:"labels"`
}

func (a Alert) String() string {
	return fmt.Sprintf("%s{namespace=%s, deployment=%s}", a.Labels["alertname"], a.Labels["namespace"], a.Labels["deployment"])
}

// alertsPath reaches ic-target's Alertmanager through the API server's
// service proxy, so no port-forward is needed.
const alertsPath = "/api/v1/namespaces/monitoring/services/kps-alertmanager:9093/proxy/api/v2/alerts?active=true"

// FiringAlerts returns the alerts active in Alertmanager for the demo namespace.
func (d Demo) FiringAlerts(ctx context.Context) ([]Alert, error) {
	out, err := d.Kubectl(ctx, "get", "--raw", alertsPath)
	if err != nil {
		return nil, err
	}
	var all []Alert
	if err := json.Unmarshal(out, &all); err != nil {
		return nil, fmt.Errorf("alerts: %w", err)
	}
	var demo []Alert
	for _, a := range all {
		if a.Labels["namespace"] == Namespace {
			demo = append(demo, a)
		}
	}
	return demo, nil
}

// LeakCheck reads what an Evidence query can read in the demo namespace
// (events, pod specs, Deployment and ReplicaSet annotations, ConfigMaps, and
// every pod's current and previous logs) and returns each place that gives
// scenario away.
func (d Demo) LeakCheck(ctx context.Context, scenario string) ([]string, error) {
	objects, err := d.Kubectl(ctx, "-n", Namespace, "get", "events,pods,deployments,replicasets,configmaps", "-o", "json")
	if err != nil {
		return nil, err
	}
	var leaks []string
	for _, w := range Leaks(scenario, string(objects)) {
		leaks = append(leaks, "objects: "+w)
	}
	var list struct {
		Items []struct {
			Kind     string `json:"kind"`
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
		} `json:"items"`
	}
	if err := json.Unmarshal(objects, &list); err != nil {
		return nil, fmt.Errorf("objects: %w", err)
	}
	for _, it := range list.Items {
		if it.Kind != "Pod" {
			continue
		}
		for _, previous := range []bool{false, true} {
			args := []string{"-n", Namespace, "logs", it.Metadata.Name, "--all-containers"}
			if previous {
				args = append(args, "--previous")
			}
			// A container that never ran, or never restarted, has no logs to read.
			logs, _ := d.Kubectl(ctx, args...)
			for _, w := range Leaks(scenario, string(logs)) {
				leaks = append(leaks, fmt.Sprintf("logs of %s: %s", it.Metadata.Name, w))
			}
		}
	}
	return leaks, nil
}

// Armed returns the scenario last injected and not yet cleared, or "".
func (d Demo) Armed() string {
	b, _ := os.ReadFile(d.armedFile())
	return strings.TrimSpace(string(b))
}

func (d Demo) armedFile() string { return filepath.Join(d.Dir, "armed") }

func (d Demo) checkBaseline() error {
	if _, err := os.Stat(filepath.Join(d.Baseline, "kustomization.yaml")); errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("no baseline at %s: run make deploy first", d.Baseline)
	}
	return nil
}
