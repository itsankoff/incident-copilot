# Development

How to build and test incident-copilot, and how to run the demo environment one part at a time. To just see incident-copilot work, run `make demo` ([`docs/demo.md`](demo.md)). The engineering conventions are in [`CONTRIBUTING.md`](../CONTRIBUTING.md).

## Prerequisites

| Tool | Version | Needed for |
|---|---|---|
| [Go](https://go.dev) | 1.25+ | Building, testing, and `make fault` |
| [Docker](https://docs.docker.com/get-docker/) | Any recent version, with 6 CPUs and 10 GB of memory allocated (4 CPUs and 8 GB at the least) | Images and the kind nodes |
| [kind](https://kind.sigs.k8s.io) | v0.31.0 | The two clusters (Kubernetes v1.35.0 nodes) |
| [kubectl](https://kubernetes.io/docs/tasks/tools/) | Within one minor version of v1.35 (v1.34 to v1.36) | Every cluster target |
| [Helm](https://helm.sh) | v3+ | The observability stack |
| [golangci-lint](https://golangci-lint.run) | v2.13.2 | `make lint` |
| [k9s](https://k9scli.io) (optional) | 0.50+ | `make k9s` |
| [jq](https://jqlang.org) (optional) | Any | The manual tests in [`docs/manual-tests`](manual-tests) |
| [gh](https://cli.github.com) (optional) | Any | GitHub Issues ticketing, and the manual tests that use it |

On macOS, install most of them with Homebrew: `brew install go kind kubectl helm k9s jq`. golangci-lint is pinned, so install it with the `go install` line below.

On Linux, use each tool's official install method, which does not depend on your distro:

- Go: the [official tarball](https://go.dev/doc/install).
- Docker: [Docker Engine](https://docs.docker.com/engine/install/) for your distro.
- kind: the release binary, `curl -Lo ./kind https://kind.sigs.k8s.io/dl/v0.31.0/kind-linux-$(uname -m | sed 's/x86_64/amd64/;s/aarch64/arm64/') && chmod +x ./kind && sudo mv ./kind /usr/local/bin/kind`.
- kubectl: [install kubectl on Linux](https://kubernetes.io/docs/tasks/tools/install-kubectl-linux/).
- Helm: [installing Helm](https://helm.sh/docs/intro/install/).
- k9s: a release binary or package from the [k9s releases](https://github.com/derailed/k9s/releases), or see [k9s installation](https://k9scli.io/topics/install/).
- jq: [download jq](https://jqlang.org/download/).

On both, install golangci-lint with `go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.13.2`. The pinned versions live in [`deploy/versions.env`](../deploy/versions.env), and `make tools` checks them.

`make demo` checks these for you and offers to install what is missing ([`docs/demo.md`](demo.md)).

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
make executor        # deploy the Executor into ic-target, which advertises its Actions to the Control plane;
                     # make demo does not, so the demo's Advice is skipped
make status          # pods in both clusters
make k9s             # browse ic-target in k9s (CLUSTER=cp for ic-control-plane)
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
