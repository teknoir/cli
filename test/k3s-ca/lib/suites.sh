# shellcheck shell=bash
# Suites: each builds its list of cases and hands them to the job pool.

V_ALL_DEFAULT="v1.34.11 v1.34.12 v1.35.9 v1.36.5 v1.37.1 stable"
V_AGENT_DEFAULT="v1.34.11 v1.34.12 v1.36.5 v1.37.1 stable"
V_CERT_DEFAULT="v1.34.11 v1.36.5 v1.37.1 stable"
REMED_HELPERS_DEFAULT="ubuntu:20.04 debian:bookworm"

# versions_for DEFAULT_WORDS -> resolved, deduplicated tags (user --versions wins)
versions_for() {
  # shellcheck disable=SC2086
  if [ -n "${VERSIONS_OPT:-}" ]; then resolve_versions $VERSIONS_OPT; else resolve_versions $1; fi
}
gens_for() { if [ -n "${GENS_OPT:-}" ]; then echo "$GENS_OPT"; else echo "$1"; fi; }

need_lib() { # SUITE -> 0 if the library is available, else records a SKIP row
  if [ -z "$CA_LIB" ] || [ ! -f "$CA_LIB" ]; then
    log "suite $1 needs the CA library: set TN_K3S_CA_LIB=/path/to/tn-k3s-ca.sh (skipped)"
    result_write "$1" "$1:needs-lib" - - lib READY SKIP 0 "TN_K3S_CA_LIB not set"
    return 1
  fi
  return 0
}
need_hooks() {
  if ! remediate_hooks_ready; then
    log "suite $1 needs TN_REMEDIATE_PREPARE/TN_REMEDIATE_FINALIZE or TN_K3S_CA_LIB (skipped)"
    result_write "$1" "$1:needs-hooks" - - lib READY SKIP 0 "remediation hooks not set"
    return 1
  fi
  return 0
}

# ------------------------------------------------------------ S1 control: legacy vs proper
suite_control() {
  SUITE=control
  local v g
  for g in $(gens_for "$GEN_LEGACY"); do
    for v in $(versions_for "$V_ALL_DEFAULT"); do
      pool_run "ctl-$g-legacy-$(ver_slug "$v")" run_case control "ctl-$g-legacy-$(ver_slug "$v")" "$v" "$g" legacy
      pool_run "ctl-$g-proper-$(ver_slug "$v")" run_case control "ctl-$g-proper-$(ver_slug "$v")" "$v" "$g" proper
    done
  done
}

# ------------------------------------------------------------ S2 fresh: the library on every version
suite_fresh() {
  SUITE=fresh
  need_lib fresh || return 0
  local v g
  for g in $(gens_for "$GEN_LEGACY"); do
    for v in $(versions_for "$V_ALL_DEFAULT"); do
      pool_run "fresh-$g-lib-$(ver_slug "$v")" run_case fresh "fresh-$g-lib-$(ver_slug "$v")" "$v" "$g" lib
    done
  done
}

# ------------------------------------------------------------ S3 legacy scripts + fixed device certs
suite_legacy_with_cert() {
  SUITE=legacy-with-cert
  local v g
  for g in $(gens_for "$GEN_FIXED"); do
    for v in $(versions_for "$V_CERT_DEFAULT"); do
      pool_run "lwc-$g-legacy-$(ver_slug "$v")" run_case legacy-with-cert "lwc-$g-legacy-$(ver_slug "$v")" "$v" "$g" legacy
    done
  done
}

# ------------------------------------------------------------ S4 server + agent
suite_agent() {
  SUITE=agent
  local ca=$AGENT_CA v g t trusts
  if [ -z "$ca" ]; then if [ -n "$CA_LIB" ] && [ -f "$CA_LIB" ]; then ca=lib; else ca=proper; log "agent suite: no TN_K3S_CA_LIB, using the proper-root control (--ca-mode)"; fi; fi
  if [ "$ca" = lib ]; then need_lib agent || return 0; fi
  case $AGENT_TRUST in both) trusts="0 1" ;; yes) trusts=1 ;; no) trusts=0 ;; *) die "--agent-os-trust must be yes|no|both" ;; esac
  for g in $(gens_for "bs-o111 go-sha1"); do
    for v in $(versions_for "$V_AGENT_DEFAULT"); do
      for t in $trusts; do
        local n="agent-$g-$ca-$(ver_slug "$v")-$([ "$t" = 1 ] && echo ostrust || echo plain)"
        pool_run "$n" run_case agent "$n" "$v" "$g" "$ca" 1 "$t"
      done
    done
  done
}

# ------------------------------------------------------------ S5-S7 remediation
upgrade_step() { # SCEN LABEL TAG GEN T0 -> swap images on the same dirs; server then agent
  local scen=$1 label=$2 tag=$3 gen=$4 t0=$5 note=""
  d_pull "$K3S_IMAGE:$tag" || { step "$scen" "$label" "$tag" "$gen" lib READY ERROR $(( $(now) - t0 )) "cannot pull"; return 1; }
  srv_up "$tag"
  wait_k3s server "$SRV" "$T_UPGRADE" "$SINCE"
  if [ "$W_STATE" != READY ]; then
    step "$scen" "$label" "$tag" "$gen" lib READY "$W_STATE" $(( $(now) - t0 )) "$W_NOTE"; return 1
  fi
  if [ "$MARKER" = 1 ] && ! marker_ok; then note="marker ConfigMap MISSING"; fi
  if docker inspect "$AGT" >/dev/null 2>&1; then
    agt_up "$tag"
    wait_k3s agent "$AGT" "$T_AGENT" "$ASINCE" "$SRV" "$AGT_HOST"
    if [ "$W_STATE" != READY ]; then
      step "$scen" "$label" "$tag" "$gen" lib READY "AGENT-$W_STATE" $(( $(now) - t0 )) "$W_NOTE"; return 1
    fi
  fi
  if [ -n "$note" ]; then step "$scen" "$label" "$tag" "$gen" lib READY ERROR $(( $(now) - t0 )) "$note"; return 1; fi
  step "$scen" "$label" "$tag" "$gen" lib READY READY $(( $(now) - t0 )) "$([ "$MARKER" = 1 ] && echo 'marker ok')$(docker inspect "$AGT" >/dev/null 2>&1 && echo ', agent ok')"
}

# S5: bootstrapped on pre-fix k3s with the legacy CA, remediate, upgrade through the fix.
scenario_s5() { # SCEN GEN HELPER_IMG
  local scen=$1 gen=$2 img=$3 t0 pre=v1.34.11-k3s1 note
  case_init "$scen"; t0=$(now)
  d_pull "$K3S_IMAGE:$pre" || true
  ca_prepare legacy "$gen" "$img" "$WORK" "dev.test.local" || { step "$scen" 1-legacy-bootstrap "$pre" "$gen" legacy READY ERROR 0 "CA preparation failed"; case_end; return 0; }
  srv_up "$pre"; wait_k3s server "$SRV" "$T_SERVER" "$SINCE"
  if [ "$W_STATE" != READY ]; then step "$scen" 1-legacy-bootstrap "$pre" "$gen" legacy READY "$W_STATE" $(( $(now) - t0 )) "$W_NOTE"; save_logs "$SRV" server; case_end; return 0; fi
  marker_set; agt_up "$pre"; wait_k3s agent "$AGT" "$T_AGENT" "$ASINCE" "$SRV" "$AGT_HOST"
  step "$scen" 1-legacy-bootstrap "$pre" "$gen" legacy READY "$([ "$W_STATE" = READY ] && echo READY || echo "AGENT-$W_STATE")" $(( $(now) - t0 )) "marker set, helper=$img"
  [ "$W_STATE" = READY ] || { case_end; return 0; }

  # 2+3 remediate (pre-fix k3s keeps running; rotate through docker exec; restart)
  t0=$(now); note=""
  if remediate "$gen" "$img"; then
    wait_k3s server "$SRV" "$T_UPGRADE" "$SINCE"
    if [ "$W_STATE" = READY ] && marker_ok && node_ready "$SRV" "$AGT_HOST"; then
      step "$scen" 2-remediate-ready "$pre" "$gen" lib READY READY $(( $(now) - t0 )) "marker ok, agent ok"
    else
      step "$scen" 2-remediate-ready "$pre" "$gen" lib READY "${W_STATE/READY/DEGRADED}" $(( $(now) - t0 )) "$W_NOTE"; save_logs "$SRV" server; case_end; return 0
    fi
  else
    step "$scen" 2-remediate-ready "$pre" "$gen" lib READY ERROR $(( $(now) - t0 )) "$R_NOTE"; save_logs "$SRV" server; case_end; return 0
  fi

  t0=$(now)
  upgrade_step "$scen" 3-upgrade-1.34.12 v1.34.12-k3s1 "$gen" "$t0" || { save_logs "$SRV" server; case_end; return 0; }
  t0=$(now)
  if ! upgrade_step "$scen" 4-upgrade-1.36.5 v1.36.5-k3s1 "$gen" "$t0"; then
    log "direct 1.34 -> 1.36 failed, going through v1.35.9"
    t0=$(now)
    upgrade_step "$scen" 4a-upgrade-1.35.9 v1.35.9-k3s1 "$gen" "$t0" &&
      { t0=$(now); upgrade_step "$scen" 4b-upgrade-1.36.5 v1.36.5-k3s1 "$gen" "$t0"; } || { save_logs "$SRV" server; case_end; return 0; }
  fi

  # 6 check returns 0 ; 7 second remediate is a no-op
  t0=$(now); h_mounts "$WORK" "$gen"
  if d_helper "$img" "${HM[@]}" -e DATA_DIR=/var/lib/rancher/k3s -- sh -c "$TN_REMEDIATE_CHECK" >"$OUT/logs/$CASE.check.log" 2>&1; then
    step "$scen" 5-check "-" "$gen" lib 0 0 $(( $(now) - t0 )) "check exit 0"
  else
    step "$scen" 5-check "-" "$gen" lib 0 "$?" $(( $(now) - t0 )) "check did not return 0"
  fi
  t0=$(now)
  local d1 d2
  d1=$(tls_digest "$gen")
  d_helper "$img" "${HM[@]}" -e DATA_DIR=/var/lib/rancher/k3s -- sh -c "$TN_REMEDIATE_PREPARE" >"$OUT/logs/$CASE.noop.log" 2>&1 || true
  d2=$(tls_digest "$gen")
  if [ "$d1" = "$d2" ]; then step "$scen" 6-second-remediate-noop "-" "$gen" lib UNCHANGED UNCHANGED $(( $(now) - t0 )) "tls sha256 stable"
  else step "$scen" 6-second-remediate-noop "-" "$gen" lib UNCHANGED CHANGED $(( $(now) - t0 )) "tls digest changed"; fi
  save_logs "$SRV" server
  case_end
}

# S6 (bricked after upgrade) and S7 (hung fresh bootstrap on post-fix k3s).
# MODE brick: legacy on v1.34.11 -> Ready -> swap to v1.36.5 -> FAIL -> remediate on the post-fix server.
# MODE hung:  legacy directly on v1.36.5 -> FAIL -> remediate.
# With FORCE_FALLBACK=1 (5th argument) the in-place remediation is skipped, to exercise the
# documented downgrade fallback itself.
scenario_postfix() { # SCEN MODE GEN HELPER_IMG [FORCE_FALLBACK]
  local scen=$1 mode=$2 gen=$3 img=$4 force=${5:-0} t0 pre=v1.34.11-k3s1 post=v1.36.5-k3s1 ok=0
  case_init "$scen"; t0=$(now)
  d_pull "$K3S_IMAGE:$post" || true
  ca_prepare legacy "$gen" "$img" "$WORK" "dev.test.local" || { step "$scen" 1-bootstrap - "$gen" legacy READY ERROR 0 "CA preparation failed"; case_end; return 0; }
  if [ "$mode" = brick ]; then
    srv_up "$pre"; wait_k3s server "$SRV" "$T_SERVER" "$SINCE"
    step "$scen" 1-prefix-ready "$pre" "$gen" legacy READY "$W_STATE" $(( $(now) - t0 )) "$W_NOTE"
    [ "$W_STATE" = READY ] || { case_end; return 0; }
    marker_set
  fi
  t0=$(now); srv_up "$post"; wait_k3s server "$SRV" "$T_SERVER" "$SINCE"
  step "$scen" 2-postfix-rejects "$post" "$gen" legacy FAIL "$W_STATE" $(( $(now) - t0 )) "$W_NOTE"
  [ "$W_STATE" = FAIL ] || { save_logs "$SRV" server; case_end; return 0; }

  # remediate through the running post-fix server (SSL_CERT_FILE path). A hung fresh bootstrap
  # (mode hung) never initialised its datastore: nothing to rotate, the PKI is regenerated.
  t0=$(now)
  if [ "$force" = 1 ]; then
    log "FORCE_FALLBACK: skipping the in-place remediation"
  elif remediate "$gen" "$img" "$([ "$mode" = hung ] && echo 0 || echo 1)"; then
    wait_k3s server "$SRV" "$T_UPGRADE" "$SINCE"
    if [ "$W_STATE" = READY ]; then
      ok=1
      local m=""; [ "$mode" = brick ] && { marker_ok && m="marker ok" || m="marker MISSING"; }
      [ "$mode" != hung ] || m="$R_NOTE"
      step "$scen" 3-remediate-postfix "$post" "$gen" lib READY READY $(( $(now) - t0 )) "post-fix server. $m"
      t0=$(now); h_mounts "$WORK" "$gen"
      if d_helper "$img" "${HM[@]}" -e DATA_DIR=/var/lib/rancher/k3s -- sh -c "$TN_REMEDIATE_CHECK" >"$OUT/logs/$CASE.check.log" 2>&1; then
        step "$scen" 3b-check "-" "$gen" lib 0 0 $(( $(now) - t0 )) "check exit 0"
      else
        step "$scen" 3b-check "-" "$gen" lib 0 "$?" $(( $(now) - t0 )) "check did not return 0"
      fi
    else
      step "$scen" 3-remediate-postfix "$post" "$gen" lib READY "$W_STATE" $(( $(now) - t0 )) "$W_NOTE"
    fi
  else
    step "$scen" 3-remediate-postfix "$post" "$gen" lib READY ERROR $(( $(now) - t0 )) "$R_NOTE"
  fi

  if [ "$ok" = 0 ]; then
    # documented fallback: downgrade to the pre-fix patch of the same minor, remediate, upgrade
    local fb; fb=$(ver_prefix_of "$post")-k3s1
    t0=$(now); d_pull "$K3S_IMAGE:$fb" || true
    srv_up "$fb"; wait_k3s server "$SRV" "$T_UPGRADE" "$SINCE"
    step "$scen" 4-fallback-downgrade "$fb" "$gen" legacy READY "$W_STATE" $(( $(now) - t0 )) "$W_NOTE"
    if [ "$W_STATE" = READY ] && remediate "$gen" "$img"; then
      wait_k3s server "$SRV" "$T_UPGRADE" "$SINCE"
      step "$scen" 5-fallback-remediate "$fb" "$gen" lib READY "$W_STATE" $(( $(now) - t0 )) "$W_NOTE"
      upgrade_step "$scen" 6-fallback-upgrade "$post" "$gen" "$(now)" || true
    fi
  fi
  save_logs "$SRV" server
  case_end
}

suite_remediation() {
  SUITE=remediation
  need_hooks remediation || return 0
  : "${TN_REMEDIATE_CHECK:=sh /tn/tn-k3s-ca.sh check --data-dir /var/lib/rancher/k3s}"
  local g h
  for g in $(gens_for "bs-o111 go-sha1 expired"); do
    if ! gen_remediable "$g"; then
      result_write remediation "rem-s5-$g:skipped" - "$g" lib READY SKIP 0 "device cert has keyCertSign: the old scripts already give a root k3s accepts, nothing to remediate"
      continue
    fi
    for h in ${HELPERS_OPT:-$REMED_HELPERS_DEFAULT}; do
      local hi; hi=$(helper_image "$h")
      pool_run "rem-s5-$g-$(echo "$h" | tr ':.' '--')" scenario_s5 "rem-s5-$g-$(echo "$h" | tr ':.' '--')" "$g" "$hi"
    done
  done
  local g1; g1=$(gens_for bs-o111); g1=${g1%% *}
  if ! gen_remediable "$g1"; then
    result_write remediation "rem-s6-s7:skipped" - "$g1" lib READY SKIP 0 "device cert has keyCertSign: nothing to remediate"
    return 0
  fi
  pool_run "rem-s6-bricked" scenario_postfix rem-s6-bricked brick "$g1" "$(helper_image debian:bookworm)"
  # the other certificate kinds on a post-fix server too: go-sha1 (keyUsage without keyCertSign) and
  # expired roots need rotate-ca --force, which must work through the SSL_CERT_FILE path as well
  local g6
  for g6 in $(gens_for "go-sha1 expired"); do
    [ "$g6" = "$g1" ] || ! gen_remediable "$g6" || pool_run "rem-s6-bricked-$g6" scenario_postfix "rem-s6-bricked-$g6" brick "$g6" "$(helper_image debian:bookworm)"
  done
  pool_run "rem-s7-hung" scenario_postfix rem-s7-hung hung "$g1" "$(helper_image debian:bookworm)"
  # the documented fallback, forced: pre-fix patch of the minor -> remediate -> upgrade again
  pool_run "rem-s6-fallback" scenario_postfix rem-s6-fallback brick "$g1" "$(helper_image debian:bookworm)" 1
}

# ------------------------------------------------------------ S8 re-run on an initialised data dir
scenario_rerun() { # SCEN GEN TAG
  local scen=$1 gen=$2 tag=$3 t0 d1 d2 rc=0
  case_init "$scen"; t0=$(now)
  d_pull "$K3S_IMAGE:$tag" || true
  ca_prepare lib "$gen" "$HELPER_IMAGE" "$WORK" "dev.test.local" || { step "$scen" 1-setup "$tag" "$gen" lib READY ERROR 0 "first setup failed"; case_end; return 0; }
  srv_up "$tag"; wait_k3s server "$SRV" "$T_SERVER" "$SINCE"
  step "$scen" 1-first-start "$tag" "$gen" lib READY "$W_STATE" $(( $(now) - t0 )) "$W_NOTE"
  [ "$W_STATE" = READY ] || { save_logs "$SRV" server; case_end; return 0; }
  t0=$(now)
  docker rm -f -v "$SRV" >/dev/null   # a re-run of the bootstrap restarts k3s anyway
  d1=$(tls_digest "$gen")
  ca_prepare lib "$gen" "$HELPER_IMAGE" "$WORK" "dev.test.local" >"$OUT/logs/$CASE.rerun.log" 2>&1 || rc=$?
  d2=$(tls_digest "$gen")
  if [ "$rc" = 0 ] && [ "$d1" = "$d2" ]; then step "$scen" 2-rerun-keeps-pki "-" "$gen" lib UNCHANGED UNCHANGED $(( $(now) - t0 )) "setup rc=0, tls sha256 unchanged"
  else step "$scen" 2-rerun-keeps-pki "-" "$gen" lib UNCHANGED "$([ "$rc" = 0 ] && echo CHANGED || echo "RC-$rc")" $(( $(now) - t0 )) "rerun rc=$rc"; fi
  t0=$(now); srv_up "$tag"; wait_k3s server "$SRV" "$T_UPGRADE" "$SINCE"
  step "$scen" 3-restart-ready "$tag" "$gen" lib READY "$W_STATE" $(( $(now) - t0 )) "$W_NOTE"
  save_logs "$SRV" server
  case_end
}

suite_rerun() {
  SUITE=rerun
  need_lib rerun || return 0
  local g v; g=$(gens_for bs-o111); g=${g%% *}
  for v in $(versions_for "v1.36.5"); do
    pool_run "rerun-$g-$(ver_slug "$v")" scenario_rerun "rerun-$g-$(ver_slug "$v")" "$g" "$v"
  done
}

# ------------------------------------------------------------ S9 host smoke test (remediate --phase all)
scenario_host() {
  local scen=host-s9 gen t0 pre=v1.34.11-k3s1 dir
  gen=$(gens_for bs-o111); gen=${gen%% *}
  case_init "$scen"; t0=$(now)
  if ! sudo -n true 2>/dev/null; then result_write host "$scen" - "$gen" lib READY SKIP 0 "needs passwordless sudo"; case_end; return 0; fi
  dir="$WORK/k3s"; DATA_CT=$dir     # same path on the host and in the containers
  d_pull "$K3S_IMAGE:$pre" || true
  ca_prepare legacy "$gen" "$HELPER_IMAGE" "$WORK" "dev.test.local" || { step "$scen" 1-bootstrap "$pre" "$gen" legacy READY ERROR 0 "CA preparation failed"; DATA_CT=/var/lib/rancher/k3s; case_end; return 0; }
  srv_up "$pre"; wait_k3s server "$SRV" "$T_SERVER" "$SINCE"
  step "$scen" 1-prefix-ready "$pre" "$gen" legacy READY "$W_STATE" $(( $(now) - t0 )) "$W_NOTE"
  [ "$W_STATE" = READY ] || { DATA_CT=/var/lib/rancher/k3s; case_end; return 0; }
  t0=$(now); SINCE=$(now)
  if sudo -n env "K3S_BIN=docker exec $SRV k3s" "TN_K3S_RESTART_CMD=docker restart $SRV" sh "$CA_LIB" remediate --phase all --data-dir "$dir" >"$OUT/logs/$CASE.remediate.log" 2>&1; then
    wait_k3s server "$SRV" "$T_UPGRADE" "$SINCE"
    step "$scen" 2-remediate-all "$pre" "$gen" lib READY "$W_STATE" $(( $(now) - t0 )) "remediate --phase all on the host $W_NOTE"
  else
    step "$scen" 2-remediate-all "$pre" "$gen" lib READY ERROR $(( $(now) - t0 )) "remediate exited non-zero (see log)"
  fi
  save_logs "$SRV" server
  DATA_CT=/var/lib/rancher/k3s
  case_end
}

# S10: a re-run of the (fixed) bootstrap script on a device bootstrapped with the old root, as root on the
# host: tn_k3s_ca_setup, sourced under set -e / noglob, repairs the CA itself (auto-remediate) before
# the k3s installer would run. Then the upgrade through the fix.
scenario_host_setup() {
  local scen=host-s10 gen t0 pre=v1.34.11-k3s1 dir
  gen=$(gens_for bs-o111); gen=${gen%% *}
  case_init "$scen"; t0=$(now)
  if ! sudo -n true 2>/dev/null; then result_write host "$scen" - "$gen" lib READY SKIP 0 "needs passwordless sudo"; case_end; return 0; fi
  dir="$WORK/k3s"; DATA_CT=$dir
  d_pull "$K3S_IMAGE:$pre" || true
  ca_prepare legacy "$gen" "$HELPER_IMAGE" "$WORK" "dev.test.local" || { step "$scen" 1-bootstrap "$pre" "$gen" legacy READY ERROR 0 "CA preparation failed"; DATA_CT=/var/lib/rancher/k3s; case_end; return 0; }
  srv_up "$pre"; wait_k3s server "$SRV" "$T_SERVER" "$SINCE"
  step "$scen" 1-prefix-ready "$pre" "$gen" legacy READY "$W_STATE" $(( $(now) - t0 )) "$W_NOTE"
  [ "$W_STATE" = READY ] || { DATA_CT=/var/lib/rancher/k3s; case_end; return 0; }
  marker_set
  t0=$(now); SINCE=$(now)
  if sudo -n env "K3S_BIN=docker exec $SRV k3s" "TN_K3S_RESTART_CMD=docker restart $SRV" "CONFIG_PATH=$(gen_dir "$gen")" "DATA_DIR=$dir" PRODUCT=k3s _DEVICE_ID=dev _DOMAIN=test.local \
       sh -c ". $HERE/shim.sh; . $CA_LIB; tn_k3s_ca_setup" >"$OUT/logs/$CASE.setup.log" 2>&1; then
    wait_k3s server "$SRV" "$T_UPGRADE" "$SINCE"
    step "$scen" 2-setup-repairs "$pre" "$gen" lib READY "$W_STATE" $(( $(now) - t0 )) "tn_k3s_ca_setup on the host $W_NOTE"
  else
    step "$scen" 2-setup-repairs "$pre" "$gen" lib READY ERROR $(( $(now) - t0 )) "setup exited non-zero (see log)"
    save_logs "$SRV" server; DATA_CT=/var/lib/rancher/k3s; case_end; return 0
  fi
  t0=$(now); upgrade_step "$scen" 3-upgrade-1.36.5 v1.36.5-k3s1 "$gen" "$t0" || true
  # a second run is a no-op
  t0=$(now)
  local d1 d2; d1=$(tls_digest "$gen")
  sudo -n env "CONFIG_PATH=$(gen_dir "$gen")" "DATA_DIR=$dir" PRODUCT=k3s sh -c ". $HERE/shim.sh; . $CA_LIB; tn_k3s_ca_setup" >"$OUT/logs/$CASE.setup2.log" 2>&1 || true
  d2=$(tls_digest "$gen")
  if [ "$d1" = "$d2" ]; then step "$scen" 4-second-setup-noop "-" "$gen" lib UNCHANGED UNCHANGED $(( $(now) - t0 )) "tls sha256 stable"
  else step "$scen" 4-second-setup-noop "-" "$gen" lib UNCHANGED CHANGED $(( $(now) - t0 )) "tls digest changed"; fi
  save_logs "$SRV" server
  DATA_CT=/var/lib/rancher/k3s
  case_end
}

suite_host() {
  SUITE=host
  need_lib host || return 0
  local g; g=$(gens_for bs-o111); g=${g%% *}
  if ! gen_remediable "$g"; then
    result_write host "host:skipped" - "$g" lib READY SKIP 0 "device cert has keyCertSign: nothing to remediate"
    return 0
  fi
  pool_run host-s9 scenario_host
  pool_run host-s10 scenario_host_setup
}

# ------------------------------------------------------------ openssl-compat (no k3s)
COMPAT_IMAGES_DEFAULT="debian:bullseye debian:bookworm debian:trixie ubuntu:18.04 ubuntu:20.04 ubuntu:22.04 ubuntu:24.04 linux/arm64/ubuntu:20.04 linux/arm64/ubuntu:22.04 linux/arm/v7/debian:bookworm linux/arm64/debian:bookworm"

compat_bin_dir() { # PLATFORM -> dir with k3sverify for that platform
  case $1 in
    "") echo "$OUT/bin" ;;
    linux/amd64) echo "$OUT/bin" ;;
    linux/arm64) echo "$OUT/bin/linux_arm64" ;;
    linux/arm/v7) echo "$OUT/bin/linux_arm_v7" ;;
    *) die "unsupported compat platform $1" ;;
  esac
}

compat_name() { printf 'compat-%s' "$(printf '%s' "${1#linux/}" | tr ':/.' '---')"; }

run_compat() { # SPEC (platform/image or image)
  local spec=$1 platform="" image t0 line g status note name tag out
  # spec forms: [linux/arm64/]ubuntu:20.04  or  [linux/arm/v7/]debian:bookworm
  case $spec in
    linux/arm/v7/*) platform=linux/arm/v7; image=${spec#linux/arm/v7/} ;;
    linux/arm64/*)  platform=linux/arm64;  image=${spec#linux/arm64/} ;;
    linux/amd64/*)  platform=linux/amd64;  image=${spec#linux/amd64/} ;;
    *) platform=""; image=$spec ;;
  esac
  name=$(compat_name "$spec")
  t0=$(now); CASE=$name
  tag=$(helper_image "$image" "$platform") || { result_write openssl-compat "$name" - - lib PASS ERROR 0 "cannot build helper image"; return 0; }
  out="$OUT/logs/$name.out"
  docker run --rm --label "$LABEL" --name "$PREFIX-$name" ${platform:+--platform "$platform"} \
    -v "$OUT/gen:/gens:ro" -v "$HERE/compat:/c:ro" -v "$HERE:/h:ro" -v "$(compat_bin_dir "$platform"):/tnbin:ro" -v "$CA_LIB:/tn/tn-k3s-ca.sh:ro" ${INSTALL_LIB:+-v "$INSTALL_LIB:/tn/tn-k3s-install.sh:ro"} \
    -e "GENS=$COMPAT_GENS" "$tag" sh /c/assert.sh > "$out" 2>&1 || true
  local seen=""
  while IFS= read -r line; do
    case $line in
      "RESULT "*)
        set -- $line; g=$2; status=$3; shift 3; note="$*"
        seen="$seen $g"
        result_write openssl-compat "$name:$g" - "$g" lib PASS "$status" $(( $(now) - t0 )) "$(sed -n 's/^# //p' "$out" | head -1 | sed 's/ (Library.*)//;s/  */ /g'): $note" ;;
    esac
  done < "$out"
  for g in $COMPAT_GENS; do
    case " $seen " in *" $g "*) ;; *)
      case " $seen " in *" * "*) ;; *) result_write openssl-compat "$name:$g" - "$g" lib PASS ERROR $(( $(now) - t0 )) "no result from container: $(tail -2 "$out" | tr '\n' ' ' | cut -c1-200)" ;; esac ;;
    esac
  done
}

suite_openssl_compat() {
  SUITE=openssl-compat
  need_lib openssl-compat || return 0
  COMPAT_GENS=$(gens_for "$GEN_LEGACY $GEN_FIXED")
  local spec
  for spec in ${COMPAT_IMAGES_OPT:-$COMPAT_IMAGES_DEFAULT}; do
    pool_run "$(compat_name "$spec")" run_compat "$spec"
  done
}

# ------------------------------------------------------------ install: release policy and cgroup v1 flag
# The library tn-k3s-install.sh ($TN_K3S_INSTALL_LIB) against real k3s.

need_install_lib() { # SUITE
  if [ -z "$INSTALL_LIB" ] || [ ! -f "$INSTALL_LIB" ]; then
    log "suite $1 needs the install library: set TN_K3S_INSTALL_LIB=/path/to/tn-k3s-install.sh (skipped)"
    result_write "$1" "$1:needs-install-lib" - - lib READY SKIP 0 "TN_K3S_INSTALL_LIB not set"
    return 1
  fi
  return 0
}

# S11 cgroup: the kubelet flag of a cgroup v1 host, on a real k3s. The helper container runs
# tn_inst_prepare with TN_K3S_CGROUPV1=true (this host is cgroup v2, so the detection is overridden)
# against an /etc/rancher/k3s that already has a user kubelet-arg; then k3s starts from that
# directory. k3s >= v1.35 must get fail-cgroupv1=false and keep the user's argument; older k3s gets nothing.
scenario_install_cgroup() { # SCEN TAG
  local scen=$1 tag t0 ver args want
  tag=$(ver_tag "$2")
  case_init "$scen"; t0=$(now)
  ver=${tag/-k3s/+k3s}
  d_pull "$K3S_IMAGE:$tag" || true
  mkdir -p "$WORK/etc-rancher/k3s"
  printf 'kubelet-arg: max-pods=77\n' > "$WORK/etc-rancher/k3s/config.yaml"
  h_mounts "$WORK" "$(gens_for bs-o111 | cut -d' ' -f1)"
  d_helper "$(helper_image debian:bookworm)" "${HM[@]}" -v "$WORK/etc-rancher:/etc/rancher" \
    -e "INSTALL_K3S_VERSION=$ver" -e TN_K3S_CGROUPV1=true -- \
    sh -c ". /h/shim.sh; . /tn/tn-k3s-install.sh; tn_inst_prepare server; tn_inst_prepare server" >"$OUT/logs/$CASE.prepare.log" 2>&1 ||
    { step "$scen" 1-prepare "$tag" - lib READY ERROR $(( $(now) - t0 )) "tn_inst_prepare failed (see log)"; case_end; return 0; }
  # the flag goes in from v1.31 (the kubelet knows it from then on); only >= v1.35 needs it
  if [ "$(ver_minor "$tag")" -ge 31 ]; then want=yes; else want=no; fi
  # (root-owned, mode 750: look from inside a container)
  local dropin=/etc/rancher/k3s/config.yaml.d/99-teknoir-cgroupv1.yaml have=no
  if d_helper "$(helper_image debian:bookworm)" -v "$WORK/etc-rancher:/etc/rancher" -- test -s "$dropin"; then have=yes; fi
  if [ "$want" = yes ] && [ "$have" = no ]; then
    step "$scen" 1-prepare "$tag" - lib READY ERROR $(( $(now) - t0 )) "no drop-in written for k3s $tag"; case_end; return 0
  fi
  if [ "$want" = no ] && [ "$have" = yes ]; then
    step "$scen" 1-prepare "$tag" - lib READY ERROR $(( $(now) - t0 )) "a drop-in was written for k3s $tag (< v1.31)"; case_end; return 0
  fi
  srv_up "$tag"; wait_k3s server "$SRV" "$T_SERVER" "$SINCE"
  if [ "$W_STATE" != READY ]; then step "$scen" 2-k3s-starts "$tag" - lib READY "$W_STATE" $(( $(now) - t0 )) "$W_NOTE"; save_logs "$SRV" server; case_end; return 0; fi
  args=$(kc "$SRV" get nodes -o 'jsonpath={.items[0].metadata.annotations.k3s\.io/node-args}' 2>/dev/null)
  if ! printf '%s' "$args" | grep -q 'max-pods=77'; then
    step "$scen" 2-k3s-starts "$tag" - lib READY ERROR $(( $(now) - t0 )) "the user's kubelet-arg max-pods=77 was lost: $args"
  elif [ "$want" = yes ] && ! printf '%s' "$args" | grep -q 'fail-cgroupv1=false'; then
    step "$scen" 2-k3s-starts "$tag" - lib READY ERROR $(( $(now) - t0 )) "fail-cgroupv1=false is not in the k3s arguments: $args"
  elif [ "$want" = no ] && printf '%s' "$args" | grep -q 'fail-cgroupv1'; then
    step "$scen" 2-k3s-starts "$tag" - lib READY ERROR $(( $(now) - t0 )) "fail-cgroupv1 was passed to k3s $tag: $args"
  else
    step "$scen" 2-k3s-starts "$tag" - lib READY READY $(( $(now) - t0 )) "kubelet-arg max-pods=77 kept$([ "$want" = yes ] && echo ', fail-cgroupv1=false passed')"
  fi
  save_logs "$SRV" server
  case_end
}

# S12 policy: the version of the installed k3s is read from the real `k3s --version` output
# (K3S_BIN='docker run --rm rancher/k3s:<tag>'), and the policy decides on it.
scenario_install_policy() { # SCEN TAG
  local scen=$1 tag t0 ver out other
  tag=$(ver_tag "$2")
  case_init "$scen"; t0=$(now)
  ver=${tag/-k3s/+k3s}
  d_pull "$K3S_IMAGE:$tag" || true
  mkdir -p "$WORK/cfg"
  # run_policy PIN [ENV...]: prints the output and a last line RC=<exit code> (it runs in a command substitution)
  run_policy() {
    local pin=$1 rc=0; shift
    env "K3S_BIN=docker run --rm $K3S_IMAGE:$tag" "INSTALL_K3S_VERSION=$pin" "TN_K3S_CONFIG_DIR=$WORK/cfg" TN_CGROUP_FSTYPE=cgroup2fs "$@" \
      sh -c '. "$1"; . "$2"; tn_inst_plan; echo "RESULT version=$INSTALL_K3S_VERSION action=$TN_INST_ACTION"' sh "$HERE/shim.sh" "$INSTALL_LIB" 2>&1 || rc=$?
    echo "RC=$rc"
  }
  P_RC=0
  # 1. the installed release is recognised: asking for the same one is "same"
  out=$(run_policy "$ver"); P_RC=$(printf '%s' "$out" | sed -n 's/^RC=//p')
  if [ "$P_RC" = 0 ] && printf '%s' "$out" | grep -q "RESULT version=$ver action=same"; then
    step "$scen" 1-recognised "$tag" - lib READY READY $(( $(now) - t0 )) "installed $ver read from k3s --version"
  else
    step "$scen" 1-recognised "$tag" - lib READY ERROR $(( $(now) - t0 )) "rc=$P_RC: $(printf '%s' "$out" | tail -2 | tr '\n' ' ' | cut -c1-200)"
  fi
  # 2. another pin is ignored: the installed release is kept
  t0=$(now); other=v1.99.0+k3s1
  out=$(run_policy "$other"); P_RC=$(printf '%s' "$out" | sed -n 's/^RC=//p')
  if [ "$P_RC" = 0 ] && printf '%s' "$out" | grep -q "RESULT version=$ver action=keep"; then
    step "$scen" 2-keeps "$tag" - lib READY READY $(( $(now) - t0 )) "pin $other ignored, $ver kept"
  else
    step "$scen" 2-keeps "$tag" - lib READY ERROR $(( $(now) - t0 )) "rc=$P_RC: $(printf '%s' "$out" | tail -2 | tr '\n' ' ' | cut -c1-200)"
  fi
  # 3. an upgrade over several minors, and any downgrade, are refused
  t0=$(now)
  out=$(run_policy "v1.99.0+k3s1" TN_K3S_UPGRADE=true); P_RC=$(printf '%s' "$out" | sed -n 's/^RC=//p')
  if [ "$P_RC" = 1 ] && printf '%s' "$out" | grep -q 'more than one minor version'; then
    step "$scen" 3-refuses-jump "$tag" - lib READY READY $(( $(now) - t0 )) "upgrade to $other refused"
  else
    step "$scen" 3-refuses-jump "$tag" - lib READY ERROR $(( $(now) - t0 )) "rc=$P_RC: $(printf '%s' "$out" | tail -2 | tr '\n' ' ' | cut -c1-200)"
  fi
  t0=$(now)
  out=$(run_policy "v1.20.0+k3s1" TN_K3S_UPGRADE=true TN_K3S_FORCE=true); P_RC=$(printf '%s' "$out" | sed -n 's/^RC=//p')
  if [ "$P_RC" = 1 ] && printf '%s' "$out" | grep -q 'refusing to downgrade'; then
    step "$scen" 4-refuses-downgrade "$tag" - lib READY READY $(( $(now) - t0 )) "downgrade refused even with TN_K3S_FORCE=true"
  else
    step "$scen" 4-refuses-downgrade "$tag" - lib READY ERROR $(( $(now) - t0 )) "rc=$P_RC: $(printf '%s' "$out" | tail -2 | tr '\n' ' ' | cut -c1-200)"
  fi
  case_end
}

# S13 follow: an agent installs the k3s release of its SERVER, not the pin of the script. k3s does not
# answer /version anonymously (401, on every release), so the server script publishes the release it
# runs as server/static/teknoir/k3s-version, which k3s serves without authentication. Here: the server
# side runs tn_inst_publish_release with the REAL k3s binary (the rancher/k3s image) on the data dir
# before the server starts (like an OS build) and again when it is up; a helper container on the same
# network runs tn_inst_prepare agent with K3S_URL=https://<server>:6443 and a default pin that is NOT the
# server's release: it must read the real server and settle on the server's release. Then a real k3s agent
# of exactly that release joins and must be Ready. A pin that was asked for and is newer than the server
# is refused; a server that publishes nothing is refused; an unreachable server is refused.
scenario_install_follow() { # SCEN TAG
  local scen=$1 tag t0 ver pin rc out agent_tag
  tag=$(ver_tag "$2")
  case_init "$scen"; t0=$(now)
  ver=${tag/-k3s/+k3s}
  # a default pin that differs from the server: the other one of v1.34.11 / v1.36.5
  if [ "$ver" = v1.36.5+k3s1 ]; then pin=v1.34.11+k3s1; else pin=v1.36.5+k3s1; fi
  d_pull "$K3S_IMAGE:$tag" || true
  ca_prepare lib "$(gens_for bs-o111 | cut -d' ' -f1)" "$HELPER_IMAGE" "$WORK" "dev.test.local" ||
    { step "$scen" 1-server-ready "$tag" - lib READY ERROR 0 "CA preparation failed"; case_end; return 0; }
  # the server script publishes the release it runs, with the real k3s binary of the image, before k3s starts
  publish_release() { # -> output of tn_inst_publish_release
    h_mounts "$WORK" "$(gens_for bs-o111 | cut -d' ' -f1)"
    d_helper "$K3S_IMAGE:$tag" --entrypoint sh "${HM[@]}" -e "DATA_DIR=$DATA_CT" -- \
      -c '. /h/shim.sh; . /tn/tn-k3s-install.sh; tn_inst_publish_release' 2>&1
  }
  out=$(publish_release) || true
  echo "$out" > "$OUT/logs/$CASE.publish.log"
  srv_up "$tag"; wait_k3s server "$SRV" "$T_SERVER" "$SINCE"
  step "$scen" 1-server-ready "$tag" - lib READY "$W_STATE" $(( $(now) - t0 )) "$W_NOTE"
  [ "$W_STATE" = READY ] || { save_logs "$SRV" server; case_end; return 0; }
  # k3s starting on a data dir that has the file must not remove it, and serves it without authentication
  t0=$(now)
  out=$(publish_release) || true
  if printf '%s' "$out" | grep -q "already published"; then
    step "$scen" 1b-published "$tag" - lib READY READY $(( $(now) - t0 )) "file survived the first start of k3s; a second publish changes nothing"
  else
    step "$scen" 1b-published "$tag" - lib READY ERROR $(( $(now) - t0 )) "$(printf '%s' "$out" | tail -2 | tr '\n' ' ' | cut -c1-200)"
  fi

  # run_follow PIN SOURCE URL [ENV...]: prints the output and a last line RC=<exit code>
  run_follow() {
    local pin=$1 source=$2 url=$3 rc=0; shift 3
    h_mounts "$WORK" "$(gens_for bs-o111 | cut -d' ' -f1)"
    d_helper "$(helper_curl_image)" --network "$NET" "${HM[@]}" \
      -e "K3S_URL=$url" -e "INSTALL_K3S_VERSION=$pin" -e "TN_K3S_PIN_SOURCE=$source" -e TN_CGROUP_FSTYPE=cgroup2fs \
      -e TN_K3S_CONFIG_DIR=/tmp/k3s-cfg -e TN_K3S_SERVER_VERSION_WAIT=1 -e K3S_BIN=/nonexistent/k3s -e DATA_DIR=/tmp/no-k3s "$@" -- \
      sh -c '. /h/shim.sh; . /tn/tn-k3s-install.sh; tn_inst_prepare agent; echo "RESULT version=$INSTALL_K3S_VERSION"' 2>&1 || rc=$?
    echo "RC=$rc"
  }

  # 2. the default pin is not the server's release: the agent gets the server's
  t0=$(now)
  out=$(run_follow "$pin" default "https://$SRV_HOST:6443")
  echo "$out" > "$OUT/logs/$CASE.follow.log"
  rc=$(printf '%s' "$out" | sed -n 's/^RC=//p')
  if [ "$rc" = 0 ] && printf '%s' "$out" | grep -q "RESULT version=$ver\$"; then
    step "$scen" 2-agent-follows "$tag" - lib READY READY $(( $(now) - t0 )) "default pin $pin ignored, the real server published $ver"
  else
    step "$scen" 2-agent-follows "$tag" - lib READY ERROR $(( $(now) - t0 )) "rc=$rc: $(printf '%s' "$out" | tail -3 | tr '\n' ' ' | cut -c1-240)"
    save_logs "$SRV" server; case_end; return 0
  fi

  # 3. a real agent of exactly that release joins
  t0=$(now); agent_tag=${ver/+k3s/-k3s}
  agt_up "$agent_tag"
  wait_k3s agent "$AGT" "$T_AGENT" "$ASINCE" "$SRV" "$AGT_HOST"
  step "$scen" 3-agent-joins "$agent_tag" - lib READY "$([ "$W_STATE" = READY ] && echo READY || echo "AGENT-$W_STATE")" $(( $(now) - t0 )) "agent $agent_tag, server $tag $W_NOTE"
  save_logs "$AGT" agent

  # 4. a release that was asked for and is newer than the server is refused
  t0=$(now)
  out=$(run_follow v1.99.0+k3s1 flag "https://$SRV_HOST:6443")
  rc=$(printf '%s' "$out" | sed -n 's/^RC=//p')
  if [ "$rc" = 1 ] && printf '%s' "$out" | grep -q "refusing to install k3s v1.99.0+k3s1 on this agent" && ! printf '%s' "$out" | grep -q '^RESULT'; then
    step "$scen" 4-refuses-newer "$tag" - lib READY READY $(( $(now) - t0 )) "v1.99.0 refused, the server runs $ver"
  else
    step "$scen" 4-refuses-newer "$tag" - lib READY ERROR $(( $(now) - t0 )) "rc=$rc: $(printf '%s' "$out" | tail -3 | tr '\n' ' ' | cut -c1-240)"
  fi

  # 4b. a server that publishes nothing gives an agent nothing to follow (k3s answers /version with 401 to
  # an anonymous caller): refused, and the message says why. The file is removed for this step.
  t0=$(now)
  h_mounts "$WORK" "$(gens_for bs-o111 | cut -d' ' -f1)"
  d_helper "$(helper_image debian:bookworm)" "${HM[@]}" -- rm -f "$DATA_CT/server/static/teknoir/k3s-version"
  out=$(run_follow "$pin" default "https://$SRV_HOST:6443")
  rc=$(printf '%s' "$out" | sed -n 's/^RC=//p')
  if [ "$rc" = 1 ] && printf '%s' "$out" | grep -q "does not publish its release" && ! printf '%s' "$out" | grep -q '^RESULT'; then
    step "$scen" 4b-refuses-unpublished "$tag" - lib READY READY $(( $(now) - t0 )) "nothing published, /version is 401: refused"
  else
    step "$scen" 4b-refuses-unpublished "$tag" - lib READY ERROR $(( $(now) - t0 )) "rc=$rc: $(printf '%s' "$out" | tail -3 | tr '\n' ' ' | cut -c1-240)"
  fi

  # 5. an unreachable server is refused (the script does not guess), unless the release was asked for
  t0=$(now)
  out=$(run_follow "$pin" default "https://no-such-server.invalid:6443" -e TN_K3S_SERVER_VERSION_TRIES=1)
  rc=$(printf '%s' "$out" | sed -n 's/^RC=//p')
  if [ "$rc" = 1 ] && printf '%s' "$out" | grep -q "cannot read the k3s release of the server" && ! printf '%s' "$out" | grep -q '^RESULT'; then
    step "$scen" 5-refuses-unreachable "$tag" - lib READY READY $(( $(now) - t0 )) "no answer from the server: refused"
  else
    step "$scen" 5-refuses-unreachable "$tag" - lib READY ERROR $(( $(now) - t0 )) "rc=$rc: $(printf '%s' "$out" | tail -3 | tr '\n' ' ' | cut -c1-240)"
  fi
  save_logs "$SRV" server
  case_end
}

suite_install() {
  SUITE=install
  need_install_lib install || return 0
  local v
  for v in $(versions_for "v1.30.14 v1.31.14 v1.34.11 v1.36.5"); do
    pool_run "inst-cgroup-$(ver_slug "$v")" scenario_install_cgroup "inst-cgroup-$(ver_slug "$v")" "$v"
    pool_run "inst-policy-$(ver_slug "$v")" scenario_install_policy "inst-policy-$(ver_slug "$v")" "$v"
  done
  # the agent follows its server: a pre-fix and a post-fix release, each with a default pin of the other kind
  for v in $(versions_for "v1.34.11 v1.36.5"); do
    pool_run "inst-follow-$(ver_slug "$v")" scenario_install_follow "inst-follow-$(ver_slug "$v")" "$v"
  done
}
