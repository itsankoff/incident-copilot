# Roadmap

Last updated: 2026-09-21

This roadmap breaks the plan in the [README](../README.md#roadmap) into tasks. Each task has a **goal**, **requirements**, and **validation** criteria that decide when it is done. A task is done only when every validation item passes, and the evidence (a command and its output, or a test name) goes in the PR description.

Milestones run in order. Tasks inside a milestone can run in parallel unless **Depends on** says otherwise. M1 is detailed because its design is settled ([`docs/design/demo-environment.md`](design/demo-environment.md)). Later milestones get a design doc of their own before work starts, and their tasks will be refined then.

Rules that apply to every task:

- `go build ./...`, `go vet ./...`, and `go test ./...` pass, and unit tests make no network calls.
- A new environment variable updates the README configuration table in the same PR.
- A new dependency is justified in the PR description.
- Changes that relax a safety invariant need an ADR (see CLAUDE.md).

---

## Progress

Scaffold landed 2026-09-21. **M1.1** is done. **M1.2** is done apart from its alert rules, which come in M1.5. **M0.1**, **M1.3**, and **M1.4** are partly done, and each has a *Remaining* line below.

## M0: Foundations

### M0.1 Go module and repo skeleton

- **Goal:** a buildable module and the agreed directory layout.
- **Requirements:**
  - `go mod init github.com/itsankoff/incident-copilot`, Go 1.25+.
  - The directories from the design doc's repository layout, each with a `doc.go` that states the package's purpose.
  - A `Makefile` with `help`, `build`, `test`, `vet`, and `lint` targets.
  - golangci-lint config with a small, explicit linter set.
- **Validation:**
  - `make build test vet lint` passes on a clean checkout.
  - `make help` lists every target with a one-line description.
- **Remaining:** golangci-lint config and a `lint` target, plus `doc.go` stubs for packages that don't exist yet.

### M0.2 CI

- **Goal:** every PR is built, vetted, linted, and unit-tested automatically.
- **Requirements:** a GitHub Actions workflow on push and pull requests that runs `make build test vet lint`, with Go module caching. The e2e tests do not run in CI yet.
- **Validation:**
  - A PR containing a deliberate `go vet` failure is marked red.
  - A clean PR is marked green in under 5 minutes.

---

## M1: Demo environment

Design: [`docs/design/demo-environment.md`](design/demo-environment.md). Exit criterion for the milestone: `make cluster build deploy smoke` passes from a clean machine, and every fault scenario fires its expected alert and then clears.

### M1.1 kind cluster

- **Goal:** one command creates and destroys a pinned, isolated local cluster.
- **Requirements:**
  - `deploy/kind/cluster.yaml`: one control-plane node and one worker node, with the node image pinned.
  - `deploy/versions.env` pins kind, kubectl, helm, the node image, and chart versions.
  - `make tools` checks that each required tool is installed at the pinned version.
  - `make cluster` and `make down` are idempotent, and every kubectl and helm call passes `--context kind-incident-copilot`.
- **Validation:**
  - Running `make cluster` twice in a row succeeds both times without errors.
  - `kubectl --context kind-incident-copilot get nodes` shows 2 Ready nodes.
  - `make down` removes the cluster, and a second `make down` still exits 0.
  - With a different current context selected, no target touches that context (checked by pointing `kubectl config current-context` at a dummy cluster).

### M1.2 Observability stack

- **Depends on:** M1.1
- **Goal:** Prometheus, Alertmanager, kube-state-metrics, Loki, and Alloy running with fast-demo settings.
- **Requirements:**
  - Helm installs from values files under `deploy/observability/`, with chart versions from `versions.env`.
  - A 15 s scrape interval and 15 s rule evaluation. Alertmanager `group_wait` of 10s and `send_resolved: true`.
  - Scrape jobs for etcd, the scheduler, and the controller manager are disabled. Grafana is off unless `GRAFANA=1`.
  - Alloy ships all pod logs from `demo` and `incident-copilot` to Loki with the labels `namespace`, `app`, `pod`, and `container`.
  - `make cluster` blocks until every component is Ready.
  - `make port-forward` exposes Prometheus, Alertmanager, and Loki on localhost for development.
- **Validation:**
  - Two minutes after `make cluster`, `ALERTS{alertstate="firing"}` is empty apart from `Watchdog`.
  - A Loki query for `{namespace="kube-system"}` returns lines.
  - `kube_pod_info` and `container_memory_working_set_bytes` return series in Prometheus.
  - Idle memory use of the whole cluster stays under 4 GB (`docker stats`).

### M1.3 demo-svc baseline

- **Goal:** the `orders`, `inventory`, and `loadgen` roles, healthy, with the full telemetry contract.
- **Requirements:**
  - `cmd/demo-svc` takes `--role=orders|inventory|loadgen`. Configuration comes from env vars and is validated at startup.
  - The endpoints, ports, metrics, and log fields listed in the design doc's telemetry contract. `version` and `commit` are injected with `-ldflags`.
  - `orders` calls `inventory` with a 1 s timeout through an interface, so tests can substitute a fake.
  - A graceful shutdown on SIGTERM that drains in-flight requests.
  - The admin API on :9091, with no handlers yet (they arrive in M1.6). Admin requests are never logged.
- **Validation:**
  - Unit tests cover each handler, config validation (including the `ORDER_TTL=15x` failure), and the metric labels.
  - Run locally (`go run` for all three roles), `curl :9090/metrics` shows each metric in the contract.
  - Every log line parses as JSON and contains `service`, `version`, and `commit`.
- **Remaining:** the admin API on :9091 (stubbed out for now).

### M1.4 Images and manifests

- **Depends on:** M1.2, M1.3
- **Goal:** `make build deploy` puts a healthy baseline workload into the cluster.
- **Requirements:**
  - A multi-stage Dockerfile that produces a distroless, non-root image. The image tag is the Git SHA.
  - A Kustomize base in `deploy/demo/` with Deployments, Services, ServiceMonitors, the resources and probes from the design, the rollout strategy `maxUnavailable: 0` and `maxSurge: 1`, the provenance annotations, and an `emptyDir` state volume on `orders` and `inventory`.
  - `make deploy` waits for `kubectl rollout status` on every Deployment.
- **Validation:**
  - After `make deploy`, the Prometheus `up` metric is 1 for the `orders`, `inventory`, and `loadgen` targets.
  - `sum(rate(http_requests_total{service="orders"}[1m]))` sits at 20 ± 2 rps.
  - The 5xx rate is 0 and p95 latency is under 100 ms for 10 minutes. No alerts fire.
  - Loki returns `orders` request logs within 30 s of them being written.
- **Remaining:** provenance annotations (`change-cause`, version, commit) on the Deployments. The first baseline check passed: 19.9 rps, no 5xx, p95 of 24 ms, all 4 targets up, only `Watchdog` firing, and 2.8 GB of memory in use.

### M1.5 Alert rules and webhook sink

- **Depends on:** M1.4
- **Goal:** symptom-based alerts reach a webhook endpoint where the agent will later run.
- **Requirements:**
  - The five alerts from the design doc in `deploy/observability/alerts.yaml`, with no cause hints in their labels or annotations.
  - `cmd/webhook-sink` accepts Alertmanager v4 webhooks and appends each one to `/data/alerts.jsonl` and to stdout. It is deployed at the future agent's Service address.
  - Rule unit tests use `promtool test rules`, with a synthetic series for each alert.
- **Validation:**
  - `promtool test rules` passes: each alert fires on its synthetic input and stays silent on the baseline.
  - Scaling `inventory` to 0 makes `HighErrorRate` for `orders` show up in the sink within 2 minutes, and scaling back produces a `resolved` payload.

### M1.6 Fault framework: `faultctl`, the admin API, and the scenario spec

- **Depends on:** M1.4
- **Goal:** one tool that injects, inspects, and clears faults from declarative scenario files.
- **Requirements:**
  - The `eval/scenarios/*.yaml` schema from the design doc, a Go type, and validation. An unknown field is an error.
  - `faultctl list | inject | status | clear`. Runtime faults go through a port-forward to :9091 on every target pod. Deploy faults go through a Kustomize patch plus a rollout.
  - The admin API persists its state to `/var/run/demo/state.json` in the `emptyDir` and reloads it at startup.
  - `clear` restores the baseline: it resets admin state, re-applies the base manifests, deletes pods holding runtime state, and waits for Ready and no firing alerts (default timeout 5 minutes). It is idempotent.
  - `--git=none` is the default for deploy faults.
- **Validation:**
  - Unit tests cover spec parsing and validation against good and bad fixtures, and the admin state round-trip.
  - `faultctl clear` on a healthy cluster exits 0 in under 10 s, and running it twice gives the same result.
  - A killed container comes back with its admin state intact. A deleted pod comes back with none.

### M1.7 to M1.10 Fault scenarios

Each fault is a separate PR containing the service behaviour, the scenario YAML with its ground truth, and an e2e test. All four share the same validation:

- **Validation (per scenario):**
  - `make fault F=<name>` makes the expected alerts show up in the webhook sink within `alert_within`.
  - Every `cause_facts` item can be checked by hand with a PromQL, LogQL, or kubectl query, and those queries are recorded in the scenario file as `reference_queries`. The eval harness later uses them as reference evidence.
  - The leakage check passes: no log line, event, pod spec, annotation, or alert contains `fault`, `inject`, or the scenario name.
  - `make clear` resolves every alert, and the baseline holds for 5 minutes afterwards.
  - Five inject/clear cycles in a row all behave the same way.

| Task | Scenario | Specific requirements |
|---|---|---|
| **M1.7** | memory-leak | An `orders` admin behaviour that retains about 1 MiB/s (configurable) in an unbounded map. The OOMKill has to repeat after each container restart. |
| **M1.8** | bad-config | A ConfigMap patch setting `INVENTORY_URL=http://inventory-v2:8080`, with `change-cause` set to a plausible message. The rollout completes, because readiness does not check the dependency. |
| **M1.9** | dependency-timeout | An `inventory` admin behaviour that adds 1.5-3 s of latency with jitter. `inventory` itself returns no errors. |
| **M1.10** | crash-loop | A ConfigMap patch setting `ORDER_TTL=15x`. The new pod crash-loops, the old pods keep serving, and the rollout is stuck. |

### M1.11 GitHub provenance for deploy faults

- **Depends on:** M1.8, M1.10
- **Goal:** deploy faults leave a real commit that `get_commit_diff` can fetch.
- **Requirements:**
  - A demo config repo (see open question 1 in the design doc) holding `orders.env`.
  - `faultctl inject --git=github` commits the change with an ordinary message and puts the SHA in `incident-copilot.dev/commit`. `clear` reverts it with a second commit.
  - `GITHUB_TOKEN` is used only by `faultctl` and never logged.
- **Validation:**
  - After injection, `gh api repos/$GITHUB_REPO/commits/<sha>` returns a one-file diff containing the bad value.
  - After `clear`, the file is back at its baseline value.
  - The leakage check still passes, including the commit message.

### M1.12 Smoke suite and the demo docs

- **Depends on:** M1.7 to M1.11
- **Goal:** one command proves the environment works, and a newcomer can run the demo.
- **Requirements:**
  - `make smoke` runs `test/e2e` (build tag `e2e`): baseline health, then each scenario's inject, alert, leakage check, and clear.
  - The README "Getting started" section matches the real targets. CLAUDE.md "Status" and "Commands" describe the actual state.
- **Validation:**
  - `make down cluster build deploy smoke` passes on a clean machine in under 30 minutes.
  - The demo has been walked through by following only the README.

---

## M2: Agent core, end to end for one fault (bad-config)

A design doc covering the agent loop, the MCP server, and the RCA schema comes before implementation.

| Task | Goal | Requirements | Validation |
|---|---|---|---|
| **M2.1** Audit log | Append-only record for every later component | A JSONL writer with fsync, a hash chain across entries, and `Write` that returns an error the caller must handle. Redaction runs before the write. | Unit tests: a tampered entry breaks the chain, a failed write returns an error, and a secret in the input never appears in the output. |
| **M2.2** LLM provider interface | Vendor-neutral tool calling | An interface in `internal/llm` and an Azure OpenAI implementation. No vendor types outside the package. Timeouts and retries. | Unit tests against a fake server. A static check (`go list -deps`) shows that no package outside `internal/llm` imports a vendor SDK. |
| **M2.3** MCP tool server | Read-only investigation tools | The six tools over the MCP Go SDK, with Prometheus, Loki, k8s, and GitHub clients behind interfaces. Output is redacted and size-capped. | Unit tests with fakes. An MCP client lists the tools and calls each one against the kind cluster (build tag `e2e`). A test asserts that the k8s client uses only get, list, and watch verbs. |
| **M2.4** RCA schema | Typed, validated output | A Go struct for the five RCA fields, a JSON schema generated from it for the LLM, and strict validation with one repair retry. | Unit tests: malformed, missing, or extra fields are rejected, and a runbook name that is not registered is rejected. |
| **M2.5** Agent loop | Investigate within budgets | Webhook receiver, tool dispatch, and budgets for steps, tokens, and wall-clock time. Running out of budget still produces an RCA with `low` confidence. Prompts live in `prompts/` with a version. Every step is audited. | Unit tests with a scripted fake LLM cover budget exhaustion and tool errors. On kind, `make fault F=bad-config` produces an RCA that names the rollout and recommends `rollback_deployment` in at least 4 of 5 runs. |

---

## M3: Ticketing

| Task | Goal | Requirements | Validation |
|---|---|---|---|
| **M3.1** Ticketing interface and GitHub Issues adapter | The RCA lands somewhere a human can read it | A `Ticketing` interface (create, update, read state) and a GitHub Issues implementation that renders the RCA as a fixed Markdown template. It is idempotent per alert fingerprint. | Unit tests with a fake HTTP server. On kind, a fault produces exactly one issue, and a re-fired alert updates that issue instead of opening a second one. |
| **M3.2** ServiceNow adapter | The primary ticketing target | Table API incident create and update, with the RCA mapped to fields. Tested against a ServiceNow Personal Developer Instance. | Contract tests shared with M3.1, which both adapters pass. A manual run against the Personal Developer Instance is documented with a screenshot in the PR. |

---

## M4: Remediation and safety

| Task | Goal | Requirements | Validation |
|---|---|---|---|
| **M4.1** ADR 0001: autonomy and trust model | Record the safety design before building it | `docs/adr/0001-autonomy-and-trust-model.md` (the README already links to it). | Reviewed and merged before M4.2 starts. |
| **M4.2** Runbook registry and executor | The only code that changes state | `rollback_deployment`, `restart_pod`, and `scale_up`, each with a typed parameter schema, a maximum level, and a per-environment ceiling. The executor checks the level in code. The audit write happens before execution, and a failed audit write blocks the action. | Unit tests: the LLM asking for a higher level is refused, an unknown runbook is refused, and a failing audit writer blocks execution. The k8s write permissions exist only in the executor's ServiceAccount (RBAC test). |
| **M4.3** Approval gate | Humans approve L2 actions | An `Approver` interface with ticket-state and Slack-button implementations. Approvals time out, and every approval is audited with who approved it. | Unit tests for approve, deny, and timeout. On kind, an L2 rollback runs only after approval. |
| **M4.4** End-to-end remediation | The loop closes | Each fault, at its configured level, runs through to execution or escalation. | memory-leak restarts at L3 in staging mode, bad-config waits for approval at L2, and dependency-timeout proposes no action. After each run, the audit log can reconstruct the whole incident. |

---

## M5: Evaluation harness

| Task | Goal | Requirements | Validation |
|---|---|---|---|
| **M5.1** Harness runner | Unattended scoring across all scenarios | `make eval [N=runs] [S=scenario]` runs inject, wait, agent, score, and clear for each scenario. The report covers diagnosis accuracy, runbook accuracy, time to diagnosis, and tokens per incident, per scenario and overall, as JSON and Markdown. | A run of 4 scenarios × 3 repetitions finishes unattended, and the report matches the audit logs. |
| **M5.2** Diagnosis scoring | A trustworthy accuracy number | Scoring against `cause` and `cause_facts`: a deterministic check first, with an optional LLM judge whose prompt is versioned. Every result is stored with the prompt and model versions. | 20 hand-labelled RCAs agree with the scorer in at least 90% of cases. |
| **M5.3** Baseline and regression gate | Changes are measured | Commit the baseline report. A script shows the difference between two reports. | A deliberately degraded prompt shows up as a regression in the difference. |
| **M5.4** (stretch) Decoy scenario | Test correlation against causation | A harmless deploy of `inventory` 1 minute before a memory-leak. | A scenario file with ground truth that says rollback is wrong. The accuracy on it is reported. |

---

## M6 (stretch): AKS

| Task | Goal | Requirements | Validation |
|---|---|---|---|
| **M6.1** IaC module | Deploy the demo to AKS | A Bicep or Terraform module for AKS, with Azure Managed Prometheus or the same Helm stack, and the agent deployed with workload identity. | `make aks-up aks-smoke aks-down` passes. No secrets are in the state or the logs. |
