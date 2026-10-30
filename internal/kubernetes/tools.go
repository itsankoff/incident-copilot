// Package kubernetes is the Kubernetes side of the platform seam: the Sensor's
// evidence tools (ADR 0002 D6). Only main imports it.
package kubernetes

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/itsankoff/incident-copilot/internal/evidence"
	"github.com/itsankoff/incident-copilot/internal/signal"
)

// InCluster returns the tools, reading the API with the pod's ServiceAccount.
func InCluster() ([]evidence.Tool, error) {
	cfg, err := rest.InClusterConfig()
	if err != nil {
		return nil, err
	}
	c, err := clientset.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}
	return Tools(c), nil
}

// Tools returns the evidence tools reading through c.
func Tools(c clientset.Interface) []evidence.Tool {
	return []evidence.Tool{
		tool{"workload_state", c, workloadState},
		tool{"events", c, events},
	}
}

type tool struct {
	name string
	c    clientset.Interface
	run  func(ctx context.Context, c clientset.Interface, q evidence.Query) (any, error)
}

func (t tool) Name() string { return t.name }

func (t tool) Run(ctx context.Context, q evidence.Query) (json.RawMessage, error) {
	v, err := t.run(ctx, t.c, q)
	if err != nil {
		return nil, err
	}
	return json.Marshal(v)
}

type workload struct {
	uid    types.UID
	spec   any
	status any
	// through lists the objects between the Target and its pods.
	through []object
}

type object struct {
	kind, name string
	uid        types.UID
}

func read(ctx context.Context, c clientset.Interface, t signal.Target) (workload, error) {
	get := metav1.GetOptions{}
	list := metav1.ListOptions{}
	switch t.Kind {
	case "Deployment":
		d, err := c.AppsV1().Deployments(t.Namespace).Get(ctx, t.Name, get)
		if err != nil {
			return workload{}, err
		}
		rs, err := c.AppsV1().ReplicaSets(t.Namespace).List(ctx, list)
		if err != nil {
			return workload{}, err
		}
		w := workload{uid: d.UID, spec: d.Spec, status: d.Status}
		for i := range rs.Items {
			if ownedBy(&rs.Items[i], d.UID) {
				w.through = append(w.through, object{"ReplicaSet", rs.Items[i].Name, rs.Items[i].UID})
			}
		}
		return w, nil
	case "StatefulSet":
		s, err := c.AppsV1().StatefulSets(t.Namespace).Get(ctx, t.Name, get)
		if err != nil {
			return workload{}, err
		}
		return workload{uid: s.UID, spec: s.Spec, status: s.Status}, nil
	case "DaemonSet":
		d, err := c.AppsV1().DaemonSets(t.Namespace).Get(ctx, t.Name, get)
		if err != nil {
			return workload{}, err
		}
		return workload{uid: d.UID, spec: d.Spec, status: d.Status}, nil
	case "Job":
		j, err := c.BatchV1().Jobs(t.Namespace).Get(ctx, t.Name, get)
		if err != nil {
			return workload{}, err
		}
		return workload{uid: j.UID, spec: j.Spec, status: j.Status}, nil
	case "CronJob":
		cj, err := c.BatchV1().CronJobs(t.Namespace).Get(ctx, t.Name, get)
		if err != nil {
			return workload{}, err
		}
		jobs, err := c.BatchV1().Jobs(t.Namespace).List(ctx, list)
		if err != nil {
			return workload{}, err
		}
		w := workload{uid: cj.UID, spec: cj.Spec, status: cj.Status}
		for i := range jobs.Items {
			if ownedBy(&jobs.Items[i], cj.UID) {
				w.through = append(w.through, object{"Job", jobs.Items[i].Name, jobs.Items[i].UID})
			}
		}
		return w, nil
	}
	return workload{}, fmt.Errorf("unsupported kind %q", t.Kind)
}

func ownedBy(o metav1.Object, uids ...types.UID) bool {
	for _, ref := range o.GetOwnerReferences() {
		if slices.Contains(uids, ref.UID) {
			return true
		}
	}
	return false
}

// pods returns the Target's pods: those owned by it or by what it owns.
func (w workload) pods(ctx context.Context, c clientset.Interface, namespace string) ([]corev1.Pod, error) {
	list, err := c.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	uids := []types.UID{w.uid}
	for _, o := range w.through {
		uids = append(uids, o.uid)
	}
	var owned []corev1.Pod
	for i := range list.Items {
		if ownedBy(&list.Items[i], uids...) {
			owned = append(owned, list.Items[i])
		}
	}
	return owned, nil
}

type podState struct {
	Name       string                   `json:"name"`
	Phase      corev1.PodPhase          `json:"phase"`
	Node       string                   `json:"node,omitempty"`
	StartTime  *metav1.Time             `json:"startTime,omitempty"`
	Conditions []corev1.PodCondition    `json:"conditions,omitempty"`
	Containers []corev1.ContainerStatus `json:"containers,omitempty"`
}

func workloadState(ctx context.Context, c clientset.Interface, q evidence.Query) (any, error) {
	w, err := read(ctx, c, q.Target)
	if err != nil {
		return nil, err
	}
	pods, err := w.pods(ctx, c, q.Target.Namespace)
	if err != nil {
		return nil, err
	}
	states := make([]podState, 0, len(pods))
	for _, p := range pods {
		states = append(states, podState{
			Name: p.Name, Phase: p.Status.Phase, Node: p.Spec.NodeName, StartTime: p.Status.StartTime,
			Conditions: p.Status.Conditions, Containers: p.Status.ContainerStatuses,
		})
	}
	return map[string]any{"spec": w.spec, "status": w.status, "pods": states}, nil
}

type eventRecord struct {
	Time    time.Time `json:"time"`
	Type    string    `json:"type"`
	Reason  string    `json:"reason"`
	Message string    `json:"message"`
	Kind    string    `json:"kind"`
	Name    string    `json:"name"`
	Count   int32     `json:"count,omitempty"`
}

// events returns events for the Target, what it owns, and its pods, oldest
// first. The pods are today's: events of a deleted pod are left out rather
// than matched by a guessed name.
func events(ctx context.Context, c clientset.Interface, q evidence.Query) (any, error) {
	t := q.Target
	w, err := read(ctx, c, t)
	if err != nil {
		return nil, err
	}
	pods, err := w.pods(ctx, c, t.Namespace)
	if err != nil {
		return nil, err
	}
	about := map[[2]string]bool{{t.Kind, t.Name}: true}
	for _, o := range w.through {
		about[[2]string{o.kind, o.name}] = true
	}
	for _, p := range pods {
		about[[2]string{"Pod", p.Name}] = true
	}

	list, err := c.CoreV1().Events(t.Namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	records := []eventRecord{}
	for i := range list.Items {
		e := &list.Items[i]
		at := eventTime(e)
		if !about[[2]string{e.InvolvedObject.Kind, e.InvolvedObject.Name}] || at.Before(q.Window.Start) || at.After(q.Window.End) {
			continue
		}
		records = append(records, eventRecord{at, e.Type, e.Reason, e.Message, e.InvolvedObject.Kind, e.InvolvedObject.Name, e.Count})
	}
	slices.SortFunc(records, func(a, b eventRecord) int { return a.Time.Compare(b.Time) })
	return records, nil
}

// eventTime is when e last happened; newer event writers set only EventTime.
func eventTime(e *corev1.Event) time.Time {
	switch {
	case !e.LastTimestamp.IsZero():
		return e.LastTimestamp.Time
	case !e.EventTime.IsZero():
		return e.EventTime.Time
	}
	return e.CreationTimestamp.Time
}
