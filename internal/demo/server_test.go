package demo

import (
	"bytes"
	"context"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// freeAddr returns a loopback address with a port that was free a moment ago.
func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	return l.Addr().String()
}

// syncBuffer is a bytes.Buffer safe for the concurrent writes of a logger.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func TestRunAdminAPI(t *testing.T) {
	cfg := Config{Role: RoleInventory, ListenAddr: freeAddr(t), MetricsAddr: freeAddr(t), AdminAddr: freeAddr(t)}
	var logs syncBuffer
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(ctx, cfg, slog.New(slog.NewJSONHandler(&logs, nil)), BuildInfo{}) }()

	url := "http://" + cfg.AdminAddr + "/admin-probe"
	var resp *http.Response
	var err error
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if resp, err = http.Get(url); err == nil {
			break
		}
	}
	if err != nil {
		cancel()
		t.Fatalf("admin API not reachable: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown admin path: status %d, want 404", resp.StatusCode)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(shutdownTimeout + time.Second):
		t.Fatal("Run did not return after cancel")
	}
	if _, err := http.Get(url); err == nil {
		t.Error("admin API still serving after shutdown")
	}
	if strings.Contains(logs.String(), "admin-probe") {
		t.Errorf("admin request was logged: %s", logs.String())
	}
}
