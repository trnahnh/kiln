IMG ?= kiln-audit-service:dev
KIND_CLUSTER ?= kiln-dev

SHELL = /usr/bin/env bash -o pipefail
.SHELLFLAGS = -ec

.PHONY: test
test: ## Unit tests: hash chain, canonical JSON, idempotency decision, role mapping, request submission against mocks.
	./gradlew --no-daemon test

.PHONY: integration-test
integration-test: ## Testcontainers tests against real Kafka and Postgres: redelivery and tamper detection.
	./gradlew --no-daemon integrationTest

.PHONY: build
build:
	./gradlew --no-daemon bootJar -x test

.PHONY: docker-build
docker-build: ## Build the image; the context is this module.
	docker build -t $(IMG) .

.PHONY: kind-load
kind-load: ## Load the image into the kind cluster so the ArgoCD-managed Deployment can start it.
	kind load docker-image $(IMG) --name $(KIND_CLUSTER)
