package kubernetes

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/itsankoff/incident-copilot/internal/evidence"
	"github.com/itsankoff/incident-copilot/internal/signal"
)

var t0 = time.Date(2026, 10, 21, 3, 0, 0, 0, time.UTC)

var orders = signal.Target{Platform: "kubernetes", Location: "ic-target", Namespace: "demo", Kind: "Deployment", Name: "orders"}

func owner(kind, name string, uid types.UID) []metav1.OwnerReference {
	return []metav1.OwnerReference{{Kind: kind, Name: name, UID: uid}}
}

func meta(name string, uid types.UID, owners []metav1.OwnerReference) metav1.ObjectMeta {
	return metav1.ObjectMeta{Namespace: "demo", Name: name, UID: uid, OwnerReferences: owners, Labels: map[string]string{"app": "orders"}}
}

func event(name, kind, object string, at time.Time) *corev1.Event {
	return &corev1.Event{
		ObjectMeta:     metav1.ObjectMeta{Namespace: "demo", Name: name},
		InvolvedObject: corev1.ObjectReference{Kind: kind, Name: object, Namespace: "demo"},
		Type:           "Warning",
		Reason:         "BackOff",
		Message:        name,
		Count:          2,
		LastTimestamp:  metav1.NewTime(at),
	}
}

// demoCluster holds orders, its ReplicaSet and pod, and a pod that carries the
// same labels but belongs to something else.
func demoCluster() *fake.Clientset {
	replicas := int32(2)
	selector := &metav1.LabelSelector{MatchLabels: map[string]string{"app": "orders"}}
	return fake.NewClientset(
		&appsv1.Deployment{
			ObjectMeta: meta("orders", "dep-1", nil),
			Spec:       appsv1.DeploymentSpec{Replicas: &replicas, Selector: selector},
			Status: appsv1.DeploymentStatus{Conditions: []appsv1.DeploymentCondition{
				{Type: appsv1.DeploymentAvailable, Status: corev1.ConditionFalse, Reason: "MinimumReplicasUnavailable"},
			}},
		},
		&appsv1.ReplicaSet{ObjectMeta: meta("orders-abc", "rs-1", owner("Deployment", "orders", "dep-1"))},
		&corev1.Pod{
			ObjectMeta: meta("orders-abc-1", "pod-1", owner("ReplicaSet", "orders-abc", "rs-1")),
			Status: corev1.PodStatus{Phase: corev1.PodRunning, ContainerStatuses: []corev1.ContainerStatus{
				{Name: "orders", RestartCount: 7},
			}},
		},
		&corev1.Pod{ObjectMeta: meta("stranger", "pod-9", owner("ReplicaSet", "other", "rs-9"))},
		event("on-deployment", "Deployment", "orders", t0.Add(-time.Minute)),
		event("on-pod", "Pod", "orders-abc-1", t0.Add(-2*time.Minute)),
		event("on-stranger", "Pod", "stranger", t0.Add(-time.Minute)),
		event("too-old", "Pod", "orders-abc-1", t0.Add(-3*time.Hour)),
	)
}

func find(t *testing.T, name string) evidence.Tool {
	t.Helper()
	for _, tl := range Tools(demoCluster()) {
		if tl.Name() == name {
			return tl
		}
	}
	t.Fatalf("no tool %s", name)
	return nil
}

func TestWorkloadStateReturnsSpecStatusAndOwnedPods(t *testing.T) {
	raw, err := find(t, "workload_state").Run(context.Background(), evidence.Query{Tool: "workload_state", Target: orders})
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Spec   appsv1.DeploymentSpec   `json:"spec"`
		Status appsv1.DeploymentStatus `json:"status"`
		Pods   []struct {
			Name       string                   `json:"name"`
			Phase      string                   `json:"phase"`
			Containers []corev1.ContainerStatus `json:"containers"`
		} `json:"pods"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.Spec.Replicas == nil || *got.Spec.Replicas != 2 {
		t.Errorf("spec = %+v, want 2 replicas", got.Spec)
	}
	if len(got.Status.Conditions) != 1 || got.Status.Conditions[0].Reason != "MinimumReplicasUnavailable" {
		t.Errorf("conditions = %+v", got.Status.Conditions)
	}
	if len(got.Pods) != 1 || got.Pods[0].Name != "orders-abc-1" || got.Pods[0].Phase != "Running" {
		t.Fatalf("pods = %+v, want only orders-abc-1", got.Pods)
	}
	if c := got.Pods[0].Containers; len(c) != 1 || c[0].RestartCount != 7 {
		t.Errorf("containers = %+v", c)
	}
}

func TestEventsCoverTheTargetAndItsPodsInsideTheWindow(t *testing.T) {
	raw, err := find(t, "events").Run(context.Background(), evidence.Query{
		Tool: "events", Target: orders, Window: evidence.Window{Start: t0.Add(-time.Hour), End: t0},
	})
	if err != nil {
		t.Fatal(err)
	}
	var got []struct {
		Message string `json:"message"`
		Reason  string `json:"reason"`
		Kind    string `json:"kind"`
		Name    string `json:"name"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Message != "on-pod" || got[1].Message != "on-deployment" {
		t.Fatalf("events = %+v, want on-pod then on-deployment", got)
	}
	if got[0].Reason != "BackOff" || got[0].Kind != "Pod" || got[0].Name != "orders-abc-1" {
		t.Errorf("event = %+v", got[0])
	}
}

func TestToolsRefuseWhatTheyCannotRead(t *testing.T) {
	for name, target := range map[string]signal.Target{
		"not found":    {Namespace: "demo", Kind: "Deployment", Name: "ghost"},
		"unknown kind": {Namespace: "demo", Kind: "Rollout", Name: "orders"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := find(t, "workload_state").Run(context.Background(), evidence.Query{Target: target}); err == nil {
				t.Fatal("want an error")
			} else if strings.Contains(name, "kind") && !strings.Contains(err.Error(), "Rollout") {
				t.Errorf("err = %v, want it to name the kind", err)
			}
		})
	}
}
