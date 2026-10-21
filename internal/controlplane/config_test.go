package controlplane

import (
	"strings"
	"testing"

	"github.com/itsankoff/incident-copilot/internal/signal"
)

const validConfig = `{
	"sources": [{"kind": "alertmanager", "instance": "prod-am", "token": "t1", "location": "kind-incident-copilot"}],
	"github": {"repo": "o/r", "token": "gh"}
}`

func TestParseConfigDefaults(t *testing.T) {
	cfg, err := ParseConfig(strings.NewReader(validConfig))
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	if cfg.Listen != ":8080" || cfg.GitHub.APIURL != "https://api.github.com" || cfg.StatePath == "" || cfg.AuditPath == "" {
		t.Errorf("unexpected defaults: %+v", cfg)
	}
	if m := cfg.Sources[0].Mapping; m.Platform != signal.KubePrometheus.Platform || len(m.Workloads) != len(signal.KubePrometheus.Workloads) {
		t.Errorf("mapping does not default to kube-prometheus: %+v", m)
	}
}

func TestParseConfigErrors(t *testing.T) {
	src := func(kind, instance, token, location string) string {
		return `{"kind":"` + kind + `","instance":"` + instance + `","token":"` + token + `","location":"` + location + `"}`
	}
	cfg := func(sources ...string) string {
		return `{"sources":[` + strings.Join(sources, ",") + `],"github":{"repo":"o/r","token":"gh"}}`
	}
	tests := []struct {
		name, config, wantErr string
	}{
		{"not JSON", "{", "config"},
		{"unknown field", `{"listn": ":1"}`, "unknown field"},
		{"no sources", cfg(), "sources: at least one"},
		{"empty token", cfg(src("alertmanager", "a", "", "l")), "token: required"},
		{"no instance", cfg(src("alertmanager", "", "t", "l")), "instance: required"},
		{"no location", cfg(src("alertmanager", "a", "t", "")), "location: required"},
		{"unknown kind", cfg(src("datadog", "a", "t", "l")), `kind: unsupported "datadog"`},
		{"shared token", cfg(src("alertmanager", "a", "t", "l"), src("alertmanager", "b", "t", "l")), "token: shared"},
		{"duplicate instance", cfg(src("alertmanager", "a", "t1", "l"), src("alertmanager", "a", "t2", "l")), "instance: duplicate"},
		{"no github repo", `{"sources":[` + src("alertmanager", "a", "t", "l") + `],"github":{"token":"gh"}}`, "github.repo: required"},
		{"no github token", `{"sources":[` + src("alertmanager", "a", "t", "l") + `],"github":{"repo":"o/r"}}`, "github.token: required"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseConfig(strings.NewReader(tt.config))
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("ParseConfig error = %v, want containing %q", err, tt.wantErr)
			}
		})
	}
}
