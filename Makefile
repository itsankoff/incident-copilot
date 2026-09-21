# incident-copilot developer and demo-environment targets.
# Run `make help` for the list.

include deploy/versions.env

SHELL       := /bin/bash
.SHELLFLAGS := -eu -o pipefail -c
.DEFAULT_GOAL := help

KUBE_CONTEXT := kind-$(CLUSTER_NAME)
KUBECTL      := kubectl --context $(KUBE_CONTEXT)
HELM         := helm --kube-context $(KUBE_CONTEXT)

COMMIT  := $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
DIRTY   := $(shell git diff --quiet HEAD -- 2>/dev/null || echo -dirty)
VERSION ?= $(COMMIT)$(DIRTY)
IMAGE   := demo-svc:$(VERSION)

# Setting up grafana during make observability
GRAFANA ?= 0
OBS_DIR := deploy/observability

##@ Development

.PHONY: help
help: ## Show this help
	@awk 'BEGIN {FS = ":.*##"} /^##@/ {printf "\n%s\n", substr($$0, 5)} /^[a-zA-Z_-]+:.*##/ {printf "  %-14s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

.PHONY: build
build: ## Compile all Go packages and binaries
	go build ./...

.PHONY: test
test: ## Run unit tests
	go test ./...

.PHONY: vet
vet: ## Run go vet
	go vet ./...

.PHONY: tools
tools: ## Check that required tools are installed
	@ok=1; \
	for t in docker kind kubectl helm go; do \
	  command -v $$t >/dev/null || { echo "missing: $$t (brew install $$t)"; ok=0; }; \
	done; \
	[ $$ok = 1 ] || exit 1; \
	docker info >/dev/null 2>&1 || { echo "docker daemon is not running"; exit 1; }; \
	kv=$$(kind version | awk '{print $$2}'); \
	[ "$$kv" = "$(KIND_VERSION)" ] || echo "warning: kind $$kv, pinned $(KIND_VERSION)"; \
	hv=$$(helm version --short | sed -E 's/^v([0-9]+).*/\1/'); \
	[ "$$hv" -ge "$(HELM_MIN_MAJOR)" ] || { echo "helm major $$hv < $(HELM_MIN_MAJOR)"; exit 1; }; \
	gv=$$(go env GOVERSION | sed 's/^go//'); \
	printf '%s\n%s\n' "$(GO_MIN_VERSION)" "$$gv" | sort -V -C || { echo "go $$gv < $(GO_MIN_VERSION)"; exit 1; }; \
	echo "tools ok (go $$gv, kind $$kv, helm $$(helm version --short))"

##@ Demo environment

.PHONY: cluster
cluster: tools ## Create the kind cluster and install the observability stack (idempotent)
	@if kind get clusters 2>/dev/null | grep -qx '$(CLUSTER_NAME)'; then \
	  echo "cluster $(CLUSTER_NAME) already exists"; \
	else \
	  kind create cluster --name $(CLUSTER_NAME) --config deploy/kind/cluster.yaml \
	    --image $(KIND_NODE_IMAGE) --wait 120s; \
	fi
	$(MAKE) observability

.PHONY: observability
observability: ## Install or upgrade Prometheus, Alertmanager, Loki, and Alloy
	helm repo add prometheus-community https://prometheus-community.github.io/helm-charts --force-update >/dev/null
	helm repo add grafana https://grafana.github.io/helm-charts --force-update >/dev/null
	helm repo update prometheus-community grafana >/dev/null
	$(HELM) upgrade --install kps prometheus-community/kube-prometheus-stack \
	  --version $(KPS_CHART_VERSION) -n monitoring --create-namespace \
	  -f $(OBS_DIR)/kube-prometheus-stack.yaml \
	  $(if $(filter 1,$(GRAFANA)),--set grafana.enabled=true) \
	  --wait --timeout 10m
	$(HELM) upgrade --install loki grafana/loki \
	  --version $(LOKI_CHART_VERSION) -n logging --create-namespace \
	  -f $(OBS_DIR)/loki.yaml --wait --timeout 10m
	$(HELM) upgrade --install alloy grafana/alloy \
	  --version $(ALLOY_CHART_VERSION) -n logging \
	  -f $(OBS_DIR)/alloy.yaml --wait --timeout 5m

.PHONY: image
image: ## Build the demo-svc image and load it into the kind cluster
	docker build --build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) -t $(IMAGE) .
	kind load docker-image $(IMAGE) --name $(CLUSTER_NAME)

.PHONY: deploy
deploy: image ## Deploy the demo workload (orders, inventory, loadgen) and wait for rollout
	@mkdir -p build/deploy
	@printf '%s\n' \
	  'apiVersion: kustomize.config.k8s.io/v1beta1' \
	  'kind: Kustomization' \
	  'resources: [../../deploy/demo]' \
	  'images: [{name: demo-svc, newName: demo-svc, newTag: "$(VERSION)"}]' \
	  > build/deploy/kustomization.yaml
	$(KUBECTL) apply -k build/deploy
	@for d in inventory orders loadgen; do \
	  $(KUBECTL) -n demo rollout status deploy/$$d --timeout 180s; \
	done

.PHONY: status
status: ## Show pods in the demo, monitoring, and logging namespaces
	$(KUBECTL) get pods -n demo -o wide
	$(KUBECTL) get pods -n monitoring
	$(KUBECTL) get pods -n logging

.PHONY: port-forward
port-forward: ## Forward Prometheus :9090, Alertmanager :9093, Loki API :3100, Grafana :3000 if enabled (Ctrl-C to stop)
	@trap 'kill 0' INT TERM EXIT; \
	$(KUBECTL) -n monitoring port-forward svc/kps-prometheus 9090:9090 & \
	$(KUBECTL) -n monitoring port-forward svc/kps-alertmanager 9093:9093 & \
	$(KUBECTL) -n logging port-forward svc/loki 3100:3100 & \
	if $(KUBECTL) -n monitoring get svc kps-grafana >/dev/null 2>&1; then \
	  $(KUBECTL) -n monitoring port-forward svc/kps-grafana 3000:80 & \
	  echo "Grafana: http://localhost:3000/explore (Loki and Prometheus datasources)"; \
	else \
	  echo "Grafana is off. Enable it with: make observability GRAFANA=1"; \
	fi; \
	wait

.PHONY: down
down: ## Delete the kind cluster
	kind delete cluster --name $(CLUSTER_NAME)
	rm -rf build
