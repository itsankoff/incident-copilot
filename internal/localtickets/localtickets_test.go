package localtickets

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var t0 = time.Date(2026, 11, 11, 9, 30, 0, 0, time.UTC)

func open(t *testing.T, path string) *Tickets {
	t.Helper()
	tk, err := Open(path, func() time.Time { return t0 })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tk.Close() })
	return tk
}

func get(t *testing.T, h http.Handler, method, path string) (int, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
	b, _ := io.ReadAll(rec.Body)
	return rec.Code, string(b)
}

func TestTicketsAndCommentsAreShownOnTheReadOnlyPage(t *testing.T) {
	ctx := context.Background()
	tk := open(t, filepath.Join(t.TempDir(), "tickets.db"))
	ref, err := tk.Create(ctx, "KubePodCrashLooping on ic-target/demo/Deployment/orders", "**Target:** `ic-target/demo/Deployment/orders`\n\n- one Signal\n")
	if err != nil {
		t.Fatal(err)
	}
	other, err := tk.Create(ctx, "OrdersErrorRate on ic-target/demo/Deployment/inventory", "body")
	if err != nil {
		t.Fatal(err)
	}
	if ref != "1" || other != "2" {
		t.Fatalf("refs %q, %q, want 1 and 2", ref, other)
	}
	if err := tk.Comment(ctx, ref, "## Root-cause analysis\n\n**Confidence:** high\n"); err != nil {
		t.Fatal(err)
	}
	if got := tk.URL(ref); got != "/tickets/1" {
		t.Errorf("URL(1) = %q, want /tickets/1", got)
	}

	h := tk.Handler()
	code, list := get(t, h, http.MethodGet, "/tickets")
	if code != http.StatusOK {
		t.Fatalf("GET /tickets: %d", code)
	}
	for _, want := range []string{"KubePodCrashLooping on ic-target/demo/Deployment/orders", "OrdersErrorRate", `href="/tickets/1"`, "demo purposes only", "GitHub Issues", "ServiceNow"} {
		if !strings.Contains(list, want) {
			t.Errorf("ticket list lacks %q:\n%s", want, list)
		}
	}

	code, page := get(t, h, http.MethodGet, "/tickets/1")
	if code != http.StatusOK {
		t.Fatalf("GET /tickets/1: %d", code)
	}
	// Bodies and comments read as markdown does in a tracker.
	for _, want := range []string{"<strong>Target:</strong>", "<code>ic-target/demo/Deployment/orders</code>", "<h2>Root-cause analysis</h2>", "demo purposes only"} {
		if !strings.Contains(page, want) {
			t.Errorf("ticket page lacks %q:\n%s", want, page)
		}
	}
	if strings.Contains(page, "OrdersErrorRate") {
		t.Error("ticket 1's page shows ticket 2")
	}

	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		if code, _ := get(t, h, method, "/tickets/1"); code != http.StatusMethodNotAllowed {
			t.Errorf("%s /tickets/1: %d, want 405", method, code)
		}
	}
	if code, _ := get(t, h, http.MethodGet, "/tickets/9"); code != http.StatusNotFound {
		t.Errorf("GET /tickets/9: %d, want 404", code)
	}
}

func TestTicketsSurviveARestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "tickets.db")
	first, err := Open(path, func() time.Time { return t0 })
	if err != nil {
		t.Fatal(err)
	}
	ref, err := first.Create(ctx, "before restart", "body")
	if err != nil {
		t.Fatal(err)
	}
	_ = first.Close()

	tk := open(t, path)
	if err := tk.Comment(ctx, ref, "after restart"); err != nil {
		t.Fatal(err)
	}
	if next, _ := tk.Create(ctx, "second", "body"); next != "2" {
		t.Errorf("next ref %q, want 2", next)
	}
	if _, page := get(t, tk.Handler(), http.MethodGet, "/tickets/"+ref); !strings.Contains(page, "after restart") {
		t.Errorf("comment after restart missing:\n%s", page)
	}
}

func TestCommentOnAnUnknownTicketFails(t *testing.T) {
	tk := open(t, filepath.Join(t.TempDir(), "tickets.db"))
	if err := tk.Comment(context.Background(), "7", "body"); err == nil {
		t.Error("comment on ticket 7, which does not exist, succeeded")
	}
}

// Ticket text carries values from Alerts, such as a Signal's source link.
func TestTicketTextCannotInjectMarkupOrScript(t *testing.T) {
	ctx := context.Background()
	tk := open(t, filepath.Join(t.TempDir(), "tickets.db"))
	ref, err := tk.Create(ctx, "<script>alert(1)</script>", "<img src=x onerror=alert(1)> [source](javascript:alert(1))")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/tickets", "/tickets/" + ref} {
		_, page := get(t, tk.Handler(), http.MethodGet, path)
		for _, bad := range []string{"<script>alert", "<img", "javascript:"} {
			if strings.Contains(page, bad) {
				t.Errorf("%s contains %q:\n%s", path, bad, page)
			}
		}
	}
}
