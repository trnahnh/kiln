# kiln

Self-service infrastructure platform for Kubernetes. Developers request infrastructure through a CRD or a thin API, the platform enforces policy before anything is provisioned, schedules workloads with cost awareness, rolls out deployments progressively with automatic rollback, resilience-tests services on demand, and logs every action to a tamper-evident audit trail.

## The problem this solves

Without a platform layer, teams get infrastructure one of two ways: a ticket to a platform engineer (hours to days per request, gated on a human being available), or raw Terraform/kubectl written by the requesting developer (fast, but inherits every way to get security, cost, and reliability wrong). Neither scales past a small team, and neither leaves a real audit trail when compliance asks who provisioned what and who approved it.

This platform replaces both paths with one: request through a CRD, get policy-checked, provisioned, scheduled, deployed, and logged automatically.

Full problem writeup and the baseline this platform is measured against: [`docs/METRICS.md`](docs/METRICS.md).

## What's in this repo

| Subsystem | What it does | Stack |
|---|---|---|
| Operator | Provisions and lifecycle-manages per-tenant Postgres/Redis instances via a custom CRD | Go, kubebuilder, controller-runtime |
| Provisioning / GitOps | Translates a request into real cloud resources, deployed declaratively through Git | Crossplane, ArgoCD, Terraform |
| Policy | Validates every request against org rules before it reaches the operator | OPA, Kyverno |
| Scheduler plugin | Places pods with cost/fragmentation-aware scoring instead of default bin packing | Go, Kubernetes scheduler framework |
| Progressive delivery | Canary rollout with automatic, statistically-gated rollback | Go, Istio or Linkerd |
| Chaos / resilience | On-demand controlled failure injection with an automated SLO-based score | Go, OpenTelemetry, tc/iptables |
| Audit / RBAC | Tamper-evident audit trail and access control in front of the whole platform | Java, Spring Boot, Kafka, PostgreSQL |

Full design rationale and the hard problem each one solves: [`docs/SYSTEM_DESIGN.md`](docs/SYSTEM_DESIGN.md).

## Architecture

```
 developer
    |
    v
 [ Audit/RBAC service ]  --auth, log "received"-->  (Kafka event stream)
    |
    v
 [ Policy layer: OPA/Kyverno ]  --reject----> logged as "denied", stop
    |  approve
    v
 [ Provisioning/GitOps: Crossplane + ArgoCD ]
    |
    +--> [ Operator ] (stateful workload lifecycle)
    |
    +--> [ Scheduler plugin ] (cost/fragmentation-aware placement)
    |
    v
 [ Progressive delivery controller ] --canary + auto-rollback--> live traffic
    |
    v
 [ Chaos/resilience module ] --on demand--> SLO-based resilience score
    |
    v
 every step --> [ Audit/RBAC service ] --> hash-chained compliance log
```

## Repo layout

```
.
├── operator/              # Go, kubebuilder-scaffolded TenantDatabase operator
├── gitops/
│   ├── compositions/      # Crossplane Compositions and XRDs
│   └── policies/          # OPA/Kyverno policy definitions
├── scheduler-plugin/      # Go, Kubernetes scheduler framework plugin
├── delivery-controller/   # Go, canary rollout + rollback decision logic
├── chaos/                 # Go, fault injection + SLO scoring
├── audit-service/         # Java/Spring Boot, Kafka consumer, audit API
├── docs/
│   ├── SYSTEM_DESIGN.md
│   ├── API_REFERENCE.md
│   ├── METRICS.md
│   ├── ROADMAP.md
│   └── TESTING.md
└── README.md
```

## Getting started (local dev)

Prerequisites: `kind`, `kubectl`, Go 1.26+, Java 21+, Docker.

```bash
# 1. Bring up a local cluster; kind's default StorageClass must allow the
#    volume growth that TenantDatabase scaling relies on
kind create cluster --config kind-config.yaml
kubectl patch storageclass standard -p '{"allowVolumeExpansion":true}'

# 2. Install the TenantDatabase CRD
cd operator && make install

# 3. Run the operator locally against the kind cluster
make run

# 4. Stand up observability
kubectl apply -f gitops/observability/

# 5. Bootstrap ArgoCD; from here Git installs Crossplane, Kyverno, the
#    DatabaseClaim composition, the admission policies and tenant claims
kubectl apply -k gitops/argocd/install --server-side
kubectl -n argocd rollout status statefulset/argocd-application-controller
kubectl apply -f gitops/argocd/root.yaml
```

Request a database by committing a `DatabaseClaim` under `gitops/tenants/<team>/`; the org rules it must satisfy live in `gitops/policies`.

Full phase-by-phase build order, with exit criteria for each: [`docs/ROADMAP.md`](docs/ROADMAP.md).

## Docs index

- [`docs/SYSTEM_DESIGN.md`](docs/SYSTEM_DESIGN.md) — architecture, request flow, per-subsystem design problems
- [`docs/API_REFERENCE.md`](docs/API_REFERENCE.md) — CRD schemas, REST endpoints, event schema
- [`docs/METRICS.md`](docs/METRICS.md) — baseline vs. target per subsystem, measurement methodology, validation plan
- [`docs/ROADMAP.md`](docs/ROADMAP.md) — build phases and exit criteria
- [`docs/TESTING.md`](docs/TESTING.md) — testing strategy per subsystem

## Status

Phases 0 (foundation) and 1 (operator) are complete; Phase 2 (policy and GitOps) is next. See [`docs/ROADMAP.md`](docs/ROADMAP.md) for the full phase tracker.
