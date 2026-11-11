package kubernetes

import (
	"context"
	"slices"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/metadata"

	"github.com/itsankoff/incident-copilot/internal/signal"
)

// The CrashLoopBackOff rule (ADR 0002 D5).
const (
	crashLoop    = "CrashLoopBackOff"
	fireAfter    = time.Minute      // waiting in CrashLoopBackOff this long fires
	resolveAfter = 10 * time.Minute // no crash loop or restart this long resolves
	maxOwners    = 10               // owner chains longer than this are cut
)

// CrashLoop is the CrashLoopBackOff Detector. It fires when a live container
// has waited in CrashLoopBackOff for a minute, and resolves when no live
// container of the Target has been in CrashLoopBackOff or restarted for ten.
// It reads only the Kubernetes API: Pods, and the metadata of their owners.
type CrashLoop struct {
	pods       clientset.Interface
	meta       metadata.Interface
	location   string
	namespaces []string

	resources map[string]*metav1.APIResourceList // by apiVersion
	firing    map[signal.Target]episode
}

type episode struct{ startsAt, lastBad time.Time }

// NewCrashLoop returns the Detector for location's namespaces. It reads Pods
// through pods, and owners through meta with pods' discovery.
func NewCrashLoop(pods clientset.Interface, meta metadata.Interface, location string, namespaces []string) *CrashLoop {
	return &CrashLoop{pods: pods, meta: meta, location: location, namespaces: namespaces,
		resources: map[string]*metav1.APIResourceList{}, firing: map[signal.Target]episode{}}
}

// Check reads the Location's Pods at now and returns the Signals that started
// firing or resolved since the last Check. A Target that is firing stays
// firing until it resolves, so a failed Check changes nothing.
func (d *CrashLoop) Check(ctx context.Context, now time.Time) ([]signal.Signal, error) {
	type state struct {
		fire    bool
		lastBad time.Time
	}
	seen := map[signal.Target]state{}
	for _, ns := range d.namespaces {
		list, err := d.pods.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{})
		if err != nil {
			return nil, err
		}
		for i := range list.Items {
			p := &list.Items[i]
			fire, lastBad := crashState(p, now)
			if now.Sub(lastBad) >= resolveAfter {
				continue // nothing recent on this pod
			}
			t, err := d.target(ctx, p)
			if err != nil {
				return nil, err
			}
			s := seen[t]
			s.fire = s.fire || fire
			s.lastBad = later(s.lastBad, lastBad)
			seen[t] = s
		}
	}

	var out []signal.Signal
	for t, s := range seen {
		if e, ok := d.firing[t]; ok {
			e.lastBad = later(e.lastBad, s.lastBad)
			d.firing[t] = e
		} else if s.fire {
			d.firing[t] = episode{startsAt: now, lastBad: s.lastBad}
			out = append(out, crashSignal(t, signal.Firing, now, nil))
		}
	}
	for t, e := range d.firing {
		if now.Sub(e.lastBad) >= resolveAfter {
			delete(d.firing, t)
			out = append(out, crashSignal(t, signal.Resolved, e.startsAt, &now))
		}
	}
	slices.SortFunc(out, func(a, b signal.Signal) int { return strings.Compare(a.Key, b.Key) })
	return out, nil
}

// crashState reports whether a container of p has waited in CrashLoopBackOff
// for fireAfter, and when p last had one in CrashLoopBackOff or restarting:
// now if one is waiting, else its last termination, else never (zero).
//
// A container in CrashLoopBackOff waits from the end of its last run, so its
// wait is read from the API and survives a Sensor restart.
func crashState(p *corev1.Pod, now time.Time) (fire bool, lastBad time.Time) {
	for _, cs := range slices.Concat(p.Status.InitContainerStatuses, p.Status.ContainerStatuses) {
		var died time.Time
		if term := cs.LastTerminationState.Terminated; term != nil {
			died = term.FinishedAt.Time
		}
		if w := cs.State.Waiting; w != nil && w.Reason == crashLoop {
			lastBad = now
			fire = fire || !died.IsZero() && now.Sub(died) >= fireAfter
			continue
		}
		lastBad = later(lastBad, died)
	}
	return fire, lastBad
}

func later(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}

func crashSignal(t signal.Target, status signal.Status, startsAt time.Time, endsAt *time.Time) signal.Signal {
	return signal.Signal{
		Key: signal.DetectorKey(crashLoop, t), Name: crashLoop, Status: status, SourceState: string(status),
		Severity: signal.Critical, SeverityRaw: string(signal.Critical), StartsAt: startsAt, EndsAt: endsAt, Target: &t,
	}
}

// target follows p's controller owners to the top one, whatever its kind; a
// Pod with no owner is its own Target. An owner the Sensor cannot read (a kind
// the API does not serve it, a read it may not make, or an owner already
// gone) is the top owner known. A cluster-scoped owner, such as the Node of a
// static Pod, is no workload and is not followed.
func (d *CrashLoop) target(ctx context.Context, p *corev1.Pod) (signal.Target, error) {
	t := signal.Target{Platform: "kubernetes", Location: d.location, Namespace: p.Namespace, Kind: "Pod", Name: p.Name}
	ref := metav1.GetControllerOfNoCopy(p)
	for range maxOwners {
		if ref == nil {
			break
		}
		res, err := d.resource(ref.APIVersion, ref.Kind)
		if err != nil {
			return t, err
		}
		if res != nil && !res.Namespaced {
			return t, nil
		}
		t.Kind, t.Name = ref.Kind, ref.Name
		if res == nil {
			return t, nil
		}
		gv, _ := schema.ParseGroupVersion(ref.APIVersion) // discovery served it, so it parses
		owner, err := d.meta.Resource(gv.WithResource(res.Name)).Namespace(p.Namespace).Get(ctx, ref.Name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) || apierrors.IsForbidden(err) {
			return t, nil
		}
		if err != nil {
			return t, err
		}
		ref = metav1.GetControllerOfNoCopy(owner)
	}
	return t, nil
}

// resource is the API resource serving kind in apiVersion, or nil if none.
func (d *CrashLoop) resource(apiVersion, kind string) (*metav1.APIResource, error) {
	list, ok := d.resources[apiVersion]
	if !ok {
		var err error
		list, err = d.pods.Discovery().ServerResourcesForGroupVersion(apiVersion)
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		d.resources[apiVersion] = list
	}
	for i, r := range list.APIResources {
		if r.Kind == kind && !strings.Contains(r.Name, "/") { // not a subresource
			return &list.APIResources[i], nil
		}
	}
	return nil, nil
}
