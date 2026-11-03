package evidence

import (
	"os/exec"
	"strings"
	"testing"
)

// The core never imports a platform package (ADR 0002 D6). Only main wires
// one in, and only the platform package itself may reach client libraries.
func TestOnlyMainImportsThePlatformPackage(t *testing.T) {
	const module = "github.com/itsankoff/incident-copilot"
	out, err := exec.Command("go", "list", "-f", `{{.ImportPath}} {{join .Deps " "}}`, module+"/...").Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	checked := 0
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		pkg, deps, _ := strings.Cut(line, " ")
		if pkg == module+"/cmd/incident-copilot" || pkg == module+"/internal/kubernetes" {
			continue
		}
		checked++
		for _, dep := range strings.Fields(deps) {
			if dep == module+"/internal/kubernetes" || strings.HasPrefix(dep, "k8s.io/") {
				t.Errorf("%s depends on %s", pkg, dep)
			}
		}
	}
	if checked < 5 {
		t.Fatalf("checked only %d packages", checked)
	}
}
