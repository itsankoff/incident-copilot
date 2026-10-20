# ADR 0002: Product scope and platform seams

- **Status:** Accepted
- **Date:** 2026-10-20
- **Deciders:** @itsankoff
- **Related:** [ADR 0003: Trust model and safety invariants](0003-trust-model-and-safety-invariants.md), [ADR 0001](0001-autonomy-and-trust-model.md) (superseded by ADR 0003), [`CONTEXT.md`](../../CONTEXT.md), [Wayfinder: production-grade incident copilot](https://github.com/itsankoff/incident-copilot/issues/3)

## Context

[ADR 0001](0001-autonomy-and-trust-model.md) was written for the demo: one kind cluster, one agent and one executor deployed next to it, Alertmanager as the only trigger, and three runbooks written in incident-copilot's own code. That is enough to prove the loop, but not to run it in production. This ADR decides what the product is and where its seams go. [ADR 0003](0003-trust-model-and-safety-invariants.md) decides how it is kept safe.

The forces:

1. **Production estates have many clusters.** A team runs production and staging, often several clusters in each, on EKS, GKE, AKS, or their own hardware. One deployment per cluster does not scale, and one deployment holding every cluster's write credentials is a large blast radius.
2. **Kubernetes first, but not Kubernetes only.** Managed Kubernetes comes next. ECS, Lambda, Cloud Run, RDS, and VMs are a long-term door. They must not be built now, but the core must not have to be rewritten to let them in.
3. **Operators already have monitoring and runbooks.** Alerts come from Alertmanager, Grafana, Datadog, or a cloud provider. Runbooks live in Confluence or Git. incident-copilot has to take both in rather than replace them.
4. **Some clusters have no alert rules.** A crash loop or a failed image pull should still start an investigation.
5. **The LLM is both investigator and fixer.** Its value is diagnosing and proposing fixes for Incidents nobody wrote a runbook for. That only works if its evidence is reviewable and its fixes are bounded (ADR 0003).
6. **Adoption.** A one-script install with sensible defaults, quick setup of each connector, and a synthetic Incident should fall out of the architecture. This is a standing design pressure, not a decision of its own.

**Prior art.** No AI SRE product reviewed (HolmesGPT, k8sgpt, Azure SRE Agent, AWS DevOps Agent, Komodor) models "a platform" as a type. Each has a target model (Kubernetes kinds, Azure resource types, an AWS topology graph) plus bundles of tools, and new platforms almost always arrive over MCP. Triggers converge on what we call a Signal: ingested alerts plus deterministic detectors on the Kubernetes-native tools. AWS adds a triage stage before investigating (link to an open investigation, skip, or proceed), which we adopt as Triage. HolmesGPT runs its loop and its tools in one process, with tools that take raw queries plus an allowlisted `bash` tool, and relies on RBAC alone, with no redaction, scoping, or citations. We borrow its log-tool shape, two-layer output budgets, per-capability health checks, and single-call remote executor, which is the closest thing to our Sensor. "Runbook" in production practice means either prose per alert or an executable procedure; incident platforms now keep the prose as skills an LLM reads and keep execution separate, with no LLM inside. Full findings: the resolutions of [Prior art: how existing AI SRE agents model platforms, signals, and actions](https://github.com/itsankoff/incident-copilot/issues/4), [Prior art: how HolmesGPT handles evidence tools](https://github.com/itsankoff/incident-copilot/issues/19), [Prior art: what a Runbook is in production SRE practice](https://github.com/itsankoff/incident-copilot/issues/29), and [Prior art: how incident platforms define, import, and bind Runbooks](https://github.com/itsankoff/incident-copilot/issues/30).

## Decision

### D1. Scope

incident-copilot is a **self-hosted, production-grade incident copilot**, run by the operator's own team. It is single-tenant: one organisation runs one or more Control planes for itself.

- **Platforms.** Kubernetes first (self-hosted and kind), managed Kubernetes (EKS, GKE, AKS) next. Platforms that are not Kubernetes shape the seams (D6) but are not implemented in this version.
- **Triggers.** Alerts from the operator's existing monitoring, plus a small set of built-in Kubernetes Detectors (D5). Both become one source-neutral Signal. There is no anomaly detection.
- **Output.** One pass per Incident: an RCA, then Advice that a human reviews, may edit, and approves into a Plan, or carries out by hand. A Signal that fires again re-investigates. There is no conversation with the on-call engineer in this version.
- **Autonomy.** L1 and L2 only: every Plan needs human approval (ADR 0003).

Out of scope for this version: a SaaS or multi-tenant Control plane; anomaly detection on metrics and logs; conversational follow-up; implementing any platform that is not Kubernetes; PagerDuty and similar aggregators as a Signal source (they are a paging output only); serving our evidence tools outward over MCP to other agents, because that would bypass the audit log, scope checks, and budgets; and the provider-side MCP connector (D7).

### D2. Topology: a Control plane, and a Sensor and an Executor per Location

"Agent" and "Install" are retired. There are three roles, all in one Go binary:

| | Control plane | Sensor | Executor |
|---|---|---|---|
| Where | Central, one per operator (or one per Environment, as a deployment choice) | One in each watched Location | One in or next to each Location |
| Does | Takes in Signals, runs Triage, runs both LLM loops, checks every LLM output, collects approvals, writes tickets, drives Plans | Runs Detectors, answers Evidence queries about the Location, relays MCP calls (later) | Runs the Actions the Control plane sends, verifies them, reports back |
| Calls an LLM | Yes, the only role that does | Never | Never |
| Changes state | Never | Never | Yes, the only role that does |
| Credentials | LLM provider, ticketing, central evidence sources (read-only) | Read-only access to the Location | Write access to the Location, granted there |

- **Location.** Where a Target lives: for Kubernetes, the **cluster**, named uniquely by the operator (for example `aks-prod-weu`). Provider, account, region, and subscription are attributes of the Location, not part of its identity, so two clusters in one subscription and region stay apart.
- **Environment.** A stage (production, staging) that sets the autonomy ceiling. Each Location belongs to exactly one Environment, set in Control plane configuration and never taken from a Signal: an `env=staging` label on a production Alert must not raise anything.
- **Connections.** Sensors and Executors dial out to the Control plane over a persistent link. Nothing connects into a Location. The protocol, mutual authentication, and enrolling a new Location are not yet specified.
- **Identity.** Sensors and Executors use in-cluster ServiceAccounts and RBAC for their own cluster's API, on any Kubernetes. An Executor acts only with credentials granted in its Location (ServiceAccount; EKS Pod Identity or IRSA, GKE Workload Identity, AKS Workload Identity for cloud APIs), whatever the Control plane sends. The Control plane reads central sources with the identity of wherever it runs: workload identity preferred, static read-only keys allowed as the simple default.
- **State.** The Control plane runs as one replica with SQLite on a persistent volume: Incidents (open, resolved, recently closed), ticket references, counters, the queued follow-up investigation, pending approvals, action history, and its own audit chain. The Executor keeps only its write-ahead audit chain and whatever it needs to finish or report an in-flight Action. Sensors keep no state. Signal sources re-send every 5 minutes and Sensors re-send what is firing, so nothing else is buffered. An investigation interrupted by a restart runs again and does not count as a re-investigation. More than one Control plane replica (Postgres, leader election) comes later.
- **Quick start.** All three roles run in one cluster.

### D3. Signals and Signal sources

A **Signal** is one source-neutral struct, not an interface. What varies is the **Source adapter**, one per kind of source: it authenticates the sender, splits a batch into alerts, and maps each alert to a Signal. A **Signal source** is one configured sender (one Alertmanager, one Sensor) with its own credential, default Location, label mapping, and liveness mode. A Sensor is a Signal source that sends ready-made Signals over its link.

| Field | Notes |
|---|---|
| `source` | `{kind, instance}`, for example `alertmanager` / `prod-am` |
| `key` | `<source instance>/<series id>` (below) |
| `name` | the rule or alert name (`alertname` for Alertmanager) |
| `status` | `firing` or `resolved`; the source's raw state is kept in `sourceState` |
| `severity` | `critical`, `warning`, `info`, or `unknown`, plus the raw value |
| `startsAt`, `endsAt?` | from the source |
| `receivedAt` | the Control plane's clock; staleness and last-seen always use it |
| `labels` | identity-bearing key/values; only these shape the key and the Target |
| `annotations` | descriptive text; never reaches the LLM |
| `link?` | back to the source |
| `target?` | set only by Detectors |
| `raw` | the original payload |

- **Key.** The series id is the source's own stable id where one exists (Alertmanager `fingerprint`, CloudWatch alarm ARN plus contributor, Datadog monitor id plus scope), otherwise a hash of rule id plus sorted labels (Azure, GCP). Detectors use `detector:<name>/<target>`. An episode is `key` plus `startsAt`. There is deliberately no cross-source identity: the same failure reported by two sources is two Signals, and Triage joins them by Target. Because the key uses the source instance, a duplicate send from an Alertmanager HA pair is only a repeat notice.
- **States.** `Warn` and `Re-Warn` mean firing. `INSUFFICIENT_DATA` and `No Data` change nothing and never count as resolved. Acknowledged-type states are ignored. A `resolved` for an unknown key is logged and dropped.
- **Liveness.** Every Signal source must either **repeat** (re-send each firing alert at least every 5 minutes) or be **polled** (the adapter reads current state from the source's API every 5 minutes with read-only credentials, for transition-only sources such as CloudWatch and Azure Monitor). A source that can do neither is not accepted. A Signal with no firing notice for 10 minutes (2 × 5) **expires**. Only *resolved* counts as verification; expiry never does.
- **Authentication** is mandatory for every Signal source, with the strongest method the source offers. This is a safety invariant (ADR 0003).
- **Receiving.** An auth failure is `401` and nothing is processed. An unparseable body is `400`. An internal failure before Signals are accepted is `5xx`, so the source retries, which is safe because a retry is only a repeat notice. An alert in a batch that fails to normalise is dropped and audited, and the rest are accepted. Alertmanager's `endsAt` on a firing alert is ignored. `truncatedAlerts > 0` is audited.
- **Ingest bounds** per Signal source: a request body of at most 1 MiB, each label or annotation value at most 4 KiB (the Signal is marked truncated if one is cut), and `raw` at most 64 KiB.
- **Shipped Alertmanager route.** Alertmanager's defaults would push paired alerts past the settle window and leave a dead route's Signals open for hours, so we ship a route: `group_by: ['...']`, `group_wait: 10s`, `group_interval: 1m`, `repeat_interval: 5m`, and a webhook receiver with `max_alerts: 0` and `send_resolved: true`.
- **Source order.** Alertmanager in v1. Then Grafana, Datadog (with a required payload template), CloudWatch and Azure Monitor (polled, with managed Kubernetes and the non-Kubernetes platforms), then GCP Monitoring.

### D4. Triage, Incidents, and Targets

- **Target.** The workload an Incident is about: `{platform, location, namespace?, kind, name}`, by name and never by UID. `kind` is opaque to the core.
- **Resolving a Target.** The Control plane resolves an Alert's Target from its labels only, through the Signal source's label mapping. The Location is the source's configured Location, or a mapped label (such as `cluster`) that must match a configured Location name. The Alertmanager defaults (kube-prometheus) are `namespace` plus the first of `deployment`, `statefulset`, `daemonset`, `cronjob`, `job_name`. There is no owner walking and no guessing: an Alert that names only a pod gets **no Target**, because scope binding must not rest on a guess such as cutting the hash off a pod name. A Detector Signal arrives with its Target resolved by the Sensor.
- **Triage** is deterministic, table-tested code, never an LLM.
  - At most one open Incident per Target. Any Signal that resolves to that Target joins it, whatever its source or name.
  - A Signal with no Target opens or joins an Incident keyed by its `key`. It is investigated and ticketed normally, and every Plan in it is capped at L1 (ADR 0003). It is never dropped.
  - Nothing is grouped across Targets: when `inventory` causes `orders` to alert, that is two Incidents. The RCA may name the cause.
- **Settle window.** The first investigation starts a settle window after an Incident opens: 30 seconds by default, configurable per Control plane, 0 for immediately. Every Signal that joins in that window feeds the first investigation without triggering another.
- **Incident states.**
  - **Open** while any member Signal is firing.
  - **Resolved** when every member Signal has ended. The outcome is *resolved* if all were resolved, *expired* otherwise. The ticket stays open.
  - **Closed** when a human closes the ticket, or after 24 hours with no activity (default), when incident-copilot closes it with a comment saying so.
  - A Signal for the Target (or `key`) of a resolved Incident reopens it, and its ticket, history, and denials carry over. A Signal after close opens a new Incident linked "recurrence of" to the old one.
- **What triggers an investigation.** An Incident opens: investigate. It reopens: re-investigate and append to the same ticket. A new distinct Signal joins after the settle window (including a member Signal that resolved and fires again with a new `startsAt`): re-investigate once, queueing at most one follow-up behind a running investigation, never cancelling it. A repeat notice updates only last-seen. At most 3 re-investigations per Incident; after that the ticket gets only "new Signal joined" notes.
- **Tickets.** One ticket per Incident, and it is the source of truth for human-facing state. The Control plane stores only its reference and re-reads its state at every decision point. If a human closes or cancels the ticket while work is running, all work stops and an audit record says why.
- **Severity** is the highest among open member Signals. It appears in the ticket only and is never an input to any level or cap.

### D5. Built-in Kubernetes Detectors

Five Detectors run in every Sensor, read only the Kubernetes API (watches on Pods, Deployments, and Events, never Prometheus or kube-state-metrics, because they exist for clusters with no alert rules), and emit one Signal per (Detector, Target), never per pod. *Live pods* are the Target's pods that currently exist.

| Detector | Fires | Resolves | Severity |
|---|---|---|---|
| CrashLoopBackOff | a live container has been waiting `CrashLoopBackOff` for ≥ 1 min | no live container in `CrashLoopBackOff` or restarted for 10 min | critical |
| OOMKilled | at once, when a live container's last termination is `OOMKilled` and its restart count went up | no new OOM kill on live pods for 15 min | critical |
| Stuck rollout | a Deployment has `Progressing=False`, reason `ProgressDeadlineExceeded` | `Progressing=True`, reason `NewReplicaSetAvailable` | warning |
| ImagePullBackOff | a live container has been waiting `ImagePullBackOff` or `ErrImagePull` for ≥ 2 min | none in either state for 1 min | warning |
| FailedScheduling | a live pod has had `PodScheduled=False`, reason `Unschedulable`, for ≥ 5 min | no live pod unschedulable for 1 min | warning |

- **Target.** The Sensor follows controller owners to the top one, whatever its kind (Deployment, StatefulSet, DaemonSet, CronJob, a bare Job or ReplicaSet, an Argo `Rollout`, an operator CRD, or a Pod with no owner).
- **Liveness.** The Sensor re-sends each firing Signal every 5 minutes, so a dead Sensor's Signals expire after 10 minutes instead of hanging open.
- **Restarts.** On start the Sensor lists current state and rebuilds which Signals are firing. A restart can delay a resolution but never fake one. OOMKilled counts only terminations within the last 15 minutes.
- **Alongside Alerts.** Detectors always run, even when an equivalent alert rule exists. Triage merges them by Target. Known gap: a pod-only Alert gets no Target, so it and the matching Detector Signal become two Incidents.
- **Defaults.** Every Detector is on across the Sensor's namespace allowlist and can be turned off. No thresholds are tunable in v1.
- **Accepted limitation.** OOMKilled "resolved" means only "no OOM for 15 minutes". A slow leak will OOM again and reopen the Incident (or open a recurrence).

### D6. The platform seam

**Anything that decides is core. Anything that talks to a platform API is per-platform.**

- **Core (platform-neutral):** Signal, Triage, Incident, tickets, both LLM loops, Runbook matching, Gate 1 guardrails, `Decide`, Environment ceilings, approvals, the audit log, the guards, the scope-binding check, and verification by Signal resolution.
- **Per platform:** Target kinds and owner resolution, Detectors, evidence tools, and each Action's platform half (read fresh state, check preconditions, compute the preview, apply, and the mechanical check).

Platform code sits behind the interfaces the core actually calls through: the Sensor's Detectors and evidence tools, and the Executor's Actions. Kubernetes is the only implementation in this version. The core defines platform-neutral data (a Target with an opaque `kind`, a snapshot, a Plan) and **never imports a platform package**. Platform packages are wired in at `main`. There is one binary, every platform is compiled in, and configuration picks the platform per Location. There is no runtime plugin loading.

- **Target version.** An opaque string the platform supplies, which changes whenever the Target's desired state changes and not on status changes. Kubernetes uses `metadata.generation`. Approval digests pin it (ADR 0003).
- **Preview.** A deterministic before→after of the fields an Action will change, computed by the Executor from fresh state: a server-side dry run where the platform has one (Kubernetes `dryRun=All`), the Action's own computation otherwise.
- **Example, not built:** a Lambda function is `{platform: lambda, location: aws:<account>:<region>, kind: function, name: <function name>}`, with no namespace. Tags are not identity, because they are mutable.

### D7. Investigation: the RCA loop and its Evidence sources

The **RCA loop** only diagnoses. A separate remediation loop turns its RCA into Advice (D8). Both run only in the Control plane.

- **Evidence queries.** The LLM investigates with read-only tools. A tool may take its source's own query language (PromQL, LogQL, a Datadog query) and may explore the Location read-only, for example from a pod to its Deployment. There is no `bash`, `exec`, or shell template. Sources: data in the Location, external observability, code and PRs, configuration, and CI/CD.
- **Only identifiers from the Alert reach the LLM:** Target, Location, `name`, `startsAt`, severity, and source references. Annotations and other free text never do.
- **Read scope.** Any Target in the Incident's Location within the Sensor's namespace allowlist, which the Sensor enforces, plus a fixed set of kinds without a namespace (nodes, cluster events). The Incident's Target is the default. No reads across Locations.
- **Evidence sources.** One configuration entry each: name, scope (central or a Location), route (`mcp`, `relay`, or `sensor`), credential reference, read-only flags, and a tool allowlist. Each backend is reached through exactly one Evidence source, so the LLM never sees two tools for the same backend; with nothing configured, the default is the Sensor's typed tool. At startup and per Incident the Control plane lists tools and runs health checks, and offers only healthy, allowlisted tools. The model is told which are unavailable, so the RCA reports the gap. A call to a tool that was not offered is rejected.
- **MCP servers** are preferred where a good one exists. Every one is reached through our own go-sdk client, whatever the model or provider: the model asks for a call, our loop makes it, and only what we pass back reaches the model. Results are plain MCP content, wrapped in an evidence-ref envelope. The provider-side MCP connector, where the provider calls the server and the model sees results before we do, is out of scope for this version.
- **v1 routes.**

  | Source | Route |
  |---|---|
  | Kubernetes API, Prometheus, Loki in the Location | Sensor typed tools: `metrics`, `logs`, `events`, `workload_state`, `changes`, `list_workloads` |
  | GitHub (code, PRs, Actions) | GitHub MCP server next to the Control plane |
  | Sentry, Datadog, GitLab | MCP next to the Control plane; attachable by the operator, not shipped or tested in v1 |

- **Sensor relay** (designed now, built after v1). MCP has no reverse-connect transport, so a server inside a dial-out-only Location is reached through the Sensor: the Control plane's client speaks JSON-RPC to it over the Sensor's link. The server's credentials stay in the Location. The Sensor enforces that server's tool allowlist and byte cap and redacts, and the Control plane redacts again. It relays only to servers named in its configuration, never to arbitrary URLs. The Sensor's namespace allowlist does not apply to relayed servers; their read scope is whatever their own credentials allow.
- **Budgets.** Redaction and a byte cap run at the edge that fetches the data, and the Control plane runs the same redactor again (ADR 0003). Each result is capped at min(a share of the context window, an absolute size). Logs are cut oldest-first with a marker, a truncated result carries a "narrow your query" hint, and identical repeat calls are refused. The loop has its own step, token, and time budgets, and running out still produces an RCA. No spill-to-disk, and no summarising of raw output by a second LLM.
- **Windows.** The model chooses each call's window, from 6 hours before the first Signal until now, at most 1 hour per logs call and 6 hours per metrics call. To be tuned with the eval harness.
- **Sensor unreachable.** A per-call deadline (about 20 seconds) and one retry on transport errors. A failure becomes a structured `unavailable` result, counts against the step budget, and is reported as *not checked*. If the Location is offline at the start, only the Control plane's tools are offered and confidence is capped at `low`.
- **RCA schema.** `summary`, `started_at`, `introduced_by` (a typed change reference such as a rollout revision, commit, config diff, or image, or an explicit `none_found`), `impact`, `confidence` (`low`, `medium`, `high`), `evidence_ledger` (affected / checked and healthy / changed for another reason / not checked), and `gaps`. Each field is made of **claims** `{id, statement, kind: observed | inferred, cites}`: an observed claim cites evidence refs, an inferred claim cites other claims. An **evidence ref** (call id, source, Location, Target, window) is the audit log's key for that call. Gate 1 checks the citations (ADR 0003); whether a claim is true is the eval harness's job.
- **Runbook guidance** matched to the Incident (D8) goes into the loop as trusted operator text, kept apart from tool output, which is untrusted. It grants no tools, scope, or Actions.
- **Prior art (exact match only).** Past Incidents with the same Target and the same alert or Detector, whose Plan ran and resolved the Incident without a reopen, are fed to both loops as hypotheses. Claims must cite fresh evidence, never the old Incident.
- **Reproducibility.** Each investigation's audit entry records the model snapshot id (never an alias), the prompt version, and the ids and versions of the Runbooks used. **Replay** re-runs a past investigation with a new model, prompt, or Runbook against its recorded tool results; a call that was not recorded returns `not_recorded`, and the miss rate is reported.

### D8. Runbooks and the remediation loop

**A Runbook is input to the investigation and the remediation. It never runs.**

- **Import.** An operator's procedure (a Confluence page, Markdown in Git, any other prose) goes to the LLM, which drafts a split into **guidance** (Markdown) and **actions**. Human-written "alert → guidance" and "alert → action" content is kept as written; anything else is inferred. The importing operator reviews, edits, and approves the draft, and only approved Runbooks are used. Each is recorded, immutable and versioned, in a registry in the Control plane's store; Git and Confluence are import sources only. A Runbook with guidance and no actions is what was earlier called an investigation guide.
- **Match key.** An optional Signal name (Alert or Detector) plus an optional Target selector (Environment, Location, namespace, kind, name, with globs). At least one must be set. Triage's (Signal, Target) selects every match: guidance feeds the RCA loop, actions feed the remediation loop. Semantic search for related Runbooks comes later.
- **Remediation loop.** Inputs: the RCA, the matched Runbooks, its own read-only tools, and prior art. Output: **Advice**, composed only of Actions that Executors offer (ADR 0003). A Runbook's actions and bounds are suggestions, not a fence: Advice may use any offered Action, and hard limits live in each Action's argument schema. Steps that follow a matched Runbook are labelled **runbook-backed**, cite its version, and exist only to tell the reviewer where a step came from. Every step cites RCA claims or evidence refs; the Target comes from the Control plane, and other values (such as the previous healthy revision) must cite evidence. Empty Advice ("escalate") is valid, and so is "no Runbook fits, because…".
- **Remediation families**, in delivery order, all expressed as Plan steps: **A** in-place actuation (rollout undo, restart, scale); **B** adjacent control (traffic, feature flags); **C** external pipelines (CI revert, jobs); **D** a pull request with a fix. B and C need an Executor for systems outside a Location, which runs next to the Control plane, never inside it, and is not yet specified.

## Consequences

**Positive**

- One Control plane serves a whole estate, while write credentials stay in each Location. A compromised Control plane can act only within what each Executor was granted.
- A new platform is a new implementation of three interfaces (Detectors, evidence tools, Actions) plus a Target model. The core does not change.
- Operators bring their own monitoring and their own runbook prose. Neither has to be rewritten into incident-copilot's code.
- Every claim in an RCA points at a recorded tool call, and every investigation can be replayed against a new model or prompt.

**Negative**

- **More moving parts than the demo.** Three roles, a persistent link, a store, and per-Location enrolment.
- **Pod-only Alerts get no Target.** Their Incidents are L1, and an Alert and a Detector on the same failure can become two Incidents. Operators add a workload label to fix it; Sensor-assisted Target resolution comes later.
- **One Control plane replica.** A restart pauses Triage and investigations until it is back. Sources re-send, so nothing is lost.
- **Signals are never grouped across Targets.** A dependency failure produces one Incident per affected workload.

## Alternatives considered

| Alternative | Why it was rejected |
|---|---|
| **One deployment per cluster** (the "Install") | Does not serve a multi-cluster estate, duplicates the LLM loop and ticketing per cluster, and offers no place for sources that are central by nature. |
| **A central Control plane with write credentials for every Location** | One compromise reaches every cluster. Executors in each Location keep write power local. |
| **A Platform plugin type loaded at runtime** | No product needs it, and it widens what has to be trusted. Interfaces compiled into one binary are enough. |
| **A cross-source identity hash for Signals** | Hashing different payloads into one identity is guesswork. Triage joins Signals by Target instead. |
| **An LLM in Triage** | Which Incident a Signal joins decides its history and its limits, so it must be deterministic. |
| **Deriving a Deployment from a pod name** | Scope binding would rest on a guess. |
| **Our own typed tools only, or MCP only** | MCP servers exist for most backends and save work, but MCP cannot reach into a dial-out-only Location yet. The route is chosen per source. |
| **Runbooks that execute** (registered in code, or imported playbooks) | Opaque playbooks cannot be checked step by step, and code-registered runbooks block adoption. Runbooks inform Advice instead, and only typed Actions run. |

## Not yet specified

These are in scope for the product but not decided here: packaging and the configuration model; incident-copilot's own observability; LLM data custody and self-hosted models; scoring L0 shadow mode on real incidents; paging integrations; Sensor-assisted Target resolution; Executor permission setup; the Control plane ↔ Sensor/Executor link; a Job failure Detector; Incident memory beyond exact matches; Runbook import sources and re-import; the external Executor; conversational Advice editing; Control plane high availability; Signal storms; and what happens to Advice that fails Gate 1.
