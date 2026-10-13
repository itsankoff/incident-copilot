# INSTRUCTIONS

## Status

Early development. Module `github.com/itsankoff/incident-copilot` (Go 1.25). What exists today is the demo environment scaffold:

- `deploy/versions.env`: pinned kind, node image, and chart versions
- `deploy/kind/`: kind cluster config
- `deploy/observability/`: Helm values for kube-prometheus-stack, Loki, and Alloy
- `deploy/demo/`: Kustomize base for `orders`, `inventory`, `loadgen`, and a ServiceMonitor
- `cmd/demo-svc`, `internal/demo`: stub demo service (roles, probes, metrics, JSON logs, no fault injection yet)

The agent, MCP server, and everything else in the architecture below is the target, not code yet. Check what actually exists before assuming a package, command, or file is present. The demo environment design is in `docs/design/demo-environment.md` and the task plan (goal, requirements, and validation for each task) is in `docs/roadmap.md`. Work through the roadmap in order, and treat a task's validation list as its definition of done.

### Components and their boundaries

- **Agent loop.** Written in plain Go with direct LLM tool calling and **no agent framework** (LangChain-style libraries are out of scope). The loop owns tool dispatch, retries, and budgets (steps, tokens, wall-clock time). It must stop when a budget runs out and still produce an RCA, with low confidence if needed.
- **MCP tool server.** Provides the investigation tools over the Model Context Protocol, kept separate from the agent so any MCP client can use them. Tools:
  - `query_metrics` (PromQL)
  - `search_logs` (LogQL)
  - `get_k8s_events`
  - `describe_pod`
  - `recent_deployments`
  - `get_commit_diff` (GitHub API)

  Tools are **read-only**. Nothing that changes cluster state belongs here.
- **LLM provider.** A small interface. Azure OpenAI is the default, and `openai` and `anthropic` are drop-in implementations. Nothing outside the provider package may import vendor SDK types.
- **Ticketing adapter.** An interface with a ServiceNow implementation (primary) and a GitHub Issues implementation (fallback). Adding Jira or PagerDuty must not require changes to the agent.
- **Approval gate.** Approval comes from a ticket state change or a Slack button, and both sit behind an interface.
- **Runbook executor.** The only component that changes cluster state.
- **Audit log.** Append-only. It records every alert, tool call (arguments and result), LLM request and response, proposed action, approval, and execution result.

### RCA contract

Every ticket has exactly these fields: **Summary**, **Evidence** (the queries run, their results, and links), **Suspected cause**, **Confidence** (`low|medium|high` plus the reason), and **Proposed action** (a runbook name and its parameters). Model this as a typed Go struct and validate the LLM's output against it. Do not pass free text through.

## Safety invariants (never weaken these)

These are the core of the product. Changes that relax them need an ADR in `docs/adr/`.

1. **Allowlist only.** The agent can choose only a registered runbook, such as `rollback_deployment`, `restart_pod`, or `scale_up`. It never runs arbitrary commands or free-form kubectl.
2. **Autonomy levels are enforced in code, not in the prompt.** Each runbook has a maximum level, and the executor checks it:
   - **L0 Observe:** report only, no action is proposed.
   - **L1 Suggest:** the action is proposed in the ticket and a human carries it out.
   - **L2 Approve:** the action runs only after explicit approval.
   - **L3 Autonomous:** the action runs immediately and is reported afterwards.

   Levels can be tightened per environment (for example L3 in staging and L2 in production) but never loosened by the LLM.
3. **Everything is audited.** A missing audit entry is a bug. Audit writes happen before the action they describe, and a failed audit write blocks the action.
4. **Investigation tools are read-only.** Only the runbook executor changes state.
5. **Secrets never reach the LLM or the audit log.** Redact tokens and credentials from tool output before either one sees it.

## Evaluation harness

`make eval` injects each fault, waits for the alert, lets the agent run to completion, and scores the result against ground truth. It reports:

- diagnosis accuracy
- runbook accuracy
- time to diagnosis
- token cost per incident

Treat it as a regression suite. Run it before and after any change to prompts, models, or tools, and report the difference.

Fault scenarios and their expected outcomes:

| Fault | Symptom | Expected runbook |
|---|---|---|
| Memory leak | Rising memory, OOMKills | `restart_pod` / `scale_up` |
| Bad config deploy | Error spike after a rollout | `rollback_deployment` |
| Dependency timeout | Latency and 5xx on downstream calls | none (escalate) |
| Crash loop | `CrashLoopBackOff` | `rollback_deployment` |

When you add a fault, add its ground truth to the harness in the same change.

## Demo environment

A local **kind** cluster runs a small Go demo service with injectable faults, plus Prometheus, Loki, and Alertmanager. Alertmanager's webhook receiver points at the agent. The stretch goal is a Bicep or Terraform module that deploys the demo onto AKS.

## Configuration

Configuration comes from environment variables, with `.env` supported for local use (`.env` is gitignored, so never commit it). The variables are:

- `LLM_PROVIDER`
- `AZURE_OPENAI_ENDPOINT` / `AZURE_OPENAI_API_KEY` / `AZURE_OPENAI_DEPLOYMENT`
- `PROMETHEUS_URL` / `LOKI_URL`
- `GITHUB_TOKEN` / `GITHUB_REPO`
- `TICKETING` (`servicenow` or `github`)
- `SERVICENOW_INSTANCE` / `SERVICENOW_USER` / `SERVICENOW_PASSWORD`
- `SLACK_BOT_TOKEN`
- `AUDIT_LOG_PATH`

When you add a variable, update the README table in the same change.

## Engineering conventions

- **Interfaces at every external boundary:** the LLM, ticketing, approval, the k8s client, Prometheus, Loki, and GitHub. This lets unit tests use fakes with no network access. Tests that need a real cluster or real APIs belong behind a build tag or in the eval harness.
- Pass `context.Context` through every call that touches I/O, and put timeouts on all outbound calls.
- Use structured logging (`log/slog`). Operational logs and the audit log are separate streams.
- Favor the standard library and a few well-known dependencies (client-go, the MCP Go SDK, provider SDKs). Justify any new dependency.
- Keep prompts in versioned files, not scattered string literals, so the eval harness can attribute score changes to them.

## Maintaining this file

Once real code lands, replace the "Status" and "Planned" wording with facts: the module path, the actual package layout, the real Makefile targets, and any lint tooling. Keep the file under 300 lines.
