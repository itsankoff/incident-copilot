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
	return cfg, nil
}
