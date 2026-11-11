# Manual test: The Incident lifecycle

This test checks an Incident's whole lifecycle (ADR 0002 D4) on the long-lived two-cluster demo, with real GitHub tickets. It covers a resolve with outcome *resolved*, the ticket's severity following the firing Signals, a reopen on the same ticket, an expiry with outcome *expired*, a ticket closed by a human that stops a running investigation, a recurrence linked to the closed Incident, and an Incident with no Target. Run it whenever Triage, the Control plane's ticket sync or Sweep, or the GitHub Issues adapter changes.

It runs on the clusters you already have. It never runs `make down` or recreates a cluster. The 24-hour idle close cannot be seen in one sitting: step 7 says how to check it the next day.

## Before you start

- **The two-cluster demo is up**, as in [the two-cluster demo manual test](two-cluster-demo.md), with the Sensor deployed (`make sensor`).
- **`.env.demo` at the repository root**, with `IC_GITHUB_REPO` pointing at a scratch repository for test tickets, `IC_GITHUB_TOKEN` with **Issues: Read and write**, and `OPENROUTER_API_KEY`, so new Incidents are investigated. Every step opens or comments on real issues there.
- **`jq` and the `gh` CLI**, logged in with access to the tickets repository. `gh` reads the results and, in step 5, closes an issue as the human would.
- **Nobody else is using the clusters.** Faults, alerts, and deploys from someone else land in the same Control plane.

Run every command from the repository root, in one shell (bash or zsh), because later steps reuse the helpers defined in step 1.

## 1. Deploy, start from empty state, and define the helpers

Triage keeps one Incident per Target, and the Control plane's state survives redeploys, so start from empty state. This deletes the Control plane's Incidents and audit log:

```sh
make deploy
kubectl --context kind-ic-control-plane -n incident-copilot scale deploy/control-plane --replicas=0
kubectl --context kind-ic-control-plane -n incident-copilot wait --for=delete pod -l app.kubernetes.io/name=control-plane --timeout=90s
kubectl --context kind-ic-control-plane -n incident-copilot delete pvc control-plane-state
make control-plane VERSION="$(git rev-parse --short HEAD)-$(date +%s)"   # a unique tag, so a dirty tree still rolls
```

> **Upgrading without the reset.** On state from a build without the lifecycle, the first Sweep (within a minute of the rollout) closes every Incident whose ticket is already closed. It expires the others with an "expired" comment on each ticket, and closes those tickets 24 hours later.

The helpers follow [the two-cluster demo](two-cluster-demo.md#2-deploy-the-demo-app-and-the-control-plane-without-envdemo). `fire` adds an alert that lasts an hour, and `endalert` resolves it. `since` lists the audit entries after a time as `[seq, time, type, incident, kind, ref or key, reason or outcome or severity]`:

```sh
am() { kubectl --context kind-ic-target -n monitoring exec alertmanager-kps-alertmanager-0 -c alertmanager -- "$@"; }
icp() { kubectl --context kind-ic-control-plane -n incident-copilot "$@"; }
audit() {
  icp debug "$(icp get pod -l app.kubernetes.io/name=control-plane -o name)" -q -i \
    --image=busybox:1.37 --target=control-plane --profile=general \
    -- cat /proc/1/root/var/lib/incident-copilot/audit.jsonl 2>/dev/null
}
since() { audit | jq -c --arg t "$1" 'select(.time > $t and (.type | test("^(llm|evidence)") | not)) | [.seq, .time[11:19], .type, .incident, (.data.kind // .data.name // ""), (.data.ref // .data.key // ""), (.data.reason // .data.outcome // .data.severity // "")]'; }
alert() { # NAME SEVERITY END LABELS...
  am amtool alert add "$1" severity="$2" "${@:4}" --end="$3" \
    --annotation=description=MANUAL-TEST-ANNOTATION --alertmanager.url=http://localhost:9093
}
inhour() { date -u -d '+1 hour' +%Y-%m-%dT%H:%M:%SZ 2>/dev/null || date -u -v+1H +%Y-%m-%dT%H:%M:%SZ; }
fire()     { alert "$1" "$3" "$(inhour)" namespace=demo deployment="$2"; }                 # NAME DEPLOYMENT SEVERITY
endalert() { alert "$1" "$3" "$(date -u +%Y-%m-%dT%H:%M:%SZ)" namespace=demo deployment="$2"; }
issue() { gh issue list -R "$R" --state all --search "$1 in:title" --json number -q '.[0].number'; }
R=$(grep '^IC_GITHUB_REPO=' .env.demo | cut -d= -f2- | tr -d '"')

icp logs deploy/control-plane -c control-plane | jq -c 'select(.msg == "control plane listening") | {version, model}'
# expect your VERSION
audit | jq -c '[.seq, .type]'
# expect only [1,"location.online"]: the state is empty
```

## 2. Resolve, and the ticket's severity

A warning opens an Incident on `inventory`, and a critical Signal joins it. Each is resolved in turn:

```sh
T=$(date -u +%FT%TZ)
fire LcWarn inventory warning; sleep 25
fire LcCrit inventory critical; sleep 25
endalert LcCrit inventory critical; sleep 80
endalert LcWarn inventory warning; sleep 80
since "$T"
gh issue view "$(issue LcWarn)" -R "$R" --comments
```

| When | Audit | Ticket |
|---|---|---|
| `LcWarn` fires | `triage.decision` `open`, `ticket.created`, then the investigation and a `ticket.commented` `rca` | New issue `[warning] LcWarn on ic-target/demo/Deployment/inventory` |
| `LcCrit` fires | `join`, `ticket.commented`, `ticket.retitled` `critical` | A `Signal joined` comment. The title starts `[critical]` |
| `LcCrit` ends | `end`, `ticket.commented`, `ticket.retitled` `warning` | `Signal resolved: LcCrit …`. The title is back to `[warning]` |
| `LcWarn` ends | `resolve` with outcome `resolved`, `ticket.commented`, `ticket.retitled` | `Signal resolved: LcWarn …` and **Resolved.** … outcome: **resolved**. The title loses its severity, and **the issue stays open** |

`MANUAL-TEST-ANNOTATION` must not appear in the issue: `gh issue view "$(issue LcWarn)" -R "$R" --json body,comments | grep -c MANUAL-TEST-ANNOTATION` prints `0`.

## 3. Reopen on the same ticket

```sh
T=$(date -u +%FT%TZ)
fire LcWarn inventory warning; sleep 25
since "$T"
# expect: reopen on the same incident, ticket.commented, ticket.retitled warning; no ticket.created
```

The same issue gets **Reopened.** Signal fired: `LcWarn` …, and its title starts `[warning]` again. No new issue appears.

## 4. Expiry

A silence stops Alertmanager's notices for `LcWarn` without a resolved notice, as a dead Signal source would. The Signal expires 10 minutes after its last firing notice, at the next Sweep, which runs once a minute:

```sh
SIL=$(am amtool silence add alertname=LcWarn --duration=40m --comment="lifecycle expiry check" --alertmanager.url=http://localhost:9093)
T=$(date -u +%FT%TZ); sleep 690
since "$T"
# expect: resolve with outcome expired, ticket.commented, ticket.retitled
```

The issue gets `Signal expired, with no firing notice for 10 minutes: …` and **Resolved.** … outcome: **expired**, and it stays open.

## 5. A human closes the ticket while the investigation runs

Fire on `loadgen`, and close its issue the moment it appears. The investigation takes about 20 to 40 seconds:

```sh
T=$(date -u +%FT%TZ)
fire LcClose loadgen warning
until N=$(issue LcClose) && [ -n "$N" ]; do sleep 1; done
gh issue close "$N" -R "$R" --reason "not planned"
sleep 75
since "$T"
gh issue view "$N" -R "$R" --comments
```

Expect, on that Incident, one of these two, depending on which comes first:

- **The Sweep comes first.** `triage.decision` `close`, with reason `the ticket was closed by a human`, then `llm.error` and `investigation.cancelled`, with reason `incident N closed: the ticket was closed by a human (ticket N, not_planned)`.
- **The investigation finishes first.** `investigation.finished`, then `investigation.cancelled`, with reason `ticket N was closed by a human (not_planned) before the RCA was posted`, then the Sweep's `close`.

Either way, no RCA comment is on the closed issue.

## 6. Recurrence, and an Incident with no Target

```sh
T=$(date -u +%FT%TZ)
fire LcAgain loadgen warning; sleep 25
alert LcPodOnly warning "$(inhour)" namespace=demo pod=orders-7d9f8c6b5-x2k4p; sleep 25
alert LcPodOnly warning "$(date -u +%Y-%m-%dT%H:%M:%SZ)" namespace=demo pod=orders-7d9f8c6b5-x2k4p; sleep 80
since "$T"
gh issue view "$(issue LcAgain)" -R "$R" --json title,body -q '.title + "\n" + .body' | head -3
gh issue view "$(issue LcPodOnly)" -R "$R" --comments
```

- `LcAgain` opens a new Incident (`open`, `ticket.created`). Its issue body starts with `**Recurrence of:** Incident N, ticket #M`, naming the Incident and issue from step 5. `audit | jq -c 'select(.type == "triage.decision" and .data.recurrenceOf)'` shows it.
- `LcPodOnly` opens an Incident keyed by its Signal, with the title `[warning] LcPodOnly (ic-target-alertmanager/<fingerprint>)`, and `investigation.skipped` because it has no Target. Its resolve gives **Resolved.** … outcome: **resolved**.

`LcClose` from step 5 is still firing. Its next repeat notice, within 5 minutes, joins the open `loadgen` Incident: a ticket closed while its alert still fires gets a new Incident on the alert's next notice.

## 7. The 24-hour idle close (the next day)

A resolved Incident with no activity for 24 hours is closed by incident-copilot. Leave the clusters running, and the next day:

```sh
gh issue view "$(issue LcWarn)" -R "$R" --json state,comments -q '.state + ": " + .comments[-1].body'
# expect: CLOSED: **Closing:** no activity for 24 hours since the Incident resolved. …
audit | jq -c 'select(.type == "ticket.closed") | [.seq, .incident, .data.ref, .data.reason]'
```

Unit tests cover the same rule with a fake clock (`TestIdleResolvedIncidentIsClosedWithAComment`).

## 8. Check the audit chain

The Control plane refuses to start on an audit log whose chain is broken. Start a local one on a copy:

```sh
RUN=$(mktemp -d); audit > "$RUN/audit.jsonl"
go build -o "$RUN/incident-copilot" ./cmd/incident-copilot
IC_LISTEN_ADDR=127.0.0.1:18391 IC_DEBUG_ADDR=127.0.0.1:18392 IC_STATE_PATH="$RUN/verify.db" IC_AUDIT_PATH="$RUN/audit.jsonl" \
  IC_ALERTMANAGER_INSTANCE=x IC_ALERTMANAGER_TOKEN=x IC_ALERTMANAGER_LOCATION=x \
  "$RUN/incident-copilot" -role control-plane > "$RUN/verify.log" 2>&1 & P=$!
sleep 2; kill $P; grep -o '"msg":"control plane listening"' "$RUN/verify.log"   # expect a match
```

## Clean up

```sh
for a in "LcClose loadgen" "LcAgain loadgen" "LcWarn inventory"; do set -- $a; endalert "$1" "$2" warning; done
am amtool silence expire "$SIL" --alertmanager.url=http://localhost:9093
make clear
rm -rf "$RUN"
```

Close the issues the run created, or keep them as a record.

## Last run

2026-11-10, 23:52 to 00:08 UTC, on the long-lived demo clusters, after the state reset in step 1. Build `78f1976` (the lifecycle and its review fixes, before this document), with real GitHub tickets and OpenRouter. The pipeline that validated the PR drove none of these scenarios live. They come from this manual session only.

| Step | Result |
|---|---|
| 2 | Pass. The `LcWarn` issue opened as `[warning]`, was retitled `[critical]` when `LcCrit` joined, and back to `[warning]` when `LcCrit` resolved. When `LcWarn` resolved, the Incident resolved with outcome resolved, the title lost its severity, and the issue stayed open. No annotation leaked. |
| 3 | Pass. The same Incident and issue reopened, with a `Reopened` comment and `[warning]` again. No new issue. |
| 4 | Pass. The silenced `LcWarn` expired at the Sweep 10 m 25 s after its last firing notice, with outcome expired. |
| 5 | Pass, both orders. Closed while the LLM was answering: the Sweep closed the Incident, the LLM call was cancelled, and `investigation.cancelled` said `incident 4 closed: the ticket was closed by a human (ticket 57, completed)`. Closed just before the investigation finished: `investigation.cancelled` said `… before the RCA was posted`. Neither issue got an RCA. |
| 6 | Pass. The recurrence issue's body started `**Recurrence of:** Incident 2, ticket #55`. The pod-only Incident was keyed by its Signal, skipped its investigation, and resolved. |
| 7 | Not run. Unit tests only. |
| 8 | Pass. The Control plane started on the copied audit log. |

A ticket closed while its alert still fired got a recurrence on the alert's next repeat notice, within 5 minutes. The issue asks for exactly this ("A Signal after close opens a new Incident"), and it is noted on the PR as a product question.
