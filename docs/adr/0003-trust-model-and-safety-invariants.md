# ADR 0003: Trust model and safety invariants

- **Status:** Accepted
- **Date:** 2026-10-20
- **Deciders:** @itsankoff
- **Supersedes:** [ADR 0001: Autonomy and trust model](0001-autonomy-and-trust-model.md)
- **Related:** [ADR 0002: Product scope and platform seams](0002-product-scope-and-platform-seams.md), [`CONTEXT.md`](../../CONTEXT.md), [Wayfinder: production-grade incident copilot](https://github.com/itsankoff/incident-copilot/issues/3)

## Context

ADR 0001 decided how much the agent may do on its own and how that is enforced, for a demo with one agent, one executor, and three runbooks registered in code. [ADR 0002](0002-product-scope-and-platform-seams.md) changes the shape of the product: a central Control plane that runs the LLM, a Sensor and an Executor in each Location, Runbooks imported from the operator's own procedures, and an LLM that writes fixes for Incidents nobody wrote a Runbook for. This ADR restates the trust model and the complete list of safety invariants for that shape. Where it changes ADR 0001, it says so.

The forces of ADR 0001 still hold:

1. **The model is not a trustworthy decision maker.** Its output is non-deterministic, its confidence is not calibrated, and it can be confidently wrong (the decoy scenario).
2. **The model reads input that attackers can influence.** Log lines, events, commit messages, and alert text all reach it. An instruction in the system prompt is not a control.
3. **Speed is the point**, but not at any price.
4. **Every action must be reconstructable** for post-incident reviews and regulated operators.
5. **Trust is earned gradually.**
6. **Small enough to verify.** The safety core must be readable in one sitting and testable exhaustively.

Two forces are new:

7. **The value is in Incidents without a Runbook.** If the LLM may only pick from a fixed set of runbooks, most Incidents end in "escalate". The LLM has to be able to compose a fix, which means the closed set must move from whole runbooks to the steps they are made of.
8. **The Control plane is central.** It already holds the LLM credentials, the ticketing credentials, and the approvals. Making the Executor re-verify every approval and recompute every decision on its own, as ADR 0001 D4/D5 did, would mean every Executor needs access to the ticketing system and the approval channels. It adds little protection against a compromised Control plane, which could forge the evidence the Executor would check.

**Prior art.** Azure SRE Agent sets its run mode, *Review* or *Autonomous*, per response plan and per scheduled task. Read-only access comes from the *Reader* permission level, and Review gates only writes to Azure infrastructure. (ADR 0001 described agent-wide *ReadOnly*, *Review*, and *Autonomous* modes; that is out of date.) Its write power is whatever the RBAC roles on its identity allow, and it runs CLI commands within them. AWS DevOps Agent requires approval for every action, with a session policy per action and a hard "never delete" list. Komodor and HolmesGPT filter free-form commands with patterns plus approval. None of them binds writes to the alerting workload, binds approval to a digest of what will change, or composes fixes from a closed, typed set of Actions. See the resolution of [Prior art: how existing AI SRE agents model platforms, signals, and actions](https://github.com/itsankoff/incident-copilot/issues/4).

## Decision

**The model drafts, deterministic code checks, and a human approves.** The model's output is data, never a command. The system is designed so that a model that is wrong, or fully hijacked by injected text, can at worst:

> draft **Advice** made of **Actions the Executors offer**, with **arguments their schemas accept**, against **the Incident's own Target**, which **a human must approve as an exact version** before anything runs, and which is **audited** before every step.

Everything below exists to make that sentence true.

### D1. Trust boundaries and the two gates

- **The Control plane is trusted, and so is its link to Sensors and Executors.** It runs Triage, both LLM loops, the checks, and the approvals. What is guarded is LLM output entering the Control plane, and Signals entering it from outside.
- **Gate 1, guardrails.** Deterministic Control plane code checks every LLM output before anything leaves the loop (D4). It cannot judge whether an RCA is right; it judges whether the output is well-formed, cited, and in scope.
- **Gate 2, approval.** A human reviews, may edit, and approves every Plan before an Executor runs it (D6). There is no opt-out in this version.
- **The Executor runs what the Control plane sends**, only with the credentials granted in its own Location, writes its audit entry before acting, verifies, and reports back. It never calls an LLM and has no access to the ticketing system or approval channels.
- **Known risks**, accepted for this version: a compromised Control plane can act in every Location, within each Executor's credentials and only through the Executors' Action catalogs; tampered Alert content can steer what the LLM chooses to read.

*Supersedes ADR 0001 D4 ("the executor validates again and does not trust the agent's decision, scope check, or claim of approval"), D5 ("the executor owns the gate"), and D3 ("computed twice, trusted once").*

### D2. A closed, typed Action catalog per Executor

The LLM composes Advice only from **Actions**: typed operations that an Executor offers. It never runs shell commands, `kubectl exec`, free-form patches, scripts, or deletes of controllers. Empty Advice ("escalate to a human") is always valid.

Each Action declares:

- a typed argument schema, whose bounds are the hard limits (an unknown or extra field, or a value out of range, is rejected);
- the Target kinds it accepts;
- its preconditions, checked against fresh state;
- its preview (ADR 0002 D6);
- its own mechanical check and timeout (rollout complete, pod Ready).

Actions carry no Autonomy level of their own. Adding an Action, or widening its bounds, is a code change that goes through review and comes with tests. Runbooks never add Actions: they suggest which Actions to use (ADR 0002 D8).

The Location Executor's Actions for Kubernetes in this version carry over ADR 0001's runbooks, without their maximum levels and cooldowns:

| Action | Effect | Bounds | Preconditions (checked in code) |
|---|---|---|---|
| `rollback_deployment` | Roll a Deployment back to its previous ReplicaSet revision | Exactly one revision back | A previous revision exists |
| `restart_pod` | Evict one pod through the Eviction API, which honours PodDisruptionBudgets | One pod per Action, named in `args` | The pod belongs to the Target, and at least one other replica is Ready |
| `scale_up` | Raise a Deployment's replica count | +1 replica per Action, never above the policy cap (default: 2× the replica count when the Incident opened) | No HorizontalPodAutoscaler targets the Deployment |

An Executor for systems outside a Location (CI/CD, flags, traffic) will offer its own catalog. It runs next to the Control plane, never inside it, and is not yet specified.

*Supersedes ADR 0001 D1 and invariant 1: the closed set moves from whole runbooks to Actions, and per-runbook maximum levels go away.*

### D3. Autonomy levels, computed in code, downgrade only

| Level | Name | What happens to a Plan | Who executes |
|---|---|---|---|
| **L0** | Observe | It is recorded in the audit log only. | Nobody |
| **L1** | Suggest | It is shown in the ticket with the reason it was not automated. | A human, by hand |
| **L2** | Approve | It waits for approval of its exact version (D6). | The Executor, after approval |
| **L3** | Autonomous | Not reachable in this version. | |

A Plan has **one** level for the whole Plan:

```
effective = min(
    environment.ceiling,           // config, per Environment, at most L2 in this version
    policy.override,               // the kill switch, re-read on every decision
    confidence_cap(rca.confidence),// high → L2 in this version, medium → L2, low → L1
    guard_cap,                     // L1 if any scope, precondition, or guard check fails
)
```

- **Downgrade only.** Every input can lower the level, and none can raise it. The LLM never states a level. Its confidence can only lower it.
- **Pure and exhaustively tested.** `Decide` is a pure function of the Plan, the Actions' declarations, the policy, a snapshot of state, and the history. It has no I/O and no clock of its own, and is table-tested over every combination of inputs.
- **Every cap is explained** in the ticket and the audit log ("capped at L1: the Incident has no Target").
- **Computed once, by the Control plane.** The Executor checks the target version and preconditions on fresh state before every step (D6), but does not recompute the level.
- **Fail closed.** An unknown Environment is treated as production. A missing or unparseable policy means a ceiling of L1. A precondition that errors counts as failed. An Incident with no Target caps its Plans at L1.
- **L0 is shadow mode.** Scoring it on real Incidents is not yet specified.

*Supersedes ADR 0001 D2 and D3: the level belongs to the Plan, not to a runbook, and L3 is out of reach.*

### D4. Gate 1: guardrails on every LLM output

The Control plane checks, in code:

- **RCA.** Every cited evidence ref exists in **this Incident's** audit log and came back successfully, and every inferred claim cites existing claims. An uncited claim survives one repair retry; after that it is still shown, labelled "not based on source evidence", and the RCA's confidence is capped at `low`.
- **Advice.** Each step names an Executor that exists and an Action it offers, its `args` pass that Action's schema, and its `target` equals the Incident's Target. Each step cites RCA claims or evidence refs, and any value other than the Target's identity (a revision, a pod name) cites evidence; Alert free text never supplies one. Plan-level limits (number of steps, the same Action repeated on the Target) are guards to be specified. What happens to Advice that fails these checks (feedback to the loop, rejection, reporting) is not yet specified.
- **Inputs.** Only identifiers from the Alert reach the LLM (Target, Location, name, start time, severity, source references), never its annotations or other free text. Tool output is framed to the model as untrusted data, kept apart from the trusted operator text of Runbook guidance.

### D5. Scope binding

- **Every step's `target` equals the Incident's Target** (`step.target == incident.target`), a plain equality in the core.
- **The Incident's Target comes from Signal data, never from model output:** from the Signal source's label mapping, or from a Detector whose Sensor is in that Target's Location. A Detector Signal whose Target is in another Location is rejected. A Target is never derived from a guessed name, such as a pod name with its hash cut off.
- **Sub-objects are arguments.** An Action that works on one copy (a pod, a revision) names it in `args`, and the Action's preconditions check that it belongs to the Target. A failed check is a guard failure, so the Plan drops to L1.
- **An Executor can act only within what its Location granted it**, whatever the Control plane sends.

Text injected into logs ("roll back payments-api") therefore cannot aim a Plan at another workload. The decoy scenario (a harmless `inventory` deploy just before an `orders` memory leak) cannot turn into a rollback of `inventory` from the `orders` Incident.

*Restates ADR 0001's scope binding and invariant 4. It is a clarification, not a relaxation: "the alerting workload" becomes the Incident's Target, resolved deterministically by the Control plane. The namespace allowlist on actions is replaced by the Executor's own credentials, which are set up per Location (not yet specified).*

### D6. Plans: approval, execution, and verification

- **Shape.** A Plan is data: an ordered list of steps `{executor, action, target, args}`. Advice becomes a Plan when a human approves it.
- **Review and edit.** The ticket shows the Advice read-only and links to a review page served by the Control plane. There a human may remove, reorder, or add steps (only Actions the Executor offers) and change `args`, but never `target`. Every save is a new version with a new preview and digest, and Gate 1 checks it again. The audit log records the diff from the Advice. Slack or a ticket comment can approve only Advice nobody edited. Who may approve, and through which channels, is a guard to be specified.
- **The approval binds one exact version** through a digest of every step's Action, Target, `args`, and preview, plus the Target version before the run. Silence is not consent: an unapproved Plan expires (15 minutes, a placeholder), expiry counts as a denial, and a denial is final for that Advice.
- **Execution.** The Control plane runs the Plan one step at a time and holds the Plan's state; step N+1 is sent only after step N has a result. For each step the Executor validates it, writes and fsyncs its audit intent, checks the Target version and preconditions, applies the Action, runs the Action's mechanical check, and returns the result. It is stateless between calls.
- **Target version across steps.** Before step 1 the Executor checks the version pinned in the digest. Before each later step it checks the version its own previous step left. Any other change means someone else changed the Target, and the run aborts and escalates. Each step's preview is computed against current state, and the review page shows later steps' previews as approximate.
- **Verification.** After the last step, the Control plane checks that the Incident's Signals **resolve** within a window. Expiry never counts as resolved.
- **Endings.** A run ends as *succeeded*, *unverified* (every step succeeded but the Signals still fire), *failed at step N*, or *aborted* (stale state, or the Target changed). A lost connection mid-step makes the result unknown, which counts as failed. Every ending other than *succeeded* escalates to a human. There are no retries, nothing rolls back automatically, and a Plan never resumes. The next remediation comes from a re-investigation when the Signals fire again, or from a human.
- **Reporting.** The Control plane writes each step's result to the ticket as it arrives.

*Supersedes ADR 0001 D5: the digest covers the whole Plan, `generation` becomes the Target version, and the dry-run result becomes the preview. ADR 0001 D6 ("no autonomous chains", L3 guardrails) is replaced by invariant 9.*

### D7. Investigation stays read-only, in layers

- **No shell.** Investigation tools are read-only, with no `bash`, `exec`, or shell templates.
- **Read-only is layered**, and the prompt is never the only layer on a path we control:
  1. read-only credentials wherever the backend supports them (Sensor RBAC of `get`, `list`, and `watch` with **no Secrets and no ConfigMaps** by default, plus `pods/log`; a fine-grained GitHub token; Sentry `inspect`; Argo CD `role:readonly`; a GitLab Reporter user);
  2. the server's read-only profile or flags, switched on whenever they exist (these hide tools; they are not the boundary);
  3. our per-source tool allowlist, enforced in code: a call that is not on it is refused before it leaves;
  4. a read-only investigation system prompt, always present, advisory only.
- **A source whose credentials cannot be made read-only** needs an explicit operator acknowledgement and an explicit, non-empty allowlist. Otherwise it is rejected, and the rejection is audited.
- **Tools that call the server's own LLM** (Sentry AI search, some Datadog tools) are off the allowlist by default, because they send query text outside our redaction.
- **MCP servers are reached only through our own client.** The provider-side MCP connector is out of scope for this version, because its results reach the model before our redaction. Where a flow is not under our control, the system prompt is the only layer left, and the operator has to acknowledge that integration's risk.
- **Tests.** A test asserts that the Sensor's Kubernetes client uses only read verbs.

*Restates ADR 0001 invariant 3 with "read-only, no shell" in place of "typed queries".*

### D8. Redaction and the audit log

- **Redaction runs at the edge that fetches the data** (the Sensor, or the Control plane's handler or MCP client) **and again in the Control plane before every LLM call**, including any summarising or compacting, and before the audit log. Server tool descriptions, `instructions`, error text, and pre-signed URLs (such as GitHub job-log links) go through it too.
- **What is recorded:** every Signal, Triage decision, tool call with its arguments and redacted result, LLM request and response, Advice, Gate 1 result, decision with each cap, approval request, approval or denial or expiry with the approver's identity, every edit, and each step's intent, result, and verification.
- **Two chains.** The Control plane keeps its own chain. Each Executor keeps a write-ahead chain: it writes and fsyncs an `intent` entry before it applies an Action and a `result` entry afterwards. **If the intent write fails, the Action does not run.** Entries carry the Incident and Plan ids so the chains can be joined.
- **Format.** Hash-chained JSONL, tamper-evident rather than tamper-proof. Shipping it to immutable storage is out of scope for this version.
- **Separate streams.** The audit log is not the operational log. Nothing reads it to make decisions, apart from guard history and Gate 1's citation check.

*Restates ADR 0001 D7, extended to every LLM call and every path we control.*

### D9. Signals are authenticated

Every Signal source has its own credential and uses the strongest method its source offers (a bearer token or mTLS for Alertmanager, HMAC for Grafana, the X.509 signature for SNS). Two credentials can be valid at once, for rotation. An unauthenticated request is rejected with `401`, nothing in it is processed, and it is audited. There is no unauthenticated mode. Labels decide the Target and scope binding trusts the Target, so a forged Signal could otherwise aim a Plan at any workload.

### D10. Deferred, not in force

Out of scope for this version, and not in force until a new ADR brings them back:

- **L3**, standing approvals, and any switch that lets Plans skip approval.
- **Earning autonomy with evidence** (ADR 0001 D8): eval thresholds for raising a ceiling, and the unsafe action rate as a gate. The eval harness still measures the unsafe action rate.
- **Promoting a Plan that worked to a Runbook.**
- **The L3 guardrails of ADR 0001 D6:** one L3 action per Incident, its verification window, and the global L3 rate limit.

### Guards, to be specified

These protections are part of the model. Their values, formats, and permission tables are not decided yet. Until they are, each is a placeholder that fails closed.

- **Cooldowns:** how soon the same Action may run again on the same Target.
- **Rate limits:** how many Plans may run per Location (configurable per Location) and overall.
- **One run per Target at a time:** a lock, so two Plans never act on one Target at once.
- **Approver sets and channels:** who may approve, through which channels, per Environment.
- **Plan approval expiry:** the 15-minute default is a placeholder.
- **Plan-level limits:** the number of steps, and the same Action repeated on the Target.
- **RBAC and credentials:** the Executor's permission setup per Location (an easy default versus least privilege), credential types, and who may change policy.

## Safety invariants

This list is the canonical one and replaces ADR 0001's. Relaxing any invariant needs a new ADR that supersedes this one.

1. **Closed Action catalog.** A Plan step can only be an Action that an Executor offers, with arguments its schema accepts. No shell, scripts, or free-form commands.
2. **Levels are enforced in code.** A Plan's level is the minimum of its inputs. Nothing the model produces can raise it, and the model never states one.
3. **Read and write are separated.** Investigation is read-only, with no shell, in the layers of D7. The Control plane and Sensors never change state. Only Executors write, only with their own Location's credentials, and they never call an LLM.
4. **Actions are bound to scope.** Every step's Target equals the Incident's Target, which comes from authenticated Signal data and never from model output or a guessed name.
5. **An approval is specific and expires.** It binds one exact Plan version through its digest, the Target version is re-checked before every step, and silence is never consent.
6. **The audit entry comes first.** A failed intent write blocks the Action.
7. **Secrets never reach the LLM or the audit log.** Redaction runs before every LLM call on every path we control.
8. **Fail closed.** Errors, missing configuration, unknown Environments, and missing Targets lower the level, never raise it.
9. **No Plan runs without human approval.** A failed step or failed verification escalates to a human. Nothing is retried or rolled back automatically.
10. **Signals are authenticated.** Every Signal source has its own credential, and a Detector's Signal is accepted only for its own Sensor's Location.
11. **Alert free text never reaches the LLM.** Only identifiers do, and tool output is always framed as untrusted data.

### What changed from ADR 0001

| ADR 0001 | ADR 0003 |
|---|---|
| D1, invariant 1: a closed set of runbooks registered in code, each with a max level | D2, invariant 1: a closed, typed Action catalog per Executor; the LLM composes Advice from it; no per-Action level |
| D2, D3: per-runbook levels, `effective = min(runbook.max_level, …)` | D3: one level per Plan, `min(Environment ceiling, kill switch, confidence cap, guard cap)`; L3 unreachable |
| D3: computed twice, trusted once (the executor's result counts) | D1, D3: computed once by the trusted Control plane |
| D3 and invariant 4: the workload named by the alert's labels, in an allowlisted namespace | D5, invariant 4: `step.target == incident.target`, from mapped labels or the Detector's own Location, never guessed |
| D4: agent and executor; the executor re-validates everything and is reachable only by the agent | D1: Control plane and Sensor (read-only) and an Executor per Location, all dialing out; the Executor trusts the Control plane |
| D5: the executor owns the gate; digest of runbook, target, params, `generation`, and dry run | D1, D6: the Control plane owns approval; the digest covers the whole Plan, its previews, and the Target version, chained across steps |
| D6 and invariant 9: one L3 action per incident; cooldowns; a lock per target | D10: deferred. Invariant 9 restated: no Plan runs without approval. Cooldowns and the lock are guards to be specified |
| D7: redaction before the LLM and the audit log | D8: redaction at the fetching edge and again before every LLM call |
| D8: autonomy earned with eval evidence | D10: deferred, not in force |
| Invariant 3: typed, read-only investigation tools | D7, invariant 3: read-only in four layers, no shell |
| none | Invariants 10 (authenticated Signals) and 11 (no Alert free text to the LLM) |

## Consequences

**Positive**

- The LLM can fix Incidents nobody wrote a Runbook for, and every fix is still bounded by the Action catalog, the Target, and a human approval of an exact version.
- The Executor stays small: no LLM, no ticketing access, no approval channels. Its job is validate, audit, act, verify, report.
- An approver sees exactly what will change (previews, Target version) and what it rests on (cited claims, runbook-backed steps).
- Every decision can be explained and reconstructed from the two audit chains.

**Negative**

- **Every Plan waits for a human.** The 3 a.m. restart of a leaking pod is no longer automatic in this version. A correct RCA and a ready-to-approve Plan are most of the value until L3 comes back with its own ADR.
- **The Control plane is a single point of trust.** If it is compromised, it can drive every Executor within their catalogs and credentials. Executor credentials per Location and the closed catalogs bound the damage; approvals do not.
- **Composed Advice is harder to evaluate** than picking one of three runbooks. The eval harness has to score Plans, not runbook names.
- **Pod-only Alerts give L1 Plans** until Sensor-assisted Target resolution exists.

**Residual risks (out of scope for this version)**

- A compromised Control plane, approver account, or insider with cluster-admin rights.
- Tampered Alert content steering what the LLM reads (not what it can do).
- Tamper-proof storage of the audit log.

## Alternatives considered

| Alternative | Why it was rejected |
|---|---|
| **Keep runbooks as the closed set** (ADR 0001 D1) | Most Incidents end in "escalate", and runbooks written in our code block adoption. Actions keep the set closed at the level of each step. |
| **Executable Runbooks imported from playbooks** | Opaque steps cannot be checked against the Target. Runbooks inform Advice instead. |
| **The Executor re-verifies approvals and recomputes the level** (ADR 0001 D4/D5) | It needs ticketing and approval-channel access in every Location, and a compromised Control plane could forge what it checks. |
| **An opt-out that lets LLM Plans run without approval** | Too much freedom before there is evidence. Deferred with L3. |
| **Guardrails in the prompt only** | Not enforceable: the same context holds attacker-controlled text. The prompt is the last read-only layer, never the only one on a path we control. |
| **Free-form commands bounded by RBAC, or filtered by patterns** | The action space is unbounded, so neither a reviewer nor the eval harness can reason about it. |
| **The provider-side MCP connector** | Its results reach the model before our redaction. |
| **Autonomy driven by confidence** | Self-reported confidence is not calibrated. It is used only to lower the level. |

## Validation

| Check |
|---|
| A table test of `Decide` over every combination of ceiling, kill switch, confidence, and guard result: the result is never above any input, and never above L2 |
| Advice with an unknown Executor or Action, an extra field, an out-of-range argument, or a `target` other than the Incident's is rejected by Gate 1 |
| An RCA citing an evidence ref from another Incident, or one that failed, is rejected; an uncited claim caps confidence at `low` |
| A fake LLM that names a level, or targets a workload outside the Incident, gains nothing |
| An Incident with no Target, or a pod-only Alert, yields Plans at L1 |
| An approval for one Plan version does not run another; editing a Plan invalidates its approval |
| A Target whose version changed after approval, or between steps by someone else, aborts the run and escalates |
| A failed step, an unknown result, and *unverified* each escalate with no retry and no rollback |
| A failing audit writer blocks the Action |
| The Sensor's ServiceAccount cannot read Secrets or ConfigMaps, or `patch`, `create`, or `delete` anything (RBAC test) |
| A tool call that is not on the source's allowlist is refused before it leaves |
| A secret in a tool result, server description, or error never reaches the LLM or the audit log |
| An unauthenticated Signal is rejected and audited; a Detector Signal for another Location is rejected |
| Alert annotations never appear in an LLM request |
| Setting the kill switch to L1 stops execution on the next decision, with no restart |
| Each Incident can be reconstructed from the audit chains |
| The unsafe action rate is reported, and is zero on the decoy scenario |
