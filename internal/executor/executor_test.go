package executor

import (
	"os/exec"
	"strings"
	"testing"
)

// The Executor never calls an LLM and has no access to ticketing (ADR 0003
// D1), so it depends on neither, nor on the Control plane that holds both.
func TestTheExecutorReachesNoLLMAndNoTicketing(t *testing.T) {
	const module = "github.com/itsankoff/incident-copilot"
	out, err := exec.Command("go", "list", "-deps", module+"/internal/executor").Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	deps := strings.Fields(string(out))
	if len(deps) < 5 {
		t.Fatalf("go list found only %v", deps)
	}
	for _, dep := range deps {
		for _, banned := range []string{"/internal/llm", "/internal/openrouter", "/internal/github", "/internal/controlplane", "/internal/store"} {
			if dep == module+banned {
				t.Errorf("the Executor depends on %s", dep)
			}
		}
	}
}
