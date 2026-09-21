package demo

import (
	"strings"
	"testing"
	"time"
)

func envMap(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestLoadConfigDefaults(t *testing.T) {
	cfg, err := LoadConfig("orders", envMap(nil))
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.InventoryURL != "http://inventory:8080" || cfg.InventoryTimeout != time.Second || cfg.OrderTTL != 15*time.Minute {
		t.Errorf("unexpected orders defaults: %+v", cfg)
	}

	cfg, err = LoadConfig("loadgen", envMap(nil))
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.RPS != 20 || cfg.ReadRatio != 0.8 || cfg.TargetURL != "http://orders:8080" {
		t.Errorf("unexpected loadgen defaults: %+v", cfg)
	}
}

func TestLoadConfigErrors(t *testing.T) {
	tests := []struct {
		name    string
		role    string
		env     map[string]string
		wantErr string
	}{
		{"missing role", "", nil, "role: required"},
		{"unknown role", "payments", nil, `role: unknown "payments"`},
		{"malformed ORDER_TTL", "orders", map[string]string{"ORDER_TTL": "15x"}, `ORDER_TTL: time: unknown unit "x"`},
		{"malformed INVENTORY_TIMEOUT", "orders", map[string]string{"INVENTORY_TIMEOUT": "soon"}, "INVENTORY_TIMEOUT"},
		{"zero RPS", "loadgen", map[string]string{"RPS": "0"}, "RPS: must be positive"},
		{"READ_RATIO out of range", "loadgen", map[string]string{"READ_RATIO": "1.5"}, "READ_RATIO"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := LoadConfig(tt.role, envMap(tt.env))
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("LoadConfig error = %v, want containing %q", err, tt.wantErr)
			}
		})
	}
}
