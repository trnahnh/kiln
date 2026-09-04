# Observability

Centrally owned Prometheus and Grafana for the whole platform. Design rationale: [`docs/SYSTEM_DESIGN.md`, cross-cutting observability](../../docs/SYSTEM_DESIGN.md#cross-cutting-observability). Why these are plain manifests rather than the Prometheus Operator or Helm: [ADR-0001](../../docs/decisions/0001-observability-as-plain-manifests.md).

Apply from the repo root:

```bash
kubectl apply -f gitops/observability/
kubectl -n monitoring rollout status deploy/prometheus deploy/grafana
```

Reach the UIs with port-forwards:

```bash
kubectl -n monitoring port-forward svc/prometheus 9090:9090   # http://localhost:9090
kubectl -n monitoring port-forward svc/grafana 3000:3000      # http://localhost:3000
```

Grafana allows anonymous read access; sign in as `admin` / `admin` to edit. Both the credentials and the `emptyDir` storage are for the local `kind` cluster only.

A subsystem exports metrics by annotating its pods:

```yaml
prometheus.io/scrape: "true"
prometheus.io/port: "8080"
prometheus.io/path: "/metrics"   # optional, defaults to /metrics
```
