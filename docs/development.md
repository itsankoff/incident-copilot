# Development

How to build and test incident-copilot, and how to run the demo environment one part at a time. To just see incident-copilot work, run `make demo` ([`docs/demo.md`](demo.md)). The engineering conventions are in [`CONTRIBUTING.md`](../CONTRIBUTING.md).

## Prerequisites

- Go 1.25+, Docker, [kind](https://kind.sigs.k8s.io), `kubectl`, and [Helm](https://helm.sh). On macOS: `brew install go kind kubectl helm`. `make tools` checks them against the versions pinned in [`deploy/versions.env`](../deploy/versions.env).
- [golangci-lint](https://golangci-lint.run), at the pinned `GOLANGCI_LINT_VERSION`, for `make lint`.
- `jq` and the `gh` CLI, for the manual tests.
- For the clusters: Docker with 6 CPUs and 10 GB of memory, and 4 CPUs and 8 GB at the least.

## Build and test

```sh
make build test vet lint            # compile, unit-test, vet, and lint the Go code; run it before a PR
go test ./internal/demo -run TestName   # one test
```

Unit tests use fakes and need no network or cluster.

## The demo environment, by hand

`make demo` runs these targets in order. Run them yourself to work on one part. Every target is idempotent, and `make help` lists them all.

```sh
make cluster         # create ic-target and ic-control-plane (make kind-clusters), and install
                     # Prometheus, Alertmanager, Loki, Alloy, and Grafana in ic-target (make observability)
make deploy          # build the demo-svc image and deploy orders, inventory, and loadgen into ic-target
make control-plane   # build the incident-copilot image and deploy the Control plane into ic-control-plane
make sensor          # build the incident-copilot image and deploy the Sensor into ic-target
make status          # pods in both clusters
make port-forward    # ic-target's Prometheus :9090, Alertmanager :9093, Loki API :3100, Grafana :3000
make fault F=crash-loop   # roll out a fault scenario; no F lists them and their ground truth
make clear           # restore the baseline; waits until all pods are Ready and no alerts fire
make down            # delete both clusters
```

- **Secrets.** `make control-plane` reads `.env.demo`, as the README's [Configuration](../README.md#configuration) explains, with every variable.
- **Grafana** is on by default. `make cluster GRAFANA=0` or `make observability GRAFANA=0` turns it off. Loki has no UI of its own.
- **Redeploying uncommitted changes.** Images are tagged `<commit>-dirty` for an uncommitted tree, so a second uncommitted build reuses the tag and does not roll out. Pass a new tag each time: `make control-plane VERSION=dev-$(date +%s)`.
- **Contexts.** Every `kubectl` and `helm` call in the Makefile pins the context of the cluster it targets, `kind-ic-target` or `kind-ic-control-plane`.

The design of the environment, including the faults and the alerts, is in [`docs/design/demo-environment.md`](design/demo-environment.md).

## Manual tests

[`docs/manual-tests/`](manual-tests) holds the checks that need real clusters or real APIs. Each says when to run it:

- [`make-demo.md`](manual-tests/make-demo.md): `make demo`, from the bootstrap to an RCA
- [`two-cluster-demo.md`](manual-tests/two-cluster-demo.md): the two clusters, from an alert to a ticket
- [`github-issues.md`](manual-tests/github-issues.md): tickets in a real GitHub repository
- [`ingest-hardening.md`](manual-tests/ingest-hardening.md): the Control plane's intake bounds
- [`sensor-link.md`](manual-tests/sensor-link.md): the Sensor link and its Evidence queries

## Planned

```sh
make eval            # run the evaluation harness across all fault scenarios (#70)
```
