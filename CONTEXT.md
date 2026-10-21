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
_Avoid_: anomaly detector, monitor, Sensor

**Signal source**:
One configured sender of Signals, such as one Alertmanager or one Sensor, with its own credential, default Location, label mapping, and way of showing that a firing Signal is still live.
_Avoid_: integration, receiver, webhook

**Source adapter**:
What turns the payload of one kind of Signal source (Alertmanager, Datadog, CloudWatch) into Signals, after checking that the sender is who it claims to be.
_Avoid_: normalizer, parser

### Incidents

**Incident**:
One problem with one Target, gathering every Signal about it; the unit that is investigated and gets one ticket. It is open while its Signals fire, resolved once they have all ended, and closed when its ticket is closed.
_Avoid_: alert, case, issue

**Target**:
The workload an Incident is about, named by platform, Location, kind, and name (plus namespace where the platform has one) rather than by any one running copy of it; an Incident may have none when no Signal names a workload.
_Avoid_: resource, pod, service

**Target version**:
A marker the platform gives a Target that changes whenever someone changes what the Target should be, and not when its running state changes; an Approval pins it, so a Target changed by someone else is never acted on.
_Avoid_: generation, revision, resourceVersion

**Platform**:
The kind of runtime a Target runs on, such as Kubernetes, Lambda, or ECS; one Location may host several.
_Avoid_: provider, cloud, backend

**Location**:
Where a Target lives on its provider and platform: a cluster, an account and region, a project, or a subscription; named by the operator, uniquely, where the provider has no name for it.
_Avoid_: cluster, region, scope

**Triage**:
The deterministic step that decides whether a Signal opens a new Incident, joins an open one, or reopens a resolved one.
_Avoid_: correlation, grouping

### Investigation

**Evidence query**:
One read-only call the LLM makes to an Evidence source during an investigation; its answer is redacted and size-bounded before it reaches the LLM and carries a reference the RCA cites.
_Avoid_: tool, tool call, lookup

**Evidence source**:
One configured place the LLM can read evidence from, such as a Location's Sensor or an MCP server for code, logs, or errors, with its own read-only credential and its own list of allowed tools; each backend is reached through exactly one Evidence source.
_Avoid_: integration, connector, plugin

**Evidence ref**:
The reference an Evidence query's answer carries, naming that call and what it read, which a Claim cites to point at the evidence behind it.
_Avoid_: citation, link, source

**Claim**:
One statement in an RCA, either observed, citing the Evidence refs it rests on, or inferred, citing the other Claims it follows from; a Claim with no citation is shown as not based on evidence.
_Avoid_: finding, fact, conclusion

**Sensor relay**:
The way a Location's Sensor carries the Control plane's calls to an MCP server inside that Location, over the connection the Sensor opened, so the Location's credentials never leave it.
_Avoid_: proxy, tunnel, bridge

### Response

**Runbook**:
An operator's procedure, imported once from a page or document and approved by a human, recorded as guidance and actions for a Signal and Target; the investigation reads its guidance and the remediation reads its actions, and it never runs by itself.
_Avoid_: playbook, investigation guide, script, fix

**Action**:
One typed operation an Executor offers, such as restarting a pod or scaling a workload, with arguments it checks; the only thing a step of a Plan can do.
_Avoid_: command, tool, job

**Advice**:
The remediation steps the LLM drafts for an Incident from its RCA, composed only of Actions that Executors offer and informed by the Incident's Runbooks, citing the RCA's claims, the evidence, and the Runbooks it used; it does nothing until a human reviews, may edit, and approves it, which turns it into a Plan.
_Avoid_: suggestion, fix, recommendation

**Plan**:
Advice a human has approved: an ordered list of Actions with their arguments, all on the Incident's Target, and the only thing an Executor runs; it carries one Autonomy level for the whole Plan, is data and never a command, and a human may always carry it out by hand instead.
_Avoid_: proposal, job, script

**Preview**:
The before and after of what an Action will change on its Target, worked out from the Target's current state and shown to whoever approves the Plan.
_Avoid_: dry run, diff

**Guardrails**:
The Control plane's deterministic checks on every RCA and every Advice the LLM writes, before anything leaves the investigation: that each Claim cites real evidence from this Incident and that each step uses an offered Action, valid arguments, and the Incident's own Target; also called Gate 1.
_Avoid_: policy, filter, validation

**Approval**:
A human's acceptance of one exact version of Advice, which turns it into a Plan the Executor may run; any edit needs a new Approval, and an unanswered request expires as a denial. Also called Gate 2.
_Avoid_: sign-off, consent, ack

**Autonomy level**:
How far a Plan may go on its own: L0 observe, L1 suggest, L2 approve, L3 autonomous.
_Avoid_: mode, permission level

**Executor**:
The only component that changes state, deployed in or next to the Location it acts on; it runs what the Control plane sends, only with that Location's own credentials, and never calls an LLM.
_Avoid_: runner, actuator

### Deployment

**Control plane**:
The centrally run part of incident-copilot that takes in Signals, runs Triage, investigates, writes tickets, and hands Plans to Executors; it is the trusted part that checks every LLM output and collects approvals. It serves one or more Environments, has read-only access, and never changes state itself.
_Avoid_: agent, install, server

**Sensor**:
A read-only component deployed in a Location that runs that Location's Detectors, the way an Executor runs its Actions, answers the Control plane's Evidence queries about Targets there, and can act as a Sensor relay; it never calls an LLM and never changes state.
_Avoid_: agent, collector, probe, investigator

**Environment**:
A stage of the operator's estate, such as production or staging, that sets the autonomy ceiling for every Location in it; each Location belongs to exactly one Environment, set by configuration and never by a Signal.
_Avoid_: stage, tier, env label
