package controlplane

import (
	"fmt"

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
	GitHubAPIURL string
	GitHubRepo   string // owner/name
	GitHubToken  string
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
		ListenAddr: env("LISTEN_ADDR", ":8080"),
		StatePath:  env("STATE_PATH", "incident-copilot.db"),
		AuditPath:  env("AUDIT_PATH", "audit.jsonl"),
		Alertmanager: signal.Source{
			Kind:     "alertmanager",
			Instance: need("ALERTMANAGER_INSTANCE"),
			Token:    need("ALERTMANAGER_TOKEN"),
			Location: need("ALERTMANAGER_LOCATION"),
			Mapping:  signal.KubePrometheus,
		},
		GitHubAPIURL: env("GITHUB_API_URL", "https://api.github.com"),
		GitHubRepo:   need("GITHUB_REPO"),
		GitHubToken:  need("GITHUB_TOKEN"),
	}
	if missing != nil {
		return Config{}, missing
	}
	return cfg, nil
}
