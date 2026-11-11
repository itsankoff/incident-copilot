# Manual test: The CrashLoopBackOff Detector

This test checks that the Sensor in `ic-target` spots a crash loop on its own, from the Kubernetes API alone, and that the Control plane takes its Detector Signal in. An `orders` rollout that crash-loops produces one Detector Signal for Deployment `orders`, however many pods crash, and it lands in the same Incident and ticket as Prometheus's `PodCrashLooping` alert. It also checks that the Sensor re-sends a firing Signal every 5 minutes, that it sends a resolution 10 quiet minutes after the fault is cleared and that the resolution resolves the Incident, that a pod with no owner is its own Target, that a namespace outside the Sensor's allowlist is not watched, and that a Detector Signal naming another Location is rejected. Run it whenever `internal/sensor`, `internal/kubernetes`, `internal/link`, the Control plane's Signal intake, or `deploy/sensor` change.

It runs on the existing, long-lived clusters. It never deletes them.

## Before you start

- **Both clusters are up**, from `make cluster` (see [the two-cluster demo](two-cluster-demo.md#before-you-start) for the prerequisites), with `jq` installed.
- **`.env.demo` at the repository root**, as in [the GitHub Issues manual test](github-issues.md#before-you-start). Without it, `make control-plane` switches the Control plane to the fake ticketing adapter. The steps below open real issues in the tickets repository, and new Incidents are investigated by the LLM.
- **Triage keeps one Incident per Target until its ticket is closed** ([the Incident lifecycle](incident-lifecycle.md)). If an earlier run left an open or resolved Incident on `demo/Deployment/orders`, this run's Signals join or reopen it instead of opening a new one. That still shows the merge. To see a fresh Incident, reset the Control plane's state in step 1. The reset deletes every Incident and the whole audit log.

Run every command from the repository root, in bash, in one shell.

## 1. Deploy

```sh
make deploy control-plane sensor
make status   # expect sensor and control-plane Running
```

Optionally, reset the Control plane's state so that `orders` has no Incident yet:

```sh
kubectl --context kind-ic-control-plane -n incident-copilot scale deploy/control-plane --replicas=0
kubectl --context kind-ic-control-plane -n incident-copilot wait --for=delete pod -l app.kubernetes.io/name=control-plane --timeout=90s
kubectl --context kind-ic-control-plane -n incident-copilot delete pvc control-plane-state
make control-plane
```

Define the helpers:

```sh
sen() { kubectl --context kind-ic-target -n incident-copilot "$@"; }
icp() { kubectl --context kind-ic-control-plane -n incident-copilot "$@"; }
demo() { kubectl --context kind-ic-target -n demo "$@"; }
audit() {
  icp debug "$(icp get pod -l app.kubernetes.io/name=control-plane -o name)" -q -i \
    --image=busybox:1.37 --target=control-plane --profile=general \
    -- cat /proc/1/root/var/lib/incident-copilot/audit.jsonl 2>/dev/null
}
# Signals since $1, what Triage did with them, and the tickets and investigations they caused.
signals() {
  audit | jq -c --arg t "$1" 'select(.time > $t and (.type | IN("signal.received", "triage.decision", "signal.rejected",
      "ticket.created", "ticket.commented", "ticket.retitled", "investigation.started", "investigation.finished")))
    | [.seq, .time[11:19], .type, .incident, (.data.source.kind // ""), (.data.key // .data.ref // ""), (.data.status // .data.kind // .data.reason // ""), (.data.outcome // "")]'
}
sen get deploy/sensor -o jsonpath='{.spec.template.spec.containers[0].env}' | jq -c '.[] | select(.name == "IC_SENSOR_NAMESPACES")'
# expect {"name":"IC_SENSOR_NAMESPACES","value":"demo"}
```

## 2. A crash-looping `orders` rollout is one Detector Signal in the alert's Incident

```sh
T0=$(date -u +%FT%TZ)
make fault F=crash-loop
demo get pods -w   # wait until the new orders pod shows CrashLoopBackOff a few times, then Ctrl-C
```

The Detector fires once a container has waited in `CrashLoopBackOff` for a full minute. The back-off doubles from 10 seconds, so that takes a few minutes after the rollout (2m20s in a run on these clusters). Prometheus's `PodCrashLooping` usually arrives first.

```sh
sleep 300; signals "$T0"
# expect, in time order (Incident N in the fourth column throughout):
#   signal.received  alertmanager  ic-target-alertmanager/<fp>                                firing
#   triage.decision                ic-target-alertmanager/<fp>                                open (or join)
#   ticket.created                 <ref>                         (only on open; an investigation follows)
#   signal.received  sensor        detector:CrashLoopBackOff/ic-target/demo/Deployment/orders firing
#   triage.decision                detector:CrashLoopBackOff/ic-target/demo/Deployment/orders join
#   ticket.commented               detector:CrashLoopBackOff/ic-target/demo/Deployment/orders
# DeploymentRolloutStuck joins the same Incident too. Nothing opens a second Incident for orders.
audit | jq -c --arg t "$T0" 'select(.time > $t and .type == "signal.received" and .data.source.kind == "sensor") | .data
  | {source, name, severity, target, startsAt, receivedAt}' | head -1
# expect source {"kind":"sensor","instance":"ic-target"}, name CrashLoopBackOff, severity critical,
# target {"platform":"kubernetes","location":"ic-target","namespace":"demo","kind":"Deployment","name":"orders"}
audit | jq -r --arg t "$T0" 'select(.time > $t and .type == "ticket.created") | .data.ref'
# expect at most one ref (none if the Incident already existed)
```

Open the Incident's ticket. It has one `Signal joined:` comment per Signal that joined, including `` `CrashLoopBackOff` · critical · … · from sensor/ic-target ``, and no second ticket for `orders`.

## 3. The Sensor re-sends a firing Signal every 5 minutes

```sh
sleep 300; signals "$T0" | grep detector:CrashLoopBackOff
# expect a sensor firing Signal every 5 minutes (give or take the Sensor's 10-second tick), each
# followed by triage.decision "repeat" on the same Incident: the same startsAt, so it only updates last-seen
```

## 4. Clearing the fault resolves the Signal 10 minutes later, and the Incident with it

```sh
make clear
T1=$(date -u +%FT%TZ)
sleep 630; signals "$T1"
# expect, in time order, on the same Incident:
#   signal.received  alertmanager  ic-target-alertmanager/<fp>   resolved   (each alert, within a couple of minutes)
#   triage.decision                ic-target-alertmanager/<fp>   end
#   ticket.commented               ic-target-alertmanager/<fp>   end
#   firing re-sends of the Detector Signal, each a repeat, until about 10 minutes after the last crash, then
#   signal.received  sensor        detector:CrashLoopBackOff/ic-target/demo/Deployment/orders  resolved
#   triage.decision                detector:CrashLoopBackOff/ic-target/demo/Deployment/orders  resolve  resolved
#   ticket.commented               detector:CrashLoopBackOff/ic-target/demo/Deployment/orders  resolve
#   ticket.retitled                <ref>
# and no further re-sends
```

The Detector Signal is the last member to end, so its resolution resolves the Incident with outcome *resolved*. The ticket gets a `Signal resolved:` comment per alert, then `` Signal resolved: `CrashLoopBackOff` · critical · … · from sensor/ic-target `` followed by **Resolved.** Every Signal has ended; outcome: **resolved**. The title loses its severity, and the ticket stays open.

## 5. A pod with no owner is its own Target; other namespaces are not watched

```sh
T2=$(date -u +%FT%TZ)
demo run crasher --image=busybox:1.37 --restart=Always -- sh -c 'exit 1'
kubectl --context kind-ic-target -n default run crasher --image=busybox:1.37 --restart=Always -- sh -c 'exit 1'
sleep 300; signals "$T2"
# expect one sensor Signal detector:CrashLoopBackOff/ic-target/demo/Pod/crasher, opening its own
# Incident and ticket; nothing for the pod in default, which is outside IC_SENSOR_NAMESPACES
demo delete pod crasher; kubectl --context kind-ic-target -n default delete pod crasher
sleep 630; signals "$T2" | tail -4
# expect the resolved Signal for Pod/crasher about 10 minutes after the delete, then triage.decision
# resolve with outcome resolved, ticket.commented and ticket.retitled on the crasher's Incident
```

## 6. A Detector Signal naming another Location is rejected

The Sensor's own Detectors always name its Location, so forge one with the Sensor's token:

```sh
icp port-forward deploy/control-plane 18080:8080 >/dev/null 2>&1 & PF=$!; sleep 3
curl -s -w '%{http_code}\n' -XPOST localhost:18080/link/ic-target/signals -H 'Authorization: Bearer demo-sensor-token' -d '{
  "name":"CrashLoopBackOff","status":"firing","severity":"critical","startsAt":"2026-11-11T00:00:00Z",
  "target":{"platform":"kubernetes","location":"aks-prod","namespace":"demo","kind":"Deployment","name":"orders"}}'
# expect "Target in another Location than the Sensor's" and 403
curl -s -w '%{http_code}\n' -XPOST localhost:18080/link/ic-target/signals -H 'Authorization: Bearer wrong' -d '{}'
# expect "unauthorized" and 401
curl -s -w '%{http_code}\n' -XPOST localhost:18080/link/ic-target/signals -H 'Authorization: Bearer demo-sensor-token' -d 'not json'
# expect "unparseable body" and 400
kill $PF
audit | jq -c 'select(.type == "signal.rejected" or .type == "link.refused") | [.type, .data.source.instance // .data.location, .data.target.location // "", .data.reason]' | tail -2
# expect ["signal.rejected","ic-target","aks-prod","Target in another Location than the Sensor's"]
# and    ["link.refused","ic-target","","bad token"]
audit | jq -c 'select(.type == "request.rejected") | [.data.source, .data.reason]' | tail -1
# expect the sensor source ic-target and the JSON error
```

## 7. Everything is audited, and the Sensor still only reads

```sh
audit | grep -c demo-sensor-token   # expect 0
as=system:serviceaccount:incident-copilot:sensor
kubectl --context kind-ic-target auth can-i get replicasets --as=$as   # expect yes: owners are read
kubectl --context kind-ic-target auth can-i delete pods --as=$as       # expect no
```

## Clean up

```sh
make clear
```
