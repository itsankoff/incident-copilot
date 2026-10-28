package controlplane

import (
	"slices"
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
		"IC_ALERTMANAGER_LOCATION": "ic-target",
	}
}

func TestLoadConfigDefaults(t *testing.T) {
	cfg, err := LoadConfig(envMap(required()))
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.ListenAddr != ":8080" || cfg.StatePath == "" || cfg.AuditPath == "" {
		t.Errorf("unexpected defaults: %+v", cfg)
	}
	am := cfg.Alertmanager
	if am.Kind != "alertmanager" || am.Instance != "kind-am" || !slices.Equal(am.Tokens, []string{"t1"}) || am.Location != "ic-target" {
		t.Errorf("unexpected Alertmanager source: %+v", am)
	}
	if am.Mapping.Platform != signal.KubePrometheus.Platform || len(am.Mapping.Workloads) != len(signal.KubePrometheus.Workloads) {
		t.Errorf("mapping is not kube-prometheus: %+v", am.Mapping)
	}
}

// Without GitHub settings the Control plane runs with the fake ticketing
// adapter; a half-set pair is a mistake, not a choice.
func TestLoadConfigGitHub(t *testing.T) {
	cfg, err := LoadConfig(envMap(required()))
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.GitHub != nil {
		t.Errorf("GitHub = %+v, want nil without IC_GITHUB_REPO and IC_GITHUB_TOKEN", cfg.GitHub)
	}

	env := required()
	env["IC_GITHUB_REPO"], env["IC_GITHUB_TOKEN"] = "o/r", "gh"
	cfg, err = LoadConfig(envMap(env))
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if want := (GitHub{APIURL: "https://api.github.com", Repo: "o/r", Token: "gh"}); cfg.GitHub == nil || *cfg.GitHub != want {
		t.Errorf("GitHub = %+v, want %+v", cfg.GitHub, want)
	}

	for _, set := range []string{"IC_GITHUB_REPO", "IC_GITHUB_TOKEN"} {
		env := required()
		env[set] = "x"
		if _, err := LoadConfig(envMap(env)); err == nil || !strings.Contains(err.Error(), "IC_GITHUB_REPO and IC_GITHUB_TOKEN") {
			t.Errorf("only %s set: error %v, want one naming both variables", set, err)
		}
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

func TestLoadConfigRotationTokens(t *testing.T) {
	env := required()
	env["IC_ALERTMANAGER_TOKEN"] = "old,next"
	cfg, err := LoadConfig(envMap(env))
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if !slices.Equal(cfg.Alertmanager.Tokens, []string{"old", "next"}) {
		t.Errorf("tokens %q, want old and next", cfg.Alertmanager.Tokens)
	}

	for _, bad := range []string{"a,b,c", "a,", ",b"} {
		env["IC_ALERTMANAGER_TOKEN"] = bad
		if _, err := LoadConfig(envMap(env)); err == nil || !strings.Contains(err.Error(), "IC_ALERTMANAGER_TOKEN") {
			t.Errorf("IC_ALERTMANAGER_TOKEN=%q: error %v, want one naming the variable", bad, err)
		}
	}
}
