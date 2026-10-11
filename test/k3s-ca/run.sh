#!/usr/bin/env bash
# run.sh - k3s custom-CA test harness. See README.md.
#
# Reproduces and guards the k3s CA hash validation fix (k3s-io/k3s#14699): k3s
# >= v1.34.12 / v1.35.9 / v1.36.5 / v1.37.1 refuses the root CA that Teknoir
# devices use (their own certificate, without keyCertSign). Runs real
# rancher/k3s containers; needs docker, go and network access.
set -Eeuo pipefail

HERE=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
REPO=$(cd "$HERE/../.." && pwd)

usage() {
  cat <<'USAGE'
usage: run.sh [options]

  --suite S[,S...]        control | fresh | legacy-with-cert | agent | remediation | rerun | host | openssl-compat | all
                          (default: control)
  --versions "V ..."      k3s versions (v1.34.11, 1.36.5, v1.36.5+k3s1, stable, latest, v1.33 = newest of a channel);
                          space or comma separated; default depends on the suite
  --gens "G ..."          device cert generators: bs-o111 bs-o3 go-sha1 go-sha256 expired fixed-bs fixed-go ext
  --ca-mode MODE          agent suite only: lib | proper (default lib if TN_K3S_CA_LIB is set, else proper)
  --agent-os-trust M      agent suite: yes | no | both (default both)
  --helper-image IMAGE    image preparing the tls dir (default debian:bookworm; openssl is added and cached)
  --helpers "I ..."       remediation suite: helper images running prepare/finalize (default: ubuntu:20.04 debian:bookworm)
  --compat-images "I ..." openssl-compat suite: images, optionally prefixed with a platform,
                          e.g. linux/arm64/ubuntu:20.04 (default: the full distro/arch list)
  --device-cert FILE      externally generated device certificate; with --device-key adds generator 'ext'
  --device-key FILE       and makes it the default generator for every suite. The expected outcome of the
                          legacy-script cases is derived from the certificate (keyCertSign present = READY on
                          post-fix k3s, absent = FAIL)
  --expect READY|FAIL     override that expectation for legacy cases on post-fix k3s (default: derived)
  -j N                    parallel cases (default 4)
  --out DIR               output directory (default $TMPDIR/tn-k3sca-<runid>)
  --keep                  keep containers, work dirs and generated keys for debugging
  --list                  print the cases and exit
  -h, --help

Hooks (environment): TN_K3S_CA_LIB, TN_REMEDIATE_PREPARE, TN_REMEDIATE_FINALIZE, TN_REMEDIATE_CHECK (see README).
USAGE
}

# The whole program is one brace group, so bash has parsed all of it before it
# runs anything: editing this file during a long run cannot break the run.
{
SUITES=control VERSIONS_OPT="" GENS_OPT="" AGENT_CA="" AGENT_TRUST=both HELPERS_OPT="" COMPAT_IMAGES_OPT=""
DEVICE_CERT="" DEVICE_KEY="" EXPECT_OVERRIDE="" JOBS=4 OUT="" KEEP=0 DRY=0
while [ $# -gt 0 ]; do
  case $1 in
    --suite) SUITES=${2//,/ }; shift 2 ;;
    --versions) VERSIONS_OPT=${2//,/ }; shift 2 ;;
    --gens) GENS_OPT=${2//,/ }; shift 2 ;;
    --ca-mode) AGENT_CA=$2; shift 2 ;;
    --agent-os-trust) AGENT_TRUST=$2; shift 2 ;;
    --helper-image) HELPER_IMAGE=$2; shift 2 ;;
    --helpers) HELPERS_OPT=$2; shift 2 ;;
    --compat-images) COMPAT_IMAGES_OPT=$2; shift 2 ;;
    --device-cert) DEVICE_CERT=$2; shift 2 ;;
    --device-key) DEVICE_KEY=$2; shift 2 ;;
    --expect) EXPECT_OVERRIDE=$2; shift 2 ;;
    -j) JOBS=$2; shift 2 ;;
    -j*) JOBS=${1#-j}; shift ;;
    --out) OUT=$2; shift 2 ;;
    --keep) KEEP=1; shift ;;
    --list) DRY=1; shift ;;
    -h|--help) usage; exit 0 ;;
    *) echo "unknown option: $1" >&2; usage >&2; exit 2 ;;
  esac
done

# shellcheck source=lib/common.sh
. "$HERE/lib/common.sh"
. "$HERE/lib/docker.sh"
. "$HERE/lib/gen.sh"
. "$HERE/lib/cases.sh"
. "$HERE/lib/suites.sh"

[ "$SUITES" = all ] && SUITES="control fresh legacy-with-cert agent remediation rerun host openssl-compat"
case $EXPECT_OVERRIDE in ""|READY|FAIL) ;; *) die "--expect must be READY or FAIL" ;; esac
export EXPECT_OVERRIDE
if [ -n "$DEVICE_CERT$DEVICE_KEY" ]; then
  [ -f "$DEVICE_CERT" ] && [ -f "$DEVICE_KEY" ] || die "--device-cert and --device-key must both be given and exist"
  [ -n "$GENS_OPT" ] || GENS_OPT=ext
fi
if [ "$DRY" != 1 ]; then
  command -v docker >/dev/null || die "docker not found"
  command -v go >/dev/null || die "go not found"
  docker info >/dev/null 2>&1 || die "cannot talk to docker"
fi

RUNID="$(date +%Y%m%d%H%M%S)-$(rand_hex 3)"
docker_init "$RUNID"
OUT_AUTO=0; [ -n "$OUT" ] || OUT_AUTO=1
: "${OUT:=${TMPDIR:-/tmp}/tn-k3sca-$RUNID}"
mkdir -p "$OUT/logs" "$OUT/bin" "$OUT/gen" "$OUT/work"
OUT=$(cd "$OUT" && pwd)
export OUT HERE REPO RUNID LABEL NET PREFIX SHORT
# keep a copy of everything this script prints, for the PRIVATE KEY scan
if [ "$DRY" != 1 ]; then exec > >(tee -a "$OUT/run.log") 2>&1; fi
K3D_BEFORE=0; [ "$DRY" = 1 ] || K3D_BEFORE=$(docker ps -q --filter name=k3d-teknoir-dev | wc -l)

STATUS=0 DONE=0
finish() {
  local rc=$?
  trap - EXIT INT TERM
  [ "$rc" -eq 0 ] || [ "${DONE:-0}" = 1 ] || log "interrupted/failed (rc=$rc); cleaning up"
  # stop background cases first, then remove only this run's docker objects
  kill $(jobs -rp) 2>/dev/null || true
  pool_wait
  if [ "$DRY" = 1 ]; then
    [ "$OUT_AUTO" = 0 ] || rm -rf "$OUT"
  elif [ "$KEEP" = 1 ]; then
    log "--keep: containers (label $LABEL), $OUT/work and $OUT/gen (private keys!) left in place"
  else
    d_cleanup
    d_rm_root "$OUT/work"
    rm -rf "$OUT/gen"
  fi
  exit "$rc"
}
trap finish EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

log "run $RUNID, out=$OUT, suites: $SUITES, -j $JOBS"

# ---- build the Go tools (stdlib only; no network). Each tool is one main.go built on its own, so
# this works inside the cli module and in a repository that vendors the harness without a go.mod.
build_tools() {
  log "building k3sverify and gencert-go"
  CGO_ENABLED=0 go build -o "$OUT/bin/k3sverify" "$HERE/cmd/k3sverify/main.go"
  CGO_ENABLED=0 go build -o "$OUT/bin/gencert-go" "$HERE/cmd/gencert-go/main.go"
  case " $SUITES " in *" openssl-compat "*)
    mkdir -p "$OUT/bin/linux_arm64" "$OUT/bin/linux_arm_v7"
    CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o "$OUT/bin/linux_arm64/k3sverify" "$HERE/cmd/k3sverify/main.go"
    CGO_ENABLED=0 GOOS=linux GOARCH=arm GOARM=7 go build -o "$OUT/bin/linux_arm_v7/k3sverify" "$HERE/cmd/k3sverify/main.go" ;;
  esac
}
[ "$DRY" = 1 ] || build_tools

# ---- which generators / images are needed
needed_gens() {
  local s g list=""
  for s in $SUITES; do
    case $s in
      control|fresh|rerun|host) g=$(gens_for "$GEN_LEGACY") ;;
      legacy-with-cert) g=$(gens_for "$GEN_FIXED") ;;
      agent) g=$(gens_for "bs-o111 go-sha1") ;;
      remediation) g=$(gens_for "bs-o111 go-sha1 expired") ;;
      openssl-compat) g=$(gens_for "$GEN_LEGACY $GEN_FIXED") ;;
      *) die "unknown suite '$s'" ;;
    esac
    list="$list $g"
  done
  printf '%s\n' $list | sort -u | tr '\n' ' '
}


if [ "$DRY" != 1 ]; then
  d_net_create
  GENS_NEEDED=$(needed_gens)
  log "preparing images and device certificates: $GENS_NEEDED"
  HELPER_IMAGE=$(helper_image "$HELPER_IMAGE") || die "cannot build the helper image"
  helper_image debian:bookworm >/dev/null
  for g in $GENS_NEEDED; do
    gen_make "$g"
    gen_check "$g"
  done
  case " $SUITES " in *" agent "*)
    # system CAs + the device certificate: what agent.sh.tmpl's update-ca-certificates produces
    d_helper "$(helper_image debian:bookworm)" -- cat /etc/ssl/certs/ca-certificates.crt > "$OUT/sys-ca.pem"
    for g in $GENS_NEEDED; do cat "$OUT/sys-ca.pem" "$(gen_dir "$g")/rsa_public.pem" > "$(gen_dir "$g")/os-trust.pem"; done ;;
  esac
  case " $SUITES " in *" openssl-compat "*)
    for spec in ${COMPAT_IMAGES_OPT:-$COMPAT_IMAGES_DEFAULT}; do
      case $spec in
        linux/arm/v7/*) helper_image "${spec#linux/arm/v7/}" linux/arm/v7 >/dev/null ;;
        linux/arm64/*)  helper_image "${spec#linux/arm64/}" linux/arm64 >/dev/null ;;
        *) helper_image "$spec" >/dev/null ;;
      esac
    done ;;
  esac
fi

# ---- run the suites
for s in $SUITES; do
  log "suite $s"
  case $s in
    control) suite_control ;;
    fresh) suite_fresh ;;
    legacy-with-cert) suite_legacy_with_cert ;;
    agent) suite_agent ;;
    remediation) suite_remediation ;;
    rerun) suite_rerun ;;
    host) suite_host ;;
    openssl-compat) suite_openssl_compat ;;
  esac
done
[ "$DRY" = 1 ] && exit 0
pool_wait

# ---- results
# (names go to a file first: `cat | grep -q` under pipefail dies of SIGPIPE on big result sets)
cat "$OUT"/results.d/*.tsv 2>/dev/null | cut -f2 > "$OUT/result-names.txt" || true
for n in "${JOB_NAMES[@]}"; do
  if ! grep -qE "^$n(:|$)" "$OUT/result-names.txt"; then
    result_write "${SUITES%% *}" "$n" - - - - ERROR 0 "job produced no result; see $OUT/logs/$n.log"
  fi
done
results_merge
echo
{ column -t -s$'\t' "$OUT/results.tsv" 2>/dev/null || cat "$OUT/results.tsv"; } | cut -c1-260
echo

mism=$(awk -F'\t' 'NR>1 && ($8=="MISMATCH"||$8=="ERROR")' "$OUT/results.tsv" | wc -l)
pass=$(awk -F'\t' 'NR>1 && $8=="PASS"' "$OUT/results.tsv" | wc -l)
info=$(awk -F'\t' 'NR>1 && $8=="INFO"' "$OUT/results.tsv" | wc -l)
skip=$(awk -F'\t' 'NR>1 && $8=="SKIP"' "$OUT/results.tsv" | wc -l)
log "summary: PASS=$pass MISMATCH/ERROR=$mism INFO=$info SKIP=$skip"

if grep -rIl 'PRIVATE KEY' "$OUT/logs" "$OUT/results.tsv" "$OUT/results.d" "$OUT/run.log" 2>/dev/null | grep -q .; then
  log "FAIL: 'PRIVATE KEY' found in:"; grep -rIl 'PRIVATE KEY' "$OUT/logs" "$OUT/results.tsv" "$OUT/results.d" "$OUT/run.log"
  STATUS=1
fi
[ "$mism" -eq 0 ] || STATUS=1

# ---- post-conditions
if [ "$KEEP" != 1 ]; then
  d_cleanup
  left=$(docker ps -aq --filter "label=$LABEL" | wc -l)
  netsleft=$(docker network ls -q --filter "label=$LABEL" | wc -l)
  [ "$left" -eq 0 ] && [ "$netsleft" -eq 0 ] || { log "FAIL: $left containers / $netsleft networks left for $LABEL"; STATUS=1; }
fi
K3D_AFTER=$(docker ps -q --filter name=k3d-teknoir-dev 2>/dev/null | wc -l)
[ "$K3D_BEFORE" -eq "$K3D_AFTER" ] || { log "FAIL: k3d-teknoir-dev containers changed ($K3D_BEFORE -> $K3D_AFTER)"; STATUS=1; }
log "results: $OUT/results.tsv   logs: $OUT/logs"
DONE=1
exit "$STATUS"
exit $?
}
