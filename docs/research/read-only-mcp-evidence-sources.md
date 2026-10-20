# Read-only MCP servers as RCA evidence sources

- **Ticket:** [#22](https://github.com/itsankoff/incident-copilot/issues/22), part of map [#3](https://github.com/itsankoff/incident-copilot/issues/3)
- **Builds on:** [#6, observability MCP ecosystem](https://github.com/itsankoff/incident-copilot/blob/research/observability-mcp-ecosystem/docs/research/observability-mcp-ecosystem.md). That file covers Kubernetes, Prometheus, Loki, Grafana, Datadog, and the cloud backends. This file does not repeat it. It adds Sentry, code and PR hosts (GitHub, GitLab), CI/CD (GitHub Actions, Argo CD), placement inside a Location, and what the host can intercept.
- **Date:** 2026-10-20. Repos were read at HEAD on that date.
- **Vocabulary:** Control plane, Location, Sensor, and Evidence query as in [`CONTEXT.md`](../../CONTEXT.md).
- **Evidence markers:** **[code]** means checked in the source code. **[docs]** means stated in the vendor's own docs or README. **[inferred]** means my reading, not stated anywhere.

## Answer

1. **Read-only by credentials.** Every new source can be made read-only with credentials alone:
   - Sentry: a token with only read scopes.
   - GitHub, including Actions: a fine-grained PAT or a GitHub App with read permissions.
   - Argo CD: an account bound to the built-in `role:readonly`.
   - GitLab: only partly. Its built-in MCP server has no read-only OAuth scope. The `mcp` scope plus the user's own role is the boundary, so the user must be a Reporter-level bot account.

   As in #6, flags such as `--read-only` and `MCP_READ_ONLY` only hide tools.
2. **Query language and size.**
   - Only the observability servers from #6 take PromQL or LogQL.
   - GitHub takes GitHub search syntax and caps job logs on the server side.
   - Sentry's issue search takes raw Sentry syntax. Its event search sends the query through a **second LLM inside the Sentry MCP server**.
   - Argo CD's log tools are fixed at 100 lines.
   - In every case, limits that the model sets need our own byte cap on top.
3. **Placement.** MCP has no reverse-connect transport. The client always dials the server. So an MCP server inside a Location that only dials out cannot be reached by the Control plane directly. The Sensor would have to carry MCP frames over its own outbound channel. The go-sdk `Transport` interface makes that possible, but nothing ships it today. Sentry, Datadog, the hosted GitHub server, and GitLab are SaaS or central services, so they belong on the Control plane side anyway.
4. **Host interception.** When the Control plane is the MCP client (go-sdk, or the Anthropic Go SDK's MCP helpers wrapped in our own tool), every tool result passes through our code before the LLM sees it. It can be redacted and capped. The assumption fails in three places:
   - **The Claude API MCP connector** (`mcp_servers`). Anthropic's API calls the server and puts `mcp_tool_result` into the model's context on Anthropic's side. Our code never sees the result first.
   - **Server-side LLMs**, such as Sentry's embedded search agent. Data reaches a different LLM provider without passing through our host at all.
   - **Text from the server that is not a tool result**: tool descriptions, server `instructions`, and error messages. These enter the context unless the host filters them too.

**Recommendation.** Use the go-sdk client path only. Never use the Claude API MCP connector for evidence sources. Treat every MCP server's text (descriptions, instructions, errors, results) as untrusted input to the redactor. For GitHub and Argo CD, the vendor servers are good candidates behind the #6 seam (MCP client + allowlist + redaction + byte budget), with read-only credentials. Use Sentry only with an `inspect`-only read token, and either without its AI search tools or with an explicit decision that queries may go to a second LLM provider. In v1, keep in-Location evidence on the Sensor's own typed queries. Revisit Sensor-carried MCP only if a Location-only source needs a vendor server.

## Per-server findings

| Source | Server | Read-only by credential? | Query language / size control | Placement |
|---|---|---|---|---|
| Sentry | [getsentry/sentry-mcp](https://github.com/getsentry/sentry-mcp) (now redirects to `getsentry/toolkit`), TypeScript. Hosted at `mcp.sentry.dev`, or stdio via `npx @sentry/mcp-server` | **Yes, if you choose the scopes.** Each tool declares `requiredScopes`. The `inspect` skill needs only `org:read`, `project:read`, `team:read`, `event:read` [code: `toolDefinitions.json`]. The README's example token scopes include `project:write`, `team:write`, `event:write` [docs], so do not copy them. Hosted OAuth derives scopes from the skills you grant [code: `skills.ts`] | `search_issues` takes Sentry issue syntax. `search_errors`, `search_logs`, `search_traces`, and the others take "natural language (preferred) or Sentry search syntax; **a configured agent translates it**". They are unavailable without an LLM provider key (OpenAI, Azure, Anthropic, OpenRouter) [docs + code: `search-events/agent.ts`]. `limit` is 1–100, enforced by the schema [code] | Sentry is SaaS or a central self-hosted install: Control plane side. `regionUrl` is a tool argument, but it is validated against an HTTPS allowlist with no embedded credentials [code: `validate-region-url.ts`], so it is not a credential exfiltration path like `loki-mcp` |
| GitHub code, PRs | [github/github-mcp-server](https://github.com/github/github-mcp-server), Go, `go-sdk` v1.8.0 [code: `go.mod`]. Local binary, or hosted at `api.githubcopilot.com/mcp` | **Yes.** Use a fine-grained PAT or a GitHub App with `contents`, `pull_requests`, `actions`, `metadata` = read [inferred from GitHub permission model]. The server does not filter tools for fine-grained PATs or App tokens. "API enforces permissions" [docs: `scope-filtering.md`]. `--read-only` (or a `/readonly` URL on the hosted server) skips write tools [docs]. Lockdown mode is "**not** an authorization boundary" [docs] | Search tools take GitHub search syntax (`search_code`, PR search) [docs]. No PromQL equivalent | Runs anywhere with network access to GitHub. GitHub is external to every Location, so Control plane side |
| GitHub Actions | Same server, `actions` toolset | Same token, `actions: read` | `get_job_logs`: `tail_lines` is set by the LLM, but bounded by `--content-window-size` (default 5000 lines) through a ring buffer [code: `actions.go`, `main.go`]. **By default (`return_content` false) it returns the job's `logs_url`**, a pre-signed download link, into the model context [code]. GitHub documents that link as short-lived [docs, GitHub REST, not re-verified]. The redactor must strip it, or the allowlist must force `return_content=true` | Control plane side |
| GitLab code, MRs, CI | GitLab's built-in MCP server at `/api/v4/mcp`. GA in GitLab 19.5, on all tiers, on SaaS and self-managed [docs] | **Partly.** Auth is OAuth with the `mcp` scope, or a fine-grained PAT with the MCP tool resource and Execute permission. Legacy tokens are rejected [docs]. **No read-only scope is documented.** Permissions are checked per call against the user's role [docs], so a read-only boundary needs a dedicated user with a Reporter-level role [inferred]. Toolset narrowing uses the `X-Gitlab-Enabled-Mcp-Server-Toolsets` header [docs]. That is not a credential boundary | Size limits not documented. GitLab.com rate limits are 60 or 600 requests per minute [docs] | Inside the GitLab instance. Control plane side |
| Argo CD | [argoproj-labs/mcp-for-argocd](https://github.com/argoproj-labs/mcp-for-argocd), TypeScript, `@modelcontextprotocol/sdk` ^1.29 [code]. stdio or HTTP | **Yes.** Argo CD's built-in `role:readonly` grants only `get`, on applications, applicationsets, clusters, repositories, projects, accounts, certificates, gpgkeys, and **logs** [code: `assets/builtin-policy.csv`]. `MCP_READ_ONLY=true` unregisters `create_`, `update_`, `delete_`, `sync_application` and `run_resource_action` [code: `server.ts`] | No query language: tools look things up by name. `list_applications` takes `limit`/`offset`. Log tools are fixed at `tailLines: 100` [code: `client.ts`]. Argo CD masks the `data` of `Secret` objects in manifest and resource API responses (`diff.HideSecretData`) [code: `argo-cd/server/application/application.go`], but not secrets in other objects, such as Deployment env values | Can run in-cluster next to Argo CD over HTTP. The listener binds to loopback by default, with optional `MCP_AUTH_TOKEN` [docs]. Every tool accepts an `argocdBaseUrl` argument, but the default token is only ever sent to the default host [docs + code: `tokenRegistry.ts`]. Allow it, but strip `argocdBaseUrl` in our allowlist |
| Datadog (addendum to #6) | Hosted Datadog MCP Server | As in #6 | `analyze_datadog_logs` takes **SQL**. Other examples in the docs are in natural language, and some tools "use AI models to build a query from a natural language description" [docs]. So like Sentry, a vendor-side LLM may see query text [inferred] | SaaS only: Control plane side |

Kubernetes, Prometheus, Loki, and Grafana: see #6. For question 3: `kubernetes-mcp-server`, `prometheus-mcp`, and `mcp-grafana` all run in-cluster over streamable HTTP or as stdio subprocesses [#6]. That still leaves the dial-in problem below.

## Placement: can an MCP server run in a Location and be reached by a host that dials out?

- MCP has two standard transports, stdio and streamable HTTP. In both, the client starts the connection. In streamable HTTP, even server-to-client messages travel on an SSE stream that the client opened with `GET` [code: go-sdk `StreamableClientTransport`, with `DisableStandaloneSSE` documented there]. The spec defines no reverse-connect mode [inferred from the transport list].
- So if the Control plane is the MCP client and a Location allows only outbound connections, the Control plane cannot reach an MCP server inside that Location.
- Possible bridge: the Sensor runs the vendor server as a stdio subprocess and relays JSON-RPC frames over its existing outbound channel. On the Control plane side, a custom go-sdk `Transport` (`Connect(ctx) (Connection, error)`, where `Connection` reads and writes `jsonrpc.Message`) turns that channel back into an MCP session [code: go-sdk `mcp/transport.go`]. This is feasible but unbuilt.
- The bridge has costs:
  - The Sensor stops being a small, typed component and starts hosting third-party processes.
  - The Evidence query stops being "typed".
  - We would ship vendor runtimes (Node, Python) into every Location.
- Sources that sit outside any Location (Sentry, Datadog, GitHub, GitLab, and often Argo CD in a management cluster) do not have this problem. Their servers run next to the Control plane.

## Host side: can every result be intercepted, redacted, and capped?

### go-sdk as the MCP client: yes, with three gaps to close [code]

- `ClientSession.CallTool` returns a `*CallToolResult` to our code. Nothing reaches the LLM unless we pass it on. Redact `Content` (text, embedded resources, resource links) **and** `StructuredContent`. The Anthropic helper sends `StructuredContent` as JSON when `Content` is empty [code: `anthropic-sdk-go/mcp/mcp.go`].
- **Gap 1, size before decode.** `CommandTransport.MaxLineLength` caps one stdio frame (default 16 MiB) [code: `mcp/cmd.go`, `transport.go`]. `StreamableClientTransport` has no such field. A cap there needs our own `HTTPClient` whose `RoundTripper` wraps response bodies in a limit reader [inferred]. Our byte budget for the LLM is a separate, smaller cut, applied after decode.
- **Gap 2, server text that is not a result.** Tool `Description`, `InitializeResult.Instructions`, and error text all come from the server. The Anthropic helper's `Description()` passes the server's description straight through. On `IsError` it turns the server's text into the error message [code: `anthropic-sdk-go/mcp/mcp.go`]. The host must use its own tool descriptions, or vetted pinned ones, and must redact error text.
- **Gap 3, server-initiated requests.** Sampling (`sampling/createMessage`) would let a server send prompts to our LLM. go-sdk only advertises sampling and elicitation when we set `CreateMessageHandler` or `ElicitationHandler` [code: `mcp/client.go`]. Leave both unset. `LoggingMessageHandler` and `ProgressNotificationHandler` messages must not go to the LLM or the audit log unredacted.
- go-sdk also has `AddReceivingMiddleware`, a single hook on every incoming message [code]. It could be the redaction point, but a plain wrapper around `CallTool` is simpler.

### Anthropic SDK tool runner with MCP helpers: yes, if we wrap the tool [code]

- `mcp.NewBetaTools(tools, session)` returns `BetaTool` values whose `Execute` calls `CallTool` and converts the result directly. It has no hook. `BetaTool` is a four-method interface (`Name`, `Description`, `InputSchema`, `Execute`), so a decorator around `Execute` can redact and cap, and can replace `Description`.
- Using the helpers unwrapped sends raw results to the model.

### Claude API MCP connector (`mcp_servers`): no [docs]

- The connector is beta (`mcp-client-2025-11-20`; `mcp-client-2026-09-15` adds tool-list pinning). It is "not eligible" for zero data retention.
- It requires the server to be "publicly exposed through HTTP". "Local STDIO servers cannot be connected directly." Only tool calls are supported.
- The API calls the server itself. The result comes back as an `mcp_tool_result` block in the response, **after the model has already used it**. The host can only allowlist tools (`mcp_toolset` with allow or deny lists) and see the results afterwards.
- This breaks ADR 0001 invariant 7 (redact before the LLM). It also requires an evidence source to be reachable from the internet.

### Other bypass paths

- **LLMs embedded in the server.** Sentry's AI search tools send the query text, environment names, and attribute metadata from their own tool calls to the provider configured in the server's environment [code: `search-events/agent.ts`, docs: `embedded-agents.md`]. Datadog's docs say some tools use AI models to build queries [docs]. The host cannot redact these. It can only choose not to call those tools, or accept the second provider.
- **Hosted servers** (`mcp.sentry.dev`, `api.githubcopilot.com/mcp`, Datadog) see every query and result in plaintext on the vendor's side. This does not bypass our redactor before our LLM, but it is a data flow to name in the ADR [inferred].
- **Links in results** (GitHub `logs_url`, artifact download URLs) are credentials inside a result. The redactor must treat them as secrets.

## Sources

- Repos read at HEAD on 2026-10-20: [getsentry/sentry-mcp](https://github.com/getsentry/sentry-mcp) (`packages/mcp-core/src/toolDefinitions.json`, `skills.ts`, `schema.ts`, `internal/tool-helpers/validate-region-url.ts`, `tools/support/search-events/agent.ts`, `docs/operations/embedded-agents.md`, README), [github/github-mcp-server](https://github.com/github/github-mcp-server) (`pkg/github/actions.go`, `cmd/github-mcp-server/main.go`, `docs/scope-filtering.md`, `docs/remote-server.md`, README), [argoproj-labs/mcp-for-argocd](https://github.com/argoproj-labs/mcp-for-argocd) (`src/server/server.ts`, `src/argocd/client.ts`, `src/server/tokenRegistry.ts`, README), [argoproj/argo-cd](https://github.com/argoproj/argo-cd) (`assets/builtin-policy.csv`, `server/application/application.go`), [modelcontextprotocol/go-sdk](https://github.com/modelcontextprotocol/go-sdk) (`mcp/client.go`, `mcp/transport.go`, `mcp/cmd.go`, `mcp/streamable.go`), [anthropics/anthropic-sdk-go](https://github.com/anthropics/anthropic-sdk-go) (`mcp/mcp.go`, `betatoolrunner.go`)
- Docs: [Claude MCP connector](https://platform.claude.com/docs/en/agents-and-tools/mcp-connector), [GitLab MCP server](https://docs.gitlab.com/user/model_context_protocol/mcp_server/), [Argo CD RBAC](https://argo-cd.readthedocs.io/en/stable/operator-manual/rbac/), [Datadog MCP Server](https://docs.datadoghq.com/bits_ai/mcp_server/), [Datadog MCP tools](https://docs.datadoghq.com/mcp_server/tools.md)
- Prior research: [observability-mcp-ecosystem.md](https://github.com/itsankoff/incident-copilot/blob/research/observability-mcp-ecosystem/docs/research/observability-mcp-ecosystem.md)
