#!/usr/bin/env bash
# How to use
#   Method 1: Auto-generate PVC list with label filtering (recommended):
# AUTO_GENERATE=1 NS=your-namespace OUT_DIR=./flows-backup ./backup-flows-from-pvcs.sh
#
#   Method 2: Manually create a file with PVC names:
#	  1.	Create a file with PVC names:
# kubectl get pvc -n your-namespace -o jsonpath='{range .items[*]}{.metadata.name}{"\n"}{end}' > pvcs.txt
#	  Or with label filtering:
# kubectl get pvc -n your-namespace -l teknoir.org/owner-app=devstudio -o jsonpath='{range .items[*]}{.metadata.name}{"\n"}{end}' > pvcs.txt
#   2.	Run the backup script:
# NS=your-namespace OUT_DIR=./flows-backup ./backup-flows-from-pvcs.sh pvcs.txt
set -euo pipefail

# -------- Config (override via env vars) --------
NS="${NS:-default}"
OUT_DIR="${OUT_DIR:-./backup-$(date +%Y%m%dT%H%M%S)}"
PATTERN="${PATTERN:-flows.json}"              # find -name pattern (glob-style)
MOUNT_PATH="${MOUNT_PATH:-/mnt/pvc}"
HELPER_IMAGE="${HELPER_IMAGE:-busybox:1.36.1}" # must contain: sh, find, tar, sleep
POD_WAIT_TIMEOUT="${POD_WAIT_TIMEOUT:-120s}"
KEEP_POD="${KEEP_POD:-0}"                     # set to 1 to keep helper pods for debugging
KUBECTL="${KUBECTL:-kubectl}"
AUTO_GENERATE="${AUTO_GENERATE:-0}"           # set to 1 to auto-generate PVC list from cluster
LABEL_SELECTOR="${LABEL_SELECTOR:-teknoir.org/owner-app=devstudio}"  # label selector for filtering PVCs

usage() {
  cat >&2 <<EOF
Usage: $0 <pvc_list_file> [namespace]
   or: AUTO_GENERATE=1 NS=your-namespace $0

Env overrides:
  NS=...             (default: $NS)
  OUT_DIR=...        (default: $OUT_DIR)
  PATTERN=...        (default: $PATTERN)
  MOUNT_PATH=...     (default: $MOUNT_PATH)
  HELPER_IMAGE=...   (default: $HELPER_IMAGE)
  POD_WAIT_TIMEOUT=... (default: $POD_WAIT_TIMEOUT)
  KEEP_POD=0|1       (default: $KEEP_POD)
  AUTO_GENERATE=0|1  (default: $AUTO_GENERATE) - auto-generate PVC list from cluster
  LABEL_SELECTOR=... (default: $LABEL_SELECTOR) - label selector for filtering PVCs

pvc_list_file: one PVC name per line (blank lines and # comments ignored)
               (not required if AUTO_GENERATE=1)
EOF
  exit 2
}

need_cmd() {
  command -v "$1" >/dev/null 2>&1 || { echo "ERROR: missing required command: $1" >&2; exit 1; }
}

hash10() {
  local s="$1"
  if command -v sha1sum >/dev/null 2>&1; then
    printf '%s' "$s" | sha1sum | awk '{print substr($1,1,10)}'
  elif command -v shasum >/dev/null 2>&1; then
    printf '%s' "$s" | shasum | awk '{print substr($1,1,10)}'
  else
    # fallback: no hash tool; use a truncated sanitized name + pid (best-effort)
    printf '%s' "${s//[^a-zA-Z0-9]/}-$$" | awk '{print substr($0,1,10)}'
  fi
}

CURRENT_POD=""
cleanup() {
  if [[ -n "${CURRENT_POD}" && "${KEEP_POD}" != "1" ]]; then
    "$KUBECTL" delete pod -n "$NS" "$CURRENT_POD" --ignore-not-found --wait=false >/dev/null 2>&1 || true
  fi
}
trap cleanup EXIT INT TERM

# -------- Args / deps --------
if [[ "$AUTO_GENERATE" == "1" ]]; then
  echo "Auto-generating PVC list from cluster..."
  echo "Namespace:      $NS"
  echo "Label selector: $LABEL_SELECTOR"

  PVC_LIST_FILE="$(mktemp)"
  trap 'rm -f "$PVC_LIST_FILE"; cleanup' EXIT INT TERM

  if ! "$KUBECTL" get pvc -n "$NS" -l "$LABEL_SELECTOR" -o jsonpath='{range .items[*]}{.metadata.name}{"\n"}{end}' > "$PVC_LIST_FILE" 2>/dev/null; then
    echo "ERROR: Failed to retrieve PVCs from cluster" >&2
    exit 1
  fi

  pvc_count=$(grep -c . "$PVC_LIST_FILE" 2>/dev/null || echo 0)
  if [[ "$pvc_count" -eq 0 ]]; then
    echo "WARNING: No PVCs found matching label selector: $LABEL_SELECTOR" >&2
    echo "DONE (nothing to backup)."
    exit 0
  fi

  echo "Found $pvc_count PVC(s)"
  echo
else
  [[ $# -lt 1 ]] && usage
  PVC_LIST_FILE="$1"
  [[ -f "$PVC_LIST_FILE" ]] || { echo "ERROR: pvc_list_file not found: $PVC_LIST_FILE" >&2; exit 1; }
  if [[ $# -ge 2 ]]; then NS="$2"; fi
fi

need_cmd "$KUBECTL"
need_cmd jq
need_cmd tar

mkdir -p "$OUT_DIR/$NS"

echo "Namespace:  $NS"
echo "Out dir:    $OUT_DIR"
echo "Pattern:    $PATTERN"
echo "Helper img: $HELPER_IMAGE"
echo

# -------- Main loop --------
while IFS= read -r pvc || [[ -n "$pvc" ]]; do
  [[ -z "$pvc" ]] && continue
  [[ "$pvc" =~ ^[[:space:]]*# ]] && continue

  pvc="$(echo "$pvc" | awk '{$1=$1;print}')"  # trim
  [[ -z "$pvc" ]] && continue

  echo "=== PVC: $pvc ==="

  dest_root="$OUT_DIR/$NS/$pvc"
  mkdir -p "$dest_root"

  if ! "$KUBECTL" get pvc -n "$NS" "$pvc" -o json > "$dest_root/__pvc.json" 2> "$dest_root/__pvc_err.txt"; then
    echo "  ! PVC not found or not readable: $pvc (see $dest_root/__pvc_err.txt)"
    continue
  fi
  rm -f "$dest_root/__pvc_err.txt" || true

  # Determine a node currently using this PVC (helps for RWO volumes).
  # Prefer Running pods, fall back to any scheduled pod.
  node="$(
    "$KUBECTL" get pods -n "$NS" -o json \
      | jq -r --arg pvc "$pvc" '
          .items[]
          | select(.status.phase=="Running")
          | select(.spec.volumes[]? | .persistentVolumeClaim?.claimName==$pvc)
          | .spec.nodeName
        ' | head -n1
  )"

  if [[ -z "$node" || "$node" == "null" ]]; then
    node="$(
      "$KUBECTL" get pods -n "$NS" -o json \
        | jq -r --arg pvc "$pvc" '
            .items[]
            | select(.spec.nodeName != null)
            | select(.spec.volumes[]? | .persistentVolumeClaim?.claimName==$pvc)
            | .spec.nodeName
          ' | head -n1
    )"
  fi

  if [[ -n "$node" && "$node" != "null" ]]; then
    echo "  Using node pinning (PVC appears in use on node): $node"
    node_block="  nodeName: $node"$'\n'
  else
    echo "  No consuming pod/node detected; helper pod will be scheduled normally."
    node_block=""
  fi

  # Helper pod name (stable, short, DNS-safe)
  pod="pvc-flows-backup-$(hash10 "$NS/$pvc")"
  CURRENT_POD="$pod"

  # Ensure no leftover
  "$KUBECTL" delete pod -n "$NS" "$pod" --ignore-not-found --wait=false >/dev/null 2>&1 || true

  # Create helper pod
  cat <<YAML | "$KUBECTL" apply -n "$NS" -f - >/dev/null
apiVersion: v1
kind: Pod
metadata:
  name: ${pod}
  labels:
    app: pvc-flows-backup
spec:
  restartPolicy: Never
${node_block}  containers:
  - name: helper
    image: ${HELPER_IMAGE}
    command: ["sh","-c","sleep 36000"]
    volumeMounts:
      - name: data
        mountPath: ${MOUNT_PATH}
        readOnly: true
  volumes:
    - name: data
      persistentVolumeClaim:
        claimName: ${pvc}
YAML

  # Wait for readiness
  if ! "$KUBECTL" wait -n "$NS" --for=condition=Ready "pod/$pod" --timeout="$POD_WAIT_TIMEOUT" >/dev/null 2>&1; then
    echo "  ! Helper pod did not become Ready (possible Multi-Attach / ImagePull / permissions)."
    "$KUBECTL" get pod -n "$NS" "$pod" -o wide > "$dest_root/__pod_wide.txt" 2>&1 || true
    "$KUBECTL" describe pod -n "$NS" "$pod" > "$dest_root/__pod_describe.txt" 2>&1 || true
    "$KUBECTL" get pod -n "$NS" "$pod" -o yaml > "$dest_root/__pod.yaml" 2>&1 || true

    if [[ "$KEEP_POD" != "1" ]]; then
      "$KUBECTL" delete pod -n "$NS" "$pod" --wait=false >/dev/null 2>&1 || true
    else
      echo "  (KEEP_POD=1) leaving pod $pod for inspection"
    fi
    CURRENT_POD=""
    continue
  fi

  # Record helper pod yaml (useful provenance)
  "$KUBECTL" get pod -n "$NS" "$pod" -o yaml > "$dest_root/__helper_pod.yaml" 2>/dev/null || true

  index="$dest_root/__index.tsv"
  printf "pvc\tpath\tbytes\tsha256\n" > "$index"

  found_any=0

  # Find candidate files
  while IFS= read -r f; do
    [[ -z "$f" ]] && continue
    found_any=1

    rel="${f#${MOUNT_PATH}/}"
    # If find returns the mount root itself (unlikely), skip
    [[ "$rel" == "$f" ]] && rel="${f#/}"  # best effort

    # Stream a single file out via tar, preserving directory structure under PVC
    mkdir -p "$dest_root/$(dirname "$rel")"

    if "$KUBECTL" exec -n "$NS" "$pod" -- sh -c 'cd "'"$MOUNT_PATH"'" && tar -cf - -- "$1"' sh "$rel" \
        | tar --no-same-owner -C "$dest_root" -xf -; then
      bytes="$(wc -c < "$dest_root/$rel" | awk '{print $1}')"
      if command -v sha256sum >/dev/null 2>&1; then
        sha="$(sha256sum "$dest_root/$rel" | awk '{print $1}')"
      elif command -v shasum >/dev/null 2>&1; then
        sha="$(shasum -a 256 "$dest_root/$rel" | awk '{print $1}')"
      else
        sha="(no-sha-tool)"
      fi
      printf "%s\t%s\t%s\t%s\n" "$pvc" "$rel" "$bytes" "$sha" >> "$index"
      echo "  + $rel"
    else
      echo "  ! Failed to copy: $rel"
      printf "%s\t%s\t%s\t%s\n" "$pvc" "$rel" "ERROR" "ERROR" >> "$index"
    fi
  done < <("$KUBECTL" exec -n "$NS" "$pod" -- sh -c "find '$MOUNT_PATH' -type f -name '$PATTERN' -print" 2>/dev/null || true)

  if [[ "$found_any" -eq 0 ]]; then
    echo "  (no matches for $PATTERN)"
  fi

  # Cleanup helper pod
  if [[ "$KEEP_POD" != "1" ]]; then
    "$KUBECTL" delete pod -n "$NS" "$pod" --wait=false >/dev/null 2>&1 || true
  else
    echo "  (KEEP_POD=1) leaving pod $pod"
  fi

  CURRENT_POD=""
  echo
done < "$PVC_LIST_FILE"

echo "DONE."
echo "Backup directory: $OUT_DIR/$NS/"