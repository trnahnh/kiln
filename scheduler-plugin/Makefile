IMG ?= kiln-scheduler:dev
KIND_CLUSTER ?= kiln-dev
LOCALBIN ?= $(shell pwd)/bin
ENVTEST ?= $(LOCALBIN)/setup-envtest
ENVTEST_VERSION ?= release-0.24
ENVTEST_K8S_VERSION ?= $(shell go list -m -f '{{.Version}}' k8s.io/api | sed -E 's/^v0\.([0-9]+)\..*/1.\1/')

SHELL = /usr/bin/env bash -o pipefail
.SHELLFLAGS = -ec

.PHONY: build
build: ## Build the kiln-scheduler binary.
	go build -o bin/kiln-scheduler ./cmd/kiln-scheduler

.PHONY: test
test: ## Unit tests: pure scoring, economics sources, plugin adapter.
	go vet ./...
	go test ./internal/... -count=1

.PHONY: bench
bench: setup-envtest ## Placement latency: Score micro-benchmarks and the envtest scheduler throughput benchmark.
	go test ./internal/scoring/ -run xxx -bench . -benchmem
	KUBEBUILDER_ASSETS="$(shell "$(ENVTEST)" use $(ENVTEST_K8S_VERSION) --bin-dir "$(LOCALBIN)" -p path)" go test -tags=perf ./test/perf/ -run xxx -bench . -benchtime=1x -timeout 30m -v

.PHONY: docker-build
docker-build: ## Build the scheduler image.
	docker build -t $(IMG) .

.PHONY: kind-load
kind-load: ## Load the image into the kind cluster so the ArgoCD-managed Deployment can start it.
	kind load docker-image $(IMG) --name $(KIND_CLUSTER)

.PHONY: setup-envtest
setup-envtest: $(ENVTEST)
	@"$(ENVTEST)" use $(ENVTEST_K8S_VERSION) --bin-dir "$(LOCALBIN)" -p path

$(ENVTEST):
	mkdir -p "$(LOCALBIN)"
	GOBIN="$(LOCALBIN)" go install sigs.k8s.io/controller-runtime/tools/setup-envtest@$(ENVTEST_VERSION)
