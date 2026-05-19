#!/usr/bin/env bash
set -euo pipefail

NS="${1:-default}"
DEPLOY="${DEPLOY:-mongodb}"
SECRET="${SECRET:-mongodb-credentials}"

# Find a running pod owned by the deployment
POD="$(
  kubectl -n "$NS" get pods \
    -l "app.kubernetes.io/name=$DEPLOY" \
    -o jsonpath='{range .items[?(@.status.phase=="Running")]}{.metadata.name}{"\n"}{end}' \
  | head -n1
)"

if [[ -z "${POD:-}" ]]; then
  echo "No Running pod found in namespace '$NS' with label app.kubernetes.io/name=$DEPLOY"
  exit 1
fi

# Read secret fields (assumes keys: username, password)
USER="$(kubectl -n "$NS" get secret "$SECRET" -o jsonpath='{.data.username}' | base64 --decode)"
PASS="$(kubectl -n "$NS" get secret "$SECRET" -o jsonpath='{.data.password}' | base64 --decode)"

if [[ -z "${USER:-}" || -z "${PASS:-}" ]]; then
  echo "Secret '$SECRET' is missing 'username' or 'password' keys."
  exit 1
fi

echo "Connecting to mongosh in pod: $POD (ns: $NS) as user: $USER"
exec kubectl -n "$NS" exec -it "$POD" -- \
  mongosh "mongodb://$USER:$PASS@localhost:27017/admin?authSource=admin"