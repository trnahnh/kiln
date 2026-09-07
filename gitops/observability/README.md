# Observability

Centrally owned Prometheus, Grafana and Jaeger for the whole platform. Design rationale: [`docs/SYSTEM_DESIGN.md`, cross-cutting observability](../../docs/SYSTEM_DESIGN.md#cross-cutting-observability). Why these are plain manifests rather than the Prometheus Operator or Helm: [ADR-0001](../../docs/decisions/0001-observability-as-plain-manifests.md); how one request becomes one trace: [ADR-0021](../../docs/decisions/0021-one-trace-per-request-via-cr-annotation-and-kafka-headers.md).

Apply from the repo root:

```bash
kubectl apply -f gitops/observability/
kubectl -n monitoring rollout status deploy/prometheus deploy/grafana deploy/jaeger
```

Reach the UIs with port-forwards:

```bash
kubectl -n monitoring port-forward svc/prometheus 9090:9090   # http://localhost:9090
kubectl -n monitoring port-forward svc/grafana 3000:3000      # http://localhost:3000
kubectl -n monitoring port-forward svc/jaeger 16686:16686     # http://localhost:16686
```

Subsystems export spans over OTLP gRPC to `jaeger.monitoring.svc:4317`; Grafana has Jaeger as a datasource next to Prometheus.

Grafana allows anonymous read access; sign in as `admin` / `admin` to edit. Both the credentials and the `emptyDir` storage are for the local `kind` cluster only.

A subsystem exports metrics by annotating its pods:

```yaml
prometheus.io/scrape: "true"
prometheus.io/port: "8080"
prometheus.io/path: "/metrics"   # optional, defaults to /metrics
```
