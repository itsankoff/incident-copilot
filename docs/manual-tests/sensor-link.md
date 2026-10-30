# Manual test: The Sensor link

This test checks that a Sensor in `ic-target` dials out to the Control plane in `ic-control-plane` as Location `ic-target`, and answers real Evidence queries about `orders`. It also checks that a killed Sensor marks the Location offline, a restart brings it back, an unconfigured Location is refused, and nothing connects into `ic-target`. Run it whenever `internal/link`, `internal/sensor`, `internal/kubernetes`, or `deploy/sensor` change.

The RCA loop does not exist yet (#48), so the queries go through the Control plane's debug listener. It has no authentication and binds to loopback inside the pod, so only `kubectl port-forward` reaches it.

## Before you start

The prerequisites of [the two-cluster demo](two-cluster-demo.md#before-you-start). `.env.demo` is optional here. Run every command from the repository root, in bash, in one shell.

## 1. Deploy

```sh
make cluster deploy control-plane sensor
make status   # expect sensor Running in ic-target's incident-copilot namespace
```

Define the helpers:

```sh
sen() { kubectl --context kind-ic-target -n incident-copilot "$@"; }
icp() { kubectl --context kind-ic-control-plane -n incident-copilot "$@"; }
audit() {
  icp debug "$(icp get pod -l app.kubernetes.io/name=control-plane -o name)" -q -i \
    --image=busybox:1.37 --target=control-plane --profile=general \
    -- cat /proc/1/root/var/lib/incident-copilot/audit.jsonl 2>/dev/null
}
icp port-forward deploy/control-plane 8081:8081 >/dev/null 2>&1 &
ask() { # TOOL
  curl -s -m 25 -w '\n%{http_code}\n' -XPOST localhost:8081/debug/evidence -d '{"tool":"'"$1"'","target":
    {"platform":"kubernetes","location":"ic-target","namespace":"demo","kind":"Deployment","name":"orders"}}'
}
```

## 2. Evidence queries return real data

```sh
curl -s localhost:8081/debug/locations
# expect: [{"name":"ic-target","environment":"demo","online":true}]
ask workload_state | head -1 | jq -c '{ref, replicas: .data.spec.replicas, pods: [.data.pods[] | {name, phase}]}'
# expect: a ref with callId, source sensor, and location ic-target; 2 replicas; both orders pods Running
ask events | head -1 | jq -c '[.data[] | {kind, name, reason}] | .[:3]'
# expect: recent events for the orders pods, ReplicaSet, or Deployment. If orders has been up for
# over an hour the list can be empty: the default window is the last hour.
```

## 3. Offline and back

```sh
sen scale deploy/sensor --replicas=0
sleep 32
curl -s localhost:8081/debug/locations   # expect "online":false
ask events                               # expect "ic-target: location offline" and 503
sen scale deploy/sensor --replicas=1
sleep 10
curl -s localhost:8081/debug/locations   # expect "online":true
```

## 4. A Location not in config is refused

```sh
sen set env deploy/sensor IC_SENSOR_LOCATION=ghost
sen rollout status deploy/sensor
sleep 8
sen logs deploy/sensor | tail -1 | jq -r .msg   # expect "link down: refused by the Control plane"
sen set env deploy/sensor IC_SENSOR_LOCATION=ic-target
sen rollout status deploy/sensor
```

## 5. Everything is audited

```sh
audit | jq -c '[.seq, .type, (.data.location // .data.ref.location // ""), (.data.tool // ""), (.data.outcome // ""), (.data.reason // "")]'
# expect, in order: location.online; evidence.query ok for each tool; location.offline; evidence.query
# error; location.online; link.refused for ghost with "location not in config"
audit | grep -c demo-sensor-token   # expect 0
audit | jq -c 'select(.type == "evidence.query") | .data | keys' | sort -u
# expect ref, tool, outcome, and bytes and sha256 or error: never the answer itself
```

## 6. Nothing connects into `ic-target`, and the Sensor only reads

```sh
kubectl --context kind-ic-target get svc -A -o json \
  | jq -r '.items[] | select(.spec.type != "ClusterIP") | "\(.metadata.namespace)/\(.metadata.name) \(.spec.type)"'
# expect nothing
sen get svc   # expect no resources
as=system:serviceaccount:incident-copilot:sensor
kubectl --context kind-ic-target auth can-i delete pods --as=$as    # expect no
kubectl --context kind-ic-target auth can-i list secrets --as=$as   # expect no
```

## Clean up

```sh
kill %1   # the port-forward
make down
```
