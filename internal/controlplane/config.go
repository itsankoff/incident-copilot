package controlplane

import (
	"fmt"
	"slices"
	"strings"

	"github.com/itsankoff/incident-copilot/internal/signal"
)

// Config is the Control plane's configuration. Packaging and the
// configuration model are still to be decided (PRD placeholders), so for now
// it is one Alertmanager Signal source and one GitHub repository.
type Config struct {
	ListenAddr   string
	StatePath    string // SQLite
	AuditPath    string // hash-chained JSONL
	Alertmanager signal.Source
	GitHub       *GitHub // nil: the fake ticketing adapter
	Locations    []Location
	DebugAddr    string // loopback only: see Evidence.DebugHandler
}

// Location is one watched place, its Environment, and the static token its
// Sensor presents. A Signal can change neither name nor Environment.
type Location struct {
	Name        string
	Environment string
	Token       string
}

// GitHub is the GitHub Issues ticketing configuration.
type GitHub struct {
	APIURL string
	Repo   string // owner/name
	Token  string
}

// LoadConfig builds a Config from environment variables read through getenv.
func LoadConfig(getenv func(string) string) (Config, error) {
	env := func(key, def string) string {
		if v := getenv(key); v != "" {
			return v
		}
		return def
	}
	var missing error
	need := func(key string) string {
		v := getenv(key)
		if v == "" && missing == nil {
			missing = fmt.Errorf("%s: required", key)
		}
		return v
	}

	cfg := Config{
		ListenAddr: env("IC_LISTEN_ADDR", ":8080"),
		StatePath:  env("IC_STATE_PATH", "incident-copilot.db"),
		AuditPath:  env("IC_AUDIT_PATH", "audit.jsonl"),
		DebugAddr:  env("IC_DEBUG_ADDR", "127.0.0.1:8081"),
		Alertmanager: signal.Source{
			Kind:     "alertmanager",
			Instance: need("IC_ALERTMANAGER_INSTANCE"),
			Tokens:   strings.Split(need("IC_ALERTMANAGER_TOKEN"), ","),
			Location: need("IC_ALERTMANAGER_LOCATION"),
			Mapping:  signal.KubePrometheus,
		},
	}
	if missing != nil {
		return Config{}, missing
	}
	switch repo, token := getenv("IC_GITHUB_REPO"), getenv("IC_GITHUB_TOKEN"); {
	case repo != "" && token != "":
		cfg.GitHub = &GitHub{APIURL: env("IC_GITHUB_API_URL", "https://api.github.com"), Repo: repo, Token: token}
	case repo != "" || token != "":
		return Config{}, fmt.Errorf("IC_GITHUB_REPO and IC_GITHUB_TOKEN: set both, or neither for the fake ticketing adapter")
	}
	// One token, or two during rotation, comma-separated: a bearer token
	// cannot contain a comma.
	if tokens := cfg.Alertmanager.Tokens; len(tokens) > 2 || slices.Contains(tokens, "") {
		return Config{}, fmt.Errorf("IC_ALERTMANAGER_TOKEN: want one token, or two comma-separated during rotation")
	}
	var err error
	if cfg.Locations, err = parseLocations(getenv("IC_LOCATIONS")); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// parseLocations reads comma-separated name:environment:token entries. The
// token comes last, so it may hold a colon.
func parseLocations(s string) ([]Location, error) {
	if s == "" {
		return nil, nil
	}
	var out []Location
	seen := map[string]bool{}
	for _, entry := range strings.Split(s, ",") {
		parts := strings.SplitN(entry, ":", 3)
		if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
			// The entry holds a token, so it stays out of the message.
			return nil, fmt.Errorf("IC_LOCATIONS: entry %d: want name:environment:token", len(out)+1)
		}
		if seen[parts[0]] {
			return nil, fmt.Errorf("IC_LOCATIONS: location %s: named twice; a Location belongs to exactly one Environment", parts[0])
		}
		seen[parts[0]] = true
		out = append(out, Location{Name: parts[0], Environment: parts[1], Token: parts[2]})
	}
	return out, nil
}
