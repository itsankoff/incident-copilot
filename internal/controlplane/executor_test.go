package controlplane

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/itsankoff/incident-copilot/internal/action"
	"github.com/itsankoff/incident-copilot/internal/executor"
	"github.com/itsankoff/incident-copilot/internal/link"
)

// executorUp runs an Executor until the returned function kills it.
func (h *linkHarness) executorUp(location, token string, catalog ...action.Declaration) (kill func()) {
	h.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	e := executor.New(link.NewClient(h.url, location, token), catalog, slog.New(slog.NewTextHandler(io.Discard, nil)))
	e.Retry = 5 * time.Millisecond
	done := make(chan struct{})
	go func() {
		defer close(done)
		e.Run(ctx)
	}()
	kill = func() {
		cancel()
		<-done
	}
	h.t.Cleanup(kill)
	return kill
}

func (h *linkHarness) waitCatalog(location string, want bool) []action.Declaration {
	h.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		c, ok := h.hub.Catalog(location)
		if ok == want {
			return c
		}
		if time.Now().After(deadline) {
			h.t.Fatalf("Catalog(%s) never became %v", location, want)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestTheExecutorDialsOutAndAdvertisesItsCatalog(t *testing.T) {
	h := startLink(t)
	h.hub.PollHold = 10 * time.Millisecond
	if _, ok := h.hub.Catalog("ic-target"); ok {
		t.Fatal("an Executor that never dialled is offered")
	}
	kill := h.executorUp("ic-target", "target-token", rollback)

	got := h.waitCatalog("ic-target", true)
	if len(got) != 1 || got[0].Name != "rollback_deployment" || got[0].Args[0].Maximum != rollback.Args[0].Maximum || got[0].ByHand != rollback.ByHand {
		t.Errorf("catalog = %+v, want the declaration it advertised", got)
	}
	if _, ok := h.hub.Catalog("aks-prod"); ok {
		t.Error("one Location's Executor is offered in another")
	}
	if h.hub.Online("ic-target") {
		t.Error("the Executor's poll brought the Sensor link online")
	}

	kill()
	h.clock.advance(link.OfflineAfter)
	if err := h.hub.Sweep(); err != nil {
		t.Fatal(err)
	}
	h.waitCatalog("ic-target", false)

	online, offline := h.entries("executor.online"), h.entries("executor.offline")
	if len(online) != 1 || !strings.Contains(string(online[0].Data), `"actions":["rollback_deployment"]`) || len(offline) != 1 {
		t.Errorf("executor.online %+v, executor.offline %+v", online, offline)
	}
}

func TestAnExecutorWithAWrongTokenIsRefused(t *testing.T) {
	h := startLink(t)
	h.executorUp("ic-target", "prod-token-x", rollback)
	deadline := time.Now().Add(5 * time.Second)
	for len(h.entries("link.refused")) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("no link.refused entry")
		}
		time.Sleep(time.Millisecond)
	}
	if _, ok := h.hub.Catalog("ic-target"); ok {
		t.Error("a refused Executor is offered")
	}
}
