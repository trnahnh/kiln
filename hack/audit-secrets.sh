#!/usr/bin/env bash
# Creates what the audit service needs but Git must not hold: the JWT verification key and
# the Postgres credentials (DATA_MODEL.md, "Write boundary"). Run before applying
# gitops/argocd/root.yaml. Idempotent: the key pair is kept once generated, and the Postgres
# Secret is never rewritten, because the roles in a persisted database keep their passwords.
set -euo pipefail

cd "$(dirname "$0")/.."
KEYS_DIR="hack/keys"
PRIVATE="$KEYS_DIR/audit-jwt-private.pem"
PUBLIC="$KEYS_DIR/audit-jwt-public.pem"
NS="kiln-audit"

mkdir -p "$KEYS_DIR"
if [ ! -f "$PRIVATE" ]; then
  openssl genpkey -algorithm RSA -pkeyopt rsa_keygen_bits:2048 -out "$PRIVATE" 2>/dev/null
  echo "generated $PRIVATE"
fi
if [ ! -f "$PUBLIC" ]; then
  openssl pkey -in "$PRIVATE" -pubout -out "$PUBLIC"
  echo "generated $PUBLIC"
fi

kubectl get namespace "$NS" >/dev/null 2>&1 || kubectl create namespace "$NS"

if kubectl -n "$NS" get secret audit-postgres >/dev/null 2>&1; then
  echo "secret audit-postgres exists, keeping it"
else
  kubectl -n "$NS" create secret generic audit-postgres \
    --from-literal=postgres-password="$(openssl rand -hex 16)" \
    --from-literal=writer-password="$(openssl rand -hex 16)" \
    --from-literal=reader-password="$(openssl rand -hex 16)"
fi

kubectl -n "$NS" create secret generic audit-jwt \
  --from-file=public.pem="$PUBLIC" --dry-run=client -o yaml | kubectl apply -f -
