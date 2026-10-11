#!/usr/bin/env bash
# One short GKE window for the kiln scheduler and the delivery controller, then teardown.
# Read terraform/gke/README.md first: it has the resources, the prices and the estimate.
# Refuses to run without KILN_GKE_APPROVED=yes, so nothing is created by accident.
set -euo pipefail

: "${PROJECT:?set PROJECT to the GCP project id}"
if [ "${KILN_GKE_APPROVED:-}" != "yes" ]; then
  echo "refusing to create cloud resources: set KILN_GKE_APPROVED=yes after reading terraform/gke/README.md" >&2
  exit 2
fi

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
TF="$ROOT/terraform/gke"
ZONE="${ZONE:-us-central1-a}"
REGION="${REGION:-us-central1}"
CLUSTER="${CLUSTER:-kiln-r2}"
WINDOW_MINUTES="${WINDOW_MINUTES:-20}"
TAG="r2-$(date -u +%Y%m%dT%H%M%SZ)"
RESULTS="$TF/results/$TAG"
mkdir -p "$RESULTS"

teardown() {
  echo "== destroying $CLUSTER in $PROJECT"
  terraform -chdir="$TF" destroy -auto-approve -var "project_id=$PROJECT" -var "zone=$ZONE" -var "region=$REGION" -var "cluster_name=$CLUSTER" | tee "$RESULTS/destroy.log"
  echo "== verifying nothing is left"
  gcloud container clusters list --project "$PROJECT" --filter "resourceLabels.owner=kiln-round2" --format "value(name)" | tee "$RESULTS/clusters-after.txt"
  gcloud artifacts repositories list --project "$PROJECT" --location "$REGION" --filter "labels.owner=kiln-round2" --format "value(name)" | tee "$RESULTS/repositories-after.txt"
  gcloud compute disks list --project "$PROJECT" --filter "labels.owner=kiln-round2" --format "value(name)" | tee "$RESULTS/disks-after.txt"
  if [ -s "$RESULTS/clusters-after.txt" ] || [ -s "$RESULTS/repositories-after.txt" ] || [ -s "$RESULTS/disks-after.txt" ]; then
    echo "teardown left resources behind; see $RESULTS" >&2
    exit 1
  fi
  echo "teardown verified: no cluster, repository or disk labelled owner=kiln-round2 remains"
}
trap teardown EXIT

# A stuck run is cut after two hours of nodes.
( sleep 7200 && echo "two-hour ceiling reached" && kill -TERM $$ ) &
WATCHDOG=$!
trap 'kill $WATCHDOG 2>/dev/null || true; teardown' EXIT

echo "== creating"
terraform -chdir="$TF" init -input=false
terraform -chdir="$TF" apply -auto-approve -input=false -var "project_id=$PROJECT" -var "zone=$ZONE" -var "region=$REGION" -var "cluster_name=$CLUSTER" | tee "$RESULTS/apply.log"
REGISTRY="$(terraform -chdir="$TF" output -raw registry)"
gcloud container clusters get-credentials "$CLUSTER" --zone "$ZONE" --project "$PROJECT"
gcloud auth configure-docker "$REGION-docker.pkg.dev" --quiet

echo "== images"
for module in scheduler-plugin delivery-controller; do
  image="kiln-$( [ "$module" = scheduler-plugin ] && echo scheduler || echo delivery-controller )"
  docker build -t "$REGISTRY/$image:$TAG" -f "$ROOT/$module/Dockerfile" "$ROOT"
  docker push "$REGISTRY/$image:$TAG"
done

echo "== observability and mesh"
kubectl apply -f "$ROOT/gitops/observability/"
kubectl -n monitoring rollout status deploy/prometheus deploy/jaeger --timeout=300s
helm repo add istio https://istio-release.storage.googleapis.com/charts >/dev/null
helm repo update >/dev/null
kubectl create namespace istio-system --dry-run=client -o yaml | kubectl apply -f -
helm upgrade --install istio-base istio/base --version 1.30.4 -n istio-system --wait
helm upgrade --install istiod istio/istiod --version 1.30.4 -n istio-system --wait

echo "== kiln scheduler as a second scheduler"
kubectl apply -k "$ROOT/scheduler-plugin/config"
kubectl -n kiln-scheduler-system set image deploy/kiln-scheduler "*=$REGISTRY/kiln-scheduler:$TAG"
kubectl -n kiln-scheduler-system rollout status deploy/kiln-scheduler --timeout=300s

echo "== delivery controller without the audit pipeline"
kubectl apply -f "$ROOT/delivery-controller/config/crd/bases"
kubectl apply -k "$ROOT/delivery-controller/config/manager"
kubectl -n kiln-delivery-system set image deploy/kiln-delivery-controller "*=$REGISTRY/kiln-delivery-controller:$TAG"
kubectl -n kiln-delivery-system patch deploy/kiln-delivery-controller --type json -p '[{"op":"replace","path":"/spec/template/spec/containers/0/args","value":["--prometheus-url=http://prometheus.monitoring.svc:9090"]}]'
kubectl -n kiln-delivery-system rollout status deploy/kiln-delivery-controller --timeout=300s

echo "== the window: Phase 3 placement and Phase 4 canary against real traffic"
kubectl get nodes -o wide --show-labels | tee "$RESULTS/nodes.txt"
( cd "$ROOT/e2e" && timeout "$((WINDOW_MINUTES * 60))" go test -tags=e2e -run 'TestPhase3|TestPhase4' ./... -v -count=1 -timeout "${WINDOW_MINUTES}m" ) 2>&1 | tee "$RESULTS/e2e.log" || echo "e2e exited non-zero; see $RESULTS/e2e.log"

echo "== evidence"
kubectl get pods -A -o wide | tee "$RESULTS/pods.txt"
kubectl get events -A --field-selector reason=Scheduled -o json > "$RESULTS/scheduled-events.json"
kubectl get canaryrollouts -A -o yaml > "$RESULTS/canaryrollouts.yaml" || true
kubectl -n monitoring port-forward svc/prometheus 9090:9090 >/dev/null 2>&1 &
PF=$!
sleep 3
curl -s 'http://localhost:9090/api/v1/query?query=istio_requests_total' > "$RESULTS/istio_requests_total.json" || true
kill $PF 2>/dev/null || true
echo "results in $RESULTS; teardown follows"
