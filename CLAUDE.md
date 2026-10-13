# Incidence Copilot

**incident-copilot** is an autonomous incident responder for Kubernetes platforms. It receives an Alertmanager webhook, investigates with real tools (metrics, logs, k8s events, deploy history, commit diffs), writes a structured RCA into a ticketing system, and proposes a remediation from an allowlist that runs only as far as its autonomy level permits.

## Commands

```sh
make build test vet                   # go build / go test / go vet ./...
go test ./internal/demo -run TestName # single test
make tools                            # check prerequisites
make cluster                          # kind cluster + Prometheus, Alertmanager, Loki, Alloy (idempotent)
make deploy                           # build demo-svc image, load into kind, apply deploy/demo
make status                           # pods in demo, monitoring, logging
make port-forward                     # Prometheus :9090, Alertmanager :9093, Loki API :3100, Grafana :3000 (can be toggled off with GRAFANA=0 during make cluster or make observability)
make down                             # delete the cluster
```

## Architecture

Architecture diagrams and decision records live in docs/adr/arch-diagram.md and docs/adr/XXXX.md

Every kubectl and helm call in the Makefile pins `--context kind-incident-copilot`. Keep it that way.

## 1. Think Before Coding

**Don't assume. Don't hide confusion. Surface tradeoffs.**

Before implementing:
- State your assumptions explicitly. If uncertain, ask.
- If multiple interpretations exist, present them - don't pick silently.
- If a simpler approach exists, say so. Push back when warranted.
- If something is unclear, stop. Name what's confusing. Ask.

## 2. Simplicity First

**Minimum code that solves the problem. Nothing speculative.**

- No features beyond what was asked.
- No abstractions for single-use code.
- No "flexibility" or "configurability" that wasn't requested.
- No error handling for impossible scenarios.
- If you write 200 lines and it could be 50, rewrite it.

Ask yourself: "Would a senior engineer say this is overcomplicated?" If yes, simplify.

## 3. Surgical Changes

**Touch only what you must. Clean up only your own mess.**

When editing existing code:
- Don't "improve" adjacent code, comments, or formatting.
- Don't refactor things that aren't broken.
- Match existing style, even if you'd do it differently.
- If you notice unrelated dead code, mention it - don't delete it.

When your changes create orphans:
- Remove imports/variables/functions that YOUR changes made unused.
- Don't remove pre-existing dead code unless asked.

The test: Every changed line should trace directly to the user's request.

## 4. Goal-Driven Execution

**Define success criteria. Loop until verified.**

Transform tasks into verifiable goals:
- "Add validation" → "Write tests for invalid inputs, then make them pass"
- "Fix the bug" → "Write a test that reproduces it, then make it pass"
- "Refactor X" → "Ensure tests pass before and after"

For multi-step tasks, state a brief plan:
```
1. [Step] → verify: [check]
2. [Step] → verify: [check]
3. [Step] → verify: [check]
```

Strong success criteria let you loop independently. Weak criteria ("make it work") require constant clarification.
