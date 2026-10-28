# Manual test: Ingest hardening and the shipped Alertmanager route

This test checks how the Control plane receives Signals from noisy, malformed, or hostile senders, and that the demo cluster's Alertmanager sends to it through the shipped route. Part 1 uses `curl` in place of Alertmanager, so it needs no cluster. It covers the status contract, ingest bounds, token rotation, an HA pair's duplicate notice, `truncatedAlerts`, and a `resolved` for an unknown key. Part 2 uses the kind cluster. Run it whenever the Alertmanager adapter, the intake path, the ingest bounds, or the shipped route changes.

## Before you start

- **Everything from [the GitHub Issues manual test](github-issues.md#before-you-start)**: a scratch repository for test tickets, a fine-grained token with **Issues: Read and write**, and `.env.demo` with `IC_GITHUB_REPO` and `IC_GITHUB_TOKEN`. Every run creates real issues there.
- **Go 1.25+, `curl`, `jq`, and the `gh` CLI.**
- **For Part 2 only:** Docker Desktop (macOS or Windows), plus the `make tools` prerequisites. In kind on Linux, `host.docker.internal` does not resolve, so Part 2 cannot reach the host.

## Part 1: Signal intake, without a cluster

### 1. Start the Control plane with two tokens

Run these commands from the repository root. `IC_ALERTMANAGER_TOKEN` holds two comma-separated tokens, as it does during a rotation.

```sh
RUN=$(mktemp -d)
go build -o "$RUN/incident-copilot" ./cmd/incident-copilot

start() { # TOKENS
  ( set -a; . ./.env.demo; set +a
    IC_LISTEN_ADDR=127.0.0.1:18090 \
    IC_STATE_PATH="$RUN/state.db" IC_AUDIT_PATH="$RUN/audit.jsonl" \
    IC_ALERTMANAGER_INSTANCE=ic-target-alertmanager \
    IC_ALERTMANAGER_TOKEN="$1" \
    IC_ALERTMANAGER_LOCATION=ic-target \
    exec "$RUN/incident-copilot" -role control-plane ) >> "$RUN/control-plane.log" 2>&1 &
  CP=$!
  sleep 1; tail -1 "$RUN/control-plane.log"   # expect "control plane listening"
}
start old-token,new-token
```

### 2. Build the payloads

`alert` prints one firing Alertmanager alert, and `batch` wraps the alerts on its input in a webhook payload. Each payload is written to a file, because the oversized ones are too long for a command-line argument. `post` sends a file and prints the HTTP status.

```sh
NOW=$(date -u +%Y-%m-%dT%H:%M:%SZ)
alert() { # NAME FINGERPRINT LABELS_JSON
  jq -nc --arg n "$1" --arg f "$2" --argjson l "$3" --arg now "$NOW" '{
    status: "firing", labels: ({alertname: $n, severity: "critical"} + $l),
    annotations: {description: "MANUAL-TEST-ANNOTATION"}, startsAt: $now,
    generatorURL: ("http://prometheus.monitoring:9090/graph?g0.expr=" + $n), fingerprint: $f}'
}
batch() { jq -sc '{version: "4", status: "firing", truncatedAlerts: 0, alerts: .}'; }
post() { # TOKEN FILE
  curl -s -o /dev/null -w '%{http_code}\n' -XPOST -H "Authorization: Bearer $1" \
    --data-binary @"$2" http://127.0.0.1:18090/webhook/alertmanager
}
fill() { head -c "$1" /dev/zero | tr '\0' x; } # BYTES

ORDERS='{"namespace":"demo","deployment":"orders","pod":"orders-7d9f8c6b5-x2k4p"}'
INVENTORY='{"namespace":"demo","deployment":"inventory"}'
LOADGEN='{"namespace":"demo","deployment":"loadgen"}'
fill $((1 << 20)) > "$RUN/1mib"; fill 5120 > "$RUN/5kib"; fill 102400 > "$RUN/100kib"

alert KubePodCrashLooping 0f1e2d3c4b5a6978 "$ORDERS" | batch > "$RUN/crash.json"
alert KubePodCrashLooping 0f1e2d3c4b5a6978 "$ORDERS" \
  | jq -c --rawfile pad "$RUN/1mib" '.annotations.description = $pad' | batch > "$RUN/oversized.json"
{ alert NoFingerprint "" "$ORDERS"; alert KubePodCrashLooping 9a8b7c6d5e4f3021 "$INVENTORY"; } | batch > "$RUN/mixed.json"
alert KubeDeploymentRolloutStuck 3c2b1a0f9e8d7c6b "$LOADGEN" \
  | jq -c --rawfile v "$RUN/5kib" --rawfile a "$RUN/100kib" '.labels.image = $v | .annotations.description = $a' \
  | batch > "$RUN/big-values.json"
jq -c '.truncatedAlerts = 2' "$RUN/crash.json" > "$RUN/truncated.json"
alert KubePodCrashLooping 7e6d5c4b3a291807 "$ORDERS" \
  | jq -c --arg now "$NOW" '.status = "resolved" | .endsAt = $now' | batch > "$RUN/resolved-unknown.json"
printf '{not json' > "$RUN/bad.json"
ls -l "$RUN"/*.json
```

### 3. Send them

```sh
post old-token "$RUN/crash.json"              # 1
post new-token "$RUN/crash.json"              # 2
post not-the-token "$RUN/crash.json"          # 3
post old-token "$RUN/bad.json"                # 4
post old-token "$RUN/oversized.json"          # 5
post old-token "$RUN/mixed.json"              # 6
post old-token "$RUN/big-values.json"         # 7
post old-token "$RUN/truncated.json"          # 8
post old-token "$RUN/resolved-unknown.json"   # 9
```

| # | Request | HTTP | Expected on GitHub |
|---|---|---|---|
| 1 | `KubePodCrashLooping` on `demo`/`orders`, with the old token | 200 | New issue `KubePodCrashLooping on ic-target/demo/Deployment/orders`. |
| 2 | The same payload with the new token: an HA pair's second member, or a sender already rotated | 200 | Nothing. Both tokens belong to one Signal source, so it is the same key and a repeat notice. |
| 3 | Wrong token | 401 | Nothing. |
| 4 | Unparseable body | 400 | Nothing. |
| 5 | Body over 1 MiB | 413 | Nothing. |
| 6 | Batch of two: an alert with no fingerprint, and `KubePodCrashLooping` on `inventory` | 200 | One new issue, for `ic-target/demo/Deployment/inventory`. The bad alert is dropped. |
| 7 | `KubeDeploymentRolloutStuck` on `loadgen` with a 5 KiB label value and a 100 KiB annotation | 200 | One new issue, for `ic-target/demo/Deployment/loadgen`. |
| 8 | Payload 1 again, with `truncatedAlerts: 2` | 200 | Nothing. It is a repeat notice. |
| 9 | A `resolved` alert whose key was never seen | 200 | Nothing. |

### 4. Rotate: remove the old token

Restart with only the new token. The state and the audit log carry over.

```sh
kill $CP; sleep 1
start new-token
post old-token "$RUN/crash.json"   # 10: expect 401
post new-token "$RUN/crash.json"   # 11: expect 200, a repeat notice
kill $CP
```

To check the other half of the rotation, run `start old-token` and send the same two requests. Now the new token gets `401` and the old one `200`. Run `kill $CP` afterwards.

### 5. Check the results

```sh
. ./.env.demo
gh issue list -R "$IC_GITHUB_REPO" --limit 3 --json number,title,comments \
  -q '.[] | "#\(.number) \(.title)  comments=\(.comments|length)"'
gh issue list -R "$IC_GITHUB_REPO" --limit 3 --json body,comments | grep -c MANUAL-TEST-ANNOTATION   # expect 0

A="$RUN/audit.jsonl"
jq -c '[.seq, .type, .incident, (.data.kind // .data.reason // .data.truncatedAlerts // "")]' "$A"
```

Expect three issues: `orders`, `inventory`, and `loadgen`, with no comments. The audit log should hold these entries in this order:

| Request | Entries |
|---|---|
| 1 | `signal.received`, `triage.decision` (`open`), `ticket.created` |
| 2 | `signal.received`, `triage.decision` (`repeat`) |
| 3 | `auth.rejected` |
| 4 | `request.rejected` (`invalid character …`) |
| 5 | `request.rejected` (`body over 1 MiB`) |
| 6 | `signal.dropped` (`alert has no fingerprint`), then `signal.received`, `triage.decision` (`open`), `ticket.created` |
| 7 | `signal.received`, `triage.decision` (`open`), `ticket.created` |
| 8 | `request.truncated` (`2`), `signal.received`, `triage.decision` (`repeat`) |
| 9 | `signal.received`, `triage.decision` (`drop`) |
| 10 | `auth.rejected` |
| 11 | `signal.received`, `triage.decision` (`repeat`) |

Then look closer at requests 7, 9, and 2:

```sh
# 7: the 5 KiB label is cut to 4096 bytes and raw to at most 64 KiB, and the Signal is marked truncated.
jq -c 'select(.type == "signal.received" and .data.name == "KubeDeploymentRolloutStuck")
  | .data | {truncated, image: (.labels.image | length), description: (.annotations.description | length), raw: (.raw | tojson | length)}' "$A"
# expect {"truncated":true,"image":4096,"description":4096,"raw":65536} (raw at most 65536)

# 9: dropped for its own reason.
jq -r 'select(.type == "triage.decision" and .data.kind == "drop") | .data.reason' "$A"
# expect: resolved Signal for an unknown key

# 2, 8, 11: one Incident and one member, whose last-seen moved on each repeat.
sqlite3 "$RUN/state.db" "SELECT incident_id, key, first_seen, last_seen FROM members WHERE key LIKE '%0f1e2d3c4b5a6978'"
# expect one row; skip this if you have no sqlite3
```

## Part 2: The shipped route in the demo cluster

### 1. Install the route

```sh
make cluster   # or `make observability` if the cluster already exists
```

It creates the `incident-copilot-webhook` Secret in `monitoring`, holding the token `demo-alertmanager-token`, and installs Alertmanager with the shipped route. Check that the route is loaded:

```sh
am() { kubectl --context kind-incident-copilot -n monitoring exec alertmanager-kps-alertmanager-0 -c alertmanager -- "$@"; }
am amtool config routes --alertmanager.url=http://localhost:9093
# expect: incident-copilot at the root, with a "null" child for Watchdog|InfoInhibitor
am amtool config show --alertmanager.url=http://localhost:9093 \
  | grep -E "'\.\.\.'|group_wait|group_interval|repeat_interval|send_resolved|max_alerts|credentials_file"
# expect '...', group_wait 10s, group_interval 1m, repeat_interval 5m, send_resolved true,
# max_alerts 0, and the incident-copilot-webhook token file. The URL shows as <secret>; step 3 proves it.
```

### 2. Start the Control plane on the host

The demo Alertmanager sends to `http://host.docker.internal:8080`, so the Control plane must listen on port 8080 on every interface.

```sh
RUN2=$(mktemp -d)
go build -o "$RUN2/incident-copilot" ./cmd/incident-copilot
( set -a; . ./.env.demo; set +a
  IC_STATE_PATH="$RUN2/state.db" IC_AUDIT_PATH="$RUN2/audit.jsonl" \
  IC_ALERTMANAGER_INSTANCE=kind-alertmanager \
  IC_ALERTMANAGER_TOKEN=demo-alertmanager-token \
  IC_ALERTMANAGER_LOCATION=kind-incident-copilot \
  exec "$RUN2/incident-copilot" -role control-plane ) > "$RUN2/control-plane.log" 2>&1 &
CP=$!
sleep 1; cat "$RUN2/control-plane.log"   # expect "control plane listening" on :8080
```

### 3. Fire an alert, then resolve it

```sh
am amtool alert add ManualRouteCheck severity=warning namespace=demo deployment=orders \
  --annotation=description=MANUAL-TEST-ANNOTATION --alertmanager.url=http://localhost:9093
sleep 20
jq -c '[.seq, .type, .incident, (.data.kind // .data.ref // "")]' "$RUN2/audit.jsonl"
```

Within about 10 seconds (`group_wait`), expect `signal.received`, `triage.decision` (`open`), and `ticket.created`. A new issue `ManualRouteCheck on kind-incident-copilot/demo/Deployment/orders` appears in the tickets repository. The demo's other alerts can arrive too, if any are firing. `Watchdog` never does.

Resolve it. With `send_resolved: true`, Alertmanager sends the resolved notice after `group_interval` (1 minute):

```sh
am amtool alert add ManualRouteCheck severity=warning namespace=demo deployment=orders \
  --end="$(date -u +%Y-%m-%dT%H:%M:%SZ)" --alertmanager.url=http://localhost:9093
sleep 75
jq -c 'select(.type == "triage.decision") | [.seq, .data.kind, (.data.reason // "")]' "$RUN2/audit.jsonl"
# expect a last entry: drop, "resolved Signals are not handled yet" (resolving Incidents is #45)
kill $CP
```

If nothing arrives, check `am wget -qO- http://localhost:9093/metrics | grep notifications_failed_total`. If the count grows, Alertmanager cannot reach the host or its token is wrong, and the Alertmanager log says which: `kubectl --context kind-incident-copilot -n monitoring logs alertmanager-kps-alertmanager-0 -c alertmanager | tail`.

## Clean up

Close the issues the run created, or keep them as a record. Then delete the temporary directories with `rm -rf "$RUN" "$RUN2"`. The `ManualRouteCheck` alert is already resolved.
