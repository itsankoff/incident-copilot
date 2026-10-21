package controlplane

import (
	"strings"
	"testing"

	"github.com/itsankoff/incident-copilot/internal/signal"
)

func envMap(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func required() map[string]string {
	return map[string]string{
		"IC_ALERTMANAGER_INSTANCE": "kind-am",
		"IC_ALERTMANAGER_TOKEN":    "t1",
		"IC_ALERTMANAGER_LOCATION": "kind-incident-copilot",
		"IC_GITHUB_REPO":           "o/r",
		"IC_GITHUB_TOKEN":          "gh",
	}
}

func TestLoadConfigDefaults(t *testing.T) {
	cfg, err := LoadConfig(envMap(required()))
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.ListenAddr != ":8080" || cfg.GitHubAPIURL != "https://api.github.com" || cfg.StatePath == "" || cfg.AuditPath == "" {
		t.Errorf("unexpected defaults: %+v", cfg)
	}
	am := cfg.Alertmanager
	if am.Kind != "alertmanager" || am.Instance != "kind-am" || am.Token != "t1" || am.Location != "kind-incident-copilot" {
		t.Errorf("unexpected Alertmanager source: %+v", am)
	}
	if am.Mapping.Platform != signal.KubePrometheus.Platform || len(am.Mapping.Workloads) != len(signal.KubePrometheus.Workloads) {
		t.Errorf("mapping is not kube-prometheus: %+v", am.Mapping)
	}
}

func TestLoadConfigErrors(t *testing.T) {
	for key := range required() {
		t.Run("missing "+key, func(t *testing.T) {
			env := required()
			delete(env, key)
			_, err := LoadConfig(envMap(env))
			if err == nil || !strings.Contains(err.Error(), key+": required") {
				t.Fatalf("LoadConfig error = %v, want %s: required", err, key)
			}
		})
	}
}
