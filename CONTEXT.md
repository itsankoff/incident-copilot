# incident-copilot

A self-hosted, production-grade incident responder that acts as a first-line SRE: it notices production and runtime issues, investigates them, reports a root-cause analysis, and resolves them within allowlisted, human-configured limits. Kubernetes is the first platform; managed Kubernetes on AWS, GCP, and Azure follows, then platforms that are not Kubernetes.

## Language

### Triggers

**Signal**:
A source-neutral notice that something may be wrong with a workload, and the only thing that starts an investigation.
_Avoid_: event, trigger, notification

**Alert**:
A Signal that comes from the operator's existing monitoring (Alertmanager first, other alert sources later).
_Avoid_: alarm

**Detector**:
A built-in rule that watches a platform directly for a known failure (a crash loop, an OOM kill, a stuck rollout) and produces a Signal, covering failures the operator has no alert rule for.
_Avoid_: anomaly detector, monitor

### Response

**Runbook**:
One allowlisted, typed remediation action registered in code, with bounded parameters and a maximum autonomy level.
_Avoid_: playbook, script, fix

**Proposal**:
The agent's suggestion to run one Runbook against one target, which is data and never a command.
_Avoid_: action, recommendation

**Autonomy level**:
How far a Proposal may go on its own: L0 observe, L1 suggest, L2 approve, L3 autonomous.
_Avoid_: mode, permission level

**Executor**:
The only component that changes state; it decides the effective autonomy level in code and never calls an LLM.
_Avoid_: runner, actuator

**Install**:
One self-hosted deployment of incident-copilot, scoped to one environment, run by the operator's own team.
_Avoid_: instance, tenant
