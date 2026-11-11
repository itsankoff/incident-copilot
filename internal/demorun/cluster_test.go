package demorun

import (
	"reflect"
	"strings"
	"testing"

	"github.com/itsankoff/incident-copilot/internal/fault"
)

// pods builds a kubectl get pods -o json answer.
func pods(items ...string) []byte {
	s := `{"items":[`
	for i, it := range items {
		if i > 0 {
			s += ","
		}
		s += it
	}
	return []byte(s + `]}`)
}

const (
	runningReady = `{"metadata":{"name":"orders-a","labels":{"app.kubernetes.io/name":"orders"}},
		"status":{"phase":"Running","conditions":[{"type":"Ready","status":"True"}],
		"containerStatuses":[{"ready":true,"restartCount":0,"state":{"running":{}}}]}}`
	inventory = `{"metadata":{"name":"inventory-a","labels":{"app.kubernetes.io/name":"inventory"}},
		"status":{"phase":"Running","conditions":[{"type":"Ready","status":"True"}],
		"containerStatuses":[{"ready":true,"restartCount":0,"state":{"running":{}}}]}}`
	crashLooping = `{"metadata":{"name":"orders-b","labels":{"app.kubernetes.io/name":"orders"}},
		"status":{"phase":"Running","conditions":[{"type":"Ready","status":"False"}],
		"containerStatuses":[{"ready":false,"restartCount":3,
		"state":{"waiting":{"reason":"CrashLoopBackOff"}},
		"lastState":{"terminated":{"reason":"Error","exitCode":1}}}]}}`
	oomKilled = `{"metadata":{"name":"orders-c","labels":{"app.kubernetes.io/name":"orders"}},
		"status":{"phase":"Running","conditions":[{"type":"Ready","status":"True"}],
		"containerStatuses":[{"ready":true,"restartCount":1,"state":{"running":{}},
		"lastState":{"terminated":{"reason":"OOMKilled","exitCode":137}}}]}}`
	unschedulable = `{"metadata":{"name":"orders-d","labels":{"app.kubernetes.io/name":"orders"}},
		"status":{"phase":"Pending","conditions":[{"type":"PodScheduled","status":"False","reason":"Unschedulable",
		"message":"0/2 nodes are available: 2 Insufficient cpu."}]}}`
	badImage = `{"metadata":{"name":"orders-e","labels":{"app.kubernetes.io/name":"orders"}},
		"status":{"phase":"Pending","conditions":[{"type":"PodScheduled","status":"True"}],
		"containerStatuses":[{"ready":false,"restartCount":0,"state":{"waiting":{"reason":"ImagePullBackOff"}}}]}}`
)

func TestPodsReportsEachTargetPodsStateOnce(t *testing.T) {
	p := &Pods{Target: "orders"}
	got, err := p.Diff(pods(runningReady, inventory))
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"pod orders-a: Running, Ready"}; !reflect.DeepEqual(got, want) {
		t.Errorf("first:\n got %q\nwant %q", got, want)
	}
	if got, _ := p.Diff(pods(runningReady, inventory)); len(got) != 0 {
		t.Errorf("unchanged: got %q", got)
	}
	got, _ = p.Diff(pods(runningReady, crashLooping, oomKilled, unschedulable, badImage))
	want := []string{
		"pod orders-b: CrashLoopBackOff, last exit Error (code 1), 3 restarts",
		"pod orders-c: Running, Ready, last exit OOMKilled (code 137), 1 restart",
		"pod orders-d: Pending, unschedulable: 0/2 nodes are available: 2 Insufficient cpu.",
		"pod orders-e: ImagePullBackOff",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("faulty:\n got %q\nwant %q", got, want)
	}
	got, _ = p.Diff(pods(runningReady))
	want = []string{"pod orders-b: gone", "pod orders-c: gone", "pod orders-d: gone", "pod orders-e: gone"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("deleted:\n got %q\nwant %q", got, want)
	}
}

func TestAlertsReportsFiringAndResolved(t *testing.T) {
	a := &Alerts{}
	crash := fault.Alert{Labels: map[string]string{"alertname": "PodCrashLooping", "namespace": "demo", "deployment": "orders"}}
	stuck := fault.Alert{Labels: map[string]string{"alertname": "DeploymentRolloutStuck", "namespace": "demo", "deployment": "orders"}}
	if got := a.Diff(nil); len(got) != 0 {
		t.Errorf("none: %q", got)
	}
	got := a.Diff([]fault.Alert{stuck, crash})
	want := []string{
		"Alertmanager: DeploymentRolloutStuck firing for demo/orders",
		"Alertmanager: PodCrashLooping firing for demo/orders",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("firing:\n got %q\nwant %q", got, want)
	}
	if got := a.Diff([]fault.Alert{crash, stuck}); len(got) != 0 {
		t.Errorf("unchanged: %q", got)
	}
	if got, want := a.Diff([]fault.Alert{crash}), []string{"Alertmanager: DeploymentRolloutStuck resolved for demo/orders"}; !reflect.DeepEqual(got, want) {
		t.Errorf("resolved:\n got %q\nwant %q", got, want)
	}
}

func TestPodsSkipsTheRunningGapBetweenCrashes(t *testing.T) {
	p := &Pods{Target: "orders"}
	if _, err := p.Diff(pods(crashLooping)); err != nil {
		t.Fatal(err)
	}
	restarted := `{"metadata":{"name":"orders-b","labels":{"app.kubernetes.io/name":"orders"}},
		"status":{"phase":"Running","conditions":[{"type":"Ready","status":"False"}],
		"containerStatuses":[{"ready":false,"restartCount":4,"state":{"running":{}},
		"lastState":{"terminated":{"reason":"Error","exitCode":1}}}]}}`
	if got, _ := p.Diff(pods(restarted)); len(got) != 0 {
		t.Errorf("gap between crashes reported: %q", got)
	}
	got, _ := p.Diff(pods(strings.Replace(crashLooping, `"restartCount":3`, `"restartCount":4`, 1)))
	if want := []string{"pod orders-b: CrashLoopBackOff, last exit Error (code 1), 4 restarts"}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}
