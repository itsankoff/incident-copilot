# Manual test: The two-cluster demo

This test checks the demo topology end to end. `ic-target` runs the demo app and the observability stack, and `ic-control-plane` runs the Control plane. An alert fired in `ic-target`'s Alertmanager opens an Incident in the Control plane: first with the fake ticketing adapter, then as a GitHub issue. It also checks that only the Alertmanager → Control plane connection exists, that every Makefile call pins its cluster's context, and that `make cluster` and `make down` cover both clusters. Run it whenever the clusters, the Makefile's demo targets, `deploy/control-plane`, or the Alertmanager route change.

## Before you start

- **The `make tools` prerequisites**, and Docker with at least 4 CPUs and 8 GB of memory, for both clusters.
- **`jq` and the `gh` CLI**, logged in with access to the tickets repository.
- **`.env.demo` at the repository root**, set up as in [the GitHub Issues manual test](github-issues.md#before-you-start): `IC_GITHUB_REPO` is a scratch repository for test tickets, and `IC_GITHUB_TOKEN` has **Issues: Read and write**. Step 4 opens one real issue there.
- **No other demo clusters running.** Triage keeps one open Incident per Target, and the Control plane's state survives redeploys. On clusters left over from an earlier run, the alerts below join old Incidents instead of opening new ones. If an older single cluster named `incident-copilot` is still around, delete it to free memory: `kind delete cluster --name incident-copilot`.

Run every command from the repository root, in one shell, because later steps reuse the helpers defined earlier.

## 1. Create both clusters

```sh
make down      # start clean; it is fine if the clusters do not exist
make cluster
kind get clusters
# expect: ic-control-plane and ic-target
kubectl config get-contexts -o name | grep '^kind-ic-'
# expect: kind-ic-control-plane and kind-ic-target
```

Run it again. Both clusters are reported as existing, and the Helm releases are upgraded in place with no changes:

```sh
make cluster 2>&1 | grep -E 'already exists|Happy Helming'
# expect: "cluster ic-target already exists", "cluster ic-control-plane already exists",
# and one "has been upgraded" line each for kps, loki, and alloy
```

## 2. Deploy the demo app and the Control plane without `.env.demo`

Move `.env.demo` aside, so the Control plane starts with the fake ticketing adapter:

```sh
mv .env.demo .env.demo.off
make deploy
make control-plane   # expect "no .env.demo: the Control plane starts with the fake ticketing adapter"
make status
# expect under "== ic-target": orders x2, inventory, and loadgen Running in demo, and the
# monitoring and logging pods Running. Under "== ic-control-plane": control-plane Running.
```

Define the helpers. `am` runs a command in `ic-target`'s Alertmanager, and `icp` runs `kubectl` against the Control plane's namespace. The Control plane image is distroless, so `audit` reads its audit log through a debug container:

```sh
am() { kubectl --context kind-ic-target -n monitoring exec alertmanager-kps-alertmanager-0 -c alertmanager -- "$@"; }
icp() { kubectl --context kind-ic-control-plane -n incident-copilot "$@"; }
audit() {
  icp debug "$(icp get pod -l app.kubernetes.io/name=control-plane -o name)" -q -i \
    --image=busybox:1.37 --target=control-plane --profile=general \
    -- cat /proc/1/root/var/lib/incident-copilot/audit.jsonl 2>/dev/null
}
fire() { # ALERTNAME DEPLOYMENT [--end=...]
  am amtool alert add "$1" severity=warning namespace=demo deployment="$2" "${@:3}" \
    --annotation=description=MANUAL-TEST-ANNOTATION --alertmanager.url=http://localhost:9093
}
icp logs deploy/control-plane | jq -r .msg
# expect: "IC_GITHUB_REPO and IC_GITHUB_TOKEN unset: using the fake ticketing adapter, ..."
# and "control plane listening"
```

## 3. Alert → Incident with the fake ticketing adapter

```sh
fire FakeTicketCheck inventory
sleep 20
audit | jq -c '[.seq, .type, .incident, (.data.kind // .data.ref // "")]'
icp logs deploy/control-plane | jq -c 'select(.msg == "ticket created") | {ref, title}'
```

Within about 10 seconds (`group_wait`), the audit log shows `signal.received`, `triage.decision` (`open`), and `ticket.created` with the ref `log-1`. The log shows `ticket created` with the title `FakeTicketCheck on ic-target/demo/Deployment/inventory`. Other demo alerts can arrive too, if any are firing. `Watchdog` never does.

Resolve the alert, so it stops repeating:

```sh
fire FakeTicketCheck inventory --end="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
```

## 4. Alert → GitHub issue

Put `.env.demo` back and redeploy. The Secret's name carries a hash of its contents, so the new Secret rolls the Control plane:

```sh
mv .env.demo.off .env.demo
make control-plane   # expect a new secret/incident-copilot-env-<hash> created
icp logs deploy/control-plane | jq -c 'select(.msg == "control plane listening") | .repo'
# expect your IC_GITHUB_REPO
```

Fire an alert on another Target, so it opens a new Incident:

```sh
fire GitHubTicketCheck orders
sleep 20
audit | jq -c 'select(.type == "ticket.created") | [.seq, .incident, .data.ref]'
gh issue list -R "$(grep '^IC_GITHUB_REPO=' .env.demo | cut -d= -f2)" --limit 3
```

A new `ticket.created` entry holds an issue number, and the issue `GitHubTicketCheck on ic-target/demo/Deployment/orders` is in the tickets repository. Open it and check that `MANUAL-TEST-ANNOTATION` is not in it.

Resolve it. With `send_resolved: true`, Alertmanager sends the resolved notice after `group_interval` (1 minute):

```sh
fire GitHubTicketCheck orders --end="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
sleep 75
audit | jq -c 'select(.type == "triage.decision") | [.seq, .data.kind, (.data.reason // "")]' | tail -1
# expect: drop, "resolved Signals are not handled yet" (resolving Incidents is #45)
```

If nothing arrives in step 3 or 4, check `am wget -qO- http://localhost:9093/metrics | grep notifications_failed_total`. If the count grows, Alertmanager cannot reach `ic-control-plane-control-plane:30080` or its token is wrong, and the Alertmanager log says which: `kubectl --context kind-ic-target -n monitoring logs alertmanager-kps-alertmanager-0 -c alertmanager | tail`.

## 5. Only Alertmanager → Control plane connects across the clusters

```sh
kubectl --context kind-ic-target get svc -A -o json \
  | jq -r '.items[] | select(.spec.type != "ClusterIP") | "\(.metadata.namespace)/\(.metadata.name) \(.spec.type)"'
# expect nothing: ic-target exposes no NodePort or LoadBalancer
kubectl --context kind-ic-control-plane get svc -A -o json \
  | jq -r '.items[] | select(.spec.type != "ClusterIP") | "\(.metadata.namespace)/\(.metadata.name) \(.spec.type) \(.spec.ports[0].nodePort)"'
# expect only: incident-copilot/control-plane NodePort 30080
icp get deploy control-plane -o json | jq -r '.spec.template.spec.containers[0].env[] | "\(.name)=\(.value)"'
icp get secret -o json | jq -r '.items[] | "\(.metadata.name): \(.data | keys | join(" "))"'
# expect no address of ic-target and no kubeconfig: the Control plane's only outbound peer is GitHub.
# Both incident-copilot-env Secrets are listed: apply does not prune the one from step 2.
```

## 6. Every Makefile call pins its cluster

```sh
grep -nE '(^|[^_])(kubectl|helm) ' Makefile | grep -vE -- '--(kube-)?context'
# expect only lines that touch no cluster: the comment, `make tools`, and `helm repo`
git grep -n 'kind-incident-copilot'
# expect nothing
```

## 7. Delete both clusters

```sh
make down
kind get clusters
# expect neither ic-target nor ic-control-plane
```

## Clean up

Close the GitHub issue that step 4 created, or keep it as a record. Check that `.env.demo` is back in place, and not still at `.env.demo.off`.
