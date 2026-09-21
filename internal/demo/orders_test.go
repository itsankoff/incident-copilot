package demo

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type fakeInventory struct {
	level StockLevel
	err   error
}

func (f fakeInventory) Stock(context.Context, string) (StockLevel, error) { return f.level, f.err }

func newOrdersServer(t *testing.T, inv Inventory) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	registerOrders(mux, inv, NewMetrics("orders", BuildInfo{}), slog.New(slog.NewJSONHandler(io.Discard, nil)))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestOrdersStatusMapping(t *testing.T) {
	tests := []struct {
		name string
		inv  fakeInventory
		want int
	}{
		{"ok", fakeInventory{level: StockLevel{SKU: "sku-7", Available: 3}}, http.StatusOK},
		{"timeout", fakeInventory{err: context.DeadlineExceeded}, http.StatusGatewayTimeout},
		{"error", fakeInventory{err: errors.New("connection refused")}, http.StatusBadGateway},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := newOrdersServer(t, tt.inv)
			resp, err := http.Get(srv.URL + "/orders/7")
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != tt.want {
				t.Fatalf("status = %d, want %d", resp.StatusCode, tt.want)
			}
		})
	}
}

func TestCreateOrderRequiresSKU(t *testing.T) {
	srv := newOrdersServer(t, fakeInventory{})
	resp, err := http.Post(srv.URL+"/orders", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

func TestHTTPInventoryAgainstStockHandler(t *testing.T) {
	mux := http.NewServeMux()
	registerInventory(mux, NewMetrics("inventory", BuildInfo{}), slog.New(slog.NewJSONHandler(io.Discard, nil)))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	got, err := NewHTTPInventory(srv.URL, time.Second).Stock(context.Background(), "sku-1")
	if err != nil {
		t.Fatalf("Stock: %v", err)
	}
	if got.SKU != "sku-1" {
		t.Errorf("SKU = %q, want sku-1", got.SKU)
	}
}

func TestHTTPInventoryTimeoutIsDetected(t *testing.T) {
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		_ = json.NewEncoder(w).Encode(StockLevel{})
	}))
	defer slow.Close()

	_, err := NewHTTPInventory(slow.URL, 20*time.Millisecond).Stock(context.Background(), "x")
	if !isTimeout(err) {
		t.Fatalf("isTimeout(%v) = false, want true", err)
	}
}
