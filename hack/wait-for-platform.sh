#!/usr/bin/env bash
# Waits for what the e2e phases wait on, under one deadline, so a platform that never comes
# up fails once and names what is missing instead of every phase timing out in turn.
set -euo pipefail

TIMEOUT="${1:-900}"

DEPLOYMENTS=(
  monitoring/prometheus
  istio-system/istiod
  kiln-scheduler-system/kiln-scheduler
  kiln-operator-system/kiln-operator-controller-manager
  kiln-delivery-system/kiln-delivery-controller
  kiln-chaos-system/kiln-chaos-controller
  kiln-audit/kiln-audit-service
)
STATEFULSETS=(kiln-kafka/kafka kiln-audit/audit-postgres)
APPLICATIONS=(kiln-platform kafka kiln-audit kiln-operator kiln-delivery kiln-chaos kiln-scheduler)

field() {
  kubectl "$@" 2>/dev/null || true
}

at_least_one() {
  [ "${1:-0}" -ge 1 ] 2>/dev/null
}

missing() {
  local established
  established="$(field get xrd databaseclaims.platform.internal -o jsonpath='{.status.conditions[?(@.type=="Established")].status}')"
  [ "$established" = "True" ] || { echo "XRD databaseclaims.platform.internal is not Established"; return; }
  for d in "${DEPLOYMENTS[@]}"; do
    at_least_one "$(field -n "${d%/*}" get deployment "${d#*/}" -o jsonpath='{.status.availableReplicas}')" ||
      { echo "deployment $d has no available replica"; return; }
  done
  for s in "${STATEFULSETS[@]}"; do
    at_least_one "$(field -n "${s%/*}" get statefulset "${s#*/}" -o jsonpath='{.status.readyReplicas}')" ||
      { echo "statefulset $s has no ready replica"; return; }
  done
  at_least_one "$(field -n kiln-chaos-system get daemonset kiln-chaos-agent -o jsonpath='{.status.numberReady}')" ||
    { echo "daemonset kiln-chaos-system/kiln-chaos-agent has no ready pod"; return; }
  for app in "${APPLICATIONS[@]}"; do
    [ "$(field -n argocd get application "$app" -o jsonpath='{.status.sync.status}')" = "Synced" ] ||
      { echo "application $app is not Synced"; return; }
  done
}

deadline=$((SECONDS + TIMEOUT))
last=""
while reason="$(missing)"; [ -n "$reason" ]; do
  if [ "$reason" != "$last" ]; then
    echo "${SECONDS}s: $reason"
    last="$reason"
  fi
  if [ "$SECONDS" -ge "$deadline" ]; then
    echo "platform not ready after ${TIMEOUT}s: $reason" >&2
    kubectl -n argocd get applications -o wide >&2 || true
    exit 1
  fi
  sleep 10
done
echo "platform ready after ${SECONDS}s"
