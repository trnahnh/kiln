IMG ?= kiln-delivery-controller:dev
KIND_CLUSTER ?= kiln-dev
LOCALBIN ?= $(shell pwd)/bin
CONTROLLER_GEN ?= $(LOCALBIN)/controller-gen
ENVTEST ?= $(LOCALBIN)/setup-envtest

CONTROLLER_TOOLS_VERSION ?= v0.22.0
ENVTEST_VERSION ?= $(shell go list -m -f '{{.Version}}' sigs.k8s.io/controller-runtime)
ENVTEST_K8S_VERSION ?= $(shell go list -m -f '{{.Version}}' k8s.io/api | sed -E 's/^v0\.([0-9]+)\..*/1.\1/')

GEN_PATHS = ./api/v1 ./internal/controller

SHELL = /usr/bin/env bash -o pipefail
.SHELLFLAGS = -ec

.PHONY: manifests
manifests: controller-gen ## Generate the CRD and RBAC manifests.
	"$(CONTROLLER_GEN)" rbac:roleName=kiln-delivery-controller crd:allowDangerousTypes=true $(addprefix paths=,$(GEN_PATHS)) output:crd:artifacts:config=config/crd/bases output:rbac:artifacts:config=config/manager

.PHONY: generate
generate: controller-gen ## Generate DeepCopy implementations.
	"$(CONTROLLER_GEN)" object:headerFile="hack/boilerplate.go.txt" $(addprefix paths=,$(GEN_PATHS))

.PHONY: test
test: manifests generate setup-envtest ## Unit tests: pure analysis with the simulation suite, metric source, router, envtest reconciler.
	go vet ./...
	KUBEBUILDER_ASSETS="$(shell "$(ENVTEST)" use $(ENVTEST_K8S_VERSION) --bin-dir "$(LOCALBIN)" -p path)" go test ./api/... ./internal/... -count=1

.PHONY: build
build: manifests generate
	go build -o bin/kiln-delivery-controller ./cmd

.PHONY: run
run: manifests generate ## Run the controller from the host against the current kubeconfig context.
	go run ./cmd --prometheus-url=http://localhost:9090

.PHONY: docker-build
docker-build: ## Build the controller image.
	docker build -t $(IMG) .

.PHONY: kind-load
kind-load: ## Load the image into the kind cluster so the ArgoCD-managed Deployment can start it.
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
