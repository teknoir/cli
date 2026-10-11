# shellcheck shell=bash
# Case flow: prepare a tls dir in a helper container, run a rancher/k3s server
# (and optionally an agent) on the private network, decide READY / FAIL.

T_SERVER=${T_SERVER:-150}     # seconds until a server must be Ready
T_AGENT=${T_AGENT:-180}       # seconds until an agent node must be Ready
T_UPGRADE=${T_UPGRADE:-240}   # seconds for restarts / upgrades of a persistent data dir
FAIL_AFTER=${FAIL_AFTER:-45}  # 'CA cert validation failed' repeating this long = FAIL
HELPER_IMAGE=${HELPER_IMAGE:-debian:bookworm}
DATA_CT=/var/lib/rancher/k3s   # data dir path inside helper and k3s containers (S9 uses the host path)
CA_LIB=${TN_K3S_CA_LIB:-}

rand_hex() { head -c "${1:-16}" /dev/urandom | od -An -tx1 | tr -d ' \n'; }

# DNS-safe, <= 50 chars name
short_name() {
  local n=$1
  if [ "${#n}" -gt 50 ]; then n="${n:0:40}-$(printf '%s' "$n" | cksum | cut -d' ' -f1 | cut -c1-6)"; fi
  printf '%s' "$n"
}

# Mounts for helper containers working on one case: sets HM.
h_mounts() { # WORK GEN
  HM=(-v "$1/k3s:$DATA_CT" -v "$(gen_dir "$2"):/etc/teknoir:ro" -v "$HERE:/h:ro" -v "$OUT/bin:/tnbin:ro")
  if [ -n "$CA_LIB" ]; then HM+=(-v "$CA_LIB:/tn/tn-k3s-ca.sh:ro"); fi
  return 0
}

# ca_prepare CA GEN HELPER_IMG WORK SUFFIX: create DATA_DIR/server/tls the way the device scripts do.
ca_prepare() {
  local ca=$1 gen=$2 img=$3 work=$4 suffix=$5 tls=$DATA_CT/server/tls
  mkdir -p "$work/k3s" "$work/etc-rancher"
  h_mounts "$work" "$gen"
  local envs=(-e PRODUCT=k3s -e "_DEVICE_ID=${suffix%%.*}" -e "_DOMAIN=${suffix#*.}" -e CONFIG_PATH=/etc/teknoir -e "DATA_DIR=$DATA_CT")
  case $ca in
    legacy)
      d_helper "$img" "${HM[@]}" "${envs[@]}" -- sh -c ". /h/shim.sh; . /h/legacy-ca.sh; tn_legacy_ca $tls /etc/teknoir/rsa_public.pem /etc/teknoir/rsa_private.pem $suffix" ;;
    proper)
      d_helper "$img" "${HM[@]}" "${envs[@]}" -- sh -c ". /h/shim.sh; . /h/legacy-ca.sh; . /h/proper-ca.sh; tn_proper_ca $tls /etc/teknoir/rsa_public.pem /etc/teknoir/rsa_private.pem $suffix" ;;
    lib)
      d_helper "$img" "${HM[@]}" "${envs[@]}" -- sh -c ". /h/shim.sh; . /tn/tn-k3s-ca.sh; tn_k3s_ca_setup" ;;
    *) die "unknown CA mode '$ca'" ;;
  esac
}

# k3sverify prediction on the prepared tls dir; echoes ok|reject
ca_predict() { # GEN HELPER_IMG WORK
  h_mounts "$3" "$1"
  if d_helper "$2" "${HM[@]}" -- /tnbin/k3sverify -q "$DATA_CT/server/tls" >"$OUT/logs/$CASE.predict.log" 2>&1; then echo ok; else echo reject; fi
}

# ------------------------------------------------------------ waiting

kc() { local c=$1; shift; docker exec "$c" kubectl "$@"; }

srv_ready() { # CONTAINER
  [ "$(kc "$1" get --raw /readyz 2>/dev/null)" = ok ] &&
    kc "$1" get nodes --no-headers 2>/dev/null | awk '{print $2}' | grep -qx Ready
}

node_ready() { # SERVER_CONTAINER NODE
  [ "$(kc "$1" get node "$2" -o 'jsonpath={.status.conditions[?(@.type=="Ready")].status}' 2>/dev/null)" = True ]
}

ca_fail_count() { # CONTAINER SINCE
  docker logs --since "$2" "$1" 2>&1 | grep -c 'CA cert validation failed' || true
}

# wait_k3s KIND CONTAINER TIMEOUT SINCE [SERVER_CONTAINER NODE]
# KIND server|agent. Sets W_STATE: READY | FAIL (CA cert validation failed for > FAIL_AFTER s) | EXITED | TIMEOUT
wait_k3s() {
  local kind=$1 c=$2 timeout=$3 since=$4 srv=${5:-} node=${6:-}
  local t0 first=0 n code restarts=${WAIT_RESTARTS:-1}
  t0=$(now); W_STATE=TIMEOUT; W_NOTE=""; W_FLAKE=""
  while :; do
    if ! d_running "$c"; then
      code=$(docker inspect -f '{{.State.ExitCode}}' "$c" 2>/dev/null)
      if [ "$restarts" -gt 0 ]; then
        # k3s in privileged containers occasionally dies in a cgroup race when many start at once: start it once more
        restarts=$((restarts - 1))
        W_FLAKE="$kind container exited (code $code) and was started once more"
        log "$W_FLAKE: $(docker logs --tail 2 "$c" 2>&1 | tr '\n' ' ' | cut -c1-200)"
        docker start "$c" >/dev/null 2>&1 || true
        sleep 2; continue
      fi
      W_STATE=EXITED; W_NOTE="$kind container exited (code $code): $(docker logs --tail 3 "$c" 2>&1 | tr '\n' ' ' | cut -c1-300)"; return 0
    fi
    if [ "$kind" = server ]; then
      if srv_ready "$c"; then W_STATE=READY; W_NOTE=$W_FLAKE; return 0; fi
    else
      if node_ready "$srv" "$node"; then W_STATE=READY; W_NOTE=$W_FLAKE; return 0; fi
    fi
    n=$(ca_fail_count "$c" "$since")
    if [ "$n" -ge 1 ]; then
      [ "$first" -ne 0 ] || first=$(now)
      if [ $(( $(now) - first )) -gt "$FAIL_AFTER" ] && [ "$n" -ge 3 ]; then
        W_STATE=FAIL; W_NOTE="$kind: CA cert validation failed x$n"; return 0
      fi
    fi
    if [ $(( $(now) - t0 )) -ge "$timeout" ]; then
      W_STATE=TIMEOUT; W_NOTE="$kind not ready after ${timeout}s: $(docker logs --tail 2 "$c" 2>&1 | tr '\n' ' ' | cut -c1-300)"; return 0
    fi
    sleep 3
  done
}

save_logs() { # CONTAINER LABEL
  docker logs "$1" > "$OUT/logs/$CASE.$2.log" 2>&1 || true
}

# ------------------------------------------------------------ one server (+agent) case

# case_init: set the per-case globals used by the scenario helpers.
case_init() { # NAME
  CASE=$1; N=$(short_name "$1")
  WORK="$OUT/work/$N"; SRV_HOST="srv-$N"; AGT_HOST="agt-$N"
  SRV="$PREFIX-$N-srv"; AGT="$PREFIX-$N-agt"
  TOKEN=$(rand_hex 16)
  mkdir -p "$WORK/k3s" "$WORK/etc-rancher" "$WORK/agent" "$OUT/logs"
}

case_end() {
  if [ "${KEEP:-0}" = 1 ]; then log "kept $WORK and containers (--keep)"; return 0; fi
  docker rm -f -v "$SRV" "$AGT" >/dev/null 2>&1 || true
  d_rm_root "$WORK"
}

srv_up() { # TAG  -> start (or replace) the server container on the persistent data dir
  docker rm -f -v "$SRV" >/dev/null 2>&1 || true
  SINCE=$(now)
  d_k3s_server "$SRV" "$1" "$WORK" "$SRV_HOST" "$TOKEN"
}

agt_up() { # TAG [TRUST_BUNDLE]
  docker rm -f -v "$AGT" >/dev/null 2>&1 || true
  ASINCE=$(now)
  d_k3s_agent "$AGT" "$1" "$WORK" "$AGT_HOST" "$SRV_HOST" "$TOKEN" "${2:-}"
}

# run_case SUITE NAME K3S GEN CA [AGENT 0|1] [TRUST 0|1] [EXPECT]
run_case() {
  local suite=$1 name=$2 k3s=$3 gen=$4 ca=$5 agent=${6:-0} trust=${7:-0} expect=${8:-}
  local tag t0 outcome note="" predict
  tag=$(ver_tag "$k3s")
  [ -n "$expect" ] || expect=$(expected_outcome "$ca" "$gen" "$tag")
  case_init "$name"
  t0=$(now)
  log "case $name: k3s=$tag gen=$gen ca=$ca agent=$agent trust=$trust expect=$expect"
  if ! d_pull "$K3S_IMAGE:$tag" 2>/dev/null; then
    result_write "$suite" "$name" "$tag" "$gen" "$ca" "$expect" ERROR $(( $(now) - t0 )) "cannot pull $K3S_IMAGE:$tag"
    case_end; return 0
  fi
  if ! ca_prepare "$ca" "$gen" "$HELPER_IMAGE" "$WORK" "dev.test.local"; then
    result_write "$suite" "$name" "$tag" "$gen" "$ca" "$expect" ERROR $(( $(now) - t0 )) "CA preparation failed (see log)"
    case_end; return 0
  fi
  predict=$(ca_predict "$gen" "$HELPER_IMAGE" "$WORK")
  note="k3sverify=$predict"

  srv_up "$tag"
  wait_k3s server "$SRV" "$T_SERVER" "$SINCE"
  outcome=$W_STATE; [ -z "$W_NOTE" ] || note="$note; $W_NOTE"
  save_logs "$SRV" server

  if [ "$outcome" = READY ] && [ "$agent" = 1 ]; then
    local tb=""
    if [ "$trust" = 1 ]; then tb="$OUT/gen/$gen/os-trust.pem"; note="$note; agent-os-trust"; fi
    agt_up "$tag" "$tb"
    wait_k3s agent "$AGT" "$T_AGENT" "$ASINCE" "$SRV" "$AGT_HOST"
    if [ "$W_STATE" = READY ]; then outcome=READY; else outcome="AGENT-$W_STATE"; fi
    [ -z "$W_NOTE" ] || note="$note; $W_NOTE"
    save_logs "$AGT" agent
  fi

  # k3sverify is a replica: flag a prediction that disagrees with what k3s did
  case "$predict:$outcome" in
    ok:FAIL|reject:READY)
      if ver_is_postfix "$tag"; then note="$note; PREDICTION MISMATCH"; fi ;;
  esac
  result_write "$suite" "$name" "$tag" "$gen" "$ca" "$expect" "$outcome" $(( $(now) - t0 )) "$note"
  case_end
}

# ------------------------------------------------------------ persistent scenarios (upgrade / remediation)

tls_digest() { # prints a stable sha256 of all files under server/tls
  h_mounts "$WORK" "$1"
  d_helper "$(helper_image debian:bookworm)" "${HM[@]}" -e "DATA_CT=$DATA_CT" -- sh -c \
    'cd "$DATA_CT/server/tls" && find . -type f ! -path "./.ca/*" | sort | xargs sha256sum | sha256sum | cut -d" " -f1'
}

MARKER=0
marker_set() { MARKER=1; kc "$SRV" create configmap tn-marker --from-literal=k=v >/dev/null 2>&1 || kc "$SRV" get configmap tn-marker >/dev/null 2>&1; }
marker_ok()  { [ "$(kc "$SRV" get configmap tn-marker -o 'jsonpath={.data.k}' 2>/dev/null)" = v ]; }

# step: record one result row of a scenario. step SCEN LABEL TAG GEN CA EXPECT OUTCOME T0 [NOTE]
step() {
  local scen=$1 label=$2; shift 2
  result_write "$SUITE" "$scen:$label" "$@"
}

# Remediation hooks (WP2 fills them in; see README). Defaults assume the tn-k3s-ca.sh CLI.
remediate_hooks_ready() {
  if [ -z "${TN_REMEDIATE_PREPARE:-}" ] && [ -z "$CA_LIB" ]; then return 1; fi
  : "${TN_REMEDIATE_PREPARE:=sh /tn/tn-k3s-ca.sh remediate --phase prepare --data-dir /var/lib/rancher/k3s}"
  : "${TN_REMEDIATE_FINALIZE:=sh /tn/tn-k3s-ca.sh remediate --phase finalize --data-dir /var/lib/rancher/k3s}"
  return 0
}

# remediate GEN HELPER_IMG TAG_FOR_ROTATE: prepare (helper) -> rotate (docker exec) -> finalize (helper) -> restart.
# Sets R_NOTE; returns non-zero if a phase failed. Logs the exact rotate command (no secrets in it).
remediate() {
  local gen=$1 img=$2 rc=0 rotate
  R_NOTE=""
  h_mounts "$WORK" "$gen"
  # the helper sees the data dir at the same path as the k3s container
  d_helper "$img" "${HM[@]}" -e DATA_DIR=/var/lib/rancher/k3s -e K3S_BIN=k3s -- sh -c "$TN_REMEDIATE_PREPARE" || { R_NOTE="prepare failed"; return 1; }
  rotate=$(d_helper "$img" "${HM[@]}" -- sh -c 'f=$(ls -d /var/lib/rancher/k3s/server/.tn-ca-rotate-*/ROTATE_CMD 2>/dev/null | tail -1); [ -n "$f" ] && cat "$f"') || true
  if [ -z "$rotate" ]; then R_NOTE="prepare left no ROTATE_CMD"; return 1; fi
  log "rotate command: $rotate"
  if ! docker exec "$SRV" sh -c "$rotate"; then R_NOTE="rotate-ca failed"; rc=1; fi
  if [ "$rc" = 0 ]; then
    d_helper "$img" "${HM[@]}" -e DATA_DIR=/var/lib/rancher/k3s -- sh -c "$TN_REMEDIATE_FINALIZE" || { R_NOTE="finalize failed"; return 1; }
    SINCE=$(now); docker restart "$SRV" >/dev/null || { R_NOTE="restart failed"; return 1; }
  fi
  return $rc
}
