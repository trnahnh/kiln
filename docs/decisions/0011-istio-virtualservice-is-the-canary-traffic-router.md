# ADR-0011: Istio in sidecar mode routes canary traffic through a weighted VirtualService

**Status:** Accepted, 2026-09-05

## Context

[`SYSTEM_DESIGN.md`](../SYSTEM_DESIGN.md#4-progressive-delivery-controller) leaves the mesh open: Istio or Linkerd. The controller needs two things from it: a way to split one host's traffic between two pod sets by percentage, and per-version request counts and latency histograms in the central Prometheus without a scrape-config change, because [ADR-0001](0001-observability-as-plain-manifests.md) makes pod annotations the only discovery contract. Linkerd splits traffic with Gateway API `HTTPRoute` and exposes proxy metrics on a port the annotation contract does not discover, so it would need either a new scrape job or its own viz stack. Istio's ambient mode needs a waypoint proxy per namespace for L7 metrics. Istio's sidecar injector adds the `prometheus.io/scrape`, `prometheus.io/port` and `prometheus.io/path` annotations to every injected pod (metrics merging), and the standard `istio_requests_total` and `istio_request_duration_milliseconds` series carry the destination workload name.

## Decision

Istio 1.30 in sidecar mode, installed by ArgoCD from the pinned `base` and `istiod` Helm charts (`gitops/argocd/apps/istio-base.yaml`, `istiod.yaml`), the same way Crossplane and Kyverno are. Rollout namespaces opt in with `istio-injection=enabled`.

The controller routes with one `networking.istio.io/v1` `VirtualService` per rollout: host = the application's own Service, two weighted destinations, `<name>-primary` and `<name>-canary`, which are Services the controller owns. No `DestinationRule` subsets: two Services with disjoint pod selectors are simpler to read back and need no label conventions beyond the controller's role label. The VirtualService is the ground truth the tests read; `status.canaryWeight` mirrors it and is never the proof.

The mesh sits behind a two-method `mesh.Router` interface (`Ensure(route, canaryPercent)`, `Weights(route)`) and the reconciler never imports Istio types. Istio objects are written as unstructured resources, so the controller does not depend on the Istio client module.

## Consequences

- Traffic split applies at the *calling* sidecar. Only meshed clients see the weights; the end-to-end test drives load from an injected pod for that reason. Non-mesh clients keep hitting the application's Service, whose selector is untouched.
- Prometheus needs no change; the delivery controller reads `reporter="destination"` counters, so every request that reached a canary pod counts whichever client sent it.
- Linkerd or Gateway API routing would be a second `Router` implementation and a new ADR, not a change to the reconciler.
- istiod's HPA is disabled in the chart values because kind has no metrics-server and ArgoCD would report the HPA Degraded forever; the validator webhook's CA bundle and failure policy are ignored in diffs because istiod rewrites them at runtime.
