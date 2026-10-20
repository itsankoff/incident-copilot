# Prior art: how incident platforms define, import, and bind Runbooks

- **Ticket:** #30 (feeds grilling ticket #16; Wayfinder map #3)
- **Date:** 2026-10-20. All sources were read on this date. These products change monthly, so re-check a detail before quoting it in an ADR.
- **Question:** What does "runbook" mean to each AI incident responder and incident platform (prose the LLM reads, an executable workflow, or both)? Which existing runbooks can teams import, and what does import turn them into? How is a runbook bound to alerts, services, or targets, and who fills its parameters at run time? What are the multi-step, approval, autonomy, and versioning semantics? How does an LLM-proposed fix become a reusable runbook?
- **Builds on:** [`prior-art-ai-sre-agents.md`](https://github.com/itsankoff/incident-copilot/blob/research/prior-art-ai-sre-agents/docs/research/prior-art-ai-sre-agents.md) (#4), which covers action gating, and [`holmesgpt-evidence-tools.md`](https://github.com/itsankoff/incident-copilot/blob/research/holmesgpt-evidence-tools/docs/research/holmesgpt-evidence-tools.md). This note does not repeat their findings on write gating.

Sources are official docs and source repositories unless marked **(marketing)** or **(third-party)**. Vocabulary follows `CONTEXT.md`: Runbook, Advice, Plan, Target, Executor, Autonomy level, Signal.

## Summary

1. **"Runbook" has split into two different things, and the industry has renamed the prose half "Skill".** Prose guidance the LLM reads is now almost universally a `SKILL.md` file in the open [Agent Skills](https://agentskills.io) format: HolmesGPT (0.26 replaced runbooks with skills), AWS DevOps Agent (runbooks auto-migrated to skills), incident.io, Resolve.ai, Azure SRE Agent, PagerDuty SRE Agent, and Grafana Assistant. Executable runbooks are a separate product line with no LLM in the loop: SSM Automation documents, Rundeck jobs, Datadog Workflow Automation, Keep workflows, FireHydrant and Rootly workflows. **No product reviewed has one object that is both prose for the LLM and an executable, bounded procedure.**
2. **Import means "index for retrieval" or "sync a Git directory of SKILL.md files", never "convert to executable steps".** Confluence, Notion, GitHub, GitLab, ADO wikis, and uploaded Markdown or PDF are indexed as evidence (incident.io, Rootly, Azure, Traversal, Cleric, Komodor, Datadog). incident.io says it plainly: connected docs are "searched as evidence"; to have agents *follow* a runbook it must be rewritten as a skill, and "the work of building a plugin is mostly translation". No product imports Ansible playbooks, Rundeck jobs, or SSM documents and turns them into LLM-selectable typed actions. Executable engines import only their own formats (Rundeck YAML, SSM documents).
3. **Binding comes in three styles.** (a) A **link in the alert payload**: PagerDuty `custom_details.runbook_url.{confluence,github,servicenow}`, a Confluence link in a Datadog monitor message, and runbook links Cleric reads passively from alert definitions. (b) **Deterministic rules**: Rootly investigation rules, Azure response plans, FireHydrant runbook conditions, Keep CEL triggers, PagerDuty Automation Actions bound to services and teams, SSM response plans, and HolmesGPT's `alerts` scoping filter. (c) **LLM semantic match on the skill's `description`**, which is the default for every skill system. PagerDuty states that "Paige does not support running a skill when specific conditions are met".
4. **Parameters of executable runbooks are filled by rule from the incident, not by the LLM.** SSM Incident Manager has static parameters plus two dynamic ones (`Incident ARN`, `Involved resources`, typed `String` and `StringList`). Datadog fills monitor-triggered workflow inputs from template variables. Keep fills `{{ alert.* }}` context. Rundeck options carry `enforced` value lists and `regex`. Where an LLM proposes parameters (AWS directed actions, Datadog one-click Run, Resolve mitigation proposals), a human approves each one. k8sgpt-operator (in #4) asks the LLM for only a container name and an image.
5. **Approval and autonomy live on the binding or the action catalog, not on the runbook.** Azure sets Review or Autonomous **per response plan**. Rootly sets Manual, Auto-run, or Paused **per investigation rule**, but only for *investigating*. Datadog Guardrails use Ask or Deny, scoped by env, service, and resource tags. Resolve requires approval for every proposal, and its model is "completely separated from the execution engine". Multi-step approval exists only inside executable engines (SSM `aws:approve`, Datadog Slack approve/reject steps, FireHydrant step conditions).
6. **Nobody turns an approved, executed LLM fix into a parameterized executable runbook.** What exists instead:
   - PagerDuty generates a prose playbook, which a human copies into the knowledge base.
   - Memory systems store prose "context notes": Rootly (Proposed→Active, with auto-activation after two graded investigations), Azure (auto-indexed session insights, no review), Cleric, AWS (learned skills moved to memory), and Komodor.
   - Keep and PagerDuty (2023) have AI *authoring assistants* for workflows, with a human in the loop.
   - Rootly says it "suggests a workflow, a team member approves it, it becomes reusable", but only in **marketing**.
   - incident.io deliberately forbids the loop: "an investigation can't change a skill based on what it learns".
7. **Version pinning is weak almost everywhere.** incident.io is the exception. It pins each plugin sync to one commit so "a skill can't change under an investigation that's already following it", and with hand-picked selection "a merge to your default branch can't change how Investigations behave without someone choosing that". FireHydrant snapshots a runbook's steps when it attaches. SSM response plans take an optional `documentVersion`. Weaker cases:
   - HolmesGPT re-pulls the skill repo every 5 minutes.
   - Resolve fetches "the latest version … at activation time".
   - Rootly's AI-generated instructions go live "as soon as it's saved", with "no separate approval step".

## Per product

### HolmesGPT and Robusta

- **Meaning.** Since Holmes 0.26, runbooks are **skills**: "step-by-step troubleshooting guides Holmes follows". Holmes "matches relevant skills from its catalog, fetches them with the `fetch_skill` tool, and executes the steps — calling tools to gather data". Skills are prose for the LLM and cover diagnostics only. The recommended body sections are Goal, Workflow, Synthesize Findings, and Recommended Remediation Steps. ([skills.md](https://github.com/HolmesGPT/holmesgpt/blob/master/docs/reference/skills.md)) The legacy format was a `catalog.json` index of `{id, update_date, description, link}` pointing at Markdown files. The `description` was "used by LLM to match runbook to issues". ([custom_runbook_catalog](https://github.com/HolmesGPT/holmesgpt/tree/master/examples/custom_runbook_catalog)) Executable remediation lives elsewhere, in **Robusta playbooks**: deterministic trigger→action pairs that run automatically (see #4).
- **Import.** Holmes reads `SKILL.md` directories from GitHub or Bitbucket repos, from inline Helm values, or from a ConfigMap or Secret. The migration guide converts each `catalog.json` entry by hand into frontmatter plus the original Markdown body. Confluence and Slab pages are reachable as *toolsets*, which means retrieval, not instructions. ([skills.md](https://github.com/HolmesGPT/holmesgpt/blob/master/docs/reference/skills.md), [Confluence toolset](https://docs.robusta.dev/master/configuration/holmesgpt/toolsets/confluence.html))
- **Binding.** By default the LLM matches on `description`. Skills authored in the Robusta SaaS platform can carry an `alerts` list. This is a **deterministic filter**: with alert context, "it drops skills scoped to other alerts". In chat the filter is off, and the scoping is appended to the description instead. Tiers (global, custom, personal, builtin) resolve name collisions. ([skill_loader.py](https://github.com/HolmesGPT/holmesgpt/blob/master/holmes/plugins/skills/skill_loader.py))
- **Versioning.** Holmes clones each repo at startup and re-pulls it every `TOOLSET_STATUS_REFRESH_INTERVAL_SECONDS` (default 5 min). If a pull fails, it "keeps serving its last good checkout". Nothing is pinned per investigation. ([skills.md](https://github.com/HolmesGPT/holmesgpt/blob/master/docs/reference/skills.md))

### PagerDuty (SRE Agent, Runbook Automation, Automation Actions)

- **Meaning.** There are two separate things.
  - The **SRE Agent (Paige)** ingests "runbooks, SOPs, and diagnostics" as prose. It also has **skills** with *When to Use*, *Prerequisites*, *Execution Steps*, *Error Handling*, and *Success Criteria* sections, which "load progressively". ([SRE Agent](https://docs.pagerduty.com/ai-automation/advance/sre-agent), [connectors, tools and skills](https://docs.pagerduty.com/ai-automation/advance/connectors-tools-and-skills))
  - **Runbook Automation** (Rundeck) jobs are executable. Each job is a `sequence` of `exec`/`script`/`jobref`/plugin steps with `errorhandler`s, node filters, and typed-ish `options` (`values`, `enforced`, `regex`, `required`, `secure`). Jobs import and export as YAML through `rd jobs load/list -F yaml`, and a `uuid` identifies a job across instances. ([job YAML](https://docs.rundeck.com/docs/manual/document-format-reference/job-yaml-v12.html))
- **Import.** Paige reads Confluence, GitHub, and ServiceNow documents, and accepts file uploads. "Linked documents must already be committed to the main branch". ([connectors](https://docs.pagerduty.com/ai-automation/advance/connectors-tools-and-skills)) Executable jobs come only from Rundeck.
- **Binding.** For prose, the alert carries `custom_details.runbook_url.{confluence|github|servicenow}`, set by the monitor or by Event Orchestration rules. "Paige fetches a document if it determines the document is relevant". For skills, Paige "does not support running a skill when specific conditions are met". For execution, an **Automation Action** wraps a Rundeck job or a runner script. It is associated with **services** and optionally **teams**, and responders pick it from "Run Actions" on an incident, or Event Orchestration runs it automatically. ([PagerDuty Automation Actions](https://support.pagerduty.com/docs/rundeck-actions), [actions with RBA](https://docs.rundeck.com/docs/learning/howto/actions-with-rba.html)) The docs do not show how incident fields map to job options.
- **Execution.** The agent docs describe Paige as recommending remediation. They do not show Paige invoking Automation Actions. The marketing page says it "runs approved automations" **(marketing)**. ([SRE Agent product page](https://pagerduty.com/platform/ai-agents/sre))
- **Promotion.** Paige can "generate and save playbooks for recurring issues". The guidance is to "review it for accuracy and completeness, then copy the approved version to your knowledge base". The result is prose, and the copy is a manual step. A 2023 announcement described AI-generated Runbook Automation jobs built from a natural-language description, then edited in the low-code editor **(press, early access)**. ([BusinessWire](https://www.businesswire.com/news/home/20230831090295/en))

### incident.io

- **Meaning.** incident.io separates the two most explicitly of any product. **Documentation** (Confluence spaces, Notion pages, GitHub or GitLab paths) is "synced and indexed for both keyword and semantic search" and surfaces "as evidence". **Skills** in a **plugin** (a repo directory using the Agent Skills and Claude Code plugin formats) are "instructions an agent follows rather than knowledge it reads". ([Documentation](https://docs.incident.io/nexus/documentation), [Plugins](https://docs.incident.io/nexus/extensions/plugins))
- **Import.** Connecting existing runbooks does not make them instructions. "The work of building a plugin is mostly translation". Runbooks written for people ("ask the on-call DBA") stall an unattended agent, and "five runbooks about the same system often become one skill". A coding-agent plugin helps write and verify skills. ([Plugins § Adapting existing runbooks](https://docs.incident.io/nexus/extensions/plugins))
- **Binding.** The LLM matches the incident's alert, description, and fields against each skill's name and description. A skill that "owns" the incident is followed at triage, before the first hypothesis. Nothing else marks a skill for an alert.
- **Execution.** Investigations "don't take action on your behalf". The only write is a pull request a human merges. ([Trust and safety](https://docs.incident.io/investigations/trust-and-safety))
- **Versioning and promotion.** Each sync "pins the plugin to one commit". With hand-picked selection, a newly merged skill "stays off until someone enables it". The repo is read-only to incident.io: "an investigation can't change a skill based on what it learns". Usage is tracked per skill: whether it was followed and whether it helped, plus written improvement feedback.

### Rootly

- **Meaning.** Rootly has three separate objects.
  - **Runbooks** are task checklists that auto-attach to incidents by service. ([setup checklist](https://docs.rootly.com/incident-response-setup-checklist.md))
  - **Workflows** run deterministic trigger + condition + action automation (152 action types: Slack, Jira, paging, HTTP client, and others). ([workflows](https://docs.rootly.com/workflows/workflows.md), [actions reference](https://docs.rootly.com/workflows/actions-reference.md))
  - **AI SRE instructions** are Markdown guidance at the team level and per **investigation rule**. "Instructions guide the investigation but don't count as evidence" and "don't grant permissions". ([instructions](https://docs.rootly.com/ai/ai-sre/instructions.md))
- **Import.** A Notion connector lets investigations "search your docs, runbooks, and databases". "Generate with AI" drafts team instructions from the knowledge graph, rules, connectors, and "a small number of Notion pages that look like runbooks". The draft goes live immediately, with "no separate approval step". The last 5 changes are kept with diffs.
- **Binding.** An investigation rule matches alerts on title, description, urgency, source, status, group status, or custom fields, and attaches *alert-class instructions*. It has a run mode (**Manual**, **Auto-run**, or **Paused**; new rules start Manual), a cooldown, and a 90-day preview of matching alerts. "At most one investigation rule adds its instructions". ([investigation rules](https://docs.rootly.com/ai/ai-sre/investigation-rules.md))
- **Promotion.** **Atlas Memory** stores "context notes", which start **Proposed** and are recalled only when **Active**. A person activates them, or they auto-activate once "at least two separate investigations have each graded the note 70 or higher". Instructions "to acknowledge, ignore, restart, or roll back a system don't belong in Memory". ([memory](https://docs.rootly.com/ai/atlas/memory.md)) Rootly's blog says workflows can run Ansible or Terraform and that AI suggests workflows a human approves **(marketing; not in docs)**. ([blog](https://rootly.com/sre/rootly-automation-workflows-explained-boost-sre-reliability))

### FireHydrant

- **Meaning.** A runbook is "the automation engine": steps such as creating a Slack channel or Jira ticket. Steps "execute immediately and concurrently" unless step conditions order them. ([runbook basics](https://docs.firehydrant.com/docs/runbook-basics))
- **Binding.** Runbook-level conditions decide attachment (for example, SEV1 or a given service). They are re-evaluated continuously, so an escalation to SEV1 attaches the SEV1 runbook. Step-level conditions gate individual steps. ([conditions](https://docs.firehydrant.com/docs/runbook-conditions))
- **Versioning.** "An attached runbook uses a snapshot of its steps at attachment time", and re-runs use the original steps. The docs say nothing about an LLM selecting or filling runbooks, or about import.

### Datadog (Bits AI SRE, Bits Remediation, Workflow Automation)

- **Meaning.**
  - For **Bits AI SRE**, the runbook is prose: steps written into the monitor message, a linked Notebook, a linked Confluence page, and a standing `bits.md`. Bits "follows documented troubleshooting steps where possible" and "incorporates remediation guidance into its recommendations". ([knowledge sources](https://docs.datadoghq.com/bits_ai/bits_ai_sre/knowledge_sources))
  - **Workflow Automation** is the executable side, as a separate product.
  - **Bits Remediation (Preview)** recommends actions from a fixed **Action Catalog** ("scaling a deployment, restarting a pod, or patching a resource"). It offers copy-the-command or one-click **Run** through a Private Action Runner, and can verify afterwards. ([Bits Remediation](https://docs.datadoghq.com/bits_ai/bits_remediation/))
- **Binding.** A workflow is bound by mentioning `@workflow-name(param="…")` in a monitor's notification. Its inputs are filled from **monitor template variables**. Runs execute as the owner, the triggering user, or a service account. ([workflow triggers](https://docs.datadoghq.com/actions/workflows/trigger/)) **Guardrails** (Preview) scope actions by environment, service, and resource tags with **Ask** (named approvers) or **Deny**.
- **Promotion.** The docs do not mention saving a remediation as a workflow. Datadog's DASH 2026 roundup says guardrails can be expanded "as Bits learns from repeated approvals" **(marketing)**. ([DASH 2026](https://datadoghq.com/blog/dash-2026-new-feature-roundup-keynote))

### AWS DevOps Agent and SSM Incident Manager

- **DevOps Agent: meaning and import.** Runbooks were "automatically migrated to Skills". A skill is a `SKILL.md` directory, plus optional `references/` and `assets/`, that is **non-executable**. Zip uploads containing `scripts/` "will be rejected". Code runs only with the Sandbox preview. A skill is targeted at agent types (Incident Triage, Incident RCA, Incident Mitigation, …). It can be created in the UI, uploaded as a zip (≤ 6 MB), or **imported from a GitHub directory**. An imported skill's content is read-only in the console and refreshed by a manual **Sync**, which "replaces the skill content entirely". ([skills](https://docs.aws.amazon.com/devopsagent/latest/userguide/about-aws-devops-agent-devops-agent-skills.html))
- **DevOps Agent: binding and execution.** The agent "evaluates [the description] to decide whether the Skill is relevant", so binding is semantic. Triage skills can encode *skip* criteria. Mitigation runs through directed actions, which a human approves one at a time with a per-operation session policy (see #4). "Learned skills" are moving to Memory. ([learned skills](https://docs.aws.amazon.com/devopsagent/latest/userguide/about-aws-devops-agent-learned-skills.html))
- **SSM Incident Manager: meaning and binding.** Incident Manager is closed to new customers, but it remains the clearest example of an executable runbook bound to a response plan. A runbook is an **SSM Automation document**: steps of typed actions, with `Pause` for manual steps. A response plan names one document plus `documentVersion` (optional), `roleArn`, `targetAccount` (`RESPONSE_PLAN_OWNER_ACCOUNT | IMPACTED_ACCOUNT`), static `parameters`, and `dynamicParameters`. ([runbooks](https://docs.aws.amazon.com/incident-manager/latest/userguide/runbooks.html), [SsmAutomation API](https://docs.aws.amazon.com/incident-manager/latest/APIReference/API_SsmAutomation.html))
- **SSM Incident Manager: parameters.** Dynamic parameters are filled **by rule** from the incident:
  - `Incident ARN` must be a `String`.
  - `Involved resources` must be a `StringList`. It is taken from the CloudWatch alarm's metrics, an EventBridge event's `Resources`, or `relatedItems` on `StartIncident`.
  - A type mismatch fails the run.
- **SSM Incident Manager: credentials.** A *Runbook service role* starts the automation, and a separate Automation `AssumeRole` runs the steps, which AWS recommends "as a security best practice". In-document approvals use the `aws:approve` action. ([aws:approve](https://docs.aws.amazon.com/systems-manager/latest/userguide/automation-action-approve.html))

### Azure SRE Agent

- **Meaning.** "A **skill** teaches your agent *how* to do something (procedures, playbooks …). A **knowledge document** teaches your agent *what* it needs to know (… runbooks)". A skill is `SKILL.md` with `name`, `description`, and `tools` frontmatter, plus supporting files. Tools attached to a skill become "dynamically available when the skill is activated". ([create skill](https://learn.microsoft.com/en-us/azure/sre-agent/create-skill))
- **Import.** The knowledge base accepts uploads in Markdown, text, PDF, Word, PowerPoint, Excel, and images, up to 16 MB per file. Re-uploading under the same name overwrites the file. ADO wikis, GitHub, and MCP sources are connected rather than uploaded. ([memory and knowledge](https://learn.microsoft.com/en-us/azure/sre-agent/memory-system))
- **Binding and autonomy.** A **response plan** filters incidents by severity, title, impacted service, incident type, or priority. It routes them to a custom agent with an autonomy level of **Autonomous (default)** or **Review**, plus a reinvestigation cooldown. It previews matching past incidents before saving. ([response plan](https://learn.microsoft.com/en-us/azure/sre-agent/response-plan))
- **Promotion.** "Session insights" (symptoms, steps that worked, root cause, pitfalls) are indexed automatically 30 minutes after a thread goes quiet. The agent also writes `memories/synthesizedKnowledge/*.md` on its own. The docs describe no review gate. Same-resource history is ranked first.

### Komodor (Klaudia)

The help center was not fetchable (403), so this section comes from search snippets of official help articles. Klaudia's **Knowledge Base** accepts uploaded runbooks (Markdown, PDF), with Confluence and Slack syncs listed as "coming soon". **Klaudia.md** is a single organisation blueprint (under 10 KB) that "Komodor manually reviews" before activation. ([Knowledge Base](https://help.komodor.com/hc/en-us/articles/28686161842834-Klaudia-Knowledge-Base-Integration), [Klaudia.md](https://help.komodor.com/hc/en-us/articles/32463600112786-Klaudia-md-Organizational-Blueprint-Knowledge)) Execution uses whitelisted one-click actions and self-healing policies scoped per cluster, namespace, or service (see #4). Komodor says Klaudia turns runbooks "into executable investigation paths" **(marketing)**.

### Keep

**Workflows** are GitHub-Actions-style YAML with `triggers`, `steps`, `actions`, and conditions. Alert triggers filter with **CEL** (`source.contains("datadog") && severity == "critical"`), and context is templated from `{{ alert.* }}` and step results. ([triggers](https://github.com/keephq/keep/blob/main/docs/workflows/syntax/triggers.mdx)) The `permissions` field restricts only who may run a workflow *manually*. ([permissions](https://github.com/keephq/keep/blob/main/docs/workflows/syntax/permissions.mdx)) An **AI Workflow Builder** drafts workflows from natural language, "applying them only after the user's explicit consent". This is help with authoring, not promotion from incidents. ([AI workflow assistant](https://github.com/keephq/keep/blob/main/docs/overview/ai-workflow-assistant.mdx))

### Grafana IRM and Grafana Assistant

Grafana Assistant **Skills** are documents of "instructions, context, and specialized knowledge" (64 KB limit). They are personal or tenant-wide under RBAC, found by semantic search, and usable as slash commands. ([skills](https://grafana.com/docs/grafana-cloud/machine-learning/assistant/guides/skills/)) Assistant Investigations (public preview) can start from IRM alert groups and incidents through webhooks, and runbooks can be @-mentioned as context. ([investigations](https://grafana.com/docs/grafana-cloud/machine-learning/assistant/platform/investigation/)) We found no IRM construct for executable runbooks beyond webhooks.

### Shoreline

NVIDIA acquired Shoreline in June 2024 ([SiliconANGLE](https://siliconangle.com/2024/06/19/nvidia-reportedly-acquires-incident-automation-startup-shoreline-100m/)), and its public docs are no longer available. From launch-era material **(third-party/marketing)**:
- "Operations Notebooks" were interactive runbooks that mixed debug data with fleet-wide repair commands, written in the `Op` DSL.
- Alarm → action → **bot** automation ran without an LLM.
- Generative AI was used to draft runbooks. ([Shoreline blog](https://shoreline.io/blog/shoreline-operations-notebooks))

This was the closest historical attempt at "notebook prose and executable steps in one object".

### Cleric

Cleric has no runbook object. It reads docs through Atlassian and GitHub, takes plain-text "Global Guidance", and passively collects "runbook links in alert definitions". It builds "Memories" and "Topics" from investigations, and its "Automations" route alerts to workflows. It is read-only except for opening PRs and issues. ([providing context](https://docs.cleric.ai/setup/providing-context.md), [how Cleric learns](https://docs.cleric.ai/learning/how-cleric-learns.md), [FAQ](https://docs.cleric.ai/reference/faq.md))

### Resolve.ai

- **Meaning.** **Team Knowledge** (`Resolve.md`, runbooks, docs) handles "interpretive" knowledge. **Skills** (Agent Skills format, `SKILL.md` only, with bundled scripts "aren't executed … yet") package procedures, because "when procedures live as prose, the agent has to interpret them, and interpretation drifts". Skills can be imported from GitHub, written from templates, or written from scratch. Binding is by description match, or deterministically through a **slash command**. ([skills](https://docs.resolve.ai/skills.md))
- **Versioning.** "The latest version is fetched at activation time", so nothing is pinned.
- **Mitigation Actions.** The model proposes, and after a human approves, a separate execution engine acts with scoped write credentials. "The AI model that generates proposals is completely separated from the execution engine". The only action type today is alert silencing. Every action type defaults to disabled, a team override can sit above the org default, proposals expire, and there is an approval-rate dashboard. ([mitigation actions](https://docs.resolve.ai/mitigation-actions.md)) This is the closest match to our Executor split, but with no runbook object at all.

### Traversal

Traversal has a "Knowledge Bank" and integrations (Confluence, Notion) that "surface runbooks". Workers take custom instructions. Incident Workers "investigate, explain, and recommend" and do "not take destructive action". ([llms.txt index](https://docs.traversal.com/llms.txt), [incident workers](https://docs.traversal.com/using-traversal/incident-workers.md)) Traversal positions itself as not requiring "large libraries of runbooks" **(marketing)**. ([glossary](https://www.traversal.com/glossary/runbook))

## Comparison

| Product | Runbook = prose (LLM reads) | Runbook = executable | Import sources → result | Binding to alert/target | Parameter filling | Approval / autonomy unit | Version pin | LLM fix → reusable |
|---|---|---|---|---|---|---|---|---|
| HolmesGPT / Robusta | `SKILL.md` | Robusta playbooks (separate) | Git repos, Helm, ConfigMap → skills; Confluence/Slab → retrieval | LLM on description; Robusta `alerts` filter | LLM (tool args) | n/a (read-only; remediation MCP in #4) | No (5-min re-pull) | No |
| PagerDuty | Paige docs + skills | Rundeck jobs via Automation Actions | Confluence/GitHub/ServiceNow → fetched; Rundeck YAML → jobs | `runbook_url` in alert; actions ↔ services/teams | Rundeck options (enforced/regex); mapping undocumented | Responder clicks "Run"; Event Orchestration auto | Docs: main branch only | Prose playbook, copied by hand |
| incident.io | Plugin skills | None (PRs only) | Confluence/Notion/GitHub/GitLab → evidence index; repo plugin → skills | LLM on description at triage | n/a | n/a | **Commit per sync; opt-in for new skills** | Explicitly forbidden |
| Rootly | Instructions + per-rule instructions | Workflows (comms/ticketing) | Notion → retrieval; AI-drafted instructions | Investigation rules (fields, run mode, cooldown) | n/a | Manual / Auto-run / Paused per rule (investigation only) | 5-entry history; AI draft live immediately | Memory notes (Proposed→Active; auto after 2 graded runs) |
| FireHydrant | No | Runbooks (steps) | None documented | Runbook and step conditions (sev, service, fields) | Templated fields | Step conditions | **Snapshot at attach** | No |
| Datadog | Monitor message, Notebook, Confluence, `bits.md` | Workflow Automation; Action Catalog | Confluence → read at investigation | `@workflow(param=…)` in monitor | Monitor template variables | Guardrails Ask/Deny by env/service/resource tags | Not documented | No (marketing hints) |
| AWS DevOps Agent | `SKILL.md` (non-executable) | Directed actions (#4) | UI, zip, **GitHub dir import** → skills | LLM on description; agent-type targeting | LLM proposes, operator edits/approves | Per action, in chat | Manual Sync replaces content | Learned skills → Memory |
| SSM Incident Manager | No | SSM Automation doc | SSM docs only | Response plan (one doc) | **Static + dynamic from incident** (`Incident ARN`, `Involved resources`) | `aws:approve` step; plan auto-starts | `documentVersion` (optional) | No |
| Azure SRE Agent | Skills + knowledge docs | Agent tools (CLI/kubectl) | Uploads (MD, PDF, Office), ADO/GitHub/MCP → retrieval | Response plan filters → custom agent | LLM | **Review / Autonomous per response plan** | Overwrite by filename | Session insights auto-indexed, no review |
| Komodor | Knowledge Base, Klaudia.md | Whitelisted actions, self-healing policies | MD/PDF upload → retrieval | Policy per cluster/ns/service | Fixed by action | Policy per scope | Klaudia.md reviewed by Komodor | Memory (marketing) |
| Keep | No | YAML workflows | Keep YAML | CEL on alert | `{{ alert.* }}` context | Manual-run permissions only | Not documented | AI builder (authoring aid) |
| Grafana | Assistant skills | Webhooks | Skills authored in UI | Semantic search; IRM webhook triggers | LLM | Not documented | Not documented | No |
| Cleric | Guidance + memories | None (PRs) | Atlassian/GitHub → retrieval | Passive runbook links | n/a | n/a | n/a | Memories |
| Resolve.ai | Team Knowledge + skills | Mitigation Actions (silence only) | GitHub → skills | Description or slash command | LLM proposes, human approves | Per proposal; per action type per team | **No (latest at activation)** | No |
| Traversal | Knowledge Bank, instructions | None | Confluence/Notion → retrieval | n/a | n/a | n/a | n/a | Corpus grows (third-party) |
| Shoreline (gone) | Notebooks | Op actions, bots | n/a | Alarms → bots | Op | Bots auto | n/a | AI drafting (talk) |

## Implications for incident-copilot

### The prose vs. executable split

- **Adopt the split, and name the halves differently.** Every product that took this seriously keeps the two separate. Prose the LLM reads is a different trust class from steps the Executor runs.
  - In our terms, a **Runbook** is the executable half: typed steps, bounded parameters, an Autonomy level, assigned to Targets.
  - The prose half (operator guidance, "if alert X, check Y") is **evidence for the investigation**. It should reach the LLM like any other Evidence query result, redacted and cited, not as instructions. Rootly says it well: instructions "don't count as evidence" and "don't grant permissions".
  - This keeps invariant 1 intact: an LLM can only pick a registered Runbook and fill its parameters, whatever a wiki page says.
- **Do not let prose carry steps the Executor runs.** HolmesGPT, AWS, and Azure all have the LLM "execute the steps" of a Markdown skill with whatever tools are loaded. That is exactly the pattern-filtered free-form execution #4 told us to avoid. A Runbook's steps must be data the Executor understands, never prose it interprets. ("A skill *is* the rollback" is Resolve's framing, and Resolve still routes every write through a separate engine.)
- **One object that is both has no prior art.** Shoreline's notebooks came closest and are gone. #16 should not invent this. Consider instead a Runbook that *links to* a guidance document, shown to humans in the ticket and given to the LLM as evidence.

### Import

- **Nobody converts existing runbooks into executable steps automatically.** incident.io, the most thoughtful product here, calls it "mostly translation" and does it with a human and a coding agent.
- Two import paths fit our model:
  1. **Guidance import** (Confluence, Notion, Markdown in Git) becomes an Evidence source. It is indexed and retrieved, and grants nothing.
  2. **Executable import** (Ansible, Rundeck, SSM) produces at best an *opaque* Runbook. #16 already proposes capping these at L2 and bounding them by the Executor's own permissions. SSM's two-role split (a service role to start, a narrower `AssumeRole` to run steps) is a ready template for "bounded by the Executor's own permissions".
- **Translation into typed Runbooks is a human (or coding-agent) authoring task that ends in review.** It is not a runtime feature.

### Binding to Alerts and Targets

- **Binding should be deterministic configuration.** The LLM picks only among bound Runbooks. Every product that executes anything binds by rule: response plans, investigation rules, conditions, CEL, Automation Action ↔ service. Semantic matching on a `description` exists only for prose. HolmesGPT's `alerts` scoping is the right shape: a hard filter when alert context exists.
- This supports the #9 input to #16: an assignment allowlist per Target, where a Runbook not assigned to the Incident's Target is capped at L1, and no assignments means L1.
- **Steal the preview.** Azure response plans and Rootly rules both show "past incidents/alerts this would have matched" before saving. A dry-run of a binding against recent Signals is cheap and catches overly broad selectors.
- **No product binds to a Target the way we do.** Bindings everywhere are to an alert class, service, severity, or cluster. Even SSM's `Involved resources` is a list the runbook may act on, with no check that writes stay inside it. Scope binding (invariant 4) stays our differentiator.

### Parameter filling

- **Prefer rule-filled parameters from the Signal and Target, as SSM does.** SSM fills `Incident ARN` and `Involved resources` with type checks that fail closed. Leave the LLM only the parameters that genuinely need judgement, as k8sgpt-operator does with container and image.
- For an LLM-filled parameter, keep ADR 0001's typed bounds. Copy Rundeck's `enforced` value lists and `regex` as the minimum vocabulary for a declarative parameter schema.
- AWS directed actions let the operator **edit parameters before approving**. That fits our "human may edit Advice before it becomes a Plan", and the digest must cover the edited values.

### Multi-step, approval, and versioning

- **Approval and autonomy belong on the binding as well as on the Runbook.** Azure (per response plan) and Rootly (per rule) both place them on the binding. This matches our `min()`: Runbook max level, Environment ceiling, and potentially a per-binding level. #16 should decide whether a binding can carry its own cap (downgrade-only).
- **Multi-step approval exists only in executable engines.** SSM has `aws:approve`, FireHydrant has step conditions, and Datadog has Slack approval steps. None of them squares steps with "no autonomous chains", so #16 decides that without prior art. The SSM pattern (one approval for the document run, with `Pause` steps for manual work) is the simplest precedent for "one Plan, one approval".
- **Pin what runs.**
  - incident.io pins a commit per sync, and new skills are opt-in.
  - FireHydrant snapshots at attachment.
  - HolmesGPT (5-minute re-pull) and Resolve ("latest at activation") show what to avoid.
  - Our digest-bound approval (D5) needs the Runbook *version* in the digest, and the Executor must fetch by digest, not by name.

### Promotion (Plan → Runbook)

- **No product promotes an approved, executed fix into an executable Runbook.** The nearest patterns:
  - PagerDuty: generated prose playbook, human copies it into the knowledge base.
  - Rootly Memory: Proposed→Active, with a human or a graded-evidence rule activating it.
  - incident.io: refuses the loop entirely.
- This supports #13's direction: a Plan that worked becomes a **draft** Runbook that a human reviews, capped at L2, with L3 a separate decision.
- Rootly's auto-activation (two independent investigations graded ≥ 70) is a concrete shape for our eval-evidence placeholder (D8). Note that Rootly applies it only to prose hints, and explicitly excludes "restart, or roll back" instructions from Memory.
- Azure's unreviewed auto-indexing of "steps that worked" is the anti-pattern. It feeds prose fixes back into the LLM with no human in between.

### Open questions for the #16 grilling

1. Is imported operator guidance (Confluence, "if alert X, do Y") an **Evidence source** only, or can it also be linked from a Runbook and shown in the ticket? Does it ever influence which Runbook is chosen beyond the deterministic binding?
2. Binding format: per Target, a selector (labels, namespace, kind), or an Alert name like HolmesGPT's `alerts`? Can a binding carry a downgrade-only autonomy cap, as Azure and Rootly do?
3. Which parameters are rule-filled from the Signal and Target (SSM-style dynamic parameters) and which are LLM-filled? Is the parameter schema vocabulary Rundeck-like (`enforced` values, `regex`, `required`) plus typed ranges?
4. Registry and pinning: a Git repo synced to one commit (incident.io) with opt-in enablement of new Runbooks? How does the Executor fetch by digest?
5. Opaque imported playbooks (Ansible, Rundeck, SSM): allowed at all? If so, is the cap L2, and is SSM's two-role split the credential pattern?
6. Multi-step: one approval per Plan with `Pause`-style manual steps (SSM), or per-step gates? How does a mid-Plan failure escalate under invariant 9?
7. Promotion: what evidence moves a promoted draft Runbook from L1/L2 upward? Is Rootly's "N independent graded uses" rule a usable stand-in for D8's eval thresholds?
8. Should Runbook usage be tracked per Runbook (picked, followed, helped), as incident.io does for skills, as an input to D8?
