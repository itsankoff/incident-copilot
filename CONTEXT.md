# incident-copilot

A self-hosted, production-grade incident responder that acts as a first-line SRE: it notices production and runtime issues, investigates them, reports a root-cause analysis, and resolves them within allowlisted, human-configured limits. Kubernetes is the first platform; managed Kubernetes on AWS, GCP, and Azure follows, then platforms that are not Kubernetes.

## Language

### Triggers

**Signal**:
incident-copilot's source-neutral form of an Alert or a Detector finding that something may be wrong with a workload, and the only thing that starts an investigation.
_Avoid_: event, trigger, notification

**Alert**:
A notice from the operator's existing monitoring (Alertmanager, Datadog, CloudWatch, and so on), before incident-copilot normalises it into a Signal.
_Avoid_: alarm

**Detector**:
A built-in rule, run by a Sensor, that watches a platform directly for a known failure (a crash loop, an OOM kill, a stuck rollout) and produces a Signal, covering failures the operator has no alert rule for.
_Avoid_: anomaly detector, monitor

### Incidents

**Incident**:
One problem with one Target, gathering every Signal about it; the unit that is investigated, gets one ticket, and carries the per-incident limits on actions.
_Avoid_: alert, case, issue

**Target**:
The workload an Incident is about, named by platform, Location, kind, and name (plus namespace where the platform has one) rather than by any one running copy of it; an Incident may have none when no Signal names a workload.
_Avoid_: resource, pod, service

**Platform**:
The kind of runtime a Target runs on, such as Kubernetes, Lambda, or ECS; one Location may host several.
_Avoid_: provider, cloud, backend

**Location**:
Where a Target lives on its provider and platform: a cluster, an account and region, a project, or a subscription; named by the operator, uniquely, where the provider has no name for it.
_Avoid_: cluster, region, scope

**Triage**:
The deterministic step that decides whether a Signal opens a new Incident, joins an open one, or reopens a closed one.
_Avoid_: correlation, grouping

### Investigation

**Evidence query**:
A typed, read-only question about a Target or its Location, answered by that Location's Sensor or, for sources outside it, by the Control plane; its answer is redacted and size-bounded before it reaches the LLM.
_Avoid_: tool, tool call, lookup

### Response

**Runbook**:
One allowlisted, typed remediation action registered in code, with bounded parameters and a maximum autonomy level.
_Avoid_: playbook, script, fix

**Proposal**:
The Control plane's request to run one Runbook against one target, which is data and never a command.
_Avoid_: action, recommendation

**Advice**:
Remediation steps written in free text when no Runbook fits, for a human to follow by hand; it never reaches the Executor.
_Avoid_: suggestion, fix

**Autonomy level**:
How far a Proposal may go on its own: L0 observe, L1 suggest, L2 approve, L3 autonomous.
_Avoid_: mode, permission level

**Executor**:
The only component that changes state, deployed in or next to the Location it acts on; it decides the effective autonomy level in code and never calls an LLM.
_Avoid_: runner, actuator

### Deployment

**Control plane**:
The centrally run part of incident-copilot that takes in Signals, runs Triage, investigates, writes tickets, and hands Proposals to Executors; it serves one or more Environments, has read-only access, and never changes state.
_Avoid_: agent, install, server

**Sensor**:
A read-only component deployed in a Location that runs Detectors and answers the Control plane's evidence queries about Targets there; it never calls an LLM and never changes state.
_Avoid_: agent, collector, probe, investigator

**Environment**:
A stage of the operator's estate, such as production or staging, that sets the autonomy ceiling for every Location in it; each Location belongs to exactly one Environment, set by configuration and never by a Signal.
_Avoid_: stage, tier, env label
