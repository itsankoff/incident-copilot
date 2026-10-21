# Manual test: Alertmanager Alerts to GitHub Issues

This test checks the Control plane's Signal path against a real GitHub repository. `curl` stands in for Alertmanager, so you need no cluster. It covers Signal authentication, Target resolution, Triage (open, join, repeat), one issue per Incident, and the audit chain. Run it whenever the Alertmanager adapter, Triage, the ticket format, or the GitHub Issues adapter changes.

## Before you start

- **A repository for test tickets.** It must have Issues enabled. Every run creates real issues there, so use a scratch repository, never this one.
- **A fine-grained GitHub token** for that repository, with **Issues: Read and write**.
- **`.env.demo` at the repository root** (it is gitignored). Copy it from [`.env.demo.example`](../../.env.demo.example) and set `IC_GITHUB_REPO` (`owner/name`) and `IC_GITHUB_TOKEN`. This test reads no other variables.
- **Go 1.25+, `curl`, and the `gh` CLI**, logged in with access to that repository. `gh` is only used to look at the results.

## 1. Start the Control plane

Run these commands from the repository root:

```sh
RUN=$(mktemp -d)
go build -o "$RUN/incident-copilot" ./cmd/incident-copilot

( set -a; . ./.env.demo; set +a
  IC_LISTEN_ADDR=127.0.0.1:18090 \
  IC_STATE_PATH="$RUN/state.db" IC_AUDIT_PATH="$RUN/audit.jsonl" \
  IC_ALERTMANAGER_INSTANCE=ic-target-alertmanager \
  IC_ALERTMANAGER_TOKEN=manual-test-token \
  IC_ALERTMANAGER_LOCATION=ic-target \
  exec "$RUN/incident-copilot" -role control-plane ) > "$RUN/control-plane.log" 2>&1 &
CP=$!
sleep 1; cat "$RUN/control-plane.log"   # expect "control plane listening"
```

The state and the audit log go into a fresh temporary directory, so every run starts with no Incidents.

## 2. Send Alerts

`alert` builds an Alertmanager webhook payload with one firing alert. The annotation holds a marker string, which must never reach GitHub. `post` sends a payload and prints the HTTP status.

```sh
NOW=$(date -u +%Y-%m-%dT%H:%M:%SZ)
alert() { # NAME FINGERPRINT SEVERITY EXTRA_LABELS_JSON
  printf '{"version":"4","status":"firing","alerts":[{"status":"firing","labels":{"alertname":"%s","severity":"%s"%s},"annotations":{"description":"MANUAL-TEST-ANNOTATION"},"startsAt":"%s","generatorURL":"http://prometheus.monitoring:9090/graph?g0.expr=%s","fingerprint":"%s"}]}' \
    "$1" "$3" "$4" "$NOW" "$1" "$2"
}
post() { # TOKEN PAYLOAD
  curl -s -o /dev/null -w '%{http_code}\n' -XPOST -H "Authorization: Bearer $1" \
    http://127.0.0.1:18090/webhook/alertmanager -d "$2"
}
ORDERS=',"namespace":"demo","deployment":"orders","pod":"orders-7d9f8c6b5-x2k4p"'

post manual-test-token "$(alert KubePodCrashLooping 0f1e2d3c4b5a6978 critical "$ORDERS")"                                   # 1
post manual-test-token "$(alert KubeDeploymentReplicasMismatch 1a2b3c4d5e6f7081 warning ',"namespace":"demo","deployment":"orders"')"   # 2
post manual-test-token "$(alert KubePodCrashLooping 0f1e2d3c4b5a6978 critical "$ORDERS")"                                   # 3
post manual-test-token "$(alert KubePodCrashLooping 9a8b7c6d5e4f3021 critical ',"namespace":"demo","deployment":"inventory"')"          # 4
post manual-test-token "$(alert KubePodNotReady 5c4d3e2f1a0b9c8d warning ',"namespace":"demo","pod":"loadgen-6b8f9d7c5-q7w2e"')"         # 5
post not-the-token     "$(alert KubePodCrashLooping 0f1e2d3c4b5a6978 critical "$ORDERS")"                                   # 6

kill $CP
```

## 3. Check the results

```sh
. ./.env.demo
gh issue list -R "$IC_GITHUB_REPO" --limit 3 --json number,title,comments \
  -q '.[] | "#\(.number) \(.title)  comments=\(.comments|length)"'
gh issue list -R "$IC_GITHUB_REPO" --limit 3 --json body,comments | grep -c MANUAL-TEST-ANNOTATION   # expect 0
cut -c1-150 "$RUN/audit.jsonl"
```

| # | Alert | HTTP | Expected on GitHub |
|---|---|---|---|
| 1 | `KubePodCrashLooping`, `demo`/`orders`, with a `pod` label | 200 | New issue `KubePodCrashLooping on ic-target/demo/Deployment/orders`. The body lists the Signal's name, severity, start time, and source link. |
| 2 | A different Alert for the same `namespace` and `deployment` | 200 | One comment on that issue, `Signal joined: …`. No new issue. |
| 3 | Alert 1 again: same fingerprint and `startsAt` | 200 | Nothing. A repeat notice only updates last-seen. |
| 4 | `KubePodCrashLooping`, `demo`/`inventory` | 200 | A second issue, for `ic-target/demo/Deployment/inventory`. |
| 5 | `KubePodNotReady` with only a `pod` label | 200 | A third issue, `KubePodNotReady (ic-target-alertmanager/5c4d3e2f1a0b9c8d)`. Its body says Target: none. No Deployment is guessed from the pod name. |
| 6 | Wrong bearer token | 401 | Nothing. |

No issue body or comment may contain `MANUAL-TEST-ANNOTATION`.

The audit log should have 15 entries, all with `incident` set except the last:

| Alert | Entries |
|---|---|
| 1 | `signal.received`, `triage.decision` (`open`), `ticket.created` |
| 2 | `signal.received`, `triage.decision` (`join`), `ticket.commented` |
| 3 | `signal.received`, `triage.decision` (`repeat`) |
| 4 | `signal.received`, `triage.decision` (`open`), `ticket.created` |
| 5 | `signal.received`, `triage.decision` (`open`), `ticket.created` |
| 6 | `auth.rejected` (incident `0`), which does not record the token that was sent |

## 4. Check the audit chain

The Control plane refuses to start on an audit log whose chain is broken. To use that as a check, start it on a copy of the log with a dummy GitHub token. It makes no GitHub call before an Alert arrives.

```sh
verify() {
  IC_LISTEN_ADDR=127.0.0.1:18091 IC_STATE_PATH="$RUN/verify.db" IC_AUDIT_PATH="$1" \
  IC_ALERTMANAGER_INSTANCE=x IC_ALERTMANAGER_TOKEN=x IC_ALERTMANAGER_LOCATION=x \
  IC_GITHUB_REPO=o/r IC_GITHUB_TOKEN=dummy \
  "$RUN/incident-copilot" -role control-plane > "$RUN/verify.log" 2>&1 & P=$!
  sleep 1; kill $P 2>/dev/null || true; grep -o '"msg":"[^"]*"' "$RUN/verify.log"
}
cp "$RUN/audit.jsonl" "$RUN/intact.jsonl";                      verify "$RUN/intact.jsonl"   # control plane listening
sed '7d' "$RUN/audit.jsonl" > "$RUN/removed.jsonl";             verify "$RUN/removed.jsonl"  # fatal: … entry 8: does not follow entry 6
sed 's/"kind":"open"/"kind":"join"/' "$RUN/audit.jsonl" > "$RUN/edited.jsonl"; verify "$RUN/edited.jsonl"   # fatal: … entry 2: hash mismatch
```

## Clean up

Close the issues the run created, or keep them as a record. Then delete the temporary directory with `rm -rf "$RUN"`.
