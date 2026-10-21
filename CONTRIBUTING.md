# Contributing

## Where things are

- [`docs/roadmap.md`](docs/roadmap.md): the task plan and current status. Work through it in order, and treat a task's validation list as its definition of done.
- [`docs/design/`](docs/design): design docs, one per milestone that needs one.
- [`docs/adr/`](docs/adr): architecture decision records and the [architecture diagram](docs/adr/arch-diagram.md).
- [ADR 0003: Safety invariants](docs/adr/0003-trust-model-and-safety-invariants.md#safety-invariants): never weaken these. Relaxing one needs a new ADR that supersedes ADR 0003.

Check what actually exists before assuming a package, command, or file is present. Much of the architecture is still the target, not code.

## Before a PR

```sh
make build test vet lint
```

## Engineering conventions

- **Interfaces at every external boundary:** the LLM, ticketing, approval, the k8s client, Prometheus, Loki, and GitHub. This lets unit tests use fakes with no network access. Tests that need a real cluster or real APIs belong behind a build tag or in the eval harness.
- Pass `context.Context` through every call that touches I/O, and put timeouts on all outbound calls.
- Use structured logging (`log/slog`). Operational logs and the audit log are separate streams.
- Favor the standard library and a few well-known dependencies (client-go, the MCP Go SDK, provider SDKs). Justify any new dependency.
- Keep prompts in versioned files, not scattered string literals, so the eval harness can attribute score changes to them.

## Configuration

Configuration comes from environment variables, with `.env` supported for local use (`.env` is gitignored, so never commit it). The variables are listed in the [README](README.md#configuration). When you add a variable, update that table in the same change.

## Evaluation harness

Treat `make eval` as a regression suite. Run it before and after any change to prompts, models, or tools, and report the difference.

When you add a fault, add its ground truth to the harness in the same change.
