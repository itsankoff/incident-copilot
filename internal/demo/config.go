package demo

import (
	"errors"
	"fmt"
	"strconv"
	"time"
)

// Role selects which part of the demo topology the process runs.
type Role string

const (
	RoleOrders    Role = "orders"
	RoleInventory Role = "inventory"
	RoleLoadgen   Role = "loadgen"
)

// Config is the validated runtime configuration of demo-svc.
type Config struct {
	Role        Role
	ListenAddr  string // API, /healthz, /readyz
	MetricsAddr string // /metrics

	// orders
	InventoryURL     string
	InventoryTimeout time.Duration
	OrderTTL         time.Duration

	// loadgen
	TargetURL string
	RPS       float64
	ReadRatio float64
}

// LoadConfig builds a Config for role from environment variables read through
// getenv. It fails on unknown roles and malformed values so a bad config
// crashes the process at startup, the way a real service would.
func LoadConfig(role string, getenv func(string) string) (Config, error) {
	env := func(key, def string) string {
		if v := getenv(key); v != "" {
			return v
		}
		return def
	}

	cfg := Config{
		Role:        Role(role),
		ListenAddr:  env("LISTEN_ADDR", ":8080"),
		MetricsAddr: env("METRICS_ADDR", ":9090"),
	}

	var err error
	switch cfg.Role {
	case RoleOrders:
		cfg.InventoryURL = env("INVENTORY_URL", "http://inventory:8080")
		if cfg.InventoryTimeout, err = parseDuration("INVENTORY_TIMEOUT", env("INVENTORY_TIMEOUT", "1s")); err != nil {
			return Config{}, err
		}
		if cfg.OrderTTL, err = parseDuration("ORDER_TTL", env("ORDER_TTL", "15m")); err != nil {
			return Config{}, err
		}
	case RoleInventory:
	case RoleLoadgen:
		cfg.TargetURL = env("TARGET_URL", "http://orders:8080")
		if cfg.RPS, err = parseFloat("RPS", env("RPS", "20")); err != nil {
			return Config{}, err
		}
		if cfg.RPS <= 0 {
			return Config{}, fmt.Errorf("RPS: must be positive, got %v", cfg.RPS)
		}
		if cfg.ReadRatio, err = parseFloat("READ_RATIO", env("READ_RATIO", "0.8")); err != nil {
			return Config{}, err
		}
		if cfg.ReadRatio < 0 || cfg.ReadRatio > 1 {
			return Config{}, fmt.Errorf("READ_RATIO: must be within [0,1], got %v", cfg.ReadRatio)
		}
	case "":
		return Config{}, errors.New("role: required (orders | inventory | loadgen)")
	default:
		return Config{}, fmt.Errorf("role: unknown %q (orders | inventory | loadgen)", role)
	}
	return cfg, nil
}

func parseDuration(key, v string) (time.Duration, error) {
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", key, err)
	}
	return d, nil
}

func parseFloat(key, v string) (float64, error) {
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", key, err)
	}
	return f, nil
}
