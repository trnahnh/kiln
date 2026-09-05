IMG ?= controller:latest
KUBECTL ?= kubectl
LOCALBIN ?= $(shell pwd)/bin
KUSTOMIZE ?= $(LOCALBIN)/kustomize
CONTROLLER_GEN ?= $(LOCALBIN)/controller-gen
ENVTEST ?= $(LOCALBIN)/setup-envtest

KUSTOMIZE_VERSION ?= v5.8.1
CONTROLLER_TOOLS_VERSION ?= v0.22.0
ENVTEST_VERSION ?= $(shell go list -m -f '{{.Version}}' sigs.k8s.io/controller-runtime)
ENVTEST_K8S_VERSION ?= $(shell go list -m -f '{{.Version}}' k8s.io/api | sed -E 's/^v0\.([0-9]+)\..*/1.\1/')

# Explicit package roots: controller-gen does not expand ./... on every host.
GEN_PATHS = ./api/v1 ./internal/controller

SHELL = /usr/bin/env bash -o pipefail
.SHELLFLAGS = -ec

.PHONY: all
all: build

##@ Development

.PHONY: manifests
manifests: controller-gen ## Generate CRD and RBAC manifests.
	"$(CONTROLLER_GEN)" rbac:roleName=manager-role crd $(addprefix paths=,$(GEN_PATHS)) output:crd:artifacts:config=config/crd/bases

.PHONY: generate
generate: controller-gen ## Generate DeepCopy implementations.
	"$(CONTROLLER_GEN)" object:headerFile="hack/boilerplate.go.txt" $(addprefix paths=,$(GEN_PATHS))

.PHONY: fmt
fmt:
	go fmt ./...

.PHONY: vet
vet:
	go vet ./...

.PHONY: test
test: manifests generate fmt vet setup-envtest ## Run unit tests (pure state machine + envtest).
	KUBEBUILDER_ASSETS="$(shell "$(ENVTEST)" use $(ENVTEST_K8S_VERSION) --bin-dir "$(LOCALBIN)" -p path)" go test ./api/... ./internal/... -coverprofile cover.out

.PHONY: test-integration
test-integration: manifests generate ## Run integration tests against the current kubeconfig context (a kind cluster).
	go test -tags=integration ./test/integration/ -v -timeout 30m

##@ Build

.PHONY: build
build: manifests generate fmt vet ## Build the manager binary.
	go build -o bin/manager cmd/main.go

.PHONY: run
run: manifests generate fmt vet ## Run the manager from the host against the current kubeconfig context.
	go run ./cmd/main.go

.PHONY: docker-build
docker-build: ## Build the manager image.
	docker build -t ${IMG} .

##@ Deployment

.PHONY: install
install: manifests kustomize ## Install CRDs into the current cluster.
	"$(KUSTOMIZE)" build config/crd | "$(KUBECTL)" apply -f -

.PHONY: uninstall
uninstall: manifests kustomize ## Remove CRDs from the current cluster.
	"$(KUSTOMIZE)" build config/crd | "$(KUBECTL)" delete --ignore-not-found -f -

.PHONY: deploy
deploy: manifests kustomize ## Deploy the manager to the current cluster.
	cd config/manager && "$(KUSTOMIZE)" edit set image controller=${IMG}
	"$(KUSTOMIZE)" build config/default | "$(KUBECTL)" apply -f -

.PHONY: undeploy
undeploy: kustomize ## Remove the manager from the current cluster.
	"$(KUSTOMIZE)" build config/default | "$(KUBECTL)" delete --ignore-not-found -f -

##@ Dependencies

$(LOCALBIN):
	mkdir -p "$(LOCALBIN)"

.PHONY: kustomize
kustomize: $(KUSTOMIZE)
$(KUSTOMIZE): $(LOCALBIN)
	$(call go-install-tool,$(KUSTOMIZE),sigs.k8s.io/kustomize/kustomize/v5,$(KUSTOMIZE_VERSION))

.PHONY: controller-gen
controller-gen: $(CONTROLLER_GEN)
$(CONTROLLER_GEN): $(LOCALBIN)
	$(call go-install-tool,$(CONTROLLER_GEN),sigs.k8s.io/controller-tools/cmd/controller-gen,$(CONTROLLER_TOOLS_VERSION))

.PHONY: setup-envtest
setup-envtest: envtest ## Download the envtest control-plane binaries.
	@"$(ENVTEST)" use $(ENVTEST_K8S_VERSION) --bin-dir "$(LOCALBIN)" -p path

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
