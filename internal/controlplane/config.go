package controlplane

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/itsankoff/incident-copilot/internal/signal"
)

// Config is the Control plane's configuration, read from one JSON file.
// Packaging and the configuration model are still to be decided (PRD
// placeholders); this is the simplest form that works.
type Config struct {
	Listen    string          `json:"listen"`
	StatePath string          `json:"statePath"`
	AuditPath string          `json:"auditPath"`
	Sources   []signal.Source `json:"sources"`
	GitHub    GitHubConfig    `json:"github"`
}

type GitHubConfig struct {
	APIURL string `json:"apiURL"`
	Repo   string `json:"repo"` // owner/name
	Token  string `json:"token"`
}

// ParseConfig reads and validates a Config, filling in defaults. A Signal
// source without a mapping gets the kube-prometheus one.
func ParseConfig(r io.Reader) (Config, error) {
	cfg := Config{
		Listen:    ":8080",
		StatePath: "incident-copilot.db",
		AuditPath: "audit.jsonl",
		GitHub:    GitHubConfig{APIURL: "https://api.github.com"},
	}
	dec := json.NewDecoder(r)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&cfg); err != nil {
		return Config{}, fmt.Errorf("config: %w", err)
	}

	if len(cfg.Sources) == 0 {
		return Config{}, errors.New("sources: at least one Signal source is required")
	}
	instances, tokens := map[string]bool{}, map[string]bool{}
	for i := range cfg.Sources {
		s := &cfg.Sources[i]
		switch {
		case s.Kind != "alertmanager":
			return Config{}, fmt.Errorf("sources[%d].kind: unsupported %q (alertmanager)", i, s.Kind)
		case s.Instance == "":
			return Config{}, fmt.Errorf("sources[%d].instance: required", i)
		case s.Token == "":
			return Config{}, fmt.Errorf("sources[%d].token: required", i)
		case s.Location == "":
			return Config{}, fmt.Errorf("sources[%d].location: required", i)
		case instances[s.Instance]:
			return Config{}, fmt.Errorf("sources[%d].instance: duplicate %q", i, s.Instance)
		case tokens[s.Token]:
			// The token identifies the source, so each needs its own.
			return Config{}, fmt.Errorf("sources[%d].token: shared with another source", i)
		}
		instances[s.Instance], tokens[s.Token] = true, true
		if s.Mapping.NamespaceLabel == "" && len(s.Mapping.Workloads) == 0 {
			s.Mapping = signal.KubePrometheus
		}
	}

	if cfg.GitHub.Repo == "" {
		return Config{}, errors.New("github.repo: required")
	}
	if cfg.GitHub.Token == "" {
		return Config{}, errors.New("github.token: required")
	}
	return cfg, nil
}
