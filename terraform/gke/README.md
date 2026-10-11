# A GKE window for the scheduler and the canary controller

This creates the smallest GKE cluster on which the kiln scheduler can run as a second
scheduler next to GKE's own and the delivery controller can analyse a canary on real
Istio traffic, runs the two for a short window, and destroys everything. It has not been
applied: creating a cloud resource needs the project owner's approval, and every step
below is written so it can be read and approved before anything exists.

## What gets created

| Resource | Why | Hourly list price (us-central1) |
|---|---|---|
| 1 zonal GKE Standard cluster, REGULAR channel, logging and monitoring off | a second scheduler needs Standard, not Autopilot | $0.10 management fee, waived for one zonal cluster per billing account by the GKE free tier |
| 2 × e2-standard-2 on-demand nodes, 50 GB pd-balanced each | the on-demand pool the class filter must keep latency-class pods on | 2 × $0.067 = $0.134, plus 2 × 50 GB × $0.10/GB-month ≈ $0.014 |
| 2 × e2-standard-2 Spot nodes, 50 GB each | the preemptible pool | 2 × about $0.020 = $0.040 (Spot prices move; check before the run), plus disks ≈ $0.014 |
| 1 Artifact Registry repository with two images of about 50 MB | the scheduler and controller images | storage under $0.01 for the window |

Hourly total: about $0.20 with the management fee waived, about $0.30 without it.

## Cost estimate for the window the script runs

The script's window is 20 minutes of traffic plus about 25 minutes of create, deploy and
destroy, so under one hour of billing. **Estimate: $0.30 to $0.50; under $1 if the
management fee applies.** The ceiling the script enforces is two hours of nodes
(`destroy` runs from a trap on exit, and `gke-run.sh` refuses to start without the
`KILN_GKE_APPROVED=yes` marker), which caps a stuck run at about $0.60. Egress for two
image pushes of about 100 MB is under $0.02. Nothing here uses a load balancer, a
persistent volume or cloud logging.

Prices are list prices as known when this was written, not a quote: confirm them on
https://cloud.google.com/compute/vm-instance-pricing and
https://cloud.google.com/kubernetes-engine/pricing before approving, since Spot prices move
and the free-tier waiver depends on the billing account. They are the inputs to this
estimate, not measured spend. The run prints the project's billing export after teardown
if `gcloud billing` is enabled, which is the measured figure to report.

## The exact commands (stop here for approval)

```bash
export PROJECT=<gcp project id>            # billing enabled, Kubernetes Engine and Artifact Registry APIs on
gcloud auth login && gcloud auth application-default login
gcloud services enable container.googleapis.com artifactregistry.googleapis.com --project "$PROJECT"

terraform -chdir=terraform/gke init
terraform -chdir=terraform/gke plan -var "project_id=$PROJECT"   # read this: 1 cluster, 2 node pools, 1 repository
# approval point: nothing above creates a resource

KILN_GKE_APPROVED=yes PROJECT="$PROJECT" bash hack/gke-run.sh      # apply, deploy, 20-minute window, destroy
```

`hack/gke-run.sh` does, in order:

1. `terraform apply` with the plan above, then `gcloud container clusters get-credentials`.
2. Builds `kiln-scheduler` and `kiln-delivery-controller` from this checkout and pushes them to the repository.
3. Installs Prometheus and Jaeger from `gitops/observability/`, Istio 1.30.4 (`base` and `istiod`, the same charts `gitops/argocd/apps` pins) with Helm, the scheduler from `scheduler-plugin/config` and the delivery controller from `delivery-controller/config` with their images pointed at the repository and the Kafka and OTLP arguments removed (no audit pipeline on this cluster).
4. Runs `go test -tags=e2e -run 'TestPhase3|TestPhase4' ./e2e/` against the cluster: the Phase 3 test places the same workload through both schedulers and bills by node occupancy using the pools' labels; the Phase 4 test drives a fortio canary through Istio with a healthy and a broken version and asserts the rollback from traffic, not from the controller's status.
5. Records `kubectl get pods -A -o wide`, the scheduler's `Scheduled` events, every `CanaryRollout` status and the Prometheus series the analysis read, into `terraform/gke/results/<timestamp>/`.
6. `terraform destroy`, then verifies: `gcloud container clusters list` and `gcloud artifacts repositories list` show nothing with the `owner=kiln-round2` label, and `gcloud compute disks list` shows no disk from the pools.

## What this would and would not prove

It would show the plugin running as a second scheduler on a managed control plane with
real Spot nodes and real list prices in the labels, and the canary controller deciding
from Istio telemetry on a cloud network. It would not change the trace-replay findings:
four nodes and two e2e workloads are a correctness check, not a capacity study, and the
node-occupancy saving on four nodes is the same empty-cluster artefact the kind run
shows.

## If a run is cut short

`terraform destroy -var "project_id=$PROJECT"` from `terraform/gke/` removes everything
the state knows. If state is lost, delete by label:

```bash
gcloud container clusters delete kiln-r2 --zone us-central1-a --project "$PROJECT" --quiet
gcloud artifacts repositories delete kiln-r2 --location us-central1 --project "$PROJECT" --quiet
gcloud compute disks list --project "$PROJECT" --filter "labels.owner=kiln-round2"
```
