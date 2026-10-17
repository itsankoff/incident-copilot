# Architecture diagram

## Architecture

```
Alertmanager --webhook--> Agent (read-only) <--tool calls--> MCP tool server --> Prometheus / Loki / k8s API / GitHub API
                              |      <--prompts / replies--> LLM provider (Azure OpenAI default)
                 +------------+-------------+
                 | RCA                      | Proposal
                 v                          v
     Ticketing adapter               Executor (never calls an LLM)
     (ServiceNow | GitHub Issues)       |
                                        v
                                 Approval gate <-- ticket state | Slack button | approval link (sent by email or push)
                                        |
                                        v
                                 Runbook --> k8s API (the only writer)

   All components ---> append-only audit log
```

Flow: **Trigger → Investigate → Report → Remediate → Record.**

The agent and the executor are one binary in two roles, with separate Deployments and ServiceAccounts ([ADR 0001](0001-autonomy-and-trust-model.md) D4).

## Components and their boundaries

- **Agent loop.** Plain Go with direct LLM tool calling and **no agent framework** (LangChain-style libraries are out of scope). The loop owns tool dispatch, retries, and budgets (steps, tokens, wall-clock time). It must stop when a budget runs out and still produce an RCA, with low confidence if needed.
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
- **Approval gate.** Owned by the executor. Approval comes from a ticket state change, a Slack button, or an approval link, all behind the `Approver` interface ([ADR 0001](0001-autonomy-and-trust-model.md) D5).
- **Runbook executor.** The only component that changes cluster state, and it never calls an LLM.
- **Audit log.** Append-only. It records every alert, tool call (arguments and result), LLM request and response, proposed action, approval, and execution result ([ADR 0001](0001-autonomy-and-trust-model.md) D7).

The safety invariants these boundaries enforce are listed in [ADR 0001](0001-autonomy-and-trust-model.md#safety-invariants).

## RCA contract

Every ticket has exactly these fields: **Summary**, **Evidence** (the queries run, their results, and links), **Suspected cause**, **Confidence** (`low|medium|high` plus the reason), and **Proposed action** (a runbook name and its parameters). Model this as a typed Go struct and validate the LLM's output against it. Do not pass free text through.
