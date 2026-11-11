# Manual test: Local ticketing and Incident milestone logs

This test checks the demo's local ticketing adapter and the Control plane's milestone logs on the existing two-cluster demo. Without GitHub settings, the Control plane starts with the local adapter, says that it is for demo purposes only, and shows its tickets, their comments, and the RCA on a read-only page at `/tickets`. An Incident's steps appear in the Control plane log, once each and in order, with the Incident id. The Sensor logs `link.up` when it connects and again after a reconnect. Run it whenever `internal/localtickets`, the Control plane's logging, `internal/sensor`, or the ticketing choice in `cmd/incident-copilot` change.

It runs on clusters that already exist and never deletes them.

## Before you start

The clusters from [the two-cluster demo](two-cluster-demo.md), already created with `make cluster deploy`, and `jq`. Run every command from the repository root, in bash or zsh, in one shell. `OPENROUTER_API_KEY` in `.env.demo` turns on the investigation, which steps 3 and 5 follow. Without it, the log stops after `ticket.created`.

Pass a new `VERSION` on every deploy of uncommitted changes. Otherwise the image tag stays `<sha>-dirty` and the pod keeps running the older build.

Define the helpers:

```sh
icp() { kubectl --context kind-ic-control-plane -n incident-copilot "$@"; }
sen() { kubectl --context kind-ic-target -n incident-copilot "$@"; }
am()  { kubectl --context kind-ic-target -n monitoring exec alertmanager-kps-alertmanager-0 -c alertmanager -- "$@"; }
fire() { # ALERTNAME DEPLOYMENT [--end=...]
  am amtool alert add "$1" severity=warning namespace=demo deployment="$2" "${@:3}" \
    --annotation=description=MANUAL-TEST-ANNOTATION --alertmanager.url=http://localhost:9093
}
# The Control plane's milestone log: one line per step, with the Incident id
milestones() { icp logs deploy/control-plane -c control-plane | jq -c 'select(.incident != null) | del(.time, .level, .role, .version, .commit)'; }
# Fresh Control plane state, so the next alert opens a new Incident. This deletes every Incident,
# the audit log, and the local tickets.
reset_state() {
  icp scale deploy/control-plane --replicas=0 && icp wait --for=delete pod -l app.kubernetes.io/name=control-plane --timeout=60s
  icp delete pvc control-plane-state
}
```

An alert on a Target that already has an Incident that is not closed joins or reopens it and starts no investigation. Use a Target with no Incident, or run `reset_state` before the deploy in step 1.

## 1. The Control plane starts with the local adapter

Deploy with an `.env.demo` that sets no `IC_GITHUB_*` variables. Keep your own file aside and restore it in step 5:

```sh
mv .env.demo .env.demo.full
grep -vE '^IC_GITHUB_(REPO|TOKEN)=' .env.demo.full > .env.demo
V=local-$(date +%s)
make control-plane sensor VERSION=$V
icp logs deploy/control-plane -c control-plane | jq -c 'select(.adapter == "local" or .msg == "control plane listening") | {level, msg, ticketing}'
# expect a WARN "IC_GITHUB_REPO and IC_GITHUB_TOKEN unset: using the local ticketing adapter, for demo purposes
# only, with its tickets at /tickets; production uses GitHub Issues, and ServiceNow is planned (#68)"
# and "control plane listening" with ticketing "local"
```

## 2. The Sensor logs link.up, and again after a reconnect

The Sensor logs `link.up` when its first poll comes back. The Control plane holds a poll for up to 20 seconds, so the line can take that long:

```sh
sleep 25
sen logs deploy/sensor | jq -c '{msg, error}'
# expect "sensor dialing", then {"msg":"link.up"}
icp rollout restart deploy/control-plane && icp rollout status deploy/control-plane
sleep 40
sen logs deploy/sensor | jq -c '{msg, error}' | tail -4
# expect one or more "link.down" lines with an error while the Control plane was away, then "link.up"
```

## 3. One Incident's milestones, in order

```sh
fire LocalTicketCheck loadgen
sleep 90   # group_wait, then the investigation
milestones
```

Expect these lines, in this order, all with the same `incident`:

| `msg` | Fields |
|---|---|
| `signal.received` | `source` `alertmanager/ic-target-alertmanager`, `name` `LocalTicketCheck`, `target` `ic-target/demo/Deployment/loadgen` |
| `triage.decision` | `decision` `open` |
| `ticket.created` | `adapter` `local`, `ref`, `kind` `incident` |
| `investigation.started` | `model`, `budget` (`steps`, `tokens`, `time`) |
| `investigation.step` | `step`, then `llm` (the model that answered) or `tool` (`workload_state`, `events`, `submit_rca`), and `ok` |
| `investigation.finished` | `confidence`, `steps`, `duration`; or `investigation.exhausted` with the `budget` that ran out |
| `rca.posted` | `ref` |
| `remediation.started` | `model`; or `remediation.skipped` with its `reason`, such as no Executor linked |
| `remediation.step` | as `investigation.step`, for the [L1 Advice](l1-advice.md) loop, ending in `submit_advice` |
| `remediation.finished` | `submitted`, `steps` |
| `advice.posted` | `ref`, `level` (`L1`) |
| `ticket.ready` | `ref`, `url` `/tickets/<ref>`, once the RCA and the Advice are on the ticket |

Later steps of the [Incident lifecycle](incident-lifecycle.md) log the same way: `triage.decision` with `decision` `end`, `resolve`, `reopen` or `close` (and its `reason`), `ticket.commented` with that `kind`, and `ticket.closed` when incident-copilot closes the ticket. An investigation stopped by a close logs `investigation.cancelled` with its `reason` instead of `rca.posted`.

`investigation.step` and `remediation.step` repeat once per LLM step and once per tool call. Every other line appears once. Nothing in the log holds `MANUAL-TEST-ANNOTATION`, Evidence data, or the model's text:

```sh
icp logs deploy/control-plane -c control-plane | grep -c MANUAL-TEST-ANNOTATION   # expect 0
```

Alertmanager re-sends the alert every few minutes. A repeat notice logs nothing at info level, so the lines above stay once each.

## 4. The local ticket page

The page is served on the Control plane's HTTP port, next to the webhook:

```sh
icp port-forward svc/control-plane 18080:8080 >/dev/null 2>&1 &
sleep 2
curl -s localhost:18080/tickets | grep -oE 'Demo only|LocalTicketCheck on [^<]*'
# expect "Demo only" and "LocalTicketCheck on ic-target/demo/Deployment/loadgen", after the title's "[warning]"
curl -s localhost:18080/tickets/1 | grep -oE '<h2>Root-cause analysis</h2>|Confidence:</strong> [a-z]+'
# expect the RCA heading and its confidence
curl -s localhost:18080/tickets/1.md | head -3
# expect the ticket as stored, in markdown: "# [warning] LocalTicketCheck on ic-target/demo/Deployment/loadgen (#1)",
# then its body and each comment, the RCA last
curl -s -o /dev/null -w '%{http_code}\n' -XPOST localhost:18080/tickets/1   # expect 405: read-only
curl -s localhost:18080/tickets/1 | grep -c MANUAL-TEST-ANNOTATION         # expect 0
```

Open <http://localhost:18080/tickets> in a browser. The banner says the tickets are for demo purposes only, and that production uses GitHub Issues with ServiceNow planned. Ticket 1 shows its Target and Signals, then the RCA comment, rendered as a tracker would show them.

Resolve the alert:

```sh
fire LocalTicketCheck loadgen --end="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
sleep 20
milestones | tail -3
# expect signal.received, triage.decision with decision "resolve", and ticket.commented with kind "resolve"
curl -s localhost:18080/tickets/1 | grep -oE '<h1>[^<]*|Resolved\.'
# expect the title without "[warning]", and the resolve comment
kill %1   # the port-forward
```

The ticket stays open. incident-copilot closes it after 24 hours with no activity, logs `ticket.closed`, and the page then shows it closed; `TestAnIncidentsLifecycleIsLogged` covers that close.

## 5. With GitHub settings, the GitHub adapter is used as before

```sh
mv .env.demo.full .env.demo
reset_state   # loadgen's Incident from step 3 would otherwise be joined
make control-plane VERSION=github-$(date +%s)
icp logs deploy/control-plane -c control-plane | jq -c 'select(.msg == "control plane listening") | {ticketing, repo}'
# expect ticketing "github" and your IC_GITHUB_REPO; no local adapter warning
fire GitHubMilestoneCheck loadgen
sleep 90
milestones | jq -c 'select(.msg | test("ticket|rca"))'
# expect ticket.created with adapter "github" and an issue number as ref, rca.posted, advice.posted, and ticket.ready with
# url https://github.com/<IC_GITHUB_REPO>/issues/<ref>
curl -s -o /dev/null -w '%{http_code}\n' localhost:18080/tickets   # with a new port-forward: expect 404
fire GitHubMilestoneCheck loadgen --end="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
```

The issue holds the same RCA comment that step 4 showed on the local page.

## Clean up

Kill any port-forward that is left, and leave the clusters running.
