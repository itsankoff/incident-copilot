package kubernetes

import (
	"context"
	"fmt"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	metadatafake "k8s.io/client-go/metadata/fake"

	"github.com/itsankoff/incident-copilot/internal/signal"
)

// controller is a controller owner reference to apiVersion kind name.
func controller(apiVersion, kind, name string) metav1.OwnerReference {
	yes := true
	return metav1.OwnerReference{APIVersion: apiVersion, Kind: kind, Name: name, UID: types.UID(kind + "/" + name), Controller: &yes}
}

// ownerObj is the metadata of an owner object in namespace demo.
func ownerObj(apiVersion, kind, name string, owners ...metav1.OwnerReference) *metav1.PartialObjectMetadata {
	return &metav1.PartialObjectMetadata{
		TypeMeta:   metav1.TypeMeta{APIVersion: apiVersion, Kind: kind},
		ObjectMeta: metav1.ObjectMeta{Namespace: "demo", Name: name, UID: types.UID(kind + "/" + name), OwnerReferences: owners},
	}
}

// crashing is a pod whose container last died at died and now waits in
// CrashLoopBackOff.
func crashing(name string, died time.Time, owners ...metav1.OwnerReference) *corev1.Pod {
	return pod(name, owners, corev1.ContainerStatus{
		Name:                 "app",
		RestartCount:         3,
		State:                corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}},
		LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 1, FinishedAt: metav1.NewTime(died)}},
	})
}

// running is a pod whose container runs, last restarted at restarted, or
// never if restarted is zero.
func running(name string, restarted time.Time, owners ...metav1.OwnerReference) *corev1.Pod {
	cs := corev1.ContainerStatus{Name: "app", State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}
	if !restarted.IsZero() {
		cs.RestartCount = 4
		cs.LastTerminationState.Terminated = &corev1.ContainerStateTerminated{ExitCode: 1, FinishedAt: metav1.NewTime(restarted)}
	}
	return pod(name, owners, cs)
}

func pod(name string, owners []metav1.OwnerReference, cs corev1.ContainerStatus) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: "demo", Name: name, UID: types.UID("Pod/" + name), OwnerReferences: owners},
		Status:     corev1.PodStatus{Phase: corev1.PodRunning, ContainerStatuses: []corev1.ContainerStatus{cs}},
	}
}

// api is a fake Kubernetes API holding pods and owners. Its discovery serves
// apps/v1, batch/v1, and the argoproj.io Rollout CRD.
type api struct {
	t    *testing.T
	pods *fake.Clientset
	meta *metadatafake.FakeMetadataClient
}

func newAPI(t *testing.T, owners ...*metav1.PartialObjectMetadata) *api {
	t.Helper()
	objs := make([]runtime.Object, len(owners))
	for i, o := range owners {
		objs[i] = o
	}
	scheme := metadatafake.NewTestScheme()
	if err := metav1.AddMetaToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	c := fake.NewClientset()
	c.Resources = []*metav1.APIResourceList{
		{GroupVersion: "apps/v1", APIResources: []metav1.APIResource{
			{Name: "deployments", Kind: "Deployment", Namespaced: true},
			{Name: "deployments/status", Kind: "Deployment", Namespaced: true},
			{Name: "replicasets", Kind: "ReplicaSet", Namespaced: true},
			{Name: "statefulsets", Kind: "StatefulSet", Namespaced: true},
		}},
		{GroupVersion: "batch/v1", APIResources: []metav1.APIResource{
			{Name: "jobs", Kind: "Job", Namespaced: true},
			{Name: "cronjobs", Kind: "CronJob", Namespaced: true},
		}},
		{GroupVersion: "argoproj.io/v1alpha1", APIResources: []metav1.APIResource{
			{Name: "rollouts", Kind: "Rollout", Namespaced: true},
		}},
	}
	return &api{t, c, metadatafake.NewSimpleMetadataClient(scheme, objs...)}
}

// set creates or replaces the pod p.
func (a *api) set(p *corev1.Pod) {
	a.t.Helper()
	pods := a.pods.CoreV1().Pods(p.Namespace)
	if _, err := pods.Update(context.Background(), p, metav1.UpdateOptions{}); err == nil {
		return
	}
	if _, err := pods.Create(context.Background(), p, metav1.CreateOptions{}); err != nil {
		a.t.Fatal(err)
	}
}

func (a *api) remove(name string) {
	a.t.Helper()
	if err := a.pods.CoreV1().Pods("demo").Delete(context.Background(), name, metav1.DeleteOptions{}); err != nil {
		a.t.Fatal(err)
	}
}

func (a *api) detector() *CrashLoop {
	return NewCrashLoop(a.pods, a.meta, "ic-target", []string{"demo"})
}

// check runs d at t0+at and returns what changed, one line per Signal.
func check(t *testing.T, d *CrashLoop, at time.Duration) []string {
	t.Helper()
	sigs, err := d.Check(context.Background(), t0.Add(at))
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, s := range sigs {
		if s.Name != "CrashLoopBackOff" || s.Severity != signal.Critical || s.Target == nil || s.Key != "detector:CrashLoopBackOff/"+s.Target.String() {
			t.Errorf("malformed Signal %+v", s)
		}
		line := fmt.Sprintf("%s %s since %s", s.Status, s.Target, s.StartsAt.Sub(t0))
		if s.EndsAt != nil {
			line += fmt.Sprintf(" until %s", s.EndsAt.Sub(t0))
		}
		out = append(out, line)
	}
	return out
}

func same(a, b []string) bool { return fmt.Sprint(a) == fmt.Sprint(b) }

var (
	toRS    = controller("apps/v1", "ReplicaSet", "orders-7d9f")
	toOrder = controller("apps/v1", "Deployment", "orders")
)

// ordersAPI holds Deployment orders and its ReplicaSet.
func ordersAPI(t *testing.T) *api {
	return newAPI(t, ownerObj("apps/v1", "Deployment", "orders"), ownerObj("apps/v1", "ReplicaSet", "orders-7d9f", toOrder))
}

const ordersTarget = "ic-target/demo/Deployment/orders"

func TestCrashLoopFiresAfterAMinuteWaiting(t *testing.T) {
	for _, tc := range []struct {
		name string
		at   time.Duration
		want []string
	}{
		{"59s waiting", 59 * time.Second, nil},
		{"1m waiting", time.Minute, []string{"firing " + ordersTarget + " since 1m0s"}},
		{"5m waiting", 5 * time.Minute, []string{"firing " + ordersTarget + " since 5m0s"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := ordersAPI(t)
			a.set(crashing("orders-7d9f-a", t0, toRS))
			if got := check(t, a.detector(), tc.at); !same(got, tc.want) {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestCrashLoopResolvesAfterTenQuietMinutes(t *testing.T) {
	type step struct {
		at     time.Duration
		change func(a *api) // applied before the Check, if set
		want   []string
	}
	const m = time.Minute
	firing := []string{"firing " + ordersTarget + " since 1m0s"}
	for _, tc := range []struct {
		name  string
		steps []step
	}{
		{"still crash-looping", []step{
			{at: m, want: firing},
			{at: 30 * m},
		}},
		{"a restart keeps it open for ten minutes", []step{
			{at: m, want: firing},
			{at: 2 * m, change: func(a *api) { a.set(running("orders-7d9f-a", t0.Add(90*time.Second), toRS)) }},
			{at: 11*m + 29*time.Second},
			{at: 11*m + 30*time.Second, want: []string{"resolved " + ordersTarget + " since 1m0s until 11m30s"}},
			{at: 12 * m},
		}},
		{"replaced by healthy pods", []step{
			{at: m, want: firing},
			{at: 2 * m, change: func(a *api) { a.remove("orders-7d9f-a"); a.set(running("orders-7d9f-b", time.Time{}, toRS)) }},
			{at: 10*m + 59*time.Second},
			{at: 11 * m, want: []string{"resolved " + ordersTarget + " since 1m0s until 11m0s"}},
		}},
		{"fires again after resolving", []step{
			{at: m, want: firing},
			{at: 2 * m, change: func(a *api) { a.set(running("orders-7d9f-a", t0.Add(90*time.Second), toRS)) }},
			{at: 12 * m, want: []string{"resolved " + ordersTarget + " since 1m0s until 12m0s"}},
			{at: 20 * m, change: func(a *api) { a.set(crashing("orders-7d9f-a", t0.Add(19*m), toRS)) }, want: []string{"firing " + ordersTarget + " since 20m0s"}},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := ordersAPI(t)
			a.set(crashing("orders-7d9f-a", t0, toRS))
			d := a.detector()
			for _, s := range tc.steps {
				if s.change != nil {
					s.change(a)
				}
				if got := check(t, d, s.at); !same(got, s.want) {
					t.Errorf("at %s: got %q, want %q", s.at, got, s.want)
				}
			}
		})
	}
}

func TestCrashLoopTargetIsTheTopControllerOwner(t *testing.T) {
	toRollout := controller("argoproj.io/v1alpha1", "Rollout", "checkout")
	toCron := controller("batch/v1", "CronJob", "nightly")
	for _, tc := range []struct {
		name   string
		owners []*metav1.PartialObjectMetadata
		pod    []metav1.OwnerReference
		want   string
	}{
		{"Deployment through its ReplicaSet", []*metav1.PartialObjectMetadata{
			ownerObj("apps/v1", "Deployment", "orders"), ownerObj("apps/v1", "ReplicaSet", "orders-7d9f", toOrder),
		}, []metav1.OwnerReference{toRS}, ordersTarget},
		{"a CRD through its ReplicaSet", []*metav1.PartialObjectMetadata{
			ownerObj("argoproj.io/v1alpha1", "Rollout", "checkout"), ownerObj("apps/v1", "ReplicaSet", "checkout-5c4b", toRollout),
		}, []metav1.OwnerReference{controller("apps/v1", "ReplicaSet", "checkout-5c4b")}, "ic-target/demo/Rollout/checkout"},
		{"a Pod with no owner", nil, nil, "ic-target/demo/Pod/p"},
		{"a StatefulSet", []*metav1.PartialObjectMetadata{ownerObj("apps/v1", "StatefulSet", "db")},
			[]metav1.OwnerReference{controller("apps/v1", "StatefulSet", "db")}, "ic-target/demo/StatefulSet/db"},
		{"a CronJob through its Job", []*metav1.PartialObjectMetadata{
			ownerObj("batch/v1", "CronJob", "nightly"), ownerObj("batch/v1", "Job", "nightly-29001", toCron),
		}, []metav1.OwnerReference{controller("batch/v1", "Job", "nightly-29001")}, "ic-target/demo/CronJob/nightly"},
		{"a bare ReplicaSet", []*metav1.PartialObjectMetadata{ownerObj("apps/v1", "ReplicaSet", "orders-7d9f")},
			[]metav1.OwnerReference{toRS}, "ic-target/demo/ReplicaSet/orders-7d9f"},
		{"a kind the API does not serve", nil,
			[]metav1.OwnerReference{controller("example.com/v1", "Widget", "w")}, "ic-target/demo/Widget/w"},
		{"an owner already gone", nil, []metav1.OwnerReference{toRS}, "ic-target/demo/ReplicaSet/orders-7d9f"},
		{"a non-controller owner is not followed", []*metav1.PartialObjectMetadata{ownerObj("apps/v1", "ReplicaSet", "orders-7d9f")},
			[]metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "ReplicaSet", Name: "orders-7d9f", UID: "ReplicaSet/orders-7d9f"}}, "ic-target/demo/Pod/p"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := newAPI(t, tc.owners...)
			a.set(crashing("p", t0, tc.pod...))
			want := []string{"firing " + tc.want + " since 1m0s"}
			if got := check(t, a.detector(), time.Minute); !same(got, want) {
				t.Errorf("got %q, want %q", got, want)
			}
		})
	}
}

func TestCrashLoopTenPodsOfOneDeploymentAreOneSignal(t *testing.T) {
	a := ordersAPI(t)
	for i := range 10 {
		a.set(crashing(fmt.Sprintf("orders-7d9f-%d", i), t0, toRS))
	}
	a.set(crashing("loner", t0)) // another Target
	d := a.detector()
	want := []string{"firing ic-target/demo/Deployment/orders since 1m0s", "firing ic-target/demo/Pod/loner since 1m0s"}
	if got := check(t, d, time.Minute); !same(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
	if got := check(t, d, 2*time.Minute); got != nil {
		t.Errorf("a firing Target fired again: %q", got)
	}
}

func TestCrashLoopWatchesOnlyItsNamespaces(t *testing.T) {
	a := ordersAPI(t)
	p := crashing("elsewhere", t0)
	p.Namespace = "kube-system"
	a.set(p)
	if got := check(t, a.detector(), time.Minute); got != nil {
		t.Errorf("a pod outside the allowlist fired: %q", got)
	}
}
