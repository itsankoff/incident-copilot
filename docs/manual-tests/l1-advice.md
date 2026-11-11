# Manual test: L1 Advice in the ticket

This test checks the remediation path of #56 on the two long-lived demo clusters. After the RCA, the Control plane drafts Advice from the Actions that `ic-target`'s Executor advertises. Gate 1 checks the Advice, Decide gives it an Autonomy level, and both go into the ticket. Nothing runs: every Advice is at L1, with the steps to carry out by hand and the reason for each cap. The test also checks that a pod-only Alert gets L1 Advice with the reason, that the kill switch caps the next Incident with no restart, that an unlinked Executor means escalate, and that every step is audited.

Run it whenever `internal/action`, `internal/executor`, `internal/link`, `internal/controlplane` (`advice.go`, `decide.go`, `killswitch.go`, `investigate.go`), `internal/kubernetes`, `deploy/executor`, or `deploy/control-plane` change.

Two scenarios are covered by seam tests only and are never run live, because a real model cannot be made to show them on demand:

- a model that names another workload as the target, or states a level, gains nothing;
- Advice that fails Gate 1 is not offered for approval.

Advice also follows the Incident lifecycle as the RCA does. If a human closes the ticket after the RCA is posted, the Advice is not posted and `remediation.cancelled` says why. That was added when this branch was rebased onto the lifecycle (#45), after the live run, so it too is covered by seam tests only. The lifecycle manual test checks the RCA side.

See `internal/controlplane/advice_test.go`. Every other step below was run live on the demo clusters, with real GitHub ticketing and a real model, before the PR for #56. The no-mistakes pipeline's own validation did not run this end-to-end path: it does not use those credentials and stays off the shared clusters.

## Before you start

- The clusters `ic-target` and `ic-control-plane` exist and run the demo, Sensor, and Control plane (`make status`). This test does not create or delete them. Never run `make down` on clusters other people use.
- `.env.demo` sets `IC_GITHUB_REPO`, `IC_GITHUB_TOKEN`, and `OPENROUTER_API_KEY`, so tickets are real GitHub issues and the RCA and remediation loops use a real model.
- `jq` and `gh` are installed. Run every command from the repository root, in bash, in one shell.

Each new Incident opens a real issue in the tickets repository. An Incident keeps its Target until it closes, 24 hours after it resolves or when a human closes its ticket, so a later Alert on that Target only adds comments. Steps 3 to 6 need four Targets with no Incident: `orders`, `inventory`, `loadgen`, and a pod-only key. On clusters that have been used before, reset the Control plane's state first (step 1). That deletes its Incidents and audit log.

## 1. Deploy, and reset the Control plane's state if needed

```sh
V="$(git rev-parse --short HEAD)-$(date +%s)"   # a fresh tag, so a rebuilt image always rolls out
icp() { kubectl --context kind-ic-control-plane -n incident-copilot "$@"; }
sen() { kubectl --context kind-ic-target -n incident-copilot "$@"; }

# Only if Incidents already hold the demo Targets: start the Control plane with no state.
icp scale deploy/control-plane --replicas=0
icp wait --for=delete pod -l app.kubernetes.io/name=control-plane --timeout=60s
icp delete pvc control-plane-state

make deploy control-plane sensor executor VERSION=$V
make status   # expect sensor and executor Running in ic-target's incident-copilot namespace
```

Define the helpers:

```sh
am() { kubectl --context kind-ic-target -n monitoring exec alertmanager-kps-alertmanager-0 -c alertmanager -- "$@"; }
audit() {
  icp debug "$(icp get pod -l app.kubernetes.io/name=control-plane -o name)" -q -i \
    --image=busybox:1.37 --target=control-plane --profile=general \
    -- cat /proc/1/root/var/lib/incident-copilot/audit.jsonl 2>/dev/null
}
fire() { # ALERTNAME LABEL=VALUE... (each Alert sets namespace=demo)
  am amtool alert add "$1" severity=warning namespace=demo "${@:2}" \
    --annotation=description=MANUAL-TEST-ANNOTATION --alertmanager.url=http://localhost:9093
}
icp port-forward deploy/control-plane 8081:8081 >/dev/null 2>&1 &
R=$(grep '^IC_GITHUB_REPO=' .env.demo | cut -d= -f2- | tr -d '"')
advice() { # INCIDENT: the Advice comment on that Incident's ticket
  ref=$(audit | jq -r --argjson i "$1" 'select(.type == "ticket.created" and .incident == $i) | .data.ref')
  gh issue view "$ref" -R "$R" --json comments --jq '.comments[] | select(.body | startswith("## Advice")) | .body'
}
```

## 2. The Executor dials out and advertises `rollback_deployment`

```sh
sen logs deploy/executor | head -1 | jq -r .msg       # expect "executor dialing"
audit | jq -c 'select(.type | startswith("executor.")) | [.seq, .type, .data]'
# expect: executor.online {"location":"ic-target","actions":["rollback_deployment"]}
kubectl --context kind-ic-target -n incident-copilot get pod -l app.kubernetes.io/name=executor \
  -o jsonpath='{.items[0].spec.automountServiceAccountToken}{"\n"}'
# expect false: the Executor runs nothing yet, so it holds no Kubernetes credentials
```

## 3. A real fault gets an RCA and L1 Advice

```sh
make fault F=crash-loop
# PodCrashLooping fires within about 3 minutes and opens Incident 1 on orders. The RCA and the
# Advice follow within a few minutes.
sleep 240
audit | jq -c 'select(.incident == 1) | [.seq, .type, (.data.reason // .data.kind // .data.passed // .data.level // "")]'
# expect, in order: triage.decision open; ticket.created; investigation.started; llm.* and
# evidence.query; gate1.rca; investigation.finished; ticket.commented rca; remediation.started;
# llm.* and evidence.query; remediation.finished; gate1.advice; advice.decided L1; ticket.commented advice
advice 1
```

Expect an `## Advice` comment that opens with `**Autonomy level:** L1 (suggest: carry out the steps below by hand; nothing runs on its own)` and lists every cap. The caps that set the level say `capped at`; the others say `allows up to`:

- `allows up to L2: Environment demo has a ceiling of L2`
- the RCA's confidence (`allows up to L2: the RCA's confidence is medium`, or `capped at L1` for `low`)
- `capped at L1: approval is not built yet (#58)`

The model decides what follows. For the crash loop it usually proposes `rollback_deployment` on `ic-target/demo/Deployment/orders` with the previous `revision`, citing claims of the RCA, with the revision citing the `workload_state` read it came from. The step then ends with `By hand, in Location ic-target, once these hold: previous_revision_exists, revision_is_one_back` and the command `kubectl -n demo rollout undo deployment/orders`, which goes exactly one revision back. If the model escalates instead, the comment says `**Escalate:**` with its reason. If its Advice fails Gate 1, the comment says `**Not offered for approval:**`, marks each failure with `⚠ fails Gate 1`, and shows no step to carry out by hand.

```sh
make clear
```

## 4. The kill switch caps the next Incident, with no restart

```sh
curl -s localhost:8081/debug/kill-switch                                  # expect {"level":""}
curl -s -o /dev/null -w '%{http_code}\n' -X PUT localhost:8081/debug/kill-switch -d '{"level":"L1"}'   # expect 204
curl -s -o /dev/null -w '%{http_code}\n' -X PUT localhost:8081/debug/kill-switch -d '{"level":"L3"}'   # expect 400
fire ManualAdviceCheck deployment=inventory
sleep 150
advice 2 | grep -E 'capped at|allows up to'
# expect, among the caps: capped at L1: the kill switch is set to L1. For a healthy inventory the
# model usually escalates: "**Escalate:**" with its own reason.
curl -s -X PUT localhost:8081/debug/kill-switch -d '{"level":""}'          # clear it
icp get pod -l app.kubernetes.io/name=control-plane \
  -o jsonpath='{.items[0].status.containerStatuses[0].restartCount}{"\n"}'  # expect 0: no restart
```

## 5. A pod-only Alert gets L1 Advice with the reason

```sh
fire ManualPodOnly pod=orders-7d9f8c6b5-x2k4p
sleep 30
advice 3
# expect: **Autonomy level:** L1, "capped at L1: the Incident has no Target", "capped at L1: there is
# no RCA", and "**Escalate:** the Incident has no Target, so no Action can be aimed at it". No LLM call:
audit | jq -c 'select(.incident == 3) | .type'   # investigation.skipped, remediation.skipped, no llm.request
```

## 6. With no Executor linked, the Advice escalates

```sh
sen scale deploy/executor --replicas=0
sleep 35
audit | jq -c 'select(.type == "executor.offline")'   # expect location ic-target
fire ManualNoExecutor deployment=loadgen
sleep 90
advice 4 | grep Escalate
# expect: **Escalate:** the Executor of Location ic-target is not linked, so no Actions are offered
sen scale deploy/executor --replicas=1
```

## 7. Everything is audited, and Alert text stays out

```sh
audit | jq -r '.type' | sort | uniq -c | grep -E 'executor\.|remediation\.|gate1\.advice|advice\.decided|killswitch\.set'
# expect executor.online, executor.offline, remediation.started, remediation.finished,
# remediation.skipped (Incidents 3 and 4), gate1.advice and advice.decided (one per Incident),
# and killswitch.set (twice)
audit | jq -c 'select(.type == "advice.decided") | [.incident, .data.level, [.data.caps[].reason]]'
audit | grep -c MANUAL-TEST-ANNOTATION   # expect only the signal.received entries: never an LLM request
for i in 1 2 3 4; do advice $i; done | grep -c MANUAL-TEST-ANNOTATION   # expect 0
```

## Clean up

```sh
end="--end=$(date -u +%Y-%m-%dT%H:%M:%SZ)"   # an Alert ends only when its labels match
fire ManualAdviceCheck deployment=inventory "$end"
fire ManualPodOnly pod=orders-7d9f8c6b5-x2k4p "$end"
fire ManualNoExecutor deployment=loadgen "$end"
make clear
kill %1   # the port-forward
```

To go back to a build without the Executor role, delete it first, or it crash-loops: `sen delete deployment/executor` and its `incident-copilot-executor-*` Secret.

The issues the test opened close 24 hours after their Alerts resolve. To close one sooner, close it in the tickets repository: the Control plane closes its Incident at the next Sweep.
