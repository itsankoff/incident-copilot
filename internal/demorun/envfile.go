package demorun

import (
	"errors"
	"os"
	"strings"
)

// ReadEnvFile reads KEY=value lines, such as .env.demo's. A missing file is
// empty. Values left as the example's placeholders ("...") count as unset.
func ReadEnvFile(path string) (map[string]string, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, err
	}
	env := map[string]string{}
	for _, line := range strings.Split(string(b), "\n") {
		k, v, ok := strings.Cut(line, "=")
		k = strings.TrimSpace(k)
		if !ok || k == "" || strings.HasPrefix(k, "#") {
			continue
		}
		v = strings.Trim(strings.TrimSpace(v), `"`)
		if v == "" || strings.HasSuffix(v, "...") {
			continue
		}
		env[k] = v
	}
	return env, nil
}

// SetEnv sets key in the env file at path, replacing its line or appending
// one, and keeps the file readable only by its owner.
func SetEnv(path, key, value string) error {
	b, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	if len(b) == 0 {
		lines = nil
	}
	set := false
	for i, line := range lines {
		if k, _, ok := strings.Cut(line, "="); ok && strings.TrimSpace(k) == key {
			lines[i], set = key+"="+value, true
		}
	}
	if !set {
		lines = append(lines, key+"="+value)
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}
