package controlplane

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/itsankoff/incident-copilot/internal/link"
	"github.com/itsankoff/incident-copilot/internal/sensor"
	"github.com/itsankoff/incident-copilot/internal/signal"
)

// detected is a Detector that reports its Signals once.
type detected []signal.Signal

func (d *detected) Check(context.Context, time.Time) ([]signal.Signal, error) {
	out := *d
	*d = nil
	return out, nil
}

func crashLoopAt(location string) signal.Signal {
	t := signal.Target{Platform: "kubernetes", Location: location, Namespace: "demo", Kind: "Deployment", Name: "orders"}
	return signal.Signal{
		Key: signal.DetectorKey("CrashLoopBackOff", t), Name: "CrashLoopBackOff", Status: signal.Firing, SourceState: "firing",
		Severity: signal.Critical, StartsAt: t0, Target: &t,
	}
}

// detect serves h's Control plane's end of the Sensor links for Locations
// prod and staging, wired as main wires it, and sends sigs from location's
// Sensor as its Detectors would.
func (h *harness) detect(location string, sigs ...signal.Signal) {
	h.t.Helper()
	hub := link.NewHub(map[string]string{"prod": "prod-sensor", "staging": "staging-sensor"}, h.log, func() time.Time { return h.now })
	hub.Signals = h.cp.SensorSignal
	srv := httptest.NewServer(hub.Handler())
	defer srv.Close()
	d := detected(sigs)
	s := sensor.New(link.NewClient(srv.URL, location, location+"-sensor"), nil, []sensor.Detector{&d}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	s.Detect(context.Background(), h.now)
}

func TestDetectorSignalForAnotherLocationIsRejected(t *testing.T) {
	h := newHarness(t, t.TempDir(), &fakeTickets{})
	h.detect("staging", crashLoopAt("prod"))

	if len(h.tickets.created) != 0 {
		t.Fatalf("%d tickets, want 0", len(h.tickets.created))
	}
	open, err := h.store.Incidents(context.Background())
	if err != nil || len(open) != 0 {
		t.Fatalf("open Incidents %v (err %v), want none", open, err)
	}
	log := h.auditLog()
	if n := len(ofType(log, "signal.received")); n != 0 {
		t.Errorf("%d signal.received, want 0", n)
	}
	rejected := ofType(log, "signal.rejected")
	if len(rejected) != 1 {
		t.Fatalf("%d signal.rejected, want 1", len(rejected))
	}
	for _, want := range []string{`"instance":"staging"`, `"location":"prod"`, "another Location"} {
		if !strings.Contains(string(rejected[0].Data), want) {
			t.Errorf("rejection %s does not hold %s", rejected[0].Data, want)
		}
	}
}

func TestAlertAndDetectorSignalOnOneDeploymentAreOneIncident(t *testing.T) {
	alertFirst := func(h *harness) {
		h.send(tokenProd, alert("PodCrashLooping", "aaa", map[string]string{"namespace": "demo", "deployment": "orders"}))
		h.detect("prod", crashLoopAt("prod"))
	}
	detectorFirst := func(h *harness) {
		h.detect("prod", crashLoopAt("prod"))
		h.send(tokenProd, alert("PodCrashLooping", "aaa", map[string]string{"namespace": "demo", "deployment": "orders"}))
	}
	for name, send := range map[string]func(*harness){"alert first": alertFirst, "detector first": detectorFirst} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, t.TempDir(), &fakeTickets{})
			send(h)

			if len(h.tickets.created) != 1 || len(h.tickets.comments) != 1 {
				t.Fatalf("%d tickets, %d comments, want 1 and 1", len(h.tickets.created), len(h.tickets.comments))
			}
			if title := h.tickets.created[0].title; !strings.Contains(title, "prod/demo/Deployment/orders") {
				t.Errorf("title %q does not name the Deployment", title)
			}
			open, err := h.store.Incidents(context.Background())
			if err != nil || len(open) != 1 || len(open[0].Members) != 2 {
				t.Fatalf("open Incidents %+v (err %v), want one with two members", open, err)
			}

			var detector *signal.Signal
			for _, e := range ofType(h.auditLog(), "signal.received") {
				var s signal.Signal
				if err := json.Unmarshal(e.Data, &s); err != nil {
					t.Fatal(err)
				}
				if s.Source.Kind == "sensor" {
					detector = &s
				}
			}
			if detector == nil {
				t.Fatal("the Detector Signal was not audited")
			}
			if detector.Source.Instance != "prod" || detector.Key != "detector:CrashLoopBackOff/prod/demo/Deployment/orders" ||
				!detector.ReceivedAt.Equal(h.now) || len(detector.Raw) == 0 {
				t.Errorf("audited Detector Signal: source %v key %q receivedAt %v raw %d bytes", detector.Source, detector.Key, detector.ReceivedAt, len(detector.Raw))
			}
		})
	}
}
