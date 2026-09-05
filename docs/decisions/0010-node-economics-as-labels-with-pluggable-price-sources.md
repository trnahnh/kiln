# ADR-0010: Node economics are node labels, resolved through pluggable price sources

**Status:** Accepted, 2026-09-06

## Context

The scoring model needs three facts per node: hourly cost, whether the capacity is reclaimable (spot), and how likely reclaim is. [`SYSTEM_DESIGN.md`](../SYSTEM_DESIGN.md#3-costgpu-aware-scheduler-plugin) names the AWS spot pricing API as the source, and ROADMAP Phase 3 lists spot-price integration in scope. The exit criterion, however, is measured on a kind cluster with no cloud behind it, and CI must never depend on cloud credentials. The grilling session proposed node annotations; kind can stamp labels on its nodes but not annotations.

## Decision

The contract is three node labels:

| Label | Values |
|---|---|
| `kiln.platform.internal/capacity-type` | `spot` or `on-demand` |
| `kiln.platform.internal/hourly-cost` | non-negative decimal, USD per hour |
| `kiln.platform.internal/preemption-risk` | optional, 0 to 1; defaults to 0.05 for spot, always 0 for on-demand |

The plugin resolves economics through a `pricing.Source` interface. `NodeLabels` reads the contract directly and is the source in kind, CI and the trace replay. The `aws` source maps EKS and Karpenter capacity-type labels and the instance type to the latest `DescribeSpotPriceHistory` price for spot nodes, an on-demand price table for the rest, and the public Spot Advisor interruption bucket (upper bound as a fraction) for risk, with ten-minute caches. It is unit-tested against a fake EC2 client and a fake advisor document and is never called live by the automated suite. A manual check behind the `liveaws` build tag confirms the fakes still match the real response shapes; it uses local credentials and is documented in `TESTING.md` as a step outside CI.

Unknown nodes fall back to on-demand at the highest known hourly cost.

## Consequences

- Labels rather than annotations: settable from `kind-config.yaml`, from cloud node templates, and usable as node selectors. The grilling wording said annotations; this ADR records the change and why.
- The exit criterion is independent of any cloud account; the same binary prices EKS nodes when the `aws` source is wired in.
- Ignorance is conservative: a node nobody labelled looks expensive and safe, so the scheduler neither prefers it for cost nor treats it as reclaimable.
- The on-demand price table is a static map inside the `aws` source; a Pricing API client would supersede that part of this ADR.
