IMG ?= kiln-chaos:dev
KIND_CLUSTER ?= kiln-dev
LOCALBIN ?= $(shell pwd)/bin
CONTROLLER_GEN ?= $(LOCALBIN)/controller-gen
ENVTEST ?= $(LOCALBIN)/setup-envtest

CONTROLLER_TOOLS_VERSION ?= v0.22.0
ENVTEST_VERSION ?= $(shell go list -m -f '{{.Version}}' sigs.k8s.io/controller-runtime)
ENVTEST_K8S_VERSION ?= $(shell go list -m -f '{{.Version}}' k8s.io/api | sed -E 's/^v0\.([0-9]+)\..*/1.\1/')

SHELL = /usr/bin/env bash -o pipefail
.SHELLFLAGS = -ec

.PHONY: manifests
manifests: controller-gen ## Generate the CRD and the controller's and the agent's ClusterRoles.
	"$(CONTROLLER_GEN)" crd:allowDangerousTypes=true paths=./api/v1 output:crd:artifacts:config=config/crd/bases
	"$(CONTROLLER_GEN)" rbac:roleName=kiln-chaos-controller paths=./internal/controller output:rbac:artifacts:config=config/manager/controller
	"$(CONTROLLER_GEN)" rbac:roleName=kiln-chaos-agent paths=./internal/agent output:rbac:artifacts:config=config/manager/agent

.PHONY: generate
generate: controller-gen ## Generate DeepCopy implementations.
	"$(CONTROLLER_GEN)" object:headerFile="hack/boilerplate.go.txt" paths=./api/v1

.PHONY: test
test: manifests generate setup-envtest ## Unit tests: abort and score analysis on synthetic streams, fault injectors on a fake executor, agent scope checks, envtest controller with the real agent loop.
	go vet ./...
	KUBEBUILDER_ASSETS="$(shell "$(ENVTEST)" use $(ENVTEST_K8S_VERSION) --bin-dir "$(LOCALBIN)" -p path)" go test ./api/... ./internal/... -count=1

.PHONY: build
build: manifests generate
	go build -o bin/kiln-chaos ./cmd

.PHONY: docker-build
docker-build: ## Build the image; the context is the repo root so the shared slo module is on hand.
	docker build -t $(IMG) -f Dockerfile ..

.PHONY: kind-load
kind-load: ## Load the image into the kind cluster so the ArgoCD-managed workloads can start it.
	kind load docker-image $(IMG) --name $(KIND_CLUSTER)

.PHONY: setup-envtest
setup-envtest: envtest
	@"$(ENVTEST)" use $(ENVTEST_K8S_VERSION) --bin-dir "$(LOCALBIN)" -p path

$(LOCALBIN):
	mkdir -p "$(LOCALBIN)"

.PHONY: controller-gen
controller-gen: $(CONTROLLER_GEN)
$(CONTROLLER_GEN): $(LOCALBIN)
	$(call go-install-tool,$(CONTROLLER_GEN),sigs.k8s.io/controller-tools/cmd/controller-gen,$(CONTROLLER_TOOLS_VERSION))

.PHONY: envtest
envtest: $(ENVTEST)
$(ENVTEST): $(LOCALBIN)
	$(call go-install-tool,$(ENVTEST),sigs.k8s.io/controller-runtime/tools/setup-envtest,$(ENVTEST_VERSION))

define go-install-tool
@[ -f "$(1)-$(3)" ] && [ "$$(readlink -- "$(1)" 2>/dev/null)" = "$(1)-$(3)" ] || { \
set -e; \
package=$(2)@$(3) ;\
echo "Downloading $${package}" ;\
rm -f "$(1)" ;\
GOBIN="$(LOCALBIN)" go install $${package} ;\
mv "$(LOCALBIN)/$$(basename "$(1)")" "$(1)-$(3)" ;\
} ;\
ln -sf "$$(realpath "$(1)-$(3)")" "$(1)"
endef
