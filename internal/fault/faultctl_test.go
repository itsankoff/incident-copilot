package fault

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sigs.k8s.io/yaml"
)

// fakeKubectl answers kubectl calls by the longest matching args prefix and
// records every call.
type fakeKubectl struct {
	calls   []string
	answers map[string][]string // args prefix -> successive outputs; the last repeats
}

func (f *fakeKubectl) run(_ context.Context, args ...string) ([]byte, error) {
	call := strings.Join(args, " ")
	f.calls = append(f.calls, call)
	match := ""
	for prefix := range f.answers {
		if strings.HasPrefix(call, prefix) && len(prefix) > len(match) {
			match = prefix
		}
	}
	if match == "" {
		return nil, nil
	}
	outs := f.answers[match]
	if len(outs) > 1 {
		f.answers[match] = outs[1:]
	}
	return []byte(outs[0]), nil
}

func newDemo(t *testing.T, k *fakeKubectl) Demo {
	t.Helper()
	root := t.TempDir()
	baseline := filepath.Join(root, "deploy")
	if err := os.MkdirAll(baseline, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(baseline, "kustomization.yaml"), []byte("resources: []\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return Demo{
		Kubectl:  k.run,
		Baseline: baseline,
		Dir:      filepath.Join(root, "fault"),
		Commit:   func() string { return "1a2b3c4" },
	}
}

func TestInjectAppliesThePatchOverTheBaseline(t *testing.T) {
	k := &fakeKubectl{}
	d := newDemo(t, k)
	s, err := ParseScenario([]byte(validSpec))
	if err != nil {
		t.Fatal(err)
	}

	if err := d.Inject(context.Background(), s); err != nil {
		t.Fatal(err)
	}

	if want := "apply -k " + d.Dir; len(k.calls) != 1 || k.calls[0] != want {
		t.Fatalf("calls = %q, want [%q]", k.calls, want)
	}
	raw, err := os.ReadFile(filepath.Join(d.Dir, "kustomization.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var kust struct {
		Resources []string
		Patches   []struct {
			Target map[string]string
			Patch  string
		}
	}
	if err := yaml.Unmarshal(raw, &kust); err != nil {
		t.Fatal(err)
	}
	if len(kust.Resources) != 1 || kust.Resources[0] != "../deploy" {
		t.Errorf("resources = %q, want [../deploy]", kust.Resources)
	}
	if len(kust.Patches) != 1 || kust.Patches[0].Target["kind"] != "Deployment" || kust.Patches[0].Target["name"] != "orders" {
		t.Fatalf("patches = %+v", kust.Patches)
	}
	var patch struct {
		Metadata struct {
			Name        string
			Annotations map[string]string
		}
		Spec struct {
			Template struct {
				Spec struct {
					Containers []struct {
						Name string
						Env  []struct{ Name, Value string }
					}
				}
			}
		}
	}
	if err := json.Unmarshal([]byte(kust.Patches[0].Patch), &patch); err != nil {
		t.Fatal(err)
	}
	if a := patch.Metadata.Annotations; a["kubernetes.io/change-cause"] != "orders: shorten order TTL" || a["incident-copilot.dev/commit"] != "1a2b3c4" {
		t.Errorf("annotations = %v", a)
	}
	if c := patch.Spec.Template.Spec.Containers; len(c) != 1 || c[0].Env[0].Value != "15x" {
		t.Errorf("containers = %+v", c)
	}
	if got := d.Armed(); got != "crash-loop" {
		t.Errorf("Armed() = %q, want crash-loop", got)
	}
}

const (
	podsNotReady = `{"items":[
	  {"metadata":{"name":"orders-a"},"status":{"conditions":[{"type":"Ready","status":"True"}]}},
	  {"metadata":{"name":"orders-b"},"status":{"conditions":[{"type":"Ready","status":"False"}]}}]}`
	podsTerminating = `{"items":[
	  {"metadata":{"name":"orders-a"},"status":{"conditions":[{"type":"Ready","status":"True"}]}},
	  {"metadata":{"name":"orders-b","deletionTimestamp":"2026-11-03T10:00:00Z"},"status":{"conditions":[{"type":"Ready","status":"True"}]}}]}`
	podsReady = `{"items":[
	  {"metadata":{"name":"orders-a"},"status":{"conditions":[{"type":"Ready","status":"True"}]}}]}`
	alertsFiring = `[
	  {"labels":{"alertname":"Watchdog","severity":"none"}},
	  {"labels":{"alertname":"PodCrashLooping","namespace":"demo","deployment":"orders","severity":"critical"}}]`
	alertsOther = `[{"labels":{"alertname":"Watchdog","severity":"none"}}]`
)

func TestClearRestoresTheBaselineAndWaitsUntilHealthy(t *testing.T) {
	k := &fakeKubectl{answers: map[string][]string{
		"-n demo get pods": {podsNotReady, podsTerminating, podsReady},
		"get --raw":        {alertsFiring, alertsOther},
	}}
	d := newDemo(t, k)
	if err := os.MkdirAll(d.Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d.Dir, "armed"), []byte("crash-loop"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := d.Clear(context.Background()); err != nil {
		t.Fatal(err)
	}

	if want := "apply -k " + d.Baseline; k.calls[0] != want {
		t.Errorf("first call = %q, want %q", k.calls[0], want)
	}
	var pods, alerts int
	for _, c := range k.calls {
		switch {
		case strings.HasPrefix(c, "-n demo get pods"):
			pods++
		case strings.HasPrefix(c, "get --raw"):
			alerts++
		}
	}
	if pods != 3 || alerts != 2 {
		t.Errorf("polled pods %d times and alerts %d times, want 3 and 2; calls %q", pods, alerts, k.calls)
	}
	if got := d.Armed(); got != "" {
		t.Errorf("Armed() = %q after clear, want none", got)
	}
}

func TestLeakCheckReadsObjectsAndEveryPodsLogs(t *testing.T) {
	objects := `{"items":[
	  {"kind":"Pod","metadata":{"name":"orders-a","annotations":{"kubernetes.io/change-cause":"orders: shorten order TTL"}}},
	  {"kind":"Event","metadata":{"name":"orders-a.1"},"reason":"BackOff","message":"Back-off restarting failed container"},
	  {"kind":"Deployment","metadata":{"name":"orders"},"spec":{"template":{"spec":{"schedulerName":"default-scheduler"}}}}]}`
	k := &fakeKubectl{answers: map[string][]string{
		"-n demo get events,pods,deployments,replicasets,configmaps": {objects},
		"-n demo logs orders-a --all-containers --previous":          {`{"msg":"fatal: load config: ORDER_TTL"}`},
		"-n demo logs orders-a --all-containers":                     {`{"msg":"crash-loop armed"}`},
	}}
	d := newDemo(t, k)

	leaks, err := d.LeakCheck(context.Background(), "crash-loop")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"logs of orders-a: crash-loop"}; strings.Join(leaks, "|") != strings.Join(want, "|") {
		t.Errorf("leaks = %q, want %q", leaks, want)
	}
}

func TestClearTimesOutNamingWhatStillFires(t *testing.T) {
	k := &fakeKubectl{answers: map[string][]string{
		"-n demo get pods": {podsReady},
		"get --raw":        {alertsFiring},
	}}
	d := newDemo(t, k)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	err := d.Clear(ctx)
	if err == nil || !strings.Contains(err.Error(), "PodCrashLooping") {
		t.Fatalf("err = %v, want a timeout naming PodCrashLooping", err)
	}
}
