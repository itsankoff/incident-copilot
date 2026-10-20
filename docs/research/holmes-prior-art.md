# Prior art: HolmesGPT

Status: research note · Last updated: 2026-10-20 · Subject: [HolmesGPT/holmesgpt](https://github.com/HolmesGPT/holmesgpt) at commit [`50c6977`](https://github.com/HolmesGPT/holmesgpt/tree/50c6977756992d267c3eec8f36fcf09e986b675e) (master, 2026-10-20) and its docs at [holmesgpt.dev/latest](https://holmesgpt.dev/latest/)

This note explains how HolmesGPT works and which architectural decisions shape it. It covers how alerts and questions get in, how it reads observability data, how the LLM turns that data into a root-cause analysis, how remediation runs, and how humans receive results and act on them. It stays at the level of mechanisms and leaves out the details of each vendor integration. The last section compares the design with incident-copilot's own decisions.

**Conventions.** Code links point at the pinned commit. `[docs]` marks a claim that comes from first-party documentation rather than code, and *Inference:* marks our own reading. Where the docs and the code disagree, the code wins and the mismatch is called out. Sections 1 to 9 use HolmesGPT's own terms (alert, tool, toolset, skill). Section 11 switches to the vocabulary in [CONTEXT.md](../../CONTEXT.md).

**Method.** Five readers studied the source, the docs source (`docs/` in the repo) and first-party PRs and issues in parallel. Each covered one slice: entry points, toolsets, the LLM loop, remediation and reporting, and overall architecture. Their key claims were then checked against the code before being included here. Nothing was built or run. The Robusta platform (relay, web UI, Slack and Teams bots, Supabase schema) is closed source, so claims about it come only from the client code in this repository and from the docs.

---

## TL;DR

1. **HolmesGPT is a chat agent with one loop.** Every entry point (CLI, HTTP, operator, Robusta SaaS queue) turns into an OpenAI-format `messages` list and ends in a single hand-rolled tool-calling loop, `ToolCallingLLM.call_stream()`. No agent framework is used (§2, §6).
2. **Alerts are pulled, one at a time, and pasted into the prompt raw.** In open source, `holmes investigate <source>` fetches open alerts once from Alertmanager, PagerDuty, OpsGenie, Jira or GitHub, and runs one full investigation for each. There is no webhook receiver, no deduplication and no grouping. The alert prompt is the general chat prompt plus one sentence, with the alert's Python `dict` pasted into the user message (§3). New Relic is a data source only, not an alert source.
3. **The structured RCA was removed.** In March 2026 PR #1688 deleted `/api/investigate` and its JSON "sections" output to "consolidat[e] all issue analysis functionality into the conversational chat interface". The RCA is now free-text markdown plus the raw tool-call records. It has no confidence score and no machine-readable citations (§6.6).
4. **Data access comes from many toolsets behind one abstraction.** Every integration (YAML shell templates, Python classes, MCP servers, HTTP and SQL connectors) is a `Toolset` of `Tool`s, and every call goes through `Tool.invoke`. Tools mostly expose the vendor's own query language (PromQL, LogQL, NRQL) plus discovery helpers. Kubernetes is read mostly through a generic `bash` tool with a command allowlist (§5).
5. **Read-only is guaranteed by credentials, not by the agent.** The Helm ClusterRole allows only `get`/`list`/`watch` and excludes Secrets. Tool output is never redacted before it reaches the LLM (§5.5).
6. **Remediation is the same loop, paused.** A write is an ordinary tool call whose result status is `APPROVAL_REQUIRED`. The stream stops, the client shows the call, and a later request with the decision resumes the loop. Approval is keyed by tool name. Kubernetes writes live in a separate MCP server with its own ServiceAccount. There is no action catalogue, dry run, post-fix verification, autonomy ladder or approval audit record (§7).
7. **Write tools that are not marked for approval run with no prompt at all.** Holmes asks for approval only on tools explicitly marked as needing it. GitHub MCP write tools (open a PR, push files) are not marked by default, so "Holmes opens fix PRs" is limited only by the GitHub token's scope (§7.4).
8. **Reporting is free text sent to output sinks.** Output goes to stdout, Slack, a webhook or a comment on the source ticket. The Slack bot, Teams bot, web UI, approval forms, feedback and conversation storage all belong to Robusta's closed platform (§8).
9. **Evals are the main quality mechanism.** About 280 live-cluster scenarios are scored by an LLM judge against expected facts. They now run on demand rather than on every commit because of cost (§9).
10. **What this means for incident-copilot:** HolmesGPT shows what to borrow for investigation: tool-output spill to disk, showing the LLM which data sources are missing, its evidence rules, and live evals. It also shows where our decisions are worth their cost: a typed RCA, an Executor separate from the agent, an Action catalogue, autonomy levels, Triage, and redaction. These are the places where HolmesGPT is thinnest (§11).

---

## 1. What HolmesGPT is

**Positioning.** "Open-source AI agent for investigating production incidents and finding root causes. Works with any stack". It is a "Cloud Native Computing Foundation sandbox project. Originally created by Robusta.Dev, with major contributions from Microsoft" ([README.md#L11](https://github.com/HolmesGPT/holmesgpt/blob/50c6977756992d267c3eec8f36fcf09e986b675e/README.md#L11)). Eleven of the twelve current maintainers work at Robusta and one at Microsoft ([MAINTAINERS.md](https://github.com/HolmesGPT/holmesgpt/blob/50c6977756992d267c3eec8f36fcf09e986b675e/MAINTAINERS.md)). Named adopters are the Microsoft AKS team and Innovaccer ([ADOPTERS.md](https://github.com/HolmesGPT/holmesgpt/blob/50c6977756992d267c3eec8f36fcf09e986b675e/ADOPTERS.md)).

**Stated values versus what the code enforces.** [docs] [Why HolmesGPT](https://holmesgpt.dev/latest/why-holmesgpt/), [README](https://github.com/HolmesGPT/holmesgpt/blob/50c6977756992d267c3eec8f36fcf09e986b675e/README.md).

| Stated value | What the code actually does |
|---|---|
| "Safe by Design": "Built-in toolsets are read-only by default … The few integrations that can write — remediation and ticketing — are opt-in" | Enforced by deployment. The Helm ClusterRole has only `get`/`list`/`watch` and no Secrets. The CLI uses *your* kubeconfig. Helm enables the `bash` toolset by default with the `extended` allowlist (§5.5). |
| "Data Privacy: By design, HolmesGPT has read-only access and respects RBAC permissions" | True for the in-cluster ServiceAccount. Nothing redacts tool output before it reaches the LLM (§5.5). |
| Bring any LLM, including on-prem | True. All model calls go through LiteLLM with a named model list (§2.4). |
| Handle large data without overflowing the context window | Per-tool budgets, spill-to-disk, and history compaction (§5.4, §6.5). |
| "Full audit trail of every tool call, prompt, and answer" through OpenTelemetry | Only when `HOLMES_LANGFUSE_ATTRIBUTES=true`. By default, spans carry metadata only (§9). |
| Operator mode: "runs in the background 24/7 … messages you in Slack with the fix" and opens PRs | Health checks return `{passed, rationale}` and have no fix field. PRs happen only if a GitHub MCP server with write access is connected (§7.4). |

An early safety principle from Robusta maintainer Natan Yellin (`aantn`), in a still-open issue: "Its important that the LLM itself never have the capability to write back to the cluster and all writes are done out-of-band" ([#490](https://github.com/HolmesGPT/holmesgpt/issues/490), June 2025). *Inference:* the later remediation MCP server (§7.2) keeps the "different process, different credentials" half of that principle. It drops the "LLM can't trigger writes" half: once a human approves, the LLM triggers writes directly through a tool call.

### 1.1 The open-core boundary

The open-source agent runs on its own. Every Robusta feature is switched on only when a Robusta token is present (`SupabaseDal.enabled`, [supabase_dal.py#L260-L270](https://github.com/HolmesGPT/holmesgpt/blob/50c6977756992d267c3eec8f36fcf09e986b675e/holmes/core/supabase_dal.py#L260-L270)). The `supabase` package is still a hard dependency, and some features, such as account-wide global instructions, exist only on that path ([#2070](https://github.com/HolmesGPT/holmesgpt/issues/2070)).

| Open source (this repo) | Robusta platform (closed, "3rd party" in the docs) |
|---|---|
| Agent loop, prompts, toolsets, skills loader | Web UI ([docs](https://holmesgpt.dev/latest/installation/ui-installation/)) |
| CLI (`ask`, `investigate`, `checks`, `toolset`) | Slack and Teams bots: "First install Robusta" ([Slack](https://holmesgpt.dev/latest/installation/slack-installation/), [Teams](https://holmesgpt.dev/latest/installation/teams-installation/)) |
| HTTP server (`/api/chat`, `/api/checks`) | Backstage plugin (closed beta) |
| Holmes Operator (health-check CRDs) | Alert ingestion, grouping (`GroupedIssues`) and auto-triage |
| Helm chart, MCP add-ons | Conversation storage, approval forms, "confirmation mode", feedback |
| Evals | Hosted models ("Robusta AI"), cross-cluster relay, platform MCP |
| Conversations worker *client* (inert without Supabase) | Conversations queue *server* (Supabase, relay) |

There is no Chrome or browser extension. The only "chrome" strings in the repo are a browserslist entry and a User-Agent header, and searching the repo's issues and PRs for it finds nothing. *Inference:* the browser interface people mean is the Robusta web UI. It extends the agent through the open-source "frontend tools" protocol (§4.3).

---

## 2. Architecture at a glance

### 2.1 Components and topologies

```
              LAPTOP                                      KUBERNETES CLUSTER
 ┌───────────────────────────┐        ┌──────────────────────────────────────────────────────┐
 │ holmes CLI / Python SDK   │        │  Holmes API pod(s)  FastAPI + uvicorn, HPA-able       │
 │  - your kubeconfig/creds  │        │   /api/chat (SSE)   /api/checks/execute  /api/admin/* │
 │  - ~/.holmes/config.yaml  │        │   ServiceAccount: ClusterRole get/list/watch only     │
 │  - pulls Alertmanager/    │        │   ┌───────────────┐  ┌────────────────────────────┐   │
 │    Jira/PD/OpsGenie APIs  │        │   │ToolCallingLLM │──│ToolExecutor: k8s, prom,    │   │
 └─────────────┬─────────────┘        │   │ (agent loop)  │  │ loki, bash, MCP clients... │   │
               │                      │   └──────┬────────┘  └──────┬───────────┬─────────┘   │
               │  HTTP POST /api/chat │  [opt] ConversationRuntime  │     MCP add-on pods    │
  custom client├─────────────────────►│   (claims queued rows,      │     (remediation: own  │
               │                      │    outbound only)           │      SA with writes)   │
               │                      │  Holmes Operator (kopf) ────┘                        │
               │                      │   CRDs → POST /api/checks/execute                    │
               │                      └──────────┼───────────────────────────────────────────┘
               ▼                                 ▼ outbound only
   ┌──────────────────────┐      ┌──────────────────────────────────────────────┐
   │ LLM provider via     │◄─────│ ROBUSTA SAAS (closed, optional)              │
   │ LiteLLM (OpenAI,     │ or   │  Supabase: Conversations, events, skills,    │
   │ Anthropic, Bedrock,  │ via  │  status, usage · relay: hosted models,       │
   │ Azure, Ollama, ...)  │ relay│  platform MCP · UI / Slack / Teams bots      │
   └──────────────────────┘      └──────────────────────────────────────────────┘
```

- **CLI**: a Typer app ([main.py#L66-L92](https://github.com/HolmesGPT/holmesgpt/blob/50c6977756992d267c3eec8f36fcf09e986b675e/holmes/main.py#L66-L92)). It runs on a laptop with the user's own credentials.
- **HTTP server**: FastAPI and uvicorn. Its routes are `/api/chat`, `/api/model`, `/api/info`, `/api/oauth/callback`, health probes, the mounted `/api/checks`, and an optional `/api/admin/reload` ([server.py#L499-L506](https://github.com/HolmesGPT/holmesgpt/blob/50c6977756992d267c3eec8f36fcf09e986b675e/server.py#L499-L506), [#L588](https://github.com/HolmesGPT/holmesgpt/blob/50c6977756992d267c3eec8f36fcf09e986b675e/server.py#L588)). There is no alert route.
- **Operator**: a separate kopf-based image. It is a thin client that calls the stateless API ([operator docs](https://holmesgpt.dev/latest/operator/)). It is alpha, and it sends no API key, so enabling `HOLMES_API_KEY` breaks every check ([#2605](https://github.com/HolmesGPT/holmesgpt/issues/2605)).
- **Two server shapes.** In the *push* shape, clients POST to `/api/chat`; the server is stateless and scales horizontally. In the *pull* shape, an in-cluster pod claims Conversation rows from Robusta's Supabase over an outbound connection, so the cluster needs no ingress. The pull shape exists only with Robusta (§3.4).

### 2.2 One funnel

Every entry point converges on one loop. `ToolCallingLLM.call()` is a "Synchronous wrapper around call_stream(). Drains the generator and reconstructs an LLMResult" ([tool_calling_llm.py#L696-L733](https://github.com/HolmesGPT/holmesgpt/blob/50c6977756992d267c3eec8f36fcf09e986b675e/holmes/core/tool_calling_llm.py#L696-L733)).

```
 Alertmanager/PD/OpsGenie/Jira/GitHub ─(pull, CLI)─► SourcePlugin.fetch_issues ─► Issue ─► _investigate_issue ─┐
 holmes ask (one-shot / -i REPL) ─────────► build_initial_ask_messages ───────────────────────────────────────┤
 Python SDK, K9s plugin (shells out to holmes ask) ─────────────────────────────────────────────────────────────┤
 POST /api/chat (client sends history back each turn; SSE optional) ─► build_chat_messages ───────────────────┤
 Robusta UI / Slack / Teams / auto-triage ─► Supabase Conversations row ─► ConversationProcessor ─► build_chat_messages ┤
 Robusta scheduled prompts ─► ScheduledPromptsExecutor ─► chat() handler ───────────────────────────────────────┤
 HealthCheck CR ◄─ ScheduledHealthCheck (cron) / TriggeredHealthCheck (rollout)                                │
        └─► POST /api/checks/execute ─► check_system_prompt + JSON schema {passed, rationale} ────────────────────┤
                                                                                                               ▼
                                         ToolCallingLLM.call_stream()  (tool loop, approvals, pauses, compaction)
```

The internal request model is an OpenAI-format `messages` list plus call options (`response_format`, `tool_decisions`, `frontend_tool_results`, `request_context`). It is not a typed "investigation" object. Health checks are the only path with their own prompt and a forced output structure.

### 2.3 Technology stack

The stack is Python `>=3.10,<3.14` with Poetry, Typer, FastAPI and uvicorn, Pydantic v2, Jinja2 prompts, LiteLLM, the `mcp` SDK, supabase-py, kopf (the operator framework), prompt_toolkit and Rich for the TUI, and OpenTelemetry as an optional dependency group. Braintrust and autoevals serve the evals ([pyproject.toml#L12-L49](https://github.com/HolmesGPT/holmesgpt/blob/50c6977756992d267c3eec8f36fcf09e986b675e/pyproject.toml#L12-L49)). The project gives little rationale for these choices. *Inference:* Python was picked for its LLM ecosystem. The repeated security pins in `pyproject.toml` show what that costs in transitive CVEs.

### 2.4 LLM provider layer

An abstract `LLM` exposes context-window size, maximum output tokens, token counting and `completion`. Its only implementation calls `litellm.completion` ([llm.py#L347-L395](https://github.com/HolmesGPT/holmesgpt/blob/50c6977756992d267c3eec8f36fcf09e986b675e/holmes/core/llm.py#L347-L395), [#L722-L837](https://github.com/HolmesGPT/holmesgpt/blob/50c6977756992d267c3eec8f36fcf09e986b675e/holmes/core/llm.py#L722-L837)). A YAML model list maps friendly names to LiteLLM arguments, and clients choose a model by name, so provider secrets stay on the server ([docs](https://holmesgpt.dev/latest/ai-providers/using-multiple-providers/)). This buys one interface to more than 100 providers, including on-prem ones. It costs three things:

- LiteLLM is pinned to exactly `1.89.0`.
- Python is capped below 3.14 because of LiteLLM.
- The loop carries a steady stream of hand-written fixes for provider quirks: always sending `max_tokens` because LiteLLM defaults unknown Anthropic aliases to 4096, stripping `temperature` for Bedrock Opus, and detecting Gemini routes ([llm.py#L751-L792](https://github.com/HolmesGPT/holmesgpt/blob/50c6977756992d267c3eec8f36fcf09e986b675e/holmes/core/llm.py#L751-L792)).

---

## 3. How alerts get in

### 3.1 Source plugins: pull, once, one alert at a time

`SourcePlugin` declares `fetch_issues()`, `fetch_issue(id)`, an optional `stream_issues()` and an optional `write_back_result(issue_id, result)` ([interfaces.py#L8-L21](https://github.com/HolmesGPT/holmesgpt/blob/50c6977756992d267c3eec8f36fcf09e986b675e/holmes/plugins/interfaces.py#L8-L21)). `stream_issues()` has no implementation anywhere. Commented-out `group_issues` and `send_grouped_issues` stubs show that grouping was considered and never built (same file, L25-L33).

Each `holmes investigate <source>` run loads config, builds a `ToolCallingLLM`, calls `fetch_issues()` once, investigates each issue in turn, and exits ([main.py#L455-L553](https://github.com/HolmesGPT/holmesgpt/blob/50c6977756992d267c3eec8f36fcf09e986b675e/holmes/main.py#L455-L553)). For Alertmanager, that means `GET /api/v2/alerts?active=true&silenced=false&inhibited=false`, with optional label and alertname filters ([prometheus/plugin.py#L61-L103](https://github.com/HolmesGPT/holmesgpt/blob/50c6977756992d267c3eec8f36fcf09e986b675e/holmes/plugins/sources/prometheus/plugin.py#L61-L103)). The issue ID is `{alertname}-{fingerprint}-{startsAt}`, unique per firing.

- **Nothing deduplicates or groups alerts.** Fifty firing alerts produce fifty independent investigations, run one after another ([main.py#L544-L550](https://github.com/HolmesGPT/holmesgpt/blob/50c6977756992d267c3eec8f36fcf09e986b675e/holmes/main.py#L544-L550)). *Inference:* reading `/api/v2/alerts` rather than `/api/v2/alerts/groups` also bypasses Alertmanager's own grouping.
- **There is no push path.** "Continuous" alert handling in open source means running the CLI from cron or CI. Webhook-driven handling exists only through Robusta (§3.4).
- **New Relic is not an alert source.** It ships only as a data toolset (NRQL query and account listing), even though the README row says "Investigate alerts" ([README.md#L71](https://github.com/HolmesGPT/holmesgpt/blob/50c6977756992d267c3eec8f36fcf09e986b675e/README.md#L71) vs [newrelic.py#L74-L190](https://github.com/HolmesGPT/holmesgpt/blob/50c6977756992d267c3eec8f36fcf09e986b675e/holmes/plugins/toolsets/newrelic/newrelic.py#L74-L190)).

### 3.2 The normalized alert is a display envelope around the raw payload

`Issue` has `id`, `name`, `source_type`, `source_instance_id`, `url`, `raw: dict`, and fields that are only for display. `priority`, `created_at`, `group_id` and `metadata` are commented out, and there is a `# TODO: look at finding in Robusta` ([issue.py#L12-L58](https://github.com/HolmesGPT/holmesgpt/blob/50c6977756992d267c3eec8f36fcf09e986b675e/holmes/core/issue.py#L12-L58)). Severity, labels and timestamps are not modelled. The LLM gets the raw payload instead.

### 3.3 How an alert becomes a prompt

From [main.py#L163-L190](https://github.com/HolmesGPT/holmesgpt/blob/50c6977756992d267c3eec8f36fcf09e986b675e/holmes/main.py#L163-L190):

```python
investigation_additions = f"Provide a terse analysis of the following {issue.source_type} alert/issue and why it is firing."
system_prompt = build_system_prompt(toolsets=ai.tool_executor.toolsets, skills=None,
    system_prompt_additions=investigation_additions, cluster_name=config.cluster_name, ask_user_enabled=False, ...)
user_prompt = generate_user_prompt(f"\n #This is context from the issue:\n{issue.raw}", context={})
return ai.call(messages, request_context=_CLI_REQUEST_CONTEXT)
```

- The system prompt is the standard chat prompt plus that one sentence. The user prompt is the Python `repr` of the raw alert. `skills=None` and `context={}` mean no runbooks or skills are attached on this path.
- Toolsets are not chosen per alert. The CLI enables every toolset whose prerequisites pass ([main.py#L514-L519](https://github.com/HolmesGPT/holmesgpt/blob/50c6977756992d267c3eec8f36fcf09e986b675e/holmes/main.py#L514-L519)). **Docs mismatch:** [Why HolmesGPT](https://holmesgpt.dev/latest/why-holmesgpt/) says Holmes "automatically extracts alert metadata (labels, severity, annotations), selects relevant toolsets".
- **History.** Until March 2026 a dedicated path existed: `/api/investigate`, `investigation_procedure.jinja2`, and a JSON-schema "sections" output. PR [#1688](https://github.com/HolmesGPT/holmesgpt/pull/1688), "Remove automated investigation API and consolidate to conversational interface", deleted it. `/api/chat` is now the only investigation endpoint. `FEATURES.md` and some docs pages still describe the removed API.

### 3.4 Alert-driven triage exists only through Robusta's queue

The conversations worker (`holmes/core/conversations_worker/`) starts inside the server only when the Supabase DAL is enabled ([server.py#L883-L887](https://github.com/HolmesGPT/holmesgpt/blob/50c6977756992d267c3eec8f36fcf09e986b675e/server.py#L883-L887)). It works like this:

1. It learns about pending `Conversations` rows from a Realtime broadcast or by discovery, and claims them atomically.
2. It rebuilds the message history from the stored events.
3. It runs the same chat pipeline as `/api/chat`.
4. It streams events back to Supabase ([processor.py#L90-L265](https://github.com/HolmesGPT/holmesgpt/blob/50c6977756992d267c3eec8f36fcf09e986b675e/holmes/core/conversations_worker/processor.py#L90-L265)).

PR [#1903](https://github.com/HolmesGPT/holmesgpt/pull/1903) introduced it as a service that "asynchronously processes pending conversations from Supabase". PR [#2482](https://github.com/HolmesGPT/holmesgpt/pull/2482) split it into `manual` executors ("a person is waiting", 10 slots) and `auto` executors ("alert-triage investigations and triggered workflows", 1 slot). The reason given: "a burst of background work took all five slots and a user's question waited behind it" ([sizing.py#L10-L48](https://github.com/HolmesGPT/holmesgpt/blob/50c6977756992d267c3eec8f36fcf09e986b675e/holmes/core/conversations_worker/sizing.py#L10-L48)).

For `alert_investigation` rows, the worker resolves Robusta's `GroupedIssues.aggregation_key` and uses it to select alert-specific skills ([processor.py#L291-L335](https://github.com/HolmesGPT/holmesgpt/blob/50c6977756992d267c3eec8f36fcf09e986b675e/holmes/core/conversations_worker/processor.py#L291-L335)). *Inference:* Robusta's closed relay receives the webhook, groups alerts into `GroupedIssues`, and inserts a queued conversation. This could not be verified because the relay is private.

### 3.5 Proactive triggers: the Holmes Operator

[docs] The operator follows the "Kubernetes Job/CronJob pattern": "a lightweight kopf-based controller handles CRD orchestration and scheduling, while stateless Holmes API servers execute the actual checks" ([operator](https://holmesgpt.dev/latest/operator/)).

- **`HealthCheck`** is a one-shot check. On create, the operator calls `POST /api/checks/execute` ([healthcheck.py#L28-L80](https://github.com/HolmesGPT/holmesgpt/blob/50c6977756992d267c3eec8f36fcf09e986b675e/holmes_operator/handlers/healthcheck.py#L28-L80)).
- **`ScheduledHealthCheck`** runs on a schedule. It uses APScheduler cron jobs rather than Kubernetes CronJobs to avoid pod start-up cost. Each tick creates a new child `HealthCheck` CR, so the history of checks is itself a set of Kubernetes objects ([scheduler/manager.py#L36-L96](https://github.com/HolmesGPT/holmesgpt/blob/50c6977756992d267c3eec8f36fcf09e986b675e/holmes_operator/scheduler/manager.py#L36-L96), [job_executor.py#L37-L100](https://github.com/HolmesGPT/holmesgpt/blob/50c6977756992d267c3eec8f36fcf09e986b675e/holmes_operator/scheduler/job_executor.py#L37-L100)).
- **`TriggeredHealthCheck`** has a single trigger type, `deploymentRollout`. The operator watches Deployments and keeps an in-memory sha256 of each `spec.template`. When the hash changes, it applies a label selector and a cooldown. After a default 300-second delay it spawns a `HealthCheck` whose query names the old and new images ([triggeredhealthcheck.py#L91-L249](https://github.com/HolmesGPT/holmesgpt/blob/50c6977756992d267c3eec8f36fcf09e986b675e/holmes_operator/handlers/triggeredhealthcheck.py#L91-L249), [trigger_executor.py#L72-L165](https://github.com/HolmesGPT/holmesgpt/blob/50c6977756992d267c3eec8f36fcf09e986b675e/holmes_operator/trigger_executor.py#L72-L165)). The first time the operator sees a Deployment, it only records the baseline, so a rollout during an operator restart is missed.
- **Server side.** Checks use their own system prompt: "You are a health check system. Evaluate ONLY what the check specifically asks about". The model is forced to return the strict JSON schema `{rationale, passed}`. Gemini rejects tools combined with a JSON schema, so it runs in two phases: tools first, then structured output ([checks.py#L39-L93](https://github.com/HolmesGPT/holmesgpt/blob/50c6977756992d267c3eec8f36fcf09e986b675e/holmes/checks/checks.py#L39-L93)). The schema says that if the model cannot evaluate the check, it "MUST return false" ([checks/models.py#L57-L65](https://github.com/HolmesGPT/holmesgpt/blob/50c6977756992d267c3eec8f36fcf09e986b675e/holmes/checks/models.py#L57-L65)).

Robusta has a second, separate scheduler. `ScheduledPromptsExecutor` polls Supabase for scheduled prompt runs and calls the `/api/chat` handler directly ([scheduled_prompts/executor.py#L110-L215](https://github.com/HolmesGPT/holmesgpt/blob/50c6977756992d267c3eec8f36fcf09e986b675e/holmes/core/scheduled_prompts/executor.py#L110-L215)).

---

## 4. How developer questions get in

### 4.1 CLI

`holmes ask` runs one-shot, reads piped stdin, or opens an interactive REPL with `-i`. In the REPL, history stays in process. A background thread drains `call_stream()` while handling approval prompts and Esc to cancel. Slash commands include `/run` and `/shell`, which execute a command and optionally share its output with the LLM, plus `/context`, `/tools`, `/feedback` and others ([interactive.py#L1144-L1162](https://github.com/HolmesGPT/holmesgpt/blob/50c6977756992d267c3eec8f36fcf09e986b675e/holmes/interactive.py#L1144-L1162); [docs](https://holmesgpt.dev/latest/reference/slash-commands/)). The K9s plugin is a `plugins.yaml` entry that shells out to `holmes ask "why is $NAME …"` ([docs](https://holmesgpt.dev/latest/installation/k9s-installation/)). The Python SDK exposes `Config`, `create_toolcalling_llm` and `ai.call(messages)` ([docs](https://holmesgpt.dev/latest/reference/python-sdk/)).

### 4.2 HTTP API: stateless, the client carries the conversation

`POST /api/chat` takes `ask`, `conversation_history` (OpenAI format), `stream`, `enable_tool_approval`, `tool_decisions`, `frontend_tools`, `frontend_tool_results`, `additional_system_prompt`, `response_format`, `behavior_controls` and `images` ([models.py#L196-L353](https://github.com/HolmesGPT/holmesgpt/blob/50c6977756992d267c3eec8f36fcf09e986b675e/holmes/core/models.py#L196-L353)). On each turn the server re-renders the system prompt from the current toolsets and skills, puts it at position 0, and appends the new user turn ([conversations.py#L46-L94](https://github.com/HolmesGPT/holmesgpt/blob/50c6977756992d267c3eec8f36fcf09e986b675e/holmes/core/conversations.py#L46-L94)).

It returns the full `conversation_history`, and the client must send it back on the next turn. That is how approvals and client-side tool pauses span two HTTP requests ([docs](https://holmesgpt.dev/latest/reference/http-api/)). The streaming events are `start_tool_calling`, `tool_calling_result`, `ai_message`, `ai_answer_end`, `approval_required`, `token_count`, compaction events and `error`. On the first turn only, the server adds canned follow-up buttons (Logs, Graphs, Articles) ([server.py#L618-L640](https://github.com/HolmesGPT/holmesgpt/blob/50c6977756992d267c3eec8f36fcf09e986b675e/server.py#L618-L640)).

### 4.3 Frontend tools: the client extends the agent

A client can declare tools in the request. A `pause` tool stops the stream with `approval_required` plus `pending_frontend_tool_calls`, and the client resumes with `frontend_tool_results`. A `noop` tool returns canned success ("The action was performed successfully in the user's browser."), and the client performs the side effect itself. These are ordinary `Tool` subclasses, so the loop stays generic ([frontend_tools.py#L1-L33](https://github.com/HolmesGPT/holmesgpt/blob/50c6977756992d267c3eec8f36fcf09e986b675e/holmes/core/tools_utils/frontend_tools.py#L1-L33)). This is how a web UI can let the agent "click" things the server cannot. `experimental/ag-ui` is a demo of the pattern and is "not intended for production use".

---

## 5. How it queries observability data

### 5.1 One abstraction: Toolset → Tool → `Tool.invoke`

A `Tool` has a name, a description, a typed `ToolParameter` schema (a small subset of JSON Schema, emitted as an OpenAI function schema), an `_invoke` method and optional transformers ([tools.py#L232-L336](https://github.com/HolmesGPT/holmesgpt/blob/50c6977756992d267c3eec8f36fcf09e986b675e/holmes/core/tools.py#L232-L336)). A `Toolset` groups tools and carries `enabled`, `prerequisites`, `config`, `llm_instructions`, `tags`, `approval_required_tools` and runtime `status` ([tools.py#L787-L905](https://github.com/HolmesGPT/holmesgpt/blob/50c6977756992d267c3eec8f36fcf09e986b675e/holmes/core/tools.py#L787-L905)). Every call, of every kind, goes through the same path ([tools.py#L392-L460](https://github.com/HolmesGPT/holmesgpt/blob/50c6977756992d267c3eec8f36fcf09e986b675e/holmes/core/tools.py#L392-L460)):

```
Tool.invoke(params, ctx)
  ├─ approval check: fnmatch(toolset.approval_required_tools) → tool.requires_approval()  → APPROVAL_REQUIRED
  ├─ _coerce_params()   (repair LLM type mistakes against the schema)
  ├─ _invoke()          (subclass)
  └─ _apply_transformers()
```

| Kind | How it is defined | Evidence |
|---|---|---|
| YAML / command | A Jinja-templated shell command. Parameters are inferred from `{{ }}` and wrapped in `shlex.quote`, then run through `bash -c` with a `ulimit -v` prefix. `${ENV}` values are hidden from the LLM. | [tools.py#L572-L749](https://github.com/HolmesGPT/holmesgpt/blob/50c6977756992d267c3eec8f36fcf09e986b675e/holmes/core/tools.py#L572-L749) |
| Python | A `Toolset` subclass with tools built in code, a callable health check and a Pydantic config | [prometheus.py#L2013-L2043](https://github.com/HolmesGPT/holmesgpt/blob/50c6977756992d267c3eec8f36fcf09e986b675e/holmes/plugins/toolsets/prometheus/prometheus.py#L2013-L2043) |
| MCP | Tools are listed from the remote server and filtered by `allowed_tools`. Transports are streamable-http, sse and stdio, plus AWS SigV4. | [toolset_mcp.py#L688-L717](https://github.com/HolmesGPT/holmesgpt/blob/50c6977756992d267c3eec8f36fcf09e986b675e/holmes/plugins/toolsets/mcp/toolset_mcp.py#L688-L717) |
| HTTP connector | One generic request tool, checked against an allowlist of hosts, paths and methods. Only `GET` is allowed by default. | [http_toolset.py#L242-L343](https://github.com/HolmesGPT/holmesgpt/blob/50c6977756992d267c3eec8f36fcf09e986b675e/holmes/plugins/toolsets/http/http_toolset.py#L242-L343) |
| Database | One query tool per dialect, with a read-only guard | [database.py#L91-L108](https://github.com/HolmesGPT/holmesgpt/blob/50c6977756992d267c3eec8f36fcf09e986b675e/holmes/plugins/toolsets/database/database.py#L91-L108) |

### 5.2 Tool shape: the vendor's query language plus discovery helpers

| Integration | Mechanism | What the LLM gets |
|---|---|---|
| Prometheus | Native Python | Raw PromQL instant and range queries (a `description` is required), plus discovery tools: rules, metric names, label values, series, metadata ([prometheus.py#L1563-L1594](https://github.com/HolmesGPT/holmesgpt/blob/50c6977756992d267c3eec8f36fcf09e986b675e/holmes/plugins/toolsets/prometheus/prometheus.py#L1563-L1594)) |
| Grafana / Loki / Tempo | Native Python, or the Grafana MCP server | Raw LogQL and TraceQL, plus dashboard search and panel render |
| Datadog | Native Python, split into four toolsets | Curated tools for metrics, spans and logs, plus a raw-GET escape hatch with an endpoint allowlist and a blocklist of write paths |
| New Relic | Native Python | Raw NRQL. Its own description warns that bad NRQL returns empty results silently. |
| Kubernetes | YAML `kubernetes/core` with three jq-over-kubectl query tools (paginated, with selectors applied server-side), Python `kubernetes/logs`, and **generic `bash`** for `kubectl get/describe/events/...` | Curated query tools plus raw kubectl through the bash allowlist ([kubernetes.yaml#L1-L40](https://github.com/HolmesGPT/holmesgpt/blob/50c6977756992d267c3eec8f36fcf09e986b675e/holmes/plugins/toolsets/kubernetes.yaml#L1-L40)) |
| Docker | YAML wrapper around the CLI, CLI-only tag | Fixed commands (`ps`, `inspect`, `logs`) |
| AWS | The hosted AWS MCP server | Whatever that server exposes. The docs say read-only "is enforced by this policy", meaning the IAM policy. |

*Inference:* the pattern is to give the model the vendor's native query language plus cheap discovery tools that ground it. Curation goes into tool descriptions and `llm_instructions`, not into adding more tools. One code comment states the opposite worry for HTTP access: a liveness-probe tool was left commented out "to avoid giving the LLM generic http GET capabilities which are more powerful than we want to expose" ([kubernetes.yaml#L643-L647](https://github.com/HolmesGPT/holmesgpt/blob/50c6977756992d267c3eec8f36fcf09e986b675e/holmes/plugins/toolsets/kubernetes.yaml#L643-L647)).

### 5.3 Lifecycle: enablement, prerequisites, and telling the model what is missing

- **Enablement has several layers, and the project itself calls them confusing.** Built-in toolsets default to `enabled=False`. The CLI force-enables every built-in that isn't missing config, while the server enables only what code or config turns on. After that come tag filters (`core`/`cli`/`cluster`), prerequisite checks (static, env, callable, command, each with a 20-second timeout) and a status cache ([toolset_manager.py#L134-L260](https://github.com/HolmesGPT/holmesgpt/blob/50c6977756992d267c3eec8f36fcf09e986b675e/holmes/core/toolset_manager.py#L134-L260)). [specs/toolset-enablement.md](https://github.com/HolmesGPT/holmesgpt/blob/50c6977756992d267c3eec8f36fcf09e986b675e/specs/toolset-enablement.md) lists eight layers and three options for fixing them, and chooses none.
- **Slow checks are deferred.** At start-up only static and env checks run. Callable and command checks run lazily on first use of a tool, so a broken integration shows up in the middle of an investigation.
- **Missing integrations are listed in the system prompt.** Disabled and failed toolsets appear with their error and setup URL, and the model is told to report them instead of guessing ([_toolsets_instructions.jinja2#L1-L36](https://github.com/HolmesGPT/holmesgpt/blob/50c6977756992d267c3eec8f36fcf09e986b675e/holmes/plugins/prompts/_toolsets_instructions.jinja2#L1-L36)).
- **Log fetching has one logical capability and many backends.** A priority chain in the prompt picks one log backend: Loki, then Coralogix, then Kubernetes logs, then Datadog, and so on ([_fetch_logs.jinja2#L1-L48](https://github.com/HolmesGPT/holmesgpt/blob/50c6977756992d267c3eec8f36fcf09e986b675e/holmes/plugins/prompts/_fetch_logs.jinja2#L1-L48)).
- **Multi-instance toolsets.** `multi_instance(Cls)` turns one toolset into N instances. It adds an `instance` parameter and a `<name>_list_instances` tool only when there is more than one instance ([multi_instance.py#L1-L30](https://github.com/HolmesGPT/holmesgpt/blob/50c6977756992d267c3eec8f36fcf09e986b675e/holmes/plugins/toolsets/multi_instance.py#L1-L30)).
- **Service discovery.** Prometheus is located automatically through well-known Service labels when no URL is configured ([service_discovery.py#L21-L90](https://github.com/HolmesGPT/holmesgpt/blob/50c6977756992d267c3eec8f36fcf09e986b675e/holmes/plugins/toolsets/service_discovery.py#L21-L90)).

### 5.4 Bounding tool output before it reaches the LLM

1. **Tools budget themselves.** Pod logs default to 100 lines and are truncated from the top to fit the per-tool token budget ([logging_api.py#L22-L121](https://github.com/HolmesGPT/holmesgpt/blob/50c6977756992d267c3eec8f36fcf09e986b675e/holmes/plugins/toolsets/logging_utils/logging_api.py#L22-L121)). When Prometheus results are over budget, the data is dropped and a summary of series count and label cardinality is returned instead ([prometheus.py#L1636-L1680](https://github.com/HolmesGPT/holmesgpt/blob/50c6977756992d267c3eec8f36fcf09e986b675e/holmes/plugins/toolsets/prometheus/prometheus.py#L1636-L1680)).
2. **A generic spill to disk applies to every tool.** Any result over min(15% of the context window, 25,000 tokens) is written to a per-chat directory. The LLM gets the path, a preview and a hint: "Use `cat {file_path}` to read it … `| jq`, `| grep`". `cat`/`head`/`jq` on that directory are pre-approved in bash. Without bash, the data is dropped with a "narrow your query" error ([tool_context_window_limiter.py#L33-L144](https://github.com/HolmesGPT/holmesgpt/blob/50c6977756992d267c3eec8f36fcf09e986b675e/holmes/core/tools_utils/tool_context_window_limiter.py#L33-L144), [llm.py#L364-L376](https://github.com/HolmesGPT/holmesgpt/blob/50c6977756992d267c3eec8f36fcf09e986b675e/holmes/core/llm.py#L364-L376)).
3. **LLM summarization of tool output has been retired.** The docs now call the `llm_summarize` transformer "a legacy feature… summarization is lossy" ([docs](https://holmesgpt.dev/latest/development/transformers/), [PR #2171](https://github.com/HolmesGPT/holmesgpt/pull/2171)).
4. **The direction of travel is spill, not truncate.** Open PRs [#2167](https://github.com/HolmesGPT/holmesgpt/pull/2167), [#2168](https://github.com/HolmesGPT/holmesgpt/pull/2168) and [#2169](https://github.com/HolmesGPT/holmesgpt/pull/2169) replace the remaining truncation with spill. #2168 gives the reason: "That steering existed because older models answered from truncated, unordered data and gave wrong answers".
5. **The process is capped too.** Every subprocess runs under `ulimit -v` (800 MB on x86). On OOM the model gets an `[OOM]` hint telling it to narrow the query ([memory_limit.py#L17-L25](https://github.com/HolmesGPT/holmesgpt/blob/50c6977756992d267c3eec8f36fcf09e986b675e/holmes/utils/memory_limit.py#L17-L25)).

### 5.5 Read-only guarantees are spread across mechanisms

| Mechanism | Guarantee |
|---|---|
| Kubernetes (in cluster) | The ClusterRole has only `get`/`list`/`watch` on every rule and no core `secrets` ([holmesgpt-service-account.yaml](https://github.com/HolmesGPT/holmesgpt/blob/50c6977756992d267c3eec8f36fcf09e986b675e/helm/holmes/templates/holmesgpt-service-account.yaml)). `namespaceScopedRBAC` renders a Role instead ([docs](https://holmesgpt.dev/latest/installation/namespace-scoped-access/)). |
| Kubernetes (CLI) | None beyond the user's own kubeconfig |
| Bash | Checks run in this order: (1) LLM-suggested prefixes must actually appear in the command; (2) a tree-sitter parse extracts every simple command, including those inside `$()`; (3) deny list; (4) allowlist (`kubectl get/describe/logs/top/events`, `jq`, `grep`…); (5) argument rules that block `find -exec`, redirection to files and similar. Anything unlisted or ambiguous needs approval ([validation.py#L552-L665](https://github.com/HolmesGPT/holmesgpt/blob/50c6977756992d267c3eec8f36fcf09e986b675e/holmes/plugins/toolsets/bash/validation.py#L552-L665)). **The default deny list is empty** ([default_lists.py#L81](https://github.com/HolmesGPT/holmesgpt/blob/50c6977756992d267c3eec8f36fcf09e986b675e/holmes/plugins/toolsets/bash/common/default_lists.py#L81)). *Inference:* with an admin kubeconfig on the CLI, `kubectl get secret -o yaml` is auto-allowed. |
| SQL | A read-only regex plus best-effort `SET TRANSACTION READ ONLY` |
| HTTP | GET-only by default |
| MCP | Only `allowed_tools` and `approval_required_tools`. Holmes does not read MCP `readOnlyHint` annotations. |
| AWS | IAM policy |

**Nothing redacts tool output.** A grep for `redact` finds only log-side masking. Whatever a tool can read (ConfigMaps, environment values in pod specs, log lines) reaches the LLM verbatim. The only defence is a prompt line: "never guess values you cannot see, like secrets".

---

## 6. How the LLM turns evidence into an RCA

### 6.1 The loop

`call_stream` is a plain `while i < max_steps` loop over LiteLLM, with no LangChain or LangGraph ([tool_calling_llm.py#L1160-L1654](https://github.com/HolmesGPT/holmesgpt/blob/50c6977756992d267c3eec8f36fcf09e986b675e/holmes/core/tool_calling_llm.py#L1160-L1654)). Each iteration does the following:

1. Replay any approval decisions and frontend-tool results. Insert denial results for orphaned tool calls, because "every tool_use block must be followed by a tool_result block".
2. On the last step, remove the tools so the model has to answer.
3. Compact the history if needed (§6.5).
4. Make one **non-streaming** `completion` call. "Streaming" means one SSE event per iteration, not per token. Temperature defaults to `0.00000001` ([env_vars.py#L79-L90](https://github.com/HolmesGPT/holmesgpt/blob/50c6977756992d267c3eec8f36fcf09e986b675e/holmes/common/env_vars.py#L79-L90)).
5. **Stop condition:** the first assistant message without `tool_calls` is the answer.
6. Otherwise, run the tool calls **in parallel** on a 16-thread pool. The prompt also says "Make multiple independent tool calls in parallel whenever possible" ([tool_calling_llm.py#L1469-L1495](https://github.com/HolmesGPT/holmesgpt/blob/50c6977756992d267c3eec8f36fcf09e986b675e/holmes/core/tool_calling_llm.py#L1469-L1495)).
7. Route each result by status. `APPROVAL_REQUIRED` and `FRONTEND_PAUSE` pause the stream (§7). Everything else is appended as a `tool` message. The message is prefixed with the parameters used, so the model can tie a claim to the call that produced it, though nothing checks that it does.

**Budgets and guards.**

- `max_steps` is 100 ([config.py#L113](https://github.com/HolmesGPT/holmesgpt/blob/50c6977756992d267c3eec8f36fcf09e986b675e/holmes/config.py#L113)). Running past it raises `"Too many LLM calls - exceeded max_steps"`, an exception rather than a partial answer, and the model is never told how many steps remain.
- The only loop guard refuses an *exact* repeat of the same tool and parameters. Its comment says: "It is only reasonable to prevent identical tool calls if Holmes is read only and does not mutate resources" ([safeguards.py#L24-L50](https://github.com/HolmesGPT/holmesgpt/blob/50c6977756992d267c3eec8f36fcf09e986b675e/holmes/core/safeguards.py#L24-L50)).
- Tool exceptions and unknown tools become `ERROR` results that the model sees. LLM API errors abort the request.

### 6.2 Prompts

One system prompt, `generic_ask.jinja2`, serves chat, alerts and scheduled prompts, with sections that can be switched off per request through `behavior_controls` ([prompt.py#L13-L40](https://github.com/HolmesGPT/holmesgpt/blob/50c6977756992d267c3eec8f36fcf09e986b675e/holmes/core/prompt.py#L13-L40), [#L206-L293](https://github.com/HolmesGPT/holmesgpt/blob/50c6977756992d267c3eec8f36fcf09e986b675e/holmes/core/prompt.py#L206-L293)). The user prompt wraps the question with the skill catalogue, team instructions and the current UTC time. Its key rules ([generic_ask.jinja2](https://github.com/HolmesGPT/holmesgpt/blob/50c6977756992d267c3eec8f36fcf09e986b675e/holmes/plugins/prompts/generic_ask.jinja2)):

- **Depth.** "keep going until you reach the deepest root cause you can find". It also asks for "five whys" across services, a list of alternative causes, a check of application logs, walking the ownership chain (Deployment → ReplicaSet → Pod), and sampling up to 3 pods per Deployment (L46-L78).
- **Evidence.** "Distinguish confirmed facts (directly observed in tool output) from hypotheses" (L54). Under "Impact and blast radius": "Never widen the blast radius by inference" and "Coincidence in time is not causation, and 'also affected' is a claim that needs its own evidence" (L63-L64). Every entity must be sorted into *Affected* (with a reason token such as `OOMKilled`), *Checked, healthy*, *Changed during the window for another reason*, or *Not checked*.
- **Citing.** "Do not say 'based on the tool output' or explicitly refer to tools at all" (L4). Logs: "ALWAYS tell the user what logs you fetched and how" ([_fetch_logs.jinja2#L12](https://github.com/HolmesGPT/holmesgpt/blob/50c6977756992d267c3eec8f36fcf09e986b675e/holmes/plugins/prompts/_fetch_logs.jinja2#L12)). Evidence is cited by quoting data inline, not by referencing tool calls.
- **Remediation is offered in prose.** "If you have a concrete suggestion for how the user can fix something, share it even if not asked explicitly" (L6).
- **Prompt hygiene.** PR [#2301](https://github.com/HolmesGPT/holmesgpt/pull/2301) cut the prompt by about 60% because "Newer models over-trigger on repetition, ALL-CAPS emphasis and consequence language".

### 6.3 Planning is advisory and off by default

`TodoWrite` (always available) echoes a task list back to the model and stores nothing. Its reply says the tasks "are now stored in session", but the list survives only as a tool message in the history, and nothing checks that the tasks are complete before the model answers ([core_investigation.py#L38-L146](https://github.com/HolmesGPT/holmesgpt/blob/50c6977756992d267c3eec8f36fcf09e986b675e/holmes/plugins/toolsets/investigator/core_investigation.py#L38-L146)). Its prompt sections have been off by default since PR [#2471](https://github.com/HolmesGPT/holmesgpt/pull/2471). The reason given: the planning round trip "a customer saw as 10–20 s before 'I'm on it' in Slack".

### 6.4 Runbooks became Skills, and the LLM chooses them

- **Format.** "Skills are supported starting in Holmes 0.26.0. Earlier versions use the legacy runbook system" ([docs](https://holmesgpt.dev/latest/reference/skills/)). A skill is a `SKILL.md` file with frontmatter (`description` is required) and a markdown body. The recommended sections include **Recommended Remediation Steps**, so skills carry remediation guidance, as text only.
- **Sources, in order of precedence.** Built-in, then filesystem paths (including git repositories synced through an atomic symlink flip), then Robusta SaaS skills, then personal SaaS skills ([skill_loader.py#L497-L574](https://github.com/HolmesGPT/holmesgpt/blob/50c6977756992d267c3eec8f36fcf09e986b675e/holmes/plugins/skills/skill_loader.py#L497-L574), [git_skill_repos.py#L1-L64](https://github.com/HolmesGPT/holmesgpt/blob/50c6977756992d267c3eec8f36fcf09e986b675e/holmes/plugins/skills/git_skill_repos.py#L1-L64)). **Docs mismatch:** the docs say built-in skills ship in `holmes/plugins/skills/builtin/`, but at this commit that directory holds only an empty `__init__.py`.
- **Matching.** The LLM sees a catalogue of `name | description` lines. If a skill "clearly matches", it loads the skill with `fetch_skill`. "Holmes matches skills to investigations using the `description` field" ([docs](https://holmesgpt.dev/latest/reference/skills-best-practices/)). The only deterministic match is an alert-name filter on SaaS skills, and it applies only to queued alert conversations ([skill_loader.py#L71-L80](https://github.com/HolmesGPT/holmesgpt/blob/50c6977756992d267c3eec8f36fcf09e986b675e/holmes/plugins/skills/skill_loader.py#L71-L80)).
- **Wrapping.** A fetched skill is wrapped as "DIRECTIONS not ACTUAL RESULTS… You must follow them by CALLING TOOLS YOURSELF". The answer must begin "I found a skill named … and used it to troubleshoot:" and mark each step ✅ or ❌ ([skills_fetcher.py#L192-L236](https://github.com/HolmesGPT/holmesgpt/blob/50c6977756992d267c3eec8f36fcf09e986b675e/holmes/plugins/toolsets/skills/skills_fetcher.py#L192-L236)). *Inference:* this checklist is the closest thing to a structured RCA that Holmes still produces, and only the prompt enforces it.

### 6.5 Context management

There are two layers.

1. **Per-result spill** (§5.4).
2. **History compaction.** It triggers when tokens plus the output reserve pass 95% of the window ([input_context_window_limiter.py#L83-L98](https://github.com/HolmesGPT/holmesgpt/blob/50c6977756992d267c3eec8f36fcf09e986b675e/holmes/core/truncation/input_context_window_limiter.py#L83-L98), [llm.py#L192](https://github.com/HolmesGPT/holmesgpt/blob/50c6977756992d267c3eec8f36fcf09e986b675e/holmes/core/llm.py#L192)). An LLM writes an 8-part summary that includes verbatim log extracts and "every tool call with ✅/❌ status and its full arguments". The new history is just the system prompt, that summary, and the last user message, so **every tool message is dropped** ([compaction.py#L36-L43](https://github.com/HolmesGPT/holmesgpt/blob/50c6977756992d267c3eec8f36fcf09e986b675e/holmes/core/truncation/compaction.py#L36-L43), [#L352-L517](https://github.com/HolmesGPT/holmesgpt/blob/50c6977756992d267c3eec8f36fcf09e986b675e/holmes/core/truncation/compaction.py#L352-L517)). Calls whose output caused the overflow stay in the duplicate guard, so the model can't loop on re-fetching them.

The output reserve is `max(64000, 12% of window)` and is always sent as `max_tokens` ([docs](https://holmesgpt.dev/latest/reference/context-management/)).

### 6.6 The RCA output

```python
class ChatResponse(BaseModel):          # holmes/core/models.py#L362-L368
    analysis: str                       # the final assistant markdown
    conversation_history: list[dict]    # full OpenAI-format messages
    tool_calls: Optional[List[ToolCallResult]] = []   # {tool_call_id, tool_name, description, toolset_name, result{status,data,error,params}}
    follow_up_actions: Optional[List[FollowUpAction]] = []
    pending_approvals: Optional[List[PendingToolApproval]] = None
    metadata: Optional[Dict[Any, Any]] = None
```

([models.py#L362-L368](https://github.com/HolmesGPT/holmesgpt/blob/50c6977756992d267c3eec8f36fcf09e986b675e/holmes/core/models.py#L362-L368))

- **No confidence and no machine-readable citations.** The only structured link from the answer to the evidence is for images: the model may embed `![caption](tool-image://<tool_call_id>)`.
- **Structured output is generic and opt-in.** A caller may pass any `response_format`, and the JSON comes back as a string in `analysis` ([docs](https://holmesgpt.dev/latest/reference/http-api/)). Health checks are the only built-in user of it.
- **What was removed.** Before PR #1688, `/api/investigate` built a JSON schema from fixed sections: "Alert Explanation", "Key Findings", "Conclusions and Possible Root causes" ("Don't say root cause but 'possible root causes'"), "Next Steps", "Related logs", "App or Infra?" and "External links". The schema used nullable strings with `strict: False` and had fallbacks for parsing markdown ([historical investigation_structured_output.py#L19-L57](https://github.com/HolmesGPT/holmesgpt/blob/ab74f401285c0ef0755977ee3d469e90a976ddf9/holmes/core/investigation_structured_output.py#L19-L57)). *Inference:* the fallbacks suggest schema compliance was unreliable across providers. That, together with the one-interface simplification, is the likely cost that led to the removal.
- **Hallucination controls.** These are the prompt rules above, parameter echoes on tool output, the "DIRECTIONS not ACTUAL RESULTS" skill wrapper, and offline evals (§9). Nothing verifies grounding at runtime.

---

## 7. Remediation

### 7.1 It is the same loop, paused

There is no planner, no separate remediation phase, and no executor. A write is a tool call whose result is `APPROVAL_REQUIRED`:

```
Tool.invoke → _check_approval_config (fnmatch on approval_required_tools) or tool.requires_approval()
   → StructuredToolResult(status=APPROVAL_REQUIRED)                                tools.py#L402-L460
call_stream: approval enabled?
   ├ yes → mint JWT {tool_call_id, tool_name, sha256(args)}; yield approval_required {messages, pending_approvals}; return
   └ no  → rewrite to ERROR "...NOT executed and NOT submitted or queued for approval..."   tool_calling_llm.py#L1520-L1561
--- client renders the prompt; sends conversation_history + tool_decisions ---
call_stream → _execute_tool_decisions: verify JWT → approved: re-invoke with user_approved=True
                                                  → denied: ERROR "denied by the user. User feedback: …"
… loop continues; the LLM reads the real output of its write and keeps going
```

([tool_calling_llm.py#L376-L468](https://github.com/HolmesGPT/holmesgpt/blob/50c6977756992d267c3eec8f36fcf09e986b675e/holmes/core/tool_calling_llm.py#L376-L468), [#L1602-L1635](https://github.com/HolmesGPT/holmesgpt/blob/50c6977756992d267c3eec8f36fcf09e986b675e/holmes/core/tool_calling_llm.py#L1602-L1635))

- **CLI.** The user picks Yes, "Yes and remember this prefix" (saved to `~/.holmes/bash_approved_prefixes.yaml`), or No with free-text feedback that goes back to the LLM ([interactive.py#L1836-L1900](https://github.com/HolmesGPT/holmesgpt/blob/50c6977756992d267c3eec8f36fcf09e986b675e/holmes/interactive.py#L1836-L1900)). `--bash-always-allow` installs a callback that approves *every* pending approval, MCP writes included ([interactive.py#L2310-L2327](https://github.com/HolmesGPT/holmesgpt/blob/50c6977756992d267c3eec8f36fcf09e986b675e/holmes/interactive.py#L2310-L2327)).
- **HTTP.** Approval works only on the streaming path. Non-streaming calls have no callback, so tools that need approval always error there ([server.py#L747-L799](https://github.com/HolmesGPT/holmesgpt/blob/50c6977756992d267c3eec8f36fcf09e986b675e/server.py#L747-L799)).
- **Batch and alert paths.** `holmes investigate …` and health checks call the loop with no approval callback. Tools that need approval therefore always fail closed there (§7.4 covers tools that don't).

### 7.2 The Kubernetes Remediation MCP server

The remediation server's spec sets out the central design principle ([specs/kubernetes-remediation-mcp.md#L22-L46](https://github.com/HolmesGPT/holmesgpt/blob/50c6977756992d267c3eec8f36fcf09e986b675e/specs/kubernetes-remediation-mcp.md#L22-L46)):

> **Approval legibility through tool separation.** Every tool is either always auto-approved or always approval-gated. The split is encoded in the *tool set*, not in per-command heuristics the model has to reason about.
>
> All *policy* lives in the MCP server … HolmesGPT only maps tool name → approval … This keeps the agent generic and puts the security-sensitive logic in one place (the server), versionable with the server image.

The parts of the design:

- **Tools.** Five read and diagnostic tools are auto-approved. One catch-all, `run_kubectl_command`, needs approval and covers "all mutations, arbitrary exec, non-allowlisted images" ([docs](https://holmesgpt.dev/latest/data-sources/builtin-toolsets/kubernetes-remediation-mcp/)).
- **Guards on the gated tool.** The server enforces a verb allowlist (`edit, patch, delete, scale, rollout, cordon, uncordon, drain, taint, label, annotate, run, exec`), a flag blocklist (`--context`, `--as`, `--token`…), no shell, a 60-second timeout, and a kill switch ([spec#L84-L96](https://github.com/HolmesGPT/holmesgpt/blob/50c6977756992d267c3eec8f36fcf09e986b675e/specs/kubernetes-remediation-mcp.md#L84-L96)).
- **Credentials.** The server runs under its own scoped ServiceAccount (no `cluster-admin`, no secrets), so Holmes's own RBAC stays read-only ([spec#L119-L128](https://github.com/HolmesGPT/holmesgpt/blob/50c6977756992d267c3eec8f36fcf09e986b675e/specs/kubernetes-remediation-mcp.md#L119-L128)). Helm wires `approvalRequiredTools: ["run_kubectl_command"]` ([values.yaml#L853-L854](https://github.com/HolmesGPT/holmesgpt/blob/50c6977756992d267c3eec8f36fcf09e986b675e/helm/holmes/values.yaml#L853-L854)).
- **History.** This design replaced an older `restricted_tools` mechanism in which skills unlocked tools ([PR #2148](https://github.com/HolmesGPT/holmesgpt/pull/2148)).
- **Residual risks the spec itself accepts** ([spec#L279-L352](https://github.com/HolmesGPT/holmesgpt/blob/50c6977756992d267c3eec8f36fcf09e986b675e/specs/kubernetes-remediation-mcp.md#L279-L352)):
  - "'Gated' is enforced only by HolmesGPT's `approval_required_tools`; the server has no notion of approval and will run `run_kubectl_command` if called directly." The backstop is an ingress NetworkPolicy, which does nothing on CNIs that don't enforce policies.
  - Auto-approved `pods/exec`.
  - The tool-name mapping must stay in sync between two repositories. A related bug: `approval_required_tools` is ignored for custom YAML toolsets ([#2312](https://github.com/HolmesGPT/holmesgpt/issues/2312)).
  - There is no single "actor + tool + target + approval + outcome" audit event. Approval decisions "live in the HolmesGPT/Robusta interaction history".
  - "There are no LLM evals specific to this toolset yet." Checking that "the model verifies the result" after a `rollout restart` or `scale` is an unticked plan item ([spec#L374-L401](https://github.com/HolmesGPT/holmesgpt/blob/50c6977756992d267c3eec8f36fcf09e986b675e/specs/kubernetes-remediation-mcp.md#L374-L401)).

### 7.3 Approval tokens: they stop forgery, but don't record who approved

The client holds the history, so a client could forge `pending_approval` entries. Holmes therefore signs each proposed call as an HS256 JWT over `{tool_call_id, tool_name, sha256(canonical args)}` and checks the token when the call resumes ([approval_tokens.py#L46-L115](https://github.com/HolmesGPT/holmesgpt/blob/50c6977756992d267c3eec8f36fcf09e986b675e/holmes/utils/approval_tokens.py#L46-L115); [PR #2210](https://github.com/HolmesGPT/holmesgpt/pull/2210), [PR #2394](https://github.com/HolmesGPT/holmesgpt/pull/2394)). The token proves that *Holmes proposed* this exact call. It says nothing about *who* approved it.

PR #2210 states: "Replay protection (jti + redemption cache) and multi-replica caveats are intentionally out of scope". **Mismatch:** the TTL is 30 days in code ([approval_tokens.py#L24](https://github.com/HolmesGPT/holmesgpt/blob/50c6977756992d267c3eec8f36fcf09e986b675e/holmes/utils/approval_tokens.py#L24)), the docs say "short-lived" ([docs](https://holmesgpt.dev/latest/reference/environment-variables/#holmes_approval_signing_key)), and PR #2394 says "15 minutes by default".

### 7.4 Writes that need no approval, and "opens fix PRs"

Approval applies only to tools that match `approval_required_tools` or override `requires_approval()` ([tools.py#L440-L460](https://github.com/HolmesGPT/holmesgpt/blob/50c6977756992d267c3eec8f36fcf09e986b675e/holmes/core/tools.py#L440-L460)). Every other tool runs on every path, including batch investigations and health checks.

The operator docs promise: "Connect the GitHub MCP server so Holmes can open PRs to fix the problems it finds" ([operator](https://holmesgpt.dev/latest/operator/)). The GitHub MCP docs say: "Write permissions … are only required if you want HolmesGPT to be able to open PRs, create issues, or trigger workflows" ([github-mcp](https://holmesgpt.dev/latest/data-sources/builtin-toolsets/github-mcp/)). The Helm chart sets an approval list only for the Kubernetes remediation add-on.

*Inference:* GitHub write tools (`create_pull_request`, `push_files`) are not approval-gated by default. The gate is the scope of the GitHub token plus human review of the PR. No code path or prompt specifically drives "open a fix PR". It is a capability the model may choose to use.

### 7.5 What is absent

- **No catalogue of named actions.** The "allowlist" is a list of kubectl verbs plus bash prefixes.
- **No dry run, no rollback primitive, and no deterministic check that a fix worked.** Verification depends on the model choosing to re-read state after the loop resumes.
- **No autonomy levels.** In open source there are three effective modes per conversation: approval off (gated calls error), approval on (prompt), and always-allow (CLI). PR [#2461](https://github.com/HolmesGPT/holmesgpt/pull/2461) mentions an account-level "confirmation mode" in the closed Robusta relay.
- **No approver identity or role check** beyond "the user who owns the conversation" ([processor.py#L127-L143](https://github.com/HolmesGPT/holmesgpt/blob/50c6977756992d267c3eec8f36fcf09e986b675e/holmes/core/conversations_worker/processor.py#L127-L143)). There is no rule such as "approver differs from requester" or "on-call only".
- **No audit table.** Approvals persist only as Robusta conversation events. The recommended audit trail is Kubernetes API audit logs plus the MCP server's own logs.

---

## 8. Reporting and the human loop

### 8.1 Output sinks

- **Destinations.** The plugin types are `slack`, `cli` and `webhook` ([destinations/__init__.py#L4-L7](https://github.com/HolmesGPT/holmesgpt/blob/50c6977756992d267c3eec8f36fcf09e986b675e/holmes/plugins/destinations/__init__.py#L4-L7)). The CLI help calls Slack "experimental".
  - **Slack** posts the answer, then threads one file per tool output, the issue metadata, and a "🐞 DEBUG: messages with OpenAI" file holding the **entire message history** ([slack/plugin.py#L18-L133](https://github.com/HolmesGPT/holmesgpt/blob/50c6977756992d267c3eec8f36fcf09e986b675e/holmes/plugins/destinations/slack/plugin.py#L18-L133)).
  - **Webhook** sends a versioned payload (`schema_version=1`, `event_type="holmes.check.result"`). It refuses redirects and URLs that contain credentials ([webhook/plugin.py#L13-L146](https://github.com/HolmesGPT/holmesgpt/blob/50c6977756992d267c3eec8f36fcf09e986b675e/holmes/plugins/destinations/webhook/plugin.py#L13-L146)).
  - **PagerDuty** (Events API v2) is used only by the CLI checks runner.
- **Writeback to the source** is opt-in with `--update`. It posts the free-text answer as a comment on the Jira issue, GitHub issue, PagerDuty incident or OpsGenie alert, prefixed "Automatic AI Investigation by Robusta/HolmesGPT". It writes no fields, labels or status changes. The Jira code still carries `# TODO: upload files and show tool usage` ([jira/__init__.py#L85-L114](https://github.com/HolmesGPT/holmesgpt/blob/50c6977756992d267c3eec8f36fcf09e986b675e/holmes/plugins/sources/jira/__init__.py#L85-L114)).
- **The HTTP response** carries the full trace: `analysis`, `tool_calls`, `conversation_history`, and `metadata.request_id`, which the frontend uses to attach feedback (§6.6).
- **Provenance link.** The prompt tells the model: "Whenever you create an artifact outside this conversation … a pull request, merge request, issue, or ticket — include that link in its description" ([generic_ask.jinja2#L122-L128](https://github.com/HolmesGPT/holmesgpt/blob/50c6977756992d267c3eec8f36fcf09e986b675e/holmes/plugins/prompts/generic_ask.jinja2#L122-L128)). That is the only link between a PR or ticket and the investigation that produced it.

### 8.2 How an operator receives an RCA and starts a fix

| Step | Who implements it |
|---|---|
| Alert lands in Slack with an "Ask Holmes" action | Robusta relay (closed). Open source has only the one-shot Slack poster. |
| RCA is produced | Open-source `/api/chat` or the conversations worker. Alert context comes from Robusta's `Issues`/`Evidence` tables or from the request body. |
| Follow-up questions | The client sends back the full message history each turn (HTTP), or the worker replays stored `ConversationEvents`. Open source has no server-side session. |
| Approve an action | The pause/resume and JWT mechanics are open source. The Slack approval form and web modal are Robusta (closed). The CLI menu is open source. |
| Open a PR | GitHub MCP write tools, not approval-gated by default (§7.4) |
| Fix it by hand | Follow the prose suggestion in the answer |

Feedback from the platform "do[es] NOT go through Holmes. The frontend calls the public.record_feedback() Postgres function directly" ([supabase_dal.py#L1268-L1273](https://github.com/HolmesGPT/holmesgpt/blob/50c6977756992d267c3eec8f36fcf09e986b675e/holmes/core/supabase_dal.py#L1268-L1273)).

### 8.3 Health-check results

The operator writes `phase`, `result`, `message`, `rationale`, the model used and the notification outcome into the CR status, plus a condition and a `Normal` or `Warning` Kubernetes Event ([handlers/healthcheck.py#L79-L163](https://github.com/HolmesGPT/holmesgpt/blob/50c6977756992d267c3eec8f36fcf09e986b675e/holmes_operator/handlers/healthcheck.py#L79-L163)). Destinations are notified only when the result is `FAIL` and `mode=alert` ([checks_api.py#L151-L184](https://github.com/HolmesGPT/holmesgpt/blob/50c6977756992d267c3eec8f36fcf09e986b675e/holmes/checks/checks_api.py#L151-L184)). A failing check takes no action. CI gating means polling `.status.result` ([docs](https://holmesgpt.dev/latest/operator/deployment-verification/)).

**Docs mismatch:** the operator docs list a PagerDuty destination, but the operator path handles only Slack and webhook (`# Add other destination types here (pagerduty, etc.) as needed`, [checks_api.py#L276](https://github.com/HolmesGPT/holmesgpt/blob/50c6977756992d267c3eec8f36fcf09e986b675e/holmes/checks/checks_api.py#L276)).

---

## 9. Quality and self-observability

- **Eval cases.** There are about 280 cases at `tests/llm/fixtures/test_ask_holmes/<NN_name>/test_case.yaml`. Each has a `user_prompt`, an `expected_output` list, `before_test`/`after_test` shell steps (`kubectl apply`/`delete` with readiness loops) and tags ([example](https://github.com/HolmesGPT/holmesgpt/blob/50c6977756992d267c3eec8f36fcf09e986b675e/tests/llm/fixtures/test_ask_holmes/01_how_many_pods/test_case.yaml)).
- **Live infrastructure, no mocks.** Recorded fixtures were removed in PR [#1631](https://github.com/HolmesGPT/holmesgpt/pull/1631) (February 2026).
- **LLM judge.** An autoevals `LLMClassifier` checks that every expected element is present (A/B choice, chain of thought) ([classifiers.py#L135-L230](https://github.com/HolmesGPT/holmesgpt/blob/50c6977756992d267c3eec8f36fcf09e986b675e/tests/llm/utils/classifiers.py#L135-L230)). The docs tell authors to use unique facts that cannot be hallucinated ([docs](https://holmesgpt.dev/latest/development/evaluations/adding-evals/)).
- **Cadence.** "Evals no longer run automatically on every commit (this was the main CI eval cost driver). They run on-demand only" ([eval-regression.yaml#L1-L8](https://github.com/HolmesGPT/holmesgpt/blob/50c6977756992d267c3eec8f36fcf09e986b675e/.github/workflows/eval-regression.yaml#L1-L8)). A fast benchmark runs weekly. The docs still say "every commit". Results are published as dated model comparisons ([benchmarks](https://holmesgpt.dev/latest/development/evaluations/latest-results/)).
- **Self-observability.** OpenTelemetry switches on when `OTEL_EXPORTER_OTLP_ENDPOINT` is set. The trace tree is `holmesgpt.investigation` → `gen_ai.chat` per iteration → `holmesgpt.tool.<name>`, and trace context propagates into MCP calls. Prompt and tool content appear in spans only with `HOLMES_LANGFUSE_ATTRIBUTES=true` ([docs](https://holmesgpt.dev/latest/reference/opentelemetry/), [tracing.py#L123-L129](https://github.com/HolmesGPT/holmesgpt/blob/50c6977756992d267c3eec8f36fcf09e986b675e/holmes/core/tracing.py#L123-L129)).

---

## 10. Key architectural decisions

| # | Decision | What it buys | What it costs | Evidence |
|---|---|---|---|---|
| 1 | One hand-rolled loop on LiteLLM under every entry point | One loop to guard, trace and eval. Full control of pauses, compaction and provider quirks. | Provider quirks are hand-patched. Entry-point needs (grouping, structured RCA) must live outside the loop. | §2.2, §6.1 |
| 2 | Everything is chat. `/api/investigate` and structured sections deleted | One prompt and one interface. Follow-up questions come free. | No RCA schema for tickets, no confidence, stale docs | PR #1688 |
| 3 | Stateless server, the client carries history | Horizontal scale with no session store | Large payloads. Approvals need JWTs to resist forgery. No server-side record. | §4.2, §7.3 |
| 4 | Alerts pulled by the CLI, raw payload in the prompt | No infrastructure, nothing exposed to attack, a new source is about 100 lines | No real-time response, no dedup or grouping, N alerts mean N investigations | §3.1–3.3 |
| 5 | Toolsets expose native query languages plus discovery helpers | Few tools, full expressiveness, vendor knowledge transfers to the model | The model must know the query language. Bad queries return empty results. | §5.2 |
| 6 | Generic bash with a parsed allowlist instead of many CLI wrappers | Open-ended kubectl investigation, plus approve-this-prefix UX | Large validator surface; empty default deny list | §5.5 |
| 7 | Read-only through credentials (RBAC/IAM), not agent logic | A guarantee that holds whatever the LLM does | The CLI inherits the user's power. MCP servers may expose writes. No redaction. | §5.5 |
| 8 | Spill oversized results to disk and retire LLM summarization | Lossless; the model greps exactly what it needs | Needs bash and a writable filesystem; extra turns | §5.4 |
| 9 | Show disabled or failed toolsets to the model | The model reports missing integrations instead of hallucinating | Prompt tokens | §5.3 |
| 10 | Skills chosen by the LLM from a description catalogue | Lazy loading, cheap in tokens | Non-deterministic; depends on description quality | §6.4 |
| 11 | Remediation = the same loop paused at a gated tool call | No new machinery. The model sees the real write output. | The proposer and the executor are the same LLM context. No plan artifact to review, replay or audit. | §7.1 |
| 12 | Approval keyed by tool name, write policy in a separate MCP server with its own ServiceAccount | Legible approval; policy versioned in one place | The server doesn't enforce approval. Tools that aren't gated (GitHub writes) run unprompted. | §7.2, §7.4 |
| 13 | Fail closed when no approval channel exists | Unattended runs never run gated commands | Alert-triggered runs can never remediate. No "propose and queue" middle level. | §7.1 |
| 14 | Operator as a thin kopf client of the stateless API, with CRs as history | Kubernetes-native UX; scheduling isolated from execution | Extra component; in-memory rollout baseline; no auth | §3.5 |
| 15 | Live-cluster evals with an LLM judge, mocks removed | Realistic; catches tool and prompt regressions | Expensive and nondeterministic, so no longer per commit | §9 |
| 16 | Open core: SaaS features gated on a token, pull-based queue for the UI | The open-source agent runs on its own. The cluster needs no ingress. | Hard `supabase` dependency; the multi-tenant runtime is closed | §1.1, §3.4 |

---

## 11. What this means for incident-copilot

This section uses our own vocabulary ([CONTEXT.md](../../CONTEXT.md)) and our accepted decisions ([ADR 0001](../adr/0001-autonomy-and-trust-model.md), [architecture](../adr/arch-diagram.md)). It is analysis, not a decision. Anything worth adopting should go through an issue or an ADR.

### 11.1 Where HolmesGPT supports our existing decisions

| Our decision | What HolmesGPT shows |
|---|---|
| A typed RCA contract validated against a struct ([arch-diagram](../adr/arch-diagram.md#rca-contract)) | HolmesGPT gave this up (PR #1688) and now cannot write structured tickets, show confidence or cite evidence machine-readably. Its removed schema used `strict: False` plus markdown fallbacks, and Gemini cannot combine tools with a JSON schema in one call. Their health checks already use the fix: **investigate with tools first, then make one schema-constrained call for the final output.** We should plan our RCA step the same way, as two phases. |
| An Executor separate from the agent, which never calls an LLM, and Plans that are data ([ADR 0001](../adr/0001-autonomy-and-trust-model.md) D4) | HolmesGPT's remediation is the same LLM context proposing and executing. Its own spec lists the residual risks our split avoids: the server doesn't enforce approval, safety depends on a NetworkPolicy, there is no structured audit event, and post-write verification is left to the model. |
| A closed set of typed Actions (D1) | Their "approval legibility through tool separation" reaches the same principle at the tool level. They stop at kubectl verbs, so one gated tool covers `delete ns` and `annotate` alike. Our Actions add typed arguments, bounds and preconditions on top. |
| Autonomy levels computed in code that can only go down (D2, D3) | HolmesGPT has approval on or off per conversation. Alert-triggered runs can never remediate, and the CLI can approve everything. Our L0 shadow mode and L1 "suggest" fill the gap they have ("no propose-and-queue level"). |
| Approval bound to the exact action (D5) | Their JWT binds the call and its argument hash, but it has a 30-day TTL, no replay protection and no approver identity. D5's digest (target generation, dry-run result), TTL and approver check are the parts they explicitly scoped out. |
| Budgets that still produce an RCA ([arch-diagram](../adr/arch-diagram.md#components-and-their-boundaries)) | HolmesGPT raises an exception after `max_steps` and never tells the model how many steps remain. It does remove tools on the final step to force an answer, which is a cheap trick worth copying. |
| Triage turns Signals into Incidents | HolmesGPT has no equivalent in open source: N alerts mean N investigations. Robusta added grouping on the SaaS side and then had to split executor pools because bulk triage starved interactive users (PR #2482). |
| Evidence queries are redacted and size-bounded | HolmesGPT redacts nothing. Size bounding is where it is strongest (next table). |

### 11.2 What to borrow

| Idea | Where in HolmesGPT | Where it would land for us |
|---|---|---|
| Spill oversized Evidence query results to storage, and give the LLM a preview plus a way to read slices, instead of truncating. Their reason: truncated, unordered data made models give wrong answers (PR #2168). | §5.4 | Evidence query size bounding (M2.3) |
| Tell the LLM which Evidence sources are unavailable or failing, and instruct it to say so rather than guess | §5.3 | Investigation prompt and Evidence source health |
| Native query languages (PromQL, LogQL) plus cheap discovery tools (metric names, label values, series) to ground them | §5.2 | `query_metrics` and `search_logs` would benefit from `list_metrics` and `label_values` companions |
| The evidence rules in the prompt: fact versus hypothesis; "Coincidence in time is not causation"; sorting every entity into Affected / Checked, healthy / Changed for another reason / Not checked | §6.2 | RCA prompt. The "changed for another reason" bucket is exactly what the M5.4 decoy scenario (an unrelated deploy just before a memory leak) needs. |
| Errors returned as tool results; refuse exact-duplicate calls; remove tools on the last step | §6.1 | Agent loop |
| Live-cluster eval cases (`before_test` manifests, expected facts, LLM judge). Watch the cost, which pushed them to on-demand runs. | §9 | Eval harness on the existing kind cluster (M5) |
| Rollout-triggered checks with a delay and cooldown | §3.5 | Evidence for a "stuck/bad rollout" Detector design |
| Separate worker pools for background versus human-facing work | §3.4 | Relevant once Incidents accept follow-up questions |

### 11.3 What to avoid

- **Pasting raw alert payloads into the prompt.** Our Source adapter normalises an Alert into a Signal. HolmesGPT's `str(issue.raw)` loses typed severity, labels and Target. It also gives attacker-influenced annotations a direct route into the prompt.
- **Gating by tool name with ungated write tools around.** The GitHub MCP case (§7.4) shows how a write capability can arrive through an integration nobody thought of as a write path. Our Evidence sources must stay read-only by construction, through credentials and allowed-tool lists, and not by convention.
- **Docs drifting from code.** Several HolmesGPT pages describe removed features. Our ADRs and CONTEXT.md should change in the same PR as the behaviour they describe.

### 11.4 Open questions for us

1. **Follow-up conversation.** HolmesGPT's main product interaction is follow-up chat over an investigation. Ours is ticket-first. Do we want follow-up questions on an Incident, and if so through which channel? This decides whether we need the stateless-history pattern (§4.2) or server-side state.
2. **Runbook matching.** HolmesGPT lets the LLM pick a skill by its description. Our Runbook is "recorded as guidance and actions for a Signal and Target". Matching Runbooks to Signals deterministically (by labels or Target) is more predictable, but it misses near-matches. Which do we want, or both: deterministic first, with an LLM fallback that is cited in the Advice?
3. **Write-capable Evidence sources.** If we ever connect a GitHub MCP for `get_commit_diff`, how do we ensure its write tools are unreachable? An allowed-tool list per Evidence source is the CONTEXT.md answer. We should test it.

---

## Appendix: corrections and unverified points

**Corrections made while checking the readers' reports:**
- One reader concluded that batch, alert and health-check runs "can never write". That holds only for approval-gated tools. Ungated write tools run on every path (§7.4).
- `json_schema_coerce.py` fixes the types of the LLM's *tool-call arguments*. It does not structure output.
- The maintainer count is 11 Robusta and 1 Microsoft among *current* maintainers. One reader also counted the emeritus list.

**Could not verify** (closed source or out of scope):
- How Robusta's relay turns an Alertmanager webhook into a queued conversation.
- How the Slack approval form works, the levels of the account-level "confirmation mode", and who may approve.
- Whether the Robusta UI renders `analysis` into sections or adds confidence on its side.
- The remediation MCP server's code, in `robusta-dev/holmes-mcp-integrations`. Its enforcement claims are taken from the spec.
- Why `kubectl get/describe` tools were moved from `kubernetes.yaml` into bash. PR #855 has no description.
- Whether the empty built-in skills directory is deliberate.
- Whether the three different approval-token TTLs (30 days in code, 15 minutes in the PR, "short-lived" in the docs) are intentional.
