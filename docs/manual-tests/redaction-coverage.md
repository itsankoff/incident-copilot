# Manual test: Redaction coverage from the vendored rules

This test checks that the provider rules vendored from betterleaks (`internal/redact/rules_gen.go`) and our three structural patterns redact secrets live, on the path an Evidence query takes: the Sensor redacts what it reads and the Control plane redacts it again (ADR 0003 D8). It plants fake provider secrets that PR #87's patterns missed: a Slack bot token in a pod env var whose name holds no sensitive word, a Stripe live key in a container's command line, and an Event with an `Authorization: Basic` header, a redis URL with an empty user, and an OpenAI key followed by a full stop. It then reads them back through the Control plane's debug listener. Run it whenever `internal/redact` changes, and after every `make redact-rules` bump.

It runs on the existing long-lived clusters and needs no `make down`. It needs no Alert, LLM, or ticket, so it opens no Incident.

## Before you start

- **Both clusters up** with the demo, the Control plane, and the Sensor deployed (`make cluster deploy control-plane sensor` the first time). Deploy the build under test to both, with a unique tag, for example `make control-plane sensor VERSION=$(git rev-parse --short HEAD)-$(date +%s)`, so an uncommitted change is not skipped as `unchanged`.
- **`jq` and `curl`.**

Run every command from the repository root, in bash or zsh, in one shell.

## 1. The unit seams

```sh
go test ./internal/redact -run 'TestString|TestJSON|TestCorpus' -v | grep -E '^(--- FAIL|ok|FAIL)|caught'
# expect "caught 22 of 24 secrets" and ok; the two misses are the out-of-scope opaque token and bare password
make redact-rules
git status --short internal/redact
# expect no output: the committed table is what the pinned betterleaks version generates
```

## 2. Plant the fake secrets

The secrets are assembled from parts, so no scanner mistakes this document for a leak:

```sh
icp() { kubectl --context kind-ic-control-plane -n incident-copilot "$@"; }
RUN=$(mktemp -d); NAME=redaction-coverage-$(date +%s)
SLACK="xoxb-""123456789012-1234567890123-AbCdEfGhIjKlMnOpQrStUvWx"
STRIPE="sk_""live_51Hx$(printf 'Ab3dEf9h%.0s' $(seq 11))Zq7Wm2K"
BASIC="dXNlcjpodW50""ZXIyMDI2IQ=="
REDIS="Rk9pX2s3""cretPa55"
OPENAI="sk-proj-$(printf 'Ab3_dE9-%.0s' $(seq 7))Ab""T3BlbkFJ$(printf 'Zx7_yW6-%.0s' $(seq 7))Zx"
```

Create a Deployment whose command line holds the Stripe key and whose env holds the Slack token under a name our key-name rule does not know. The `pause` image is already on the kind nodes and ignores its arguments:

```sh
kubectl --context kind-ic-target -n demo create deployment "$NAME" --image=registry.k8s.io/pause:3.10 -- /pause --channel=#alerts --stripe="$STRIPE"
kubectl --context kind-ic-target -n demo set env deployment/"$NAME" SLACK_NOTIFY="$SLACK" LOG_LEVEL=debug
kubectl --context kind-ic-target -n demo rollout status deployment/"$NAME"
NOW=$(date -u +%Y-%m-%dT%H:%M:%SZ)
kubectl --context kind-ic-target -n demo create -f - <<EOF
apiVersion: v1
kind: Event
metadata: {name: $NAME, namespace: demo}
involvedObject: {apiVersion: apps/v1, kind: Deployment, name: $NAME, namespace: demo}
type: Warning
reason: UpstreamAuthFailed
message: "pull failed, Authorization: Basic $BASIC; cache redis://:$REDIS@redis.demo:6379/0 refused; openai: 401 Incorrect API key provided: $OPENAI."
firstTimestamp: "$NOW"
lastTimestamp: "$NOW"
count: 1
source: {component: manual-test}
EOF
```

## 3. The Evidence query returns them redacted

The Control plane's debug listener has no authentication and binds to loopback inside the pod, so only a port-forward reaches it:

```sh
icp port-forward deploy/control-plane 8081:8081 >/dev/null 2>&1 &
sleep 2
T='{"platform":"kubernetes","location":"ic-target","namespace":"demo","kind":"Deployment","name":"'"$NAME"'"}'
curl -s -XPOST localhost:8081/debug/evidence -d '{"tool":"workload_state","target":'"$T"'}' > "$RUN/state.json"
jq -c '.data.spec.template.spec.containers[0] | {command, env}' "$RUN/state.json"
# expect: {"command":["/pause","--channel=#alerts","--stripe=[REDACTED]"],"env":[{"name":"SLACK_NOTIFY","value":"[REDACTED]"},{"name":"LOG_LEVEL","value":"debug"}]}
curl -s -XPOST localhost:8081/debug/evidence -d '{"tool":"events","target":'"$T"'}' > "$RUN/events.json"
jq -r '.data[] | select(.reason == "UpstreamAuthFailed") | .message' "$RUN/events.json"
# expect: pull failed, Authorization: Basic [REDACTED]; cache redis://:[REDACTED]@redis.demo:6379/0 refused; openai: 401 Incorrect API key provided: [REDACTED].
for s in "$SLACK" "$STRIPE" "$BASIC" "$REDIS" "$OPENAI"; do cat "$RUN"/*.json | grep -c -- "$s"; done
# expect 0 on every line
```

## 4. Nothing else is redacted

The demo's own workloads hold no secret, so the rules should leave them as they are:

```sh
O='{"platform":"kubernetes","location":"ic-target","namespace":"demo","kind":"Deployment","name":"orders"}'
curl -s -XPOST localhost:8081/debug/evidence -d '{"tool":"workload_state","target":'"$O"'}' | grep -o '.\{60\}\[REDACTED\].\{20\}'
curl -s -XPOST localhost:8081/debug/evidence -d '{"tool":"events","target":'"$O"'}' | grep -o '.\{60\}\[REDACTED\].\{20\}'
# expect no output; anything printed is a false positive to report, with the line around it
kill %1   # the port-forward
```

## Clean up

```sh
kubectl --context kind-ic-target -n demo delete event "$NAME"
kubectl --context kind-ic-target -n demo delete deployment "$NAME"
rm -rf "$RUN"
```

## Last run

Not yet run live on the demo clusters. Steps 1 and the expected outputs of step 3 were checked against the redactor in unit form; the deploy and the Evidence query path were not.
