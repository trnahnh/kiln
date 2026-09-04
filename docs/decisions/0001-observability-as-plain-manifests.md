# ADR-0001: Observability is deployed as plain manifests

**Status:** Accepted, 2026-09-04

## Context

[`SYSTEM_DESIGN.md`](../SYSTEM_DESIGN.md#cross-cutting-observability) makes Prometheus and Grafana centrally owned: every subsystem exports to the same instance. Phase 0 has to stand them up on an empty `kind` cluster, and `README.md` installs them with `kubectl apply -f gitops/observability/`. The alternatives were the kube-prometheus manifest bundle (Prometheus Operator, Alertmanager, node-exporter, kube-state-metrics) or the kube-prometheus-stack Helm chart.

## Decision

`gitops/observability/` holds hand-written, flat Kubernetes manifests: a `monitoring` namespace, one Prometheus Deployment driven by a ConfigMap scrape config, one Grafana Deployment with a provisioned Prometheus datasource, ClusterIP Services, `emptyDir` storage, pinned image tags. No Prometheus Operator, no Helm.

Subsystems expose metrics by carrying the `prometheus.io/scrape`, `prometheus.io/port`, and `prometheus.io/path` pod annotations. Prometheus discovers them through Kubernetes service discovery; no per-subsystem `ServiceMonitor` or Prometheus configuration change is needed.

## Consequences

- `kubectl apply -f` works exactly as `README.md` states, with no Helm dependency.
- Later phases export metrics by annotating pods, not by shipping CRDs. Anything that would need `ServiceMonitor`, `PrometheusRule`, or Alertmanager must supersede this ADR.
- No persistence: metrics and dashboards are lost on pod restart. Acceptable for a local development cluster with nothing to observe yet; revisit when a phase's exit criterion depends on retained metrics.
- The scrape config is the single place where discovery rules live, so a change there affects every subsystem.
