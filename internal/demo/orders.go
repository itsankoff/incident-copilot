package demo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"time"
)

// Inventory is the orders service's view of its downstream dependency.
type Inventory interface {
	Stock(ctx context.Context, sku string) (StockLevel, error)
}

// HTTPInventory calls the inventory service over HTTP.
type HTTPInventory struct {
	baseURL string
	client  *http.Client
}

// NewHTTPInventory returns an Inventory client with a per-call timeout.
func NewHTTPInventory(baseURL string, timeout time.Duration) *HTTPInventory {
	return &HTTPInventory{baseURL: baseURL, client: &http.Client{Timeout: timeout}}
}

// Stock fetches the stock level of sku.
func (c *HTTPInventory) Stock(ctx context.Context, sku string) (StockLevel, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/stock/"+url.PathEscape(sku), nil)
	if err != nil {
		return StockLevel{}, err
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return StockLevel{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return StockLevel{}, fmt.Errorf("inventory returned %d", resp.StatusCode)
	}
	var s StockLevel
	if err := json.NewDecoder(resp.Body).Decode(&s); err != nil {
		return StockLevel{}, fmt.Errorf("decode inventory response: %w", err)
	}
	return s, nil
}

// Order is the orders API response.
type Order struct {
	ID        string `json:"id"`
	SKU       string `json:"sku"`
	Available int    `json:"available"`
}

type ordersAPI struct {
	inv     Inventory
	metrics *Metrics
	logger  *slog.Logger
}

// registerOrders mounts the orders API: GET /orders/{id} and POST /orders.
func registerOrders(mux *http.ServeMux, inv Inventory, m *Metrics, logger *slog.Logger) {
	a := &ordersAPI{inv: inv, metrics: m, logger: logger}
	mux.Handle("GET /orders/{id}", instrument("/orders/{id}", http.HandlerFunc(a.get), m, logger))
	mux.Handle("POST /orders", instrument("/orders", http.HandlerFunc(a.create), m, logger))
}

func (a *ordersAPI) get(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	a.respond(w, r, http.StatusOK, id, "sku-"+id)
}

func (a *ordersAPI) create(w http.ResponseWriter, r *http.Request) {
	var body struct {
		SKU string `json:"sku"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.SKU == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "sku is required"})
		return
	}
	a.respond(w, r, http.StatusCreated, requestID(r), body.SKU)
}

// respond checks stock for sku and writes the order, mapping dependency
// failures to 504 (timeout) or 502 (any other error).
func (a *ordersAPI) respond(w http.ResponseWriter, r *http.Request, status int, id, sku string) {
	start := time.Now()
	stock, err := a.inv.Stock(r.Context(), sku)
	elapsed := time.Since(start)

	switch {
	case err == nil:
		a.metrics.ObserveDependency("inventory", "ok", elapsed)
		writeJSON(w, status, Order{ID: id, SKU: sku, Available: stock.Available})
	case isTimeout(err):
		a.metrics.ObserveDependency("inventory", "timeout", elapsed)
		a.logger.ErrorContext(r.Context(), "inventory call failed: "+err.Error(), "dependency", "inventory")
		writeJSON(w, http.StatusGatewayTimeout, map[string]string{"error": "inventory timeout"})
	default:
		a.metrics.ObserveDependency("inventory", "error", elapsed)
		a.logger.ErrorContext(r.Context(), "inventory call failed: "+err.Error(), "dependency", "inventory")
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "inventory unavailable"})
	}
}

func isTimeout(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var t interface{ Timeout() bool }
	return errors.As(err, &t) && t.Timeout()
}
