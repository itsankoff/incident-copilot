package demo

import (
	"encoding/json"
	"hash/fnv"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"time"
)

// registerInventory mounts the inventory API: GET /stock/{sku}.
func registerInventory(mux *http.ServeMux, m *Metrics, logger *slog.Logger) {
	mux.Handle("GET /stock/{sku}", instrument("/stock/{sku}", http.HandlerFunc(stockHandler), m, logger))
}

func stockHandler(w http.ResponseWriter, r *http.Request) {
	// Simulated lookup cost: 5-20 ms.
	time.Sleep(5*time.Millisecond + rand.N(15*time.Millisecond))

	sku := r.PathValue("sku")
	h := fnv.New32a()
	_, _ = h.Write([]byte(sku))
	writeJSON(w, http.StatusOK, StockLevel{SKU: sku, Available: int(h.Sum32() % 500)})
}

// StockLevel is the inventory API response.
type StockLevel struct {
	SKU       string `json:"sku"`
	Available int    `json:"available"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
