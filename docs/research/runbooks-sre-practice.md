# Prior art: what a Runbook is in production SRE practice

- **Ticket:** #29. Feeds the grilling ticket #16 (Production Runbooks). Wayfinder map #3.
- **Date:** 2026-10-20. All sources were read on this date.
- **Builds on:** [`prior-art-ai-sre-agents.md`](https://github.com/itsankoff/incident-copilot/blob/research/prior-art-ai-sre-agents/docs/research/prior-art-ai-sre-agents.md) (#4), which covered how AI SRE agents gate actions. This note covers the procedures themselves: playbooks, runbook-automation tools, and the remediation mechanisms they call.
- **Question:** What does "runbook" mean to SRE teams and in runbook-automation tools? How is a generic procedure bound to one target at run time, and who fills its parameters? Does the remediation spread (rollout undo, CI-triggered revert, pod restart, scale, feature-flag flip, traffic shift, open a PR with a fix) fit one shape, or does it split into families?

Sources are primary: the Google SRE books, official product docs, and specs. Snippets are short excerpts. "Abridged" means fields were left out, not changed. Vocabulary follows `CONTEXT.md`: Runbook, Advice, Plan, Target, Executor, Autonomy level, Location.

## Summary

1. **"Runbook" means two things in practice, and they sit at different ends of one path.** For Google SRE a *playbook* is prose: one entry per alert, giving severity, impact, and debugging hints. It is read by a trained human. When its steps become "a deterministic list of commands", the workbook says to automate them. In runbook-automation tools (AWS SSM, Rundeck/PagerDuty, StackStorm, Azure Automation, Ansible) a *runbook* is an executable, parameterised procedure. incident-copilot's Runbook is the second meaning. Its Advice is the first meaning, produced on demand.
2. **The tools converge on one anatomy:** typed **parameters** with allowed values or patterns, a **target binding**, ordered **steps** of typed actions whose outputs feed later steps, per-step **failure policy** (abort, continue, go to a step), **wait/assert steps** for verification, an optional **approval step**, and **versioning** with a way to pin the version that runs. Only AWS SSM spells out all of these as schema. In the other tools some of them are conventions inside scripts.
3. **Binding a procedure to a target is a fan-out mechanism everywhere, never a scope guard.** SSM `Targets` plus `TargetParameterName` selects resources by tag, resource group, or parameter values. Rundeck has node filters, which can be set from an option (`${option.name}`). Ansible has `hosts:` plus `--limit`. All three exist to widen one procedure across many targets, with rate control (`MaxConcurrency`/`MaxErrors`, `serial`, thread count). None of them checks that a target is *the one an incident is about*. The closest prior art for incident-copilot's model is **AWS Incident Manager's dynamic parameters**: `Involved resources` is filled from the incident, not by a human or an LLM. Next closest is **PagerDuty Automation Actions scoped to services**: an action is "available on any incidents that trigger on the selected service(s)".
4. **No tool lets an LLM fill parameters.** Parameters come from a human at run time (Rundeck, the SSM console), from the trigger (StackStorm rule Jinja, Robusta's `ALERT_LABEL_*` environment variables, Incident Manager dynamic values), or from static configuration. incident-copilot's split is new: the Control plane binds the Target from the Incident, and the LLM fills only the remaining bounded parameters.
5. **Approval is a step, not an envelope.** SSM `aws:approve` and StackStorm `core.ask` pause a running workflow at a chosen point, with approvers, a minimum count, and a timeout. Nothing binds the approval to a digest of what will run. Later steps act on whatever state exists when the workflow resumes.
6. **Dry run is per step type, not per procedure.** Ansible check mode is "just a simulation", and modules that don't support it "report nothing and do nothing". `kubectl --dry-run=server` exists only for Kubernetes API writes. SSM Automation has no general dry run. Azure Automation's *test pane runs the Draft for real*. A generic preview over arbitrary steps is not available anywhere. A preview that can go into an approval digest needs typed steps.
7. **The remediation spread splits into four families:** (A) in-place actuation of the Target through the platform API, (B) control-plane changes to an object *adjacent* to the Target (traffic weights, feature flags), (C) triggering an opaque pipeline elsewhere (CI revert, Rundeck/SSM/Ansible job), and (D) proposing a change to source (open a PR). They differ in what object is written, whether a server-side dry run exists, whether the effect is synchronous, and how scope can be proven. One Runbook *envelope* fits all four. One *step type* does not. See [Implications](#implications-for-incident-copilot).

## 1. Forms: who runs a runbook

| Form | Example (source) | Run by |
|---|---|---|
| **Prose playbook entry**, one per alert | Google SRE: "Each alert should have a corresponding playbook entry"; entries "explain the severity and impact of the alert, and include debugging suggestions" ([workbook, On-Call](https://sre.google/workbook/on-call/)) | Human |
| **Checklist of manual steps** inside an automation document | Incident Manager: "Runbooks use the **Pause** action type to describe a manual step"; `AWSIncidents-CriticalIncidentRunbookTemplate` "provides the Incident Manager incident lifecycle in manual steps" ([Incident Manager runbooks](https://docs.aws.amazon.com/incident-manager/latest/userguide/runbooks.html)) | Human, tracked by machine |
| **Executable document** | Runme: Markdown code blocks are cells with `name`, `interactive`, `promptEnv` ("Prompt user to set exported environment vars"), `excludeFromRunAll` ([Runme cell options](https://docs.runme.dev/configuration/cell-level)) | Human pressing run, cell by cell |
| **Declarative workflow** | SSM Automation (`schemaVersion: '0.3'`, `mainSteps`), StackStorm Orquesta, Keptn `Remediation` | Machine |
| **Script or playbook** | Azure Automation PowerShell/Python runbooks, Ansible plays, Rundeck `exec`/`script` steps | Machine (or a human at a terminal) |
| **Trigger → action rule** | Robusta `customPlaybooks` (`on_prometheus_alert` → `alert_handling_job`), StackStorm rules, PagerDuty Automation Actions | Machine, on event or on a click in the incident |

The Google books treat the prose form as a starting point to move away from. The introduction says that recording best practices ahead of time "in a 'playbook' produces roughly a 3x improvement in MTTR", and that "Humans add latency" ([SRE book, Introduction](https://sre.google/sre-book/introduction/)). The workbook says: "If your playbooks are a deterministic list of commands that the on-call engineer runs, we recommend implementing automation", and to "turn new, hard-won, production knowledge into automation" ([workbook, On-Call](https://sre.google/workbook/on-call/)). It also records an unresolved split. "Some SREs at Google advocate keeping playbook entries general so they change slowly", while others want "step-by-step playbooks to reduce human variability". The SRE book's automation chapter describes the end of the path as "Systems that don't need any automation" or "Autonomous systems that need no human intervention" ([SRE book, Automation at Google](https://sre.google/sre-book/automation-at-google/)).

For incident-copilot, **Advice is a generated prose playbook entry for one Incident**, and a **Runbook is the automated form**. "Plan → Runbook promotion" (#13 input to #16) is the workbook's "turn knowledge into automation" step, done with an audit trail.

## 2. Anatomy

### AWS Systems Manager Automation: the most complete schema

Top level: `schemaVersion: '0.3'`, `description`, `assumeRole`, `parameters`, `variables`, `mainSteps`, `outputs`, and `files` (attachments with `checksums: sha256`) ([schema](https://docs.aws.amazon.com/systems-manager/latest/userguide/documents-schemas-features.html)). Parameters have a `type` (`String`, `StringList`, `Integer`, `Boolean`, `MapList`, `StringMap`, …), `default`, `allowedValues`, `allowedPattern`, and `description`. Some parameter types are **resource-typed**: `AWS::EC2::Instance::Id`, `List<AWS::EC2::Instance::Id>`, `AWS::S3::Bucket::Name`, `AWS::IAM::Role::Arn`. Each is validated by a regex, and the console lists matching resources in a dropdown ([populating input parameters](https://docs.aws.amazon.com/systems-manager/latest/userguide/populating-input-parameters.html)).

Every step shares `name`, `action`, `maxAttempts`, `timeoutSeconds`, `onFailure` (`Abort | Continue | step:<name>`, default abort), `onCancel` (`Abort | step:<name>`), `isEnd`, `nextStep`, `isCritical`, and `inputs` ([actions reference](https://docs.aws.amazon.com/systems-manager/latest/userguide/automation-actions.html)). Step types include `aws:executeAwsApi`, `aws:executeScript`, `aws:runCommand`, `aws:branch`, `aws:loop`, `aws:waitForAwsResourceProperty`, `aws:assertAwsResourceProperty`, `aws:approve`, `aws:pause`, `aws:sleep`, `aws:executeAutomation` (a child runbook), and `aws:invokeWebhook`.

Abridged from AWS's own "restore root volume" example:

```yaml
schemaVersion: '0.3'
assumeRole: "{{ AutomationAssumeRole }}"
parameters:
  InstanceId: { type: String }
mainSteps:
- name: getInstanceDetails
  action: aws:executeAwsApi
  onFailure: Abort
  inputs: { Service: ec2, Api: DescribeInstances, InstanceIds: ["{{ InstanceId }}"] }
  outputs:
  - { Name: rootDeviceName, Selector: "$.Reservations[0].Instances[0].RootDeviceName", Type: String }
- name: stopInstance
  action: aws:executeAwsApi
  onFailure: Abort
  inputs: { Service: ec2, Api: StopInstances, InstanceIds: ["{{ InstanceId }}"] }
- name: verifyInstanceStopped
  action: aws:waitForAwsResourceProperty
  timeoutSeconds: 120
  inputs:
    Service: ec2
    Api: DescribeInstances
    InstanceIds: ["{{ InstanceId }}"]
    PropertySelector: "$.Reservations[0].Instances[0].State.Name"
    DesiredValues: ["stopped"]
```

That example shows the pattern most hand-written runbooks follow: **read state → act → wait for a property → act → verify**.

Approval is a step that can go anywhere in `mainSteps` ([aws:approve](https://docs.aws.amazon.com/systems-manager/latest/userguide/automation-action-approve.html)):

```yaml
- name: approve
  action: aws:approve
  timeoutSeconds: 1000          # default 7 days, max 30 days
  onFailure: Abort
  inputs:
    NotificationArn: arn:aws:sns:us-east-2:12345678901:AutomationApproval
    Message: "{{ message }}"
    MinRequiredApprovals: 1
    Approvers: [arn:aws:iam::12345678901:user/AWS-User-1]
```

Outputs are `ApprovalStatus` (`Approved | Rejected | Waiting`) and `ApproverDecisions`. There is no binding to the state the later steps will act on.

An execution can also watch a **CloudWatch alarm**: "If a specified alarm enters `ALARM` state, Automation stops the execution … If you define `onCancel` steps in your runbook, those steps run before the execution ends". The default is to stop if the alarm state can't be read ([alarm monitoring](https://docs.aws.amazon.com/systems-manager/latest/userguide/automation-cw-alarm-monitoring.html)). This is the only built-in *verification-during-execution* mechanism found in this survey.

### Rundeck / PagerDuty Runbook Automation

A job has `options`, `nodefilters`, and a `sequence` ([job YAML reference](https://docs.rundeck.com/docs/manual/document-format-reference/job-yaml-v12.html)). Abridged:

```yaml
- name: restart service
  multipleExecutions: false
  options:
    - name: environment
      required: true
      enforced: true          # only values from the list
      values: [dev, prod]
      regex: '^(dev|prod)$'
  nodefilters:
    filter: 'tags: web'
    dispatch: { threadcount: 1, keepgoing: false, rankAttribute: rank }
  sequence:
    keepgoing: false
    strategy: node-first      # or step-first / parallel
    commands:
      - exec: systemctl restart app
        errorhandler: { exec: echo "cleanup" }
      - jobref: { name: verify, group: ops }
```

Options can be required, enforced from a static list or a **remote URL that returns JSON**, regex-validated, secure, or multi-valued. They reach steps as `${option.name}` (shell-quoted by default), `@option.name@`, or `$RD_OPTION_NAME`. The docs flag `${unquotedoption.name}` as a security risk. `rundeck.execution.rejectUndeclaredOptions=true` rejects options the job does not declare. It is off by default ([job options](https://docs.rundeck.com/docs/manual/jobs/job-options.html)). A node filter can be set to `${option.name}`, so the person running the job picks the nodes ([creating jobs](https://docs.rundeck.com/docs/manual/jobs/creating-jobs.html)). Failure handling: "Stop at the failed step" (the default) or "Run remaining steps before failing". A step's error handler, if it succeeds, means "the step is not considered to have failed" ([workflows](https://docs.rundeck.com/docs/manual/jobs/job-workflows.html)).

PagerDuty **Automation Actions** wrap a script or a Runbook Automation job. They run on a runner, are attached to services ("The action will be available on any incidents that trigger on the selected service(s)"), and can be restricted to a team. They receive context variables "that use data from the first triggering alert attached to an incident" ([Automation Actions](https://docs.pagerduty.com/ai-automation/automation/automation-actions)).

### StackStorm: action metadata + Orquesta + Inquiries

An action is a metadata file plus an entry point ([actions](https://docs.stackstorm.com/actions.html)):

```yaml
name: restart_service
runner_type: remote-shell-script
entry_point: restart_service.sh
parameters:
  service:  { type: string, required: true, enum: [nginx, postgresql] }
  timeout_seconds: { type: integer, default: 30 }
  api_token: { type: string, required: true, secret: true }
  kwarg_op: { type: string, immutable: true, default: "-" }
```

`immutable` "defines whether the default value of a parameter can be overridden". This is how an operator pins a parameter that callers may not change. An Orquesta workflow is a graph of tasks with `next: [{when: <% succeeded() %>, publish: …, do: …}]`. An unhandled failure fails the workflow, and the `retry` and `noop` engine commands can recover from it ([Orquesta](https://docs.stackstorm.com/orquesta/languages/orquesta.html)). `core.ask` pauses a workflow "to wait for additional information". It validates the response against a JSON `schema`, restricts who may answer by `users` and `roles`, and expires after a `ttl` (default 24 h). An expired Inquiry is marked "timed out" ([Inquiries](https://docs.stackstorm.com/inquiries.html)). Rules bind a trigger to exactly one action, with parameters templated from the event: `baz: "{{ trigger.payload_parameter_1 }}"` ([rules](https://docs.stackstorm.com/rules.html)).

### Ansible

A play binds tasks to `hosts:` (an inventory pattern). `ansible-playbook site.yml --limit datacenter2` narrows a run ([patterns](https://docs.ansible.com/ansible/latest/inventory_guide/intro_patterns.html)). `serial` sets the batch size and `max_fail_percentage` the failure tolerance per batch. `throttle` and `run_once` give per-task control ([strategies](https://docs.ansible.com/ansible/latest/playbook_guide/playbooks_strategies.html)). `block`/`rescue`/`always` gives try/catch/finally. A successful rescue "reverts the failed status", but unreachable hosts and invalid task definitions skip `rescue` ([blocks](https://docs.ansible.com/ansible/latest/playbook_guide/playbooks_blocks.html)). Check mode: "Check mode is just a simulation", and "Modules that do not support check mode report nothing and do nothing". A task can opt out with `check_mode: false`, which makes it *write even under `--check`* ([check mode](https://docs.ansible.com/ansible/latest/playbook_guide/playbooks_checkmode.html)).

### Robusta and Keptn: trigger → remediation lists

Robusta's `alert_handling_job` runs a Kubernetes Job (`image`, `command`, `wait_for_completion`, `completion_timeout`) on `on_prometheus_alert: {alert_name: …}`, with "alert labels … added as environment variables" (`ALERT_LABEL_{NAME}`) ([job remediation](https://docs.robusta.dev/master/playbook-reference/automatic-remediation-examples/job-to-remediate-alert.html)). Its built-in remediation actions are `delete_pod`, `delete_alert_pod` (with an optional rate limit), `rollout_restart`, `restart_named_rollout`, `cordon`/`uncordon`/`drain`, `alert_on_hpa_reached_limit` (offers to raise the HPA max), and `kubectl_command` with `$namespace`/`$kind`/`$name` placeholders ([remediation actions](https://docs.robusta.dev/master/playbook-reference/actions/remediation.html)). As #4 found, these run on the trigger with no documented approval step.

Keptn v1 (EOL December 2023) mapped a problem type to an ordered list of actions per service ([remediation file](https://v1.keptn.sh/docs/1.0.x/reference/files/remediation/)):

```yaml
apiVersion: spec.keptn.sh/0.1.4
kind: Remediation
metadata: { name: serviceXYZ-remediation }
spec:
  remediations:
  - problemType: Response time degradation
    actionsOnOpen:
    - action: scaling
      name: Scaling ReplicaSet by 1
      value: "1"
```

The remediation sequence ran `get-action` → `action` → `evaluation` (`triggeredAfter: "10m"`). A failed evaluation re-triggered the sequence ([remediation sequence](https://v1.keptn.sh/docs/1.0.x/define/remediation-sequence/)). That is an **autonomous escalation chain**, which ADR 0001 invariant 9 rules out.

### Azure Automation

Runbooks are PowerShell, Python, or graphical. They run in an Azure sandbox or on a Hybrid Runbook Worker, which is the analogue of a Location-local Executor ([runbook types](https://learn.microsoft.com/en-us/azure/automation/automation-runbook-types)). "Each runbook … has a Draft version and a Published version. Only the Published version is available to be run, and only the Draft version can be edited." Testing executes the Draft, and "the runbook still executes normally and performs any actions against resources" ([manage runbooks](https://learn.microsoft.com/en-us/azure/automation/manage-runbooks)). The same page tells authors to check whether a resource exists before creating it, because "If a runbook fails, Azure Automation retries it". Idempotence is left to the author.

### Cross-tool anatomy table

| Concern | SSM Automation | Rundeck | StackStorm | Ansible | Robusta / Keptn |
|---|---|---|---|---|---|
| Parameter schema | typed, `allowedValues`, `allowedPattern`, resource types | options: enforced list, regex, remote list, secure | JSON-schema-like, `enum`, `immutable`, `secret` | vars, no schema | action params / `value` string |
| Target binding | `Targets` + `TargetParameterName`, or a parameter | node filter (may be `${option.x}`) | action param; rule Jinja from trigger | `hosts:` + `--limit` | alert subject; per-service file |
| Preconditions | `aws:assertAwsResourceProperty`, `aws:branch` | script steps | workflow tasks | `assert`, `when` | none |
| Failure policy | `onFailure`, `isCritical`, `maxAttempts` | stop / run remaining, error handler | `when: failed()`, `retry`, `noop` | `rescue`/`always`, `max_fail_percentage` | next action in list (Keptn) |
| Verification | `aws:waitForAwsResourceProperty`, alarm watch | script steps | tasks | tasks | Keptn `evaluation` after 10 min |
| Rollback | `onCancel`, `step:` jumps (manual compensation) | error handler | failure branch | `rescue` | none |
| Approval | `aws:approve` step (N of M, TTL) | none in job (PagerDuty: human clicks) | `core.ask` (schema, users/roles, TTL) | none | none |
| Dry run | none | none | none | `--check` / `--diff` (module-dependent) | none |
| Rate control | `MaxConcurrency`, `MaxErrors` | thread count, keepgoing | not reviewed | `serial`, `throttle` | Robusta `delete_alert_pod` rate limit |
| Versioning / pin | auto-versioned; run "Default", "Latest", or `DocumentVersion`; `files` sha256 | job UUID; Git SCM plugin | pack pinned to commit/tag | git | git / config repo |

Sources for the rows are the pages cited above. The StackStorm version pin is from [packs](https://docs.stackstorm.com/packs.html) (`st2 pack install cloudflare=776b9a4`; the docs "recommend pinning versions in production"). The SSM versions are from [running at scale](https://docs.aws.amazon.com/systems-manager/latest/userguide/running-automations-scale.html).

## 3. Generic vs bound: how a procedure meets its target

- **Fan-out selectors (SSM, Rundeck, Ansible).** The procedure takes one resource parameter, and the run supplies a *selector* that expands to many resources:

  ```sh
  aws ssm start-automation-execution --document-name AWS-RestartEC2Instance \
    --targets Key=tag:ENV,Values=DEV --target-parameter-name InstanceId \
    --max-concurrency 10 --max-errors 25%
  ```

  The selector belongs to the *run*, not the runbook. Whoever starts the run decides the blast radius. Rundeck's `${option.name}` in a node filter and Ansible's `--limit` behave the same way.
- **Resource-typed parameters (SSM).** `AWS::EC2::Instance::Id` checks format (a regex) and lets the console offer a list. It does not check ownership or relevance.
- **Incident-derived parameters (Incident Manager, Robusta, StackStorm rules, PagerDuty).** Incident Manager response plans take static values or dynamic ones: `Incident ARN` (String only) and `Involved resources` (StringList only). The latter is extracted from the CloudWatch alarm's metric dimensions or the EventBridge event's `Resources` ([Incident Manager runbooks](https://docs.aws.amazon.com/incident-manager/latest/userguide/runbooks.html)). This is the nearest thing to scope binding found, but it is a fill rule, not a check. A human starting the same document by hand can pass any instance.
- **Assignment of procedures to services (PagerDuty, Keptn).** PagerDuty attaches an action to services, and Keptn keeps one remediation file per service. That limits *which* runbooks are offered for an incident, which is the "assigned Runbooks" allowlist from #9. Neither limits *what* the runbook then touches.
- **Who fills parameters:** a human at run time (Rundeck, the SSM console, Runme `promptEnv`), the event (StackStorm, Robusta, Incident Manager dynamic), or static configuration (response plan static values, StackStorm `immutable` defaults). No surveyed tool lets a model fill them.

## 4. Multi-step semantics

- **Approval placement.** SSM and StackStorm approve *one point inside* a run. Nothing records what the approver saw, and steps after the approval act on whatever state exists then. PagerDuty and Rundeck approve *by starting the run*: the human clicks.
- **Partial failure.** All the workflow engines default to stop on first failure (SSM `Abort`, Rundeck "Stop at the failed step", Orquesta fail-fast). Each offers continue-anyway and jump-to-handler escape hatches. Compensation (rollback) is always **author-written steps**. No tool derives an inverse automatically.
- **Idempotence.** The SRE book describes "fix scripts" re-run every 15 minutes, which required idempotent fixes. It notes that "Not all fixes were naturally idempotent", that the automation should stop after repeated failure, and that it should be "careful about relying on implicit 'safety' signals". In the Diskerase incident, an empty target set "was used as a special value, interpreted to mean 'everything'", and the fix was "more sanity checks—including rate limiting" ([Automation at Google](https://sre.google/sre-book/automation-at-google/)). Azure leaves idempotence to the author.
- **Rate control** exists to stage a fan-out (SSM `MaxConcurrency`/`MaxErrors`, Ansible `serial`/`max_fail_percentage`). With one Target per Incident it reduces to a cooldown, which ADR 0001 already has.
- **Dry run.** Ansible check mode is the only procedure-wide simulation, and it silently skips unsupported modules. `kubectl rollout undo --dry-run=server` will "submit server-side request without persisting the resource" ([kubectl rollout undo](https://kubernetes.io/docs/reference/kubectl/generated/kubectl_rollout/kubectl_rollout_undo/)). That is per API call, which is what ADR 0001 D5 relies on.
- **Chains.** Keptn's "next action if evaluation failed" and SSM `aws:executeAutomation` both let one runbook start more remediation without a human.

## 5. Lifecycle

- **Where runbooks live.** Most live in a vendor store with versions (SSM documents, Azure Automation accounts, Rundeck projects). StackStorm uses git packs, and Ansible plain git. Rundeck and Azure add git sync on top of their store.
- **Pinning.** SSM can run "Default version at runtime", "Latest version at runtime", or an explicit version. Its `files` carry `sha256` checksums. StackStorm pins a pack to a commit or tag. Azure runs only the Published version. Nothing pins by a *content digest of the whole procedure plus its bound parameters*. ADR 0001 D5 does.
- **Review.** None of the tools has a review workflow of its own. Review happens in git, if anywhere. The SRE workbook's rule is operational: "On-call engineers should update the playbook with fresh information when the corresponding page fires", because "Details in playbooks go out of date at the same rate as production environment changes".

## 6. The remediation spread

| Remediation | Mechanism (primary source) | Object written | Server-side preview | Effect | Natural verification |
|---|---|---|---|---|---|
| Rollout undo | `kubectl rollout undo deployment/abc [--to-revision=N] [--dry-run=server]` ([docs](https://kubernetes.io/docs/reference/kubectl/generated/kubectl_rollout/kubectl_rollout_undo/)) | The Target | Yes | Sync write, async rollout | Rollout status, then the alert clears |
| Pod restart / rollout restart | Eviction (ADR 0001); `kubectl rollout restart deployment/nginx` ([docs](https://kubernetes.io/docs/reference/kubectl/generated/kubectl_rollout/kubectl_rollout_restart/)); Robusta `delete_pod`, `rollout_restart` | The Target or its pod | Yes | Same | Ready replicas |
| Scale | `deployments/scale` (ADR 0001); Keptn `scaling` `value: "1"` | The Target | Yes | Same | Replicas available |
| Traffic shift | Gateway API `HTTPRoute` `backendRefs[].weight`: "the weights can be updated to quickly shift traffic back to `foo-v1`" ([traffic splitting](https://gateway-api.sigs.k8s.io/guides/traffic-splitting/)); Argo Rollouts `setWeight`/abort ([spec](https://argo-rollouts.readthedocs.io/en/stable/features/specification/)) | An adjacent route or a Rollout | Yes (it is a K8s object) | Sync | Per-backend error rate |
| Feature-flag flip | LaunchDarkly semantic patch `{"environmentKey": …, "instructions": [{"kind": "turnFlagOff"}]}`, optional `comment` ([API](https://launchdarkly.com/docs/api/feature-flags/patch-feature-flag)). OpenFeature specifies evaluation only ([spec](https://openfeature.dev/specification/types)), so there is no vendor-neutral write API | An external SaaS object | No | Sync, global to the environment | Metric after flag propagation |
| CI-triggered revert | GitHub `workflow_dispatch` with typed `inputs` (`choice`, `boolean`, `environment`; `required`), run "via the GitHub API or GitHub CLI" ([events](https://docs.github.com/en/actions/reference/workflows-and-actions/events-that-trigger-workflows)); equally a Rundeck/SSM/Ansible job | Whatever the pipeline does | No (opaque) | Async, minutes | Pipeline status, then the Target's revision, then the alert |
| Open a PR with a fix | `POST /repos/{owner}/{repo}/pulls` with `head`, `base`, `title`, `body`, `draft` ([REST](https://docs.github.com/en/rest/pulls/pulls#create-a-pull-request)) | A repository branch | The diff *is* the preview | Nothing changes at runtime until a human merges | None at PR time; the merge goes through the repo's own review and CD |

## Implications for incident-copilot

### A candidate common shape (the Runbook envelope)

Every tool reviewed fits this envelope. Each field below has a prior-art anchor:

```yaml
runbook: rollback_deployment          # identity
version: <content digest>             # SSM files sha256 / StackStorm commit pin → D5 digest
platform: kubernetes                  # #9: platform half is per-platform
target:                               # NOT a selector: exactly the Incident's Target
  kinds: [Deployment]                 # SSM resource-typed params, narrowed
  bound_from: incident                # Incident Manager "Involved resources", made a guard
parameters:                           # filled by the LLM, bounded by schema
  to_revision: { type: integer, enum_from: precondition.previous_revision }
  # StackStorm `immutable` → operator-pinned values the LLM cannot change
preconditions:                        # SSM aws:assertAwsResourceProperty
  - previous_revision_exists
  - sub_objects_belong_to_target      # #9: pod/revision params must be owned by the Target
steps:                                # typed, closed set per platform; outputs feed inputs
  - action: k8s.rollout_undo
    inputs: { revision: "{{ to_revision }}" }
    on_failure: abort                 # default everywhere; only abort | step:<compensate>
verification:                         # SSM waitForAwsResourceProperty + alarm watch
  wait: { property: rollout.complete, timeout: 5m }
  watch: { signal: incident, abort_on: still_firing_after 10m }
rollback:                             # author-written compensation, like onCancel/rescue
  - action: report_only               # or an explicit inverse step; never derived
max_level: L2                         # D1/D8
```

What changes from ADR 0001 D1: steps become a list of typed actions from a closed per-platform set, not one Go function. Preconditions, verification, and rollback become declared fields, not code.

What stays the same: the closed action vocabulary, the downgrade-only `min()` with the Runbook's max level as one input, and the digest. The digest now covers *all* steps' inputs and *each* write step's dry-run result.

**Departures from the prior art that should stay:**

- **Target binding is a guard, not a selector.** Every tool lets the run widen scope (tags, `--limit`, `${option.x}` node filters). Don't copy that.
- **Approval is one envelope around the whole Plan, before the first write, bound to the digest.** It is not an `aws:approve` step in the middle. A mid-run approval approves state nobody has previewed.
- **No step that starts another Runbook** (Keptn's next action, `aws:executeAutomation`), per invariant 9.
- **`rejectUndeclaredOptions` is on by default.** Unknown fields reject the Plan, which ADR 0001 already does.

### Remediation families

The spread does not fit one *step* shape. It does fit one envelope if each family has its own step types and its own scope proof:

| Family | Members | Scope proof the Executor can check | Preview for digest | Suggested ceiling |
|---|---|---|---|---|
| **A. In-place actuation** | rollout undo, restart, scale, Argo Rollouts abort | The write object *is* the Target, or a sub-object owned by it (#9) | Server-side dry run | Up to L3 per Runbook |
| **B. Adjacent control** | traffic shift (HTTPRoute, mesh), feature-flag flip | A **declared relation** from Target to object: the route's backends include the Target's Service; the flag is listed against the Target by the operator. Needs an operator-maintained mapping | K8s dry run for routes; none for flag SaaS (record before/after state instead) | L2 by default; L3 only for K8s-native routes with dry run |
| **C. Delegated pipeline** | CI-triggered revert, Rundeck/SSM/Ansible/GitHub Actions job | Only the *inputs* can be checked (e.g. `service` input = Target). What the job touches is bounded by the job's own credentials, not by the Executor | None. The digest pins the job's identity and version, plus its inputs | **L2 cap** (the #16 "opaque playbook" option) |
| **D. Change proposal** | open a PR with a fix, a revert PR | The repo is mapped to the Target. The write is to a branch, not to runtime | The diff | Effectively L1 for runtime: the repo's own merge review is the human approval. Opening the PR itself can be automatic |

Family D arguably is not remediation in ADR 0001's sense. It is **Advice delivered as a diff**. Family C is where most existing operator runbooks (Ansible, Rundeck, Confluence "run job X") would enter. Family A is the only one where ADR 0001's full guarantees hold today.

### Open questions for the #16 grilling

1. **One envelope, four families?** Should every Runbook carry a declared `family`, which sets its scope proof, preview, and level ceiling? Or should B/C/D be separate concepts (e.g. "Runbook" for A/B, "Delegated job" for C, "Advice as PR" for D)?
2. **Family B mapping.** Where does the Target → route/flag relation live (Target assignment config, Runbook parameters checked by a precondition, or a platform resolver)? Is a flag flip, which is global to an environment, ever scoped enough for L3?
3. **Family C ceiling.** Is L2 the hard cap for opaque pipelines, or can an operator prove scope by giving the pipeline credentials limited to one Target? (That would be the AWS session-policy pattern from #4.)
4. **Mid-run human steps.** SSM `aws:pause` and Incident Manager's manual steps mix human and machine steps in one runbook. Should a Plan allow a human step (e.g. "confirm the migration is reversible") between writes? If so, does the digest cover the steps after it? Or must the Plan split into two Plans, each approved?
5. **Verification semantics.** Is "Signal stopped firing within N minutes" part of the Runbook (SSM alarm-watch style, with `onCancel` compensation), or only an outcome recorded for D8 promotion evidence? What happens on verification failure at L3: compensate automatically, or report and escalate?
6. **Rollback.** Required, optional, or forbidden as an automatic step? Every tool leaves it author-written. An automatic compensating write is itself a second remediation (invariant 9).
7. **Who may pin parameters?** Should operators be able to mark parameters `immutable` (StackStorm) per Target assignment, so the LLM fills only what is left? Example: `scale_up` with `max_replicas` fixed for `orders`.
8. **Importing existing runbooks.** Prose playbooks (Confluence) can only become Advice templates. Rundeck/SSM/Ansible jobs can only become family C. Is there a supported path from a family C import to typed family A/B steps, and is that the "Plan → Runbook promotion" flow?
9. **Dry run for imported Ansible.** Is `--check` acceptable as a preview, given that unsupported modules "report nothing and do nothing" and `check_mode: false` tasks write anyway? Proposed answer: no, it is not a digestable preview, which is one more reason family C sits at L2.
10. **Version pin source of truth.** Is the registry git (StackStorm/Ansible style: commit pin and review in PRs) or a store with publish semantics (Azure Draft/Published, SSM default version)? The digest needs one immutable content address either way.
