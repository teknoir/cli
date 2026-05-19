#!/usr/bin/env bash
# Delete a fixed list of device.kubeflow.org resources using kubectl.
# - Lowercases device names automatically.
# - Dry-run by default (prints the kubectl commands without executing).
# - Use --execute to actually perform deletions.
# - Optional: --context <ctx> to target a specific kube context.
# - Optional: --namespace <ns> to force using a specific namespace.
#   If not provided, the script attempts to auto-detect the namespace per device across all namespaces.

set -uo pipefail

# --- Configuration: list of devices to delete (dates intentionally omitted) ---
DEVICES=(
  "VMV3-A1168"
  "VMV3-A1169"
  "VMV3-A1172"
  "VMV3-A1173"
  "VMV3-A1174"
  "VMV3-A1175"
  "VMV3-A1176"
  "VMV3-A1177"
  "VMV3-A1178"
  "VMV3-A1184"
  "VMV3-A1185"
  "VMV3-A1187"
  "VMV3-A1188"
  "VMV3-A1189"
  "VMV3-A1194"
  "VMV3-A1196"
  "VMV3-A1197"
  "VMV3-A1200"
  "VMV3-A1201"
  "VMV3-A1202"
  "VMV3-A1186"
  "VMV3-A1203"
  "VMV3-A1205"
  "VMV3-A1207"
  "VMV3-A1208"
  "VMV3-A1210"
  "VMV3-A1211"
  "VMV3-A1213"
  "VMV3-A1219"
  "VMV3-A1220"
  "VMV3-A1221"
  "VMV3-A1223"
  "VMV3-A1225"
  "VMV3-A1228"
  "VMV3-A1229"
  "VMV3-A1230"
  "VMV3-A1232"
  "VMV3-A1235"
  "VMV3-A1236"
  "VMV3-A1237"
  "VMV3-A1238"
  "VMV3-A1239"
  "VMV3-A1240"
  "VMV3-A1245"
)

# --- CLI parsing ---
DRY_RUN=true
KUBE_CONTEXT=""
KUBE_NAMESPACE=""

usage() {
  cat <<EOF
Usage: $(basename "$0") [options]

Options:
  -x, --execute          Execute deletions (default is dry-run).
  -d, --dry-run          Dry-run only (prints commands). This is the default.
      --context <ctx>    Set kubectl context.
      --namespace <ns>   Set kubectl namespace (only if the CRD is namespaced).
  -h, --help             Show this help.

Notes:
- Device names provided in this script are converted to lowercase automatically.
- By default, this script does not perform destructive actions until --execute is provided.
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    -x|--execute)
      DRY_RUN=false
      shift
      ;;
    -d|--dry-run)
      DRY_RUN=true
      shift
      ;;
    --context)
      KUBE_CONTEXT=${2:-}
      if [[ -z "$KUBE_CONTEXT" ]]; then echo "--context requires a value" >&2; exit 2; fi
      shift 2
      ;;
    --namespace)
      KUBE_NAMESPACE=${2:-}
      if [[ -z "$KUBE_NAMESPACE" ]]; then echo "--namespace requires a value" >&2; exit 2; fi
      shift 2
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      echo "Unknown argument: $1" >&2
      usage
      exit 2
      ;;
  esac
done

# Compose optional kubectl args
CTX_ARG=()
[[ -n "$KUBE_CONTEXT" ]] && CTX_ARG=(--context "$KUBE_CONTEXT")

# Helper: resolve namespace for a given device (if namespaced) across all namespaces
# Returns empty string if not found.
resolve_namespace() {
  local name="$1"
  # Try to find the resource across all namespaces; suppress errors
  kubectl "${CTX_ARG[@]}" get device.kubeflow.org "$name" -A -o jsonpath='{.metadata.namespace}' 2>/dev/null || true
}

# Summary counters
success=0
failed=0
skipped=0
notfound=0

# Processing loop
for device in "${DEVICES[@]}"; do
  name_lc=$(printf '%s' "$device" | tr '[:upper:]' '[:lower:]')

  # Determine target namespace: use provided override, else auto-detect
  target_ns="${KUBE_NAMESPACE}"
  if [[ -z "$target_ns" ]]; then
    target_ns=$(resolve_namespace "$name_lc")
  fi

  # Build namespace arg if we have one
  NS_ARG=()
  [[ -n "$target_ns" ]] && NS_ARG=(-n "$target_ns")

  if [[ "$DRY_RUN" == "true" ]]; then
    if [[ -n "$target_ns" ]]; then
      echo "DRY-RUN: kubectl ${CTX_ARG[*]} delete device.kubeflow.org ${NS_ARG[*]} --ignore-not-found '$name_lc'"
    else
      echo "DRY-RUN: NOT FOUND in any namespace (or cluster-scoped?): '$name_lc'"
      ((notfound++))
    fi
    ((skipped++))
    continue
  fi

  if [[ -z "$target_ns" ]]; then
    echo "Skip: device not found across namespaces: $name_lc"
    ((notfound++))
    ((failed++))
    continue
  fi

  echo "Deleting device: $name_lc (namespace: $target_ns)"
  if kubectl "${CTX_ARG[@]}" delete device.kubeflow.org "${NS_ARG[@]}" --ignore-not-found "$name_lc"; then
    ((success++))
  else
    ((failed++))
  fi
done

# Summary
echo ""
echo "Summary:"
if [[ "$DRY_RUN" == "true" ]]; then
  echo "  Planned deletions: $skipped"
  echo "  Not found (dry-run): $notfound"
else
  echo "  Deleted: $success"
  echo "  Failed:  $failed"
  echo "  Not found: $notfound"
fi

# Exit code reflects failures if executing
if [[ "$DRY_RUN" == "false" && $failed -gt 0 ]]; then
  exit 1
fi
exit 0
