package demorun

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEnvFileReadsValuesAndSkipsPlaceholders(t *testing.T) {
	p := filepath.Join(t.TempDir(), ".env.demo")
	if err := os.WriteFile(p, []byte("# comment\nOPENROUTER_API_KEY=\"sk-or-v1-abc\"\nIC_GITHUB_REPO=...\nIC_GITHUB_TOKEN=github_pat_...\n\nIC_LLM_MODEL = openrouter/free\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	env, err := ReadEnvFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if env["OPENROUTER_API_KEY"] != "sk-or-v1-abc" || env["IC_LLM_MODEL"] != "openrouter/free" {
		t.Errorf("env = %q", env)
	}
	if _, ok := env["IC_GITHUB_REPO"]; ok {
		t.Errorf("placeholder kept: %q", env)
	}
	if _, ok := env["IC_GITHUB_TOKEN"]; ok {
		t.Errorf("placeholder kept: %q", env)
	}
}

func TestEnvFileMissingIsEmpty(t *testing.T) {
	env, err := ReadEnvFile(filepath.Join(t.TempDir(), "none"))
	if err != nil || len(env) != 0 {
		t.Errorf("env %q err %v", env, err)
	}
}

func TestSetEnvReplacesOrAppendsPrivately(t *testing.T) {
	p := filepath.Join(t.TempDir(), ".env.demo")
	if err := SetEnv(p, "OPENROUTER_API_KEY", "sk-or-1"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("IC_LLM_MODEL=openrouter/free\nOPENROUTER_API_KEY=sk-...\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := SetEnv(p, "OPENROUTER_API_KEY", "sk-or-2"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	if got, want := string(b), "IC_LLM_MODEL=openrouter/free\nOPENROUTER_API_KEY=sk-or-2\n"; got != want {
		t.Errorf("file = %q, want %q", got, want)
	}
	if fi, _ := os.Stat(p); fi.Mode().Perm() != 0o600 {
		t.Errorf("mode %v", fi.Mode().Perm())
	}
}
