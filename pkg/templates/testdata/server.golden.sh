#!/bin/sh
set -e
set -o noglob
#set -x
umask 027

# --- helper functions ---
info() { echo '[INFO] ' "$@"; }
warn() { echo '[WARN] ' "$@" >&2; }
fatal() { echo '[ERROR] ' "$@" >&2; exit 1; }

# Exit if not running as root
[ $(id -u) -eq 0 ] || fatal 'You must run this script as root'

DOWNLOADER=
# --- download ---
download() {
    [ $# -eq 2 ] || fatal 'download needs exactly 2 arguments'

    case $DOWNLOADER in
        curl)
            if [ "${OS_BUILD}" ] || [ "${INSECURE}" ]; then
                info "Running installation without verifying ssl certs on URLs"
                $SUDO curl --insecure -o $1 -sSfL $2
            else
                $SUDO curl -o $1 -sSfL $2
            fi
            ;;
        wget)
            $SUDO wget -qO $1 $2
            ;;
        *)
            fatal "Incorrect executable '$DOWNLOADER'"
            ;;
    esac

    # Abort if download command failed
    [ $? -eq 0 ] || fatal 'Download failed'
}

# --- verify existence of network downloader executable ---
verify_downloader() {
    # Return failure if it doesn't exist or is no executable
    [ -x "$(command -v $1)" ] || return 1

    # Set verified executable as our downloader program and return success
    DOWNLOADER=$1
    return 0
}

verify_downloader curl || verify_downloader wget || fatal 'Can not find curl or wget for downloading files'


# This is the drop in device bootstrap script for the Teknoir platform.
# It should work for most arm32/arm64/amd64 devices running Debian or Ubuntu.

# Usage:
#   TBD
#
# Environment variables:
#   OS_BUILD=true (default: false, for OS builds)
#   INSECURE=true (default: false, for OS builds, or if you want to run without ssl cert verification)
#   INSTALL_WATCHDOG=true (default: false, enable or disable the teknoir watchdog service)
#   USE_DOCKER=true (default: false, enable the use of docker as container runtime for k3sm inly works for k3s < 1.24)
#   CREATE_USER=false (default: ask, create a user for the device without asking)
#   TN_K3S_CA_AUTO_REMEDIATE=false (default: true; a re-run on a device bootstrapped with the old
#                          root CA repairs it before k3s is touched. false stops with advice instead)
#   TN_K3S_UPGRADE=true (default: false; a re-run KEEPS the installed k3s version. true lets the pin
#                          below replace it: never a downgrade, never more than one minor version)
#   TN_K3S_FORCE=true (default: false; allows an upgrade over more than one minor version)
#   TN_K3S_CGROUPV1=true|false (default: detected; on cgroup v1 with k3s >= v1.31 the kubelet gets
#                          fail-cgroupv1=false, see /etc/rancher/k3s/config.yaml.d/99-teknoir-cgroupv1.yaml)


TMP=$(mktemp -dt)
# --- cleanup/error trap ---
error() {
  local code="$1"
  echo "[ERROR] Error: exiting with status ${code}"
  exit "${code}"
}
cleanup() {
  rm -rf "$TMP"
  [ $? -eq 0 ] && exit
  error "$?"
}
trap 'cleanup' EXIT

cd "$TMP"
info "Teknoir bootstrapping...${TMP}"

_DOMAIN="example.test"
_DEVICE_ID="golden-device"
_NAMESPACE="golden-ns"
_FIRST_USER_NAME="golden-user"
_FIRST_USER_PASS="TEST-ONLY-password"
_FIRST_USER_KEY="ssh-ed25519 TEST-ONLY-ssh-public golden@example.test"
_RSA_PUBLIC="TEST-ONLY-rsa-public
TEST-ONLY-rsa-public-line2"
_RSA_PRIVATE="TEST-ONLY-rsa-private
TEST-ONLY-rsa-private-line2"
_K3S_TOKEN="TEST-ONLY-k3s-token"


info "Install device specific keys"
export CONFIG_PATH=/etc/teknoir
$SUDO mkdir -p ${CONFIG_PATH}
$SUDO mkdir -p /toe_conf && $SUDO rm -rf /toe_conf && $SUDO ln -s ${CONFIG_PATH}/ /toe_conf # For backward compatibility
download ${CONFIG_PATH}/roots.pem https://pki.goog/roots.pem
$SUDO chmod 440 ${CONFIG_PATH}/roots.pem
$SUDO chown 65532:root ${CONFIG_PATH}/roots.pem
$SUDO tee ${CONFIG_PATH}/rsa_private.pem > /dev/null << EOL
${_RSA_PRIVATE}
EOL
$SUDO chmod 440 ${CONFIG_PATH}/rsa_private.pem
$SUDO chown 65532:root ${CONFIG_PATH}/rsa_private.pem
$SUDO tee ${CONFIG_PATH}/rsa_public.pem > /dev/null << EOL
${_RSA_PUBLIC}
EOL
$SUDO chmod 444 ${CONFIG_PATH}/rsa_public.pem
$SUDO chown 65532:root ${CONFIG_PATH}/rsa_public.pem
$SUDO chgrp 65532 /etc/teknoir 2>/dev/null || true

# The k3s CA of the device: the device certificate becomes the root CA (re-issued with
# keyCertSign, which k3s >= v1.34.12/v1.35.9/v1.36.5/v1.37.1 requires). The library is also
# available stand-alone as pkg/templates/scripts/tn-k3s-ca.sh (check / remediate).
TN_K3S_CA_NO_MAIN=1
#!/bin/sh
# tn-k3s-ca.sh - the custom k3s CA of a Teknoir device: setup, check, remediate.
#
# Background. A Teknoir device uses its own certificate (Device CR settings,
# /etc/teknoir/rsa_public.pem + rsa_private.pem) as the ROOT of the k3s cluster
# CA and issues the intermediate and the five leaf CAs below it. k3s v1.34.12,
# v1.35.9, v1.36.5, v1.37.1 and later (k3s-io/k3s#14699, "Improve CA hash
# validation") drop every certificate without the keyCertSign key usage from the
# CA bundle before they build the trust pool. The device certificates of the
# platform never had keyCertSign, so such a k3s refuses to start
# ("CA cert validation failed ... x509: certificate signed by unknown authority").
#
# The fix is to give the root the missing key usage. The root is re-issued from
# the SAME key and subject (and subject key identifier): a new self-signed root
# with CA:TRUE and keyCertSign, valid 20 years. Everything signed below the old
# root stays valid (same key, same subject), and the device files are never
# modified.
#
# Three ways to use this file:
#
#   . tn-k3s-ca.sh; tn_k3s_ca_setup     bootstrap scripts (sourced): creates the
#                                       PKI of a new device, leaves an
#                                       initialised one alone, repairs an old one
#   sh tn-k3s-ca.sh check               is the root of this device acceptable to k3s?
#   sh tn-k3s-ca.sh remediate           repair a device bootstrapped with the old root
#
# POSIX sh. It runs under `set -e` and `set -o noglob` (as the bootstrap scripts
# do) and does not change shell options, traps or the working directory of the
# caller. Sourcing it defines functions only.
#
# Environment (all optional when run as a program):
#   CONFIG_PATH    directory with rsa_public.pem and rsa_private.pem (/etc/teknoir)
#   DATA_DIR       k3s data dir. When it is not set (and no --data-dir is given) it is resolved the way
#                  k3s resolves it, see tn_ca_resolve_datadir: an explicit DATA_DIR/--data-dir, then
#                  --data-dir/-d in the ExecStart of the k3s systemd unit, then the last data-dir in
#                  /etc/rancher/k3s/config.yaml.d/*.yaml, then data-dir in /etc/rancher/k3s/config.yaml,
#                  then K3S_DATA_DIR of the unit's environment, then /var/lib/rancher/k3s
#   PRODUCT        k3s
#   _DEVICE_ID, _DOMAIN   used in the CN of the generated CAs (taken from the
#                  existing CAs when not set)
#   K3S_BIN        k3s command used by `rotate-ca` (k3s)
#   TN_K3S_RESTART_CMD    restarts k3s after a remediation (systemctl restart k3s)
#   TN_K3S_SERVICE_TIMEOUT  seconds that a stop, start or restart of k3s may take (600: a Raspberry Pi or a
#                  Jetson with a large datastore takes minutes); then the unit is watched for TN_K3S_READY_GRACE
#                  more seconds (120) and only after that reported as failed instead of hanging (the k3s unit
#                  waits forever for a k3s that cannot start)
#   TN_K3S_READY_GRACE, TN_K3S_READY_POLL   seconds to keep polling the unit for 'active' after a restart that
#                  timed out (120), and the interval between polls (5)
#   TN_K3S_ROTATE_TRIES, TN_K3S_ROTATE_WAIT   `k3s certificate rotate-ca` needs a k3s server that answers. When the
#                  k3s unit is starting (or just restarted) it is tried up to this many times (6), this many
#                  seconds apart (10). Without a k3s unit it is tried once
#   TN_K3S_STOP_CMD, TN_K3S_START_CMD   stop/start k3s around the replacement of server/tls when the
#                  PKI is regenerated (systemctl stop|start k3s). Without TN_K3S_STOP_CMD k3s is only
#                  stopped if the systemd unit is active, and then started again
#   TN_K3S_CONFIG_DIR     /etc/rancher/k3s (config.yaml and config.yaml.d)
#   TN_K3S_UNIT_FILE      a unit file to read instead of `systemctl cat k3s.service` (tests)
#   TN_K3S_CA_AUTO_REMEDIATE  true (default) / false: a re-run of a bootstrap script
#                  repairs an initialised data dir itself; false stops with advice
#
# check exit codes:
#   0  ok (the root is acceptable to k3s, or this is not a server: no k3s server unit and no
#      server/tls/server-ca.crt)
#   2  needs-rotation: initialised data dir, root unusable => `remediate`
#   3  needs-regeneration: data dir never initialised, PKI unusable => `remediate`
#      (or re-run the bootstrap script)
#   4  foreign: the root is unusable or expires soon but we do not hold its key (not a Teknoir CA);
#      or no-server-data: a k3s server unit exists but the data dir that was resolved has no
#      server/tls/server-ca.crt (k3s has not started yet, or its data dir is elsewhere: pass --data-dir)
#   1  error
#
# remediate exit codes: 0 done, 1 failed (nothing changed, except the saved backup), 2 usage,
#   4 foreign/no server data, 5 the CA WAS changed but a later step failed (finalize or the k3s restart):
#   the datastore holds the new CA, do not roll back; the message says how to continue.
#
# Rollback limits. The backups (server/tls-backup-tn-<ts>.tar, the newest 3 are kept) hold server/tls
# only. `k3s certificate rotate-ca` also writes the new CA into the datastore of k3s, which this tool
# does not back up: once it has succeeded, restoring the tar alone does not go back (the files and
# the datastore would disagree). A real rollback needs a snapshot of the datastore taken before
# (stop k3s, copy DATA_DIR/server/db). The staged files are not committed before rotate-ca
# succeeded, and a failing rotate-ca leaves the device as it was.

TN_K3S_CA_LIB_VERSION=1
export TN_K3S_CA_LIB_VERSION

# ----------------------------------------------------------------- messages
# The library prints with its own functions, in the format of header.sh.tmpl. It must not use a
# caller's info/warn/fatal (there may be none: a stand-alone run) and must not trust `info`
# to be a function either: on Debian/Ubuntu it is the GNU info reader.
tn_ca_info() { echo '[INFO] ' "$@"; }
tn_ca_warn() { echo '[WARN] ' "$@" >&2; }
tn_ca_fatal() { echo '[ERROR] ' "$@" >&2; exit 1; }

# ----------------------------------------------------------------- small helpers

# Select the openssl binary (3.x if installed side by side, as on some distros).
tn_ca_openssl() {
  if command -v openssl-3 >/dev/null 2>&1; then
    OPENSSL=openssl-3
  elif command -v openssl >/dev/null 2>&1; then
    OPENSSL=openssl
  else
    tn_ca_warn "openssl not found"
    return 1
  fi
  if ! "$OPENSSL" ecparam -name prime256v1 -genkey -noout -out /dev/null >/dev/null 2>&1; then
    tn_ca_warn "openssl missing Elliptic Curve (ecparam) support"
    return 1
  fi
  return 0
}

tn_ca_ts() { date +%Y%m%dT%H%M%S; }

# tn_ca_changed TEXT: records, in TN_K3S_CA_CHANGED (exported), what this run has changed on the device
# for good. A later refusal (the k3s release policy, a failing step) says it instead of claiming that
# nothing was changed.
tn_ca_changed() {
  if [ -n "${TN_K3S_CA_CHANGED:-}" ]; then TN_K3S_CA_CHANGED="$TN_K3S_CA_CHANGED; $1"; else TN_K3S_CA_CHANGED=$1; fi
  export TN_K3S_CA_CHANGED
}

# tn_ca_split PEMFILE DIR: write every certificate of PEMFILE to DIR/c<N>.pem, N from 0.
# Echoes the number of certificates.
tn_ca_split() {
  mkdir -p "$2" || return 1
  awk -v d="$2" '
    /-----BEGIN CERTIFICATE-----/ { f = sprintf("%s/c%d.pem", d, n); n++ }
    f { print > f }
    /-----END CERTIFICATE-----/ { if (f != "") close(f); f = "" }
    END { print n + 0 }' "$1"
}

# tn_ca_fp CERT: sha256 fingerprint
tn_ca_fp() { "$OPENSSL" x509 -in "$1" -noout -fingerprint -sha256 | cut -d= -f2; }

# tn_ca_pubhash_cert CERT / tn_ca_pubhash_key KEY: sha256 of the public key (SPKI)
tn_ca_pubhash_cert() { "$OPENSSL" x509 -in "$1" -noout -pubkey 2>/dev/null | sha256sum | cut -d' ' -f1; }
tn_ca_pubhash_key() { "$OPENSSL" pkey -in "$1" -pubout 2>/dev/null | sha256sum | cut -d' ' -f1; }

# tn_ca_ski CERT: subject key identifier as printed by openssl, or empty
tn_ca_ski() {
  "$OPENSSL" x509 -in "$1" -noout -text 2>/dev/null |
    awk '/X509v3 Subject Key Identifier/ { getline; gsub(/[ \t]/, ""); print; exit }'
}

# tn_ca_has_certsign CERT
tn_ca_has_certsign() { "$OPENSSL" x509 -in "$1" -noout -text 2>/dev/null | grep -q 'Certificate Sign'; }

# tn_ca_dir_ok DIR: a data dir whose server/token exists was started by k3s once
tn_ca_initialised() { [ -s "$1/server/token" ]; }

# tn_ca_lastcert BUNDLE OUTFILE: write the last (root) certificate of BUNDLE
tn_ca_lastcert() {
  _tn_w=$(mktemp -d) || return 1
  _tn_n=$(tn_ca_split "$1" "$_tn_w")
  if [ "${_tn_n:-0}" -lt 1 ]; then rm -rf "$_tn_w"; return 1; fi
  cp "$_tn_w/c$((_tn_n - 1)).pem" "$2"
  rm -rf "$_tn_w"
}

# Bundles k3s reads (leaf first, root last), relative to server/tls.
TN_CA_BUNDLES="server-ca.crt client-ca.crt request-header-ca.crt etcd/peer-ca.crt etcd/server-ca.crt intermediate-ca.crt root-ca.crt"

# ----------------------------------------------------------------- the k3s data dir

# The data dir of a k3s server, resolved the way k3s resolves it (verified against rancher/k3s
# v1.36.5: a command line flag beats config.yaml.d/*.yaml, which beats config.yaml). First match wins:
#   1. an explicit DATA_DIR (the environment, or --data-dir of this tool)
#   2. --data-dir DIR, --data-dir=DIR, -d DIR or -d=DIR in the ExecStart of the k3s systemd unit
#   3. the last data-dir in /etc/rancher/k3s/config.yaml.d/*.yaml (lexical order)
#   4. data-dir in /etc/rancher/k3s/config.yaml
#   5. K3S_DATA_DIR in the environment of the unit (Environment=, EnvironmentFile=)
#   6. /var/lib/rancher/k3s
# Sets and exports DATA_DIR and TN_CA_DATADIR_SRC (where it came from). Reading only; no globbing
# (the bootstrap scripts run with noglob). A data-dir with white space in it is not supported.

tn_ca_unit_name() { echo "${PRODUCT:-k3s}.service"; }

# tn_ca_unit_text: the text of the k3s server unit (merged with drop-ins when systemd tells), or fail
tn_ca_unit_text() {
  _tn_un=$(tn_ca_unit_name)
  if [ -n "${TN_K3S_UNIT_FILE:-}" ]; then
    [ -r "$TN_K3S_UNIT_FILE" ] && cat "$TN_K3S_UNIT_FILE"
    return
  fi
  if command -v systemctl >/dev/null 2>&1 && systemctl cat "$_tn_un" 2>/dev/null; then return 0; fi
  for _tn_uf in "/etc/systemd/system/$_tn_un" "/lib/systemd/system/$_tn_un" "/usr/lib/systemd/system/$_tn_un"; do
    if [ -r "$_tn_uf" ]; then cat "$_tn_uf"; return 0; fi
  done
  return 1
}

# tn_ca_unit_exists: is there a k3s SERVER unit (k3s.service; an agent has k3s-agent.service)
tn_ca_unit_exists() { tn_ca_unit_text >/dev/null 2>&1; }

# tn_ca_yaml_datadir FILE: the value of the last top-level `data-dir:` key, or nothing
tn_ca_yaml_datadir() {
  [ -r "$1" ] || return 0
  awk '
    { sub(/\r$/, "") }
    /^data-dir[ \t]*:/ {
      v = $0; sub(/^data-dir[ \t]*:[ \t]*/, "", v)
      q = substr(v, 1, 1)
      if (q == "\"" || q == "\047") { v = substr(v, 2); i = index(v, q); if (i > 0) v = substr(v, 1, i - 1) }
      else { sub(/[ \t]+#.*$/, "", v); sub(/^#.*$/, "", v); sub(/[ \t]+$/, "", v) }
      val = v
    }
    END { if (val != "") print val }' "$1"
}

# tn_ca_unit_datadir: --data-dir/-d of the last ExecStart= of the unit, or nothing
tn_ca_unit_datadir() {
  tn_ca_unit_text 2>/dev/null | awk '
    cont { line = $0; if (line ~ /\\[ \t]*$/) { sub(/\\[ \t]*$/, "", line); buf = buf " " line } else { buf = buf " " line; cont = 0; last = buf; buf = "" } ; next }
    /^ExecStart=/ {
      line = $0; sub(/^ExecStart=/, "", line)
      if (line ~ /\\[ \t]*$/) { sub(/\\[ \t]*$/, "", line); buf = line; cont = 1 } else { last = line }
      next }
    END {
      if (cont) last = buf
      gsub(/["\047]/, "", last); sub(/^[ \t]*[-@+!:]+/, "", last)
      n = split(last, t, /[ \t]+/); val = ""
      for (i = 1; i <= n; i++) {
        if ((t[i] == "--data-dir" || t[i] == "-d") && i < n) val = t[i + 1]
        else if (t[i] ~ /^--data-dir=/) val = substr(t[i], 12)
        else if (t[i] ~ /^-d=/) val = substr(t[i], 4)
      }
      if (val != "") print val }'
}

# tn_ca_unit_env_datadir: K3S_DATA_DIR from Environment=/EnvironmentFile= of the unit
tn_ca_unit_env_datadir() {
  _tn_ut=$(tn_ca_unit_text 2>/dev/null) || return 0
  _tn_ev=$(printf '%s\n' "$_tn_ut" | sed -n 's/^Environment=.*K3S_DATA_DIR=\([^ "]*\).*/\1/p' | tail -n 1)
  if [ -z "$_tn_ev" ]; then
    for _tn_ef in $(printf '%s\n' "$_tn_ut" | sed -n 's/^EnvironmentFile=-\{0,1\}//p'); do
      [ -r "$_tn_ef" ] || continue
      _tn_v=$(sed -n 's/^K3S_DATA_DIR=//p' "$_tn_ef" | tail -n 1 | sed "s/^['\"]//; s/['\"]\$//")
      [ -z "$_tn_v" ] || _tn_ev=$_tn_v
    done
  fi
  [ -z "$_tn_ev" ] || echo "$_tn_ev"
}

# tn_ca_config_dropins DIR: the drop-ins k3s reads from DIR, in the order it reads them (the last one wins):
# *.yaml and *.yml, the suffix without regard to case (k3s: HasSuffixI in pkg/configfilearg), by name.
tn_ca_config_dropins() {
  find "$1" -maxdepth 1 -type f \( -iname '*.yaml' -o -iname '*.yml' \) 2>/dev/null | LC_ALL=C sort
}

tn_ca_resolve_datadir() {
  _tn_pr=${PRODUCT:-k3s}
  if [ -n "${DATA_DIR:-}" ]; then
    # our own earlier resolution keeps its source; anything else was given by the caller
    if [ "${TN_CA_DATADIR_RES:-}" != "$DATA_DIR" ]; then TN_CA_DATADIR_SRC="--data-dir / DATA_DIR"; fi
    export DATA_DIR TN_CA_DATADIR_SRC; return 0
  fi
  _tn_cd=${TN_K3S_CONFIG_DIR:-/etc/rancher/$_tn_pr}
  _tn_v=$(tn_ca_unit_datadir)
  if [ -n "$_tn_v" ]; then DATA_DIR=$_tn_v; TN_CA_DATADIR_SRC="ExecStart of $(tn_ca_unit_name)"; fi
  if [ -z "$_tn_v" ] && [ -d "$_tn_cd/config.yaml.d" ]; then
    for _tn_f in $(tn_ca_config_dropins "$_tn_cd/config.yaml.d"); do
      _tn_x=$(tn_ca_yaml_datadir "$_tn_f")
      if [ -n "$_tn_x" ]; then _tn_v=$_tn_x; TN_CA_DATADIR_SRC=$_tn_f; fi
    done
    [ -z "$_tn_v" ] || DATA_DIR=$_tn_v
  fi
  if [ -z "$_tn_v" ]; then
    _tn_v=$(tn_ca_yaml_datadir "$_tn_cd/config.yaml")
    if [ -n "$_tn_v" ]; then DATA_DIR=$_tn_v; TN_CA_DATADIR_SRC="$_tn_cd/config.yaml"; fi
  fi
  if [ -z "$_tn_v" ]; then
    _tn_v=$(tn_ca_unit_env_datadir)
    if [ -n "$_tn_v" ]; then DATA_DIR=$_tn_v; TN_CA_DATADIR_SRC="K3S_DATA_DIR of $(tn_ca_unit_name)"; fi
  fi
  if [ -z "$_tn_v" ]; then DATA_DIR=/var/lib/rancher/$_tn_pr; TN_CA_DATADIR_SRC=default; fi
  TN_CA_DATADIR_RES=$DATA_DIR
  export DATA_DIR TN_CA_DATADIR_SRC TN_CA_DATADIR_RES
}

# tn_ca_datadir_consistent: 0 if DATA_DIR is the data dir that the config files of k3s set, or they set
# none. Sets TN_CA_CFG_DATADIR / TN_CA_CFG_DATADIR_SRC to the config file's value and file otherwise.
# The k3s installer writes a new unit WITHOUT --data-dir and K3S_DATA_DIR: from then on k3s takes its data
# dir from the config files. A DATA_DIR that came from anywhere else (the ExecStart of the old unit,
# K3S_DATA_DIR, the caller) and differs from them would send k3s to a different directory, with the
# CA we prepared, and the cluster, left behind.
tn_ca_datadir_consistent() {
  TN_CA_CFG_DATADIR=; TN_CA_CFG_DATADIR_SRC=
  _tn_cd=${TN_K3S_CONFIG_DIR:-/etc/rancher/${PRODUCT:-k3s}}
  if [ -d "$_tn_cd/config.yaml.d" ]; then
    for _tn_f in $(tn_ca_config_dropins "$_tn_cd/config.yaml.d"); do
      _tn_x=$(tn_ca_yaml_datadir "$_tn_f")
      if [ -n "$_tn_x" ]; then TN_CA_CFG_DATADIR=$_tn_x; TN_CA_CFG_DATADIR_SRC=$_tn_f; fi
    done
  fi
  if [ -z "$TN_CA_CFG_DATADIR" ]; then
    _tn_x=$(tn_ca_yaml_datadir "$_tn_cd/config.yaml")
    if [ -n "$_tn_x" ]; then TN_CA_CFG_DATADIR=$_tn_x; TN_CA_CFG_DATADIR_SRC=$_tn_cd/config.yaml; fi
  fi
  [ -n "$TN_CA_CFG_DATADIR" ] || return 0
  _tn_a=$(printf '%s\n' "$DATA_DIR" | sed 's|//*|/|g; s|\(.\)/$|\1|')
  _tn_b=$(printf '%s\n' "$TN_CA_CFG_DATADIR" | sed 's|//*|/|g; s|\(.\)/$|\1|')
  [ "$_tn_a" = "$_tn_b" ]
}

# tn_ca_require_consistent_datadir: tn_ca_datadir_consistent, or stop with advice. Needs DATA_DIR (call
# tn_ca_resolve_datadir first). A server script calls it right after tn_ca_resolve_datadir, before the
# k3s release step changes anything; tn_k3s_ca_setup calls it again (cheap, and the library stays safe on its own).
tn_ca_require_consistent_datadir() {
  tn_ca_datadir_consistent ||
    tn_ca_fatal "k3s runs from ${DATA_DIR} (${TN_CA_DATADIR_SRC}), but ${TN_CA_CFG_DATADIR_SRC} sets data-dir: ${TN_CA_CFG_DATADIR}. The k3s installer that follows writes a new unit without --data-dir, after which k3s would start from ${TN_CA_CFG_DATADIR}: an empty directory, with its own new CA, and the cluster in ${DATA_DIR} left behind. Make them agree first: either set data-dir in ${TN_CA_CFG_DATADIR_SRC} to ${DATA_DIR} (if that is where the cluster is), or remove that line and run this script again. The CA and k3s were not changed"
}

# ----------------------------------------------------------------- the root certificate

# tn_ca_reissue_root SRC_CERT KEY OUT_CERT
# New self-signed root from the SAME key and subject as SRC_CERT (and the same subject key
# identifier), with CA:TRUE and keyUsage keyCertSign, valid 7300 days and a random 128-bit
# serial (never the serial of SRC_CERT). The extensions of SRC_CERT are not carried over (no
# duplicates, no extendedKeyUsage).
tn_ca_reissue_root() {
  _tn_rw=$(mktemp -d) || return 1
  _tn_rski=$(tn_ca_ski "$1")
  [ -n "$_tn_rski" ] || _tn_rski='hash'
  cat > "$_tn_rw/root.cnf" <<EOF
[v3_root]
subjectKeyIdentifier = $_tn_rski
authorityKeyIdentifier = keyid:always
basicConstraints = critical, CA:true
keyUsage = critical, digitalSignature, keyEncipherment, keyCertSign, cRLSign
EOF
  _tn_serial=$("$OPENSSL" rand -hex 16) || { rm -rf "$_tn_rw"; return 1; }
  # Not `x509 -in SRC -signkey KEY -set_serial`: OpenSSL 1.1.1 (Jetson Ubuntu 18.04/20.04,
  # Raspberry Pi bullseye) silently ignores -set_serial there and the new root would keep the
  # serial of SRC, so two different certificates would share issuer and serial (RFC 5280
  # 4.1.2.2; NSS rejects that). `x509 -req` honours it on every version. The request carries
  # the subject and public key of SRC only; the extensions come from root.cnf.
  { "$OPENSSL" x509 -x509toreq -in "$1" -signkey "$2" -out "$_tn_rw/root.csr" &&
    "$OPENSSL" x509 -req -in "$_tn_rw/root.csr" -signkey "$2" -extfile "$_tn_rw/root.cnf" -extensions v3_root \
      -set_serial "0x$_tn_serial" -days 7300 -sha256 -out "$3"; } >/dev/null 2>&1
  _tn_rc=$?
  rm -rf "$_tn_rw"
  if [ "$_tn_rc" -ne 0 ]; then tn_ca_warn "cannot re-issue the root certificate from $1"; return 1; fi
  tn_ca_has_certsign "$3" || { tn_ca_warn "re-issued root has no keyCertSign"; return 1; }
  if [ "$("$OPENSSL" x509 -in "$1" -noout -serial 2>/dev/null)" = "$("$OPENSSL" x509 -in "$3" -noout -serial 2>/dev/null)" ]; then
    tn_ca_warn "re-issued root has the serial of the original certificate (this openssl ignores -set_serial)"
  fi
}

# ----------------------------------------------------------------- PKI generation

# tn_ca_generate_pki TLS_DIR ROOT_CERT ROOT_KEY SUFFIX
# TLS_DIR must exist and be empty. Creates root-ca.{pem,key,crt}, intermediate-ca.*, the five
# leaf CAs and service.key in it exactly as the old bootstrap scripts did, below ROOT_CERT.
# Runs in a subshell: the caller's directory and traps are not touched.
tn_ca_generate_pki() {
  (
    # `set -e` is only a convenience here: this function is always called in an `if`/`||`
    # context, where every POSIX shell ignores errexit, even when it is set again in a
    # subshell. So EVERY step ends in `|| exit 1`, and the caller validates the result.
    set -e
    tls=$1 root=$2 rkey=$3 suffix=$4
    product=${PRODUCT:-k3s}
    cd "$tls" || exit 1
    mkdir -p etcd .ca/certs || exit 1
    cp "$root" root-ca.pem || exit 1
    cp "$rkey" root-ca.key || exit 1
    : > .ca/index || exit 1
    "$OPENSSL" rand -hex 8 > .ca/serial || exit 1
    cat > .ca/config <<'EOF' || exit 1
[ca]
default_ca = ca_default
[ca_default]
dir = ./.ca
database = $dir/index
serial = $dir/serial
new_certs_dir = $dir/certs
default_md = sha256
policy = policy_anything
[policy_anything]
commonName = supplied
[req]
distinguished_name = req_distinguished_name
[req_distinguished_name]
[v3_ca]
subjectKeyIdentifier = hash
authorityKeyIdentifier = keyid:always
basicConstraints = critical, CA:true
keyUsage = critical, digitalSignature, keyEncipherment, keyCertSign
EOF
    if "$OPENSSL" version | grep -qF 'OpenSSL 3'; then GENRSA_FLAGS=-traditional; else GENRSA_FLAGS=; fi

    # a CN is at most 64 characters (long device names used to break the bootstrap here)
    cn() { printf '%s' "$1" | cut -c1-64; }
    # quiet openssl, but show what it said when it fails
    quiet() { "$@" > .ca/log 2>&1 || { cat .ca/log >&2; return 1; }; }
    # tn_issue CSR OUT SIGNER_KEY SIGNER_CERT. Requests go through files, not a pipe: no
    # /dev/stdin (missing in some chroots), and a failing `req` is not hidden by the pipeline.
    tn_issue() {
      quiet "$OPENSSL" ca -batch -notext -days 3700 -in "$1" -out "$2" \
        -keyfile "$3" -cert "$4" -config .ca/config -extensions v3_ca
    }

    tn_ca_info "Generating Kubernetes service account issuer RSA key"
    # shellcheck disable=SC2086
    quiet "$OPENSSL" genrsa $GENRSA_FLAGS -out service.key 2048 || exit 1

    cat root-ca.pem > root-ca.crt || exit 1

    tn_ca_info "Generating intermediate certificate authority RSA key and certificate"
    # shellcheck disable=SC2086
    quiet "$OPENSSL" genrsa $GENRSA_FLAGS -out intermediate-ca.key 4096 || exit 1
    quiet "$OPENSSL" req -new -nodes -subj "/CN=$(cn "${product}-intermediate-ca@${suffix}")" \
      -key intermediate-ca.key -out .ca/req.csr || exit 1
    tn_issue .ca/req.csr intermediate-ca.pem root-ca.key root-ca.pem || exit 1
    cat intermediate-ca.pem root-ca.pem > intermediate-ca.crt || exit 1

    # a new leaf CA for every control-plane and etcd component
    for TYPE in client server request-header etcd/peer etcd/server; do
      name="${product}-$(echo "$TYPE" | tr / -)-ca"
      tn_ca_info "Generating ${name} leaf certificate authority EC key and certificate"
      quiet "$OPENSSL" ecparam -name prime256v1 -genkey -noout -out "${TYPE}-ca.key" || exit 1
      quiet "$OPENSSL" req -new -nodes -subj "/CN=$(cn "${name}@${suffix}")" -key "${TYPE}-ca.key" \
        -out .ca/req.csr || exit 1
      tn_issue .ca/req.csr "${TYPE}-ca.pem" intermediate-ca.key intermediate-ca.pem || exit 1
      cat "${TYPE}-ca.pem" intermediate-ca.pem root-ca.pem > "${TYPE}-ca.crt" || exit 1
    done
    rm -rf .ca || exit 1
  )
}

# tn_ca_validate_pki TLS_DIR: 0 only if TLS_DIR holds a complete PKI: every file k3s reads is
# there and not empty, the bundles have the expected number of certificates, every key matches
# its certificate, and the intermediate and all leaf CAs chain to root-ca.pem. Warns about the
# first problem. Run on a freshly generated directory BEFORE it replaces the live one.
tn_ca_validate_pki() {
  _tn_vd=$1
  for _tn_f in root-ca.pem root-ca.key root-ca.crt service.key intermediate-ca.pem intermediate-ca.key intermediate-ca.crt \
    client-ca.pem client-ca.key client-ca.crt server-ca.pem server-ca.key server-ca.crt \
    request-header-ca.pem request-header-ca.key request-header-ca.crt \
    etcd/peer-ca.pem etcd/peer-ca.key etcd/peer-ca.crt etcd/server-ca.pem etcd/server-ca.key etcd/server-ca.crt; do
    [ -s "$_tn_vd/$_tn_f" ] || { tn_ca_warn "generated PKI is incomplete: $_tn_f is missing or empty"; return 1; }
  done
  _tn_vw=$(mktemp -d) || return 1
  _tn_vok=1
  for _tn_t in intermediate client server request-header etcd/peer etcd/server; do
    _tn_c=$_tn_vd/${_tn_t}-ca.pem
    "$OPENSSL" verify -CAfile "$_tn_vd/root-ca.pem" -untrusted "$_tn_vd/intermediate-ca.pem" "$_tn_c" >/dev/null 2>&1 ||
      { tn_ca_warn "generated PKI: ${_tn_t}-ca.pem does not chain to root-ca.pem"; _tn_vok=0; break; }
    [ "$(tn_ca_pubhash_cert "$_tn_c")" = "$(tn_ca_pubhash_key "$_tn_vd/${_tn_t}-ca.key")" ] ||
      { tn_ca_warn "generated PKI: ${_tn_t}-ca.key does not belong to ${_tn_t}-ca.pem"; _tn_vok=0; break; }
    case $_tn_t in intermediate) _tn_want=2 ;; *) _tn_want=3 ;; esac
    rm -rf "$_tn_vw/b"
    _tn_n=$(tn_ca_split "$_tn_vd/${_tn_t}-ca.crt" "$_tn_vw/b")
    [ "${_tn_n:-0}" -eq "$_tn_want" ] ||
      { tn_ca_warn "generated PKI: ${_tn_t}-ca.crt holds ${_tn_n:-0} certificates, expected $_tn_want"; _tn_vok=0; break; }
  done
  if [ "$_tn_vok" = 1 ]; then
    [ "$(tn_ca_pubhash_cert "$_tn_vd/root-ca.pem")" = "$(tn_ca_pubhash_key "$_tn_vd/root-ca.key")" ] ||
      { tn_ca_warn "generated PKI: root-ca.key does not belong to root-ca.pem"; _tn_vok=0; }
  fi
  rm -rf "$_tn_vw"
  [ "$_tn_vok" = 1 ]
}

# ----------------------------------------------------------------- state of a data dir

# tn_ca_find_key DATA_DIR ROOT_CERT -> sets TN_CA_KEY to a private key file whose public
# key matches ROOT_CERT (server/tls/root-ca.key first, then the device key); returns 1 if none.
tn_ca_find_key() {
  TN_CA_KEY=; TN_CA_UNREADABLE=
  _tn_want=$(tn_ca_pubhash_cert "$2")
  [ -n "$_tn_want" ] || return 1
  for _tn_k in "$1/server/tls/root-ca.key" "${CONFIG_PATH:-/etc/teknoir}/rsa_private.pem"; do
    [ -f "$_tn_k" ] || continue
    [ -r "$_tn_k" ] || { TN_CA_UNREADABLE=$_tn_k; continue; }
    if [ "$(tn_ca_pubhash_key "$_tn_k")" = "$_tn_want" ]; then TN_CA_KEY=$_tn_k; return 0; fi
  done
  return 1
}

# tn_ca_state DATA_DIR
# Sets TN_CA_STATE and TN_CA_MSG; returns the `check` exit code.
#   not-a-server        no server/tls/server-ca.crt and no k3s server unit (an agent node, or nothing
#                       installed yet)
#   no-server-data      a k3s server unit exists but there is no server/tls/server-ca.crt in the data
#                       dir we resolved: k3s has not started yet, or its data dir is somewhere else.
#                       Never reported as ok (rc 4)
#   ok                  the root has keyCertSign and is valid for more than 90 days
#   needs-rotation      initialised, root unusable, we hold the key
#   needs-regeneration  never initialised, PKI unusable, we hold the key
#   foreign             root unusable and we do not hold its key
# Also sets TN_CA_ROOT_USABLE: 0 when k3s refuses the root as it is (a multi-certificate bundle
# whose root has no keyCertSign, or an expired root), 1 otherwise (including "expires soon").
tn_ca_state() {
  TN_CA_STATE=; TN_CA_MSG=; TN_CA_ROOT_USABLE=1
  _tn_tls=$1/server/tls
  if [ ! -f "$_tn_tls/server-ca.crt" ]; then
    if tn_ca_unit_exists; then
      TN_CA_STATE=no-server-data
      TN_CA_MSG="the k3s server unit $(tn_ca_unit_name) exists, but $_tn_tls/server-ca.crt does not (data dir $1, taken from ${TN_CA_DATADIR_SRC:-the caller}): k3s has not started yet, or its data dir is elsewhere. Check 'systemctl cat $(tn_ca_unit_name)' and ${TN_K3S_CONFIG_DIR:-/etc/rancher/${PRODUCT:-k3s}}/config.yaml*, then pass --data-dir DIR"
      return 4
    fi
    TN_CA_STATE=not-a-server; TN_CA_MSG="no $_tn_tls/server-ca.crt and no k3s server unit: nothing to check"; return 0
  fi
  _tn_sw=$(mktemp -d) || return 1
  if ! tn_ca_lastcert "$_tn_tls/server-ca.crt" "$_tn_sw/root.pem"; then
    rm -rf "$_tn_sw"; TN_CA_STATE=error; TN_CA_MSG="cannot read the root certificate from $_tn_tls/server-ca.crt"; return 1
  fi
  _tn_ncert=$(tn_ca_split "$_tn_tls/server-ca.crt" "$_tn_sw/b")
  _tn_why=
  TN_CA_ROOT_USABLE=1   # 0: k3s refuses this root now (no keyCertSign in a bundle, or expired)
  if [ "$_tn_ncert" -gt 1 ] && ! tn_ca_has_certsign "$_tn_sw/root.pem"; then
    _tn_why="the root certificate has no keyCertSign"; TN_CA_ROOT_USABLE=0
  fi
  if ! "$OPENSSL" x509 -in "$_tn_sw/root.pem" -noout -checkend 0 >/dev/null 2>&1; then
    TN_CA_ROOT_USABLE=0
    [ -n "$_tn_why" ] || _tn_why="the root certificate is expired"
  elif [ -z "$_tn_why" ] && ! "$OPENSSL" x509 -in "$_tn_sw/root.pem" -noout -checkend 7776000 >/dev/null 2>&1; then
    _tn_why="the root certificate expires within 90 days"
  fi
  if [ -z "$_tn_why" ]; then
    rm -rf "$_tn_sw"; TN_CA_STATE=ok; TN_CA_MSG="the root certificate is acceptable to k3s"; return 0
  fi
  if ! tn_ca_find_key "$1" "$_tn_sw/root.pem" && [ -n "$TN_CA_UNREADABLE" ]; then
    rm -rf "$_tn_sw"; TN_CA_STATE=error; TN_CA_MSG="cannot read $TN_CA_UNREADABLE: run as root"; return 1
  fi
  if tn_ca_find_key "$1" "$_tn_sw/root.pem"; then
    if tn_ca_initialised "$1"; then
      TN_CA_STATE=needs-rotation; TN_CA_MSG="$_tn_why: k3s v1.34.12/v1.35.9/v1.36.5/v1.37.1 and later will refuse to start; run: sh tn-k3s-ca.sh remediate"
      rm -rf "$_tn_sw"; return 2
    fi
    TN_CA_STATE=needs-regeneration; TN_CA_MSG="$_tn_why, and k3s never finished its first start here: the PKI can simply be regenerated; run: sh tn-k3s-ca.sh remediate"
    rm -rf "$_tn_sw"; return 3
  fi
  TN_CA_STATE=foreign; TN_CA_MSG="$_tn_why, but this is not the Teknoir device CA (no matching key in $_tn_tls/root-ca.key or ${CONFIG_PATH:-/etc/teknoir}/rsa_private.pem)"
  rm -rf "$_tn_sw"; return 4
}

# tn_ca_suffix DATA_DIR: "<device-id>.<domain>" for the CN of new CAs
tn_ca_suffix() {
  if [ -n "${_DEVICE_ID:-}" ] && [ -n "${_DOMAIN:-}" ]; then echo "${_DEVICE_ID}.${_DOMAIN}"; return 0; fi
  _tn_f=$1/server/tls/intermediate-ca.pem
  if [ -f "$_tn_f" ]; then
    _tn_cn=$("$OPENSSL" x509 -in "$_tn_f" -noout -subject 2>/dev/null | sed -n 's/.*CN *= *//p' | sed 's/,.*//')
    case $_tn_cn in *@*) echo "${_tn_cn#*@}"; return 0 ;; esac
  fi
  echo "$(hostname 2>/dev/null || echo device).local"
}

# ----------------------------------------------------------------- setup (sourced by the bootstrap scripts)

# tn_ca_pki_current DATA_DIR: 0 if the PKI in server/tls is complete, belongs to the current
# device key, has a usable root and chains from the leaf CAs to it.
tn_ca_pki_current() {
  _tn_tls=$1/server/tls
  for _tn_f in $TN_CA_BUNDLES root-ca.pem root-ca.key service.key intermediate-ca.pem intermediate-ca.key; do
    [ -s "$_tn_tls/$_tn_f" ] || return 1
  done
  tn_ca_state "$1" >/dev/null || return 1
  [ "$TN_CA_STATE" = ok ] || return 1
  [ "$(tn_ca_pubhash_cert "$_tn_tls/root-ca.pem")" = "$(tn_ca_pubhash_key "${CONFIG_PATH:-/etc/teknoir}/rsa_private.pem")" ] || return 1
  _tn_cw=$(mktemp -d) || return 1
  _tn_cok=0
  tn_ca_split "$_tn_tls/server-ca.crt" "$_tn_cw" >/dev/null
  if "$OPENSSL" verify -CAfile "$_tn_tls/root-ca.pem" -untrusted "$_tn_tls/intermediate-ca.pem" "$_tn_cw/c0.pem" >/dev/null 2>&1; then _tn_cok=1; fi
  rm -rf "$_tn_cw"
  [ "$_tn_cok" = 1 ]
}

# tn_ca_backup_tls DATA_DIR: tar server/tls (if it holds anything) to server/tls-backup-tn-<ts>.tar.
# The tars hold keys: the newest 3 are kept.
tn_ca_backup_tls() {
  [ -d "$1/server/tls" ] || return 0
  _tn_bk="$1/server/tls-backup-tn-$(tn_ca_ts).tar"
  ( umask 077; tar -C "$1/server" -cf "$_tn_bk" tls ) || { tn_ca_warn "cannot back up $1/server/tls"; return 1; }
  chmod 600 "$_tn_bk" 2>/dev/null || true
  for _tn_o in $(find "$1/server" -maxdepth 1 -type f -name 'tls-backup-tn-*.tar' 2>/dev/null | LC_ALL=C sort |
    awk '{ a[NR] = $0 } END { for (i = 1; i <= NR - 3; i++) print a[i] }'); do rm -f "$_tn_o"; done
  tn_ca_info "Previous CA files saved to $_tn_bk"
}

# tn_ca_run_bounded SECONDS CMD...: run CMD, give up (return 124) after SECONDS. The k3s unit has
# TimeoutStartSec=0, so `systemctl start|restart` of a k3s that cannot start (the very case this tool
# repairs) never returns. Without `timeout` (very old coreutils) the command runs unbounded.
tn_ca_run_bounded() {
  _tn_rb=$1; shift
  if command -v timeout >/dev/null 2>&1; then timeout "$_tn_rb" "$@"; else "$@"; fi
}
tn_ca_svc_timeout() { echo "${TN_K3S_SERVICE_TIMEOUT:-600}"; }

# tn_ca_unit_state: the ActiveState of the k3s unit (active, activating, ...), or nothing
tn_ca_unit_state() {
  command -v systemctl >/dev/null 2>&1 || return 0
  systemctl is-active "$(tn_ca_unit_name)" 2>/dev/null || true
}

# k3s around the replacement of server/tls. Stopped if the unit runs OR is starting (activating: a
# k3s that hangs in its first start, or restarts in a loop, is exactly the device that is repaired
# here, and it would rewrite files under us), never when there is no unit (a bootstrap of a new
# device has no k3s yet), and started again only if we stopped it.
tn_ca_k3s_active() {
  if [ -n "${TN_K3S_STOP_CMD:-}" ]; then return 0; fi
  case $(tn_ca_unit_state) in active|activating|reloading) return 0 ;; esac
  return 1
}
# tn_ca_k3s_ready: the unit is up and READY (Type=notify: k3s said so). Only that state.
tn_ca_k3s_ready() { [ "$(tn_ca_unit_state)" = active ]; }
tn_ca_k3s_stop() {
  if [ -n "${TN_K3S_STOP_CMD:-}" ]; then tn_ca_run_bounded "$(tn_ca_svc_timeout)" sh -c "$TN_K3S_STOP_CMD"
  else tn_ca_run_bounded "$(tn_ca_svc_timeout)" systemctl stop "$(tn_ca_unit_name)"; fi
}
tn_ca_k3s_start() {
  if [ -n "${TN_K3S_START_CMD:-}" ]; then tn_ca_run_bounded "$(tn_ca_svc_timeout)" sh -c "$TN_K3S_START_CMD"
  else tn_ca_run_bounded "$(tn_ca_svc_timeout)" systemctl start "$(tn_ca_unit_name)"; fi
}

# tn_ca_guard_unknown_datadir DATA_DIR: 0 if it is fine to go on. A data dir that k3s never
# initialised (no server/token) while the k3s unit is up and READY means that k3s runs from a
# DIFFERENT data dir than the one we resolved (a --config file, a drop-in we do not read ...):
# generating a CA here would not touch the CA that k3s uses, and the installer that follows would
# upgrade a k3s whose CA was never checked.
tn_ca_guard_unknown_datadir() {
  tn_ca_initialised "$1" && return 0
  tn_ca_k3s_ready || return 0
  tn_ca_warn "the k3s unit $(tn_ca_unit_name) is active and ready, but the data dir $1 (taken from ${TN_CA_DATADIR_SRC:-the caller}) was never initialised by k3s (no server/token): k3s runs from another data dir, so the CA that k3s uses was not looked at. Find it ('systemctl cat $(tn_ca_unit_name)', ${TN_K3S_CONFIG_DIR:-/etc/rancher/${PRODUCT:-k3s}}/config.yaml*, k3s server --config) and run again with DATA_DIR=/that/dir (or --data-dir). The CA and k3s were not changed"
  return 1
}

# tn_ca_install_pki DATA_DIR SUFFIX: build a complete new PKI from the device certificate and
# replace server/tls with it. The data dir must not be initialised. Order: generate and validate in
# a work dir; stop k3s (if it runs: a hung start would rewrite files under us); back server/tls up;
# move the old dir aside (rename); check that the target is gone; move the new dir in; validate the
# INSTALLED result. Any failure puts the old dir back and starts k3s again if it was stopped.
tn_ca_install_pki() {
  _tn_dd=$1 _tn_sfx=$2
  _tn_cfg=${CONFIG_PATH:-/etc/teknoir}
  [ -s "$_tn_cfg/rsa_public.pem" ] && [ -s "$_tn_cfg/rsa_private.pem" ] ||
    { tn_ca_warn "device certificate or key missing in $_tn_cfg"; return 1; }
  mkdir -p "$_tn_dd/server" || return 1
  _tn_new=$(mktemp -d "$_tn_dd/server/.tn-tls-new.XXXXXX") || return 1
  _tn_rt=$(mktemp -d) || { rm -rf "$_tn_new"; return 1; }
  tn_ca_info "Re-issuing the device certificate as the k3s root CA (keyCertSign)"
  if ! tn_ca_reissue_root "$_tn_cfg/rsa_public.pem" "$_tn_cfg/rsa_private.pem" "$_tn_rt/root.pem"; then
    rm -rf "$_tn_new" "$_tn_rt"; return 1
  fi
  mkdir -p "$_tn_new/etcd"
  if ! tn_ca_generate_pki "$_tn_new" "$_tn_rt/root.pem" "$_tn_cfg/rsa_private.pem" "$_tn_sfx"; then
    tn_ca_warn "PKI generation failed"
    rm -rf "$_tn_new" "$_tn_rt"; return 1
  fi
  if ! tn_ca_validate_pki "$_tn_new"; then
    tn_ca_warn "the generated PKI is not usable: $_tn_dd/server/tls was not changed"
    rm -rf "$_tn_new" "$_tn_rt"; return 1
  fi
  rm -rf "$_tn_rt"
  _tn_tlsd=$_tn_dd/server/tls
  _tn_stopped=0
  if tn_ca_k3s_active; then
    tn_ca_info "Stopping k3s while $_tn_tlsd is replaced"
    if ! tn_ca_k3s_stop; then
      tn_ca_warn "cannot stop k3s: $_tn_tlsd was not changed"
      rm -rf "$_tn_new"; return 1
    fi
    _tn_stopped=1
  fi
  _tn_aside=
  _tn_ok=1
  if [ -d "$_tn_tlsd" ]; then
    tn_ca_backup_tls "$_tn_dd" || _tn_ok=0
    # keep an old service account key so tokens issued before stay valid
    if [ "$_tn_ok" = 1 ] && [ -s "$_tn_tlsd/service.key" ]; then
      cat "$_tn_tlsd/service.key" >> "$_tn_new/service.key" || _tn_ok=0
    fi
    if [ "$_tn_ok" = 1 ]; then
      _tn_aside=$_tn_dd/server/.tn-tls-old.$(tn_ca_ts).$$
      if ! mv "$_tn_tlsd" "$_tn_aside"; then _tn_aside=; _tn_ok=0; fi
    fi
  fi
  if [ "$_tn_ok" = 1 ]; then
    if [ -e "$_tn_tlsd" ] || [ -L "$_tn_tlsd" ]; then
      tn_ca_warn "$_tn_tlsd is still there after it was moved aside: not replacing it"; _tn_ok=0
    elif ! mv "$_tn_new" "$_tn_tlsd"; then
      tn_ca_warn "cannot move the new PKI to $_tn_tlsd"; _tn_ok=0
    else
      chmod 750 "$_tn_tlsd" 2>/dev/null || true
      tn_ca_validate_pki "$_tn_tlsd" || { tn_ca_warn "the installed PKI in $_tn_tlsd does not validate"; _tn_ok=0; }
    fi
  fi
  if [ "$_tn_ok" != 1 ]; then
    if [ -n "$_tn_aside" ]; then
      rm -rf "$_tn_tlsd"
      if mv "$_tn_aside" "$_tn_tlsd"; then tn_ca_warn "the previous $_tn_tlsd was restored"
      else tn_ca_warn "CANNOT RESTORE the previous server/tls: it is in $_tn_aside (a backup tar is next to it)"; fi
    fi
    rm -rf "$_tn_new"
    if [ "$_tn_stopped" = 1 ]; then
      tn_ca_k3s_start || tn_ca_warn "k3s was stopped and could not be started again: start it yourself (systemctl start $(tn_ca_unit_name))"
    fi
    return 1
  fi
  [ -z "$_tn_aside" ] || rm -rf "$_tn_aside"
  tn_ca_changed "the k3s PKI in $_tn_tlsd was generated from the device certificate"
  if [ "$_tn_stopped" = 1 ]; then
    if tn_ca_k3s_start; then
      tn_ca_changed "k3s was stopped and started again"
    else
      tn_ca_changed "k3s was stopped and could not be started again"
      tn_ca_warn "the new CA is installed, but k3s could not be started: start it yourself (systemctl start $(tn_ca_unit_name))"
    fi
  fi
  return 0
}

# tn_k3s_ca_setup: the bootstrap step. Uses CONFIG_PATH, DATA_DIR, PRODUCT, _DEVICE_ID, _DOMAIN.
#   never started   -> create the PKI below the re-issued root
#   started, ok     -> leave everything alone (re-running a bootstrap script is safe)
#   started, old    -> repair it first (TN_K3S_CA_AUTO_REMEDIATE=false: stop with advice)
#   started, foreign (not the Teknoir device CA, we hold no key): left alone while k3s still
#                      accepts its root; otherwise STOP (exit 1): the k3s installer that follows
#                      would upgrade k3s and the device would no longer start
tn_k3s_ca_setup() {
  tn_ca_info "Prepare K3s CA Certs"
  export PRODUCT="${PRODUCT:-k3s}"
  tn_ca_openssl || tn_ca_fatal "openssl not found or missing Elliptic Curve (ecparam) support."
  tn_ca_info "Using ${OPENSSL}: $("$OPENSSL" version)"
  tn_ca_resolve_datadir
  tn_ca_info "k3s data dir: ${DATA_DIR} (${TN_CA_DATADIR_SRC})"
  tn_ca_require_consistent_datadir
  _tn_cfg=${CONFIG_PATH:-/etc/teknoir}
  [ -s "$_tn_cfg/rsa_public.pem" ] && [ -s "$_tn_cfg/rsa_private.pem" ] ||
    tn_ca_fatal "device certificate or key missing in $_tn_cfg"

  if tn_ca_initialised "$DATA_DIR"; then
    tn_ca_state "$DATA_DIR" || true
    case $TN_CA_STATE in
      ok|not-a-server)
        [ "$TN_CA_STATE" != ok ] || tn_ca_clean_stages "$DATA_DIR"
        tn_ca_info "k3s has already started on this device: the existing CA is left alone ($TN_CA_STATE: $TN_CA_MSG)"
        return 0 ;;
      foreign)
        # not the Teknoir device CA: we cannot repair it. A root k3s refuses must stop the
        # bootstrap, or the installer below would upgrade k3s and brick the device.
        if [ "$TN_CA_ROOT_USABLE" != 1 ]; then
          tn_ca_fatal "$TN_CA_MSG. The k3s CA of this device cannot be repaired here, and a k3s upgrade would stop it from starting: k3s must not be upgraded. The CA and k3s were not changed. Restore the matching CA key, or contact Teknoir support"
        fi
        tn_ca_warn "$TN_CA_MSG"
        tn_ca_info "k3s has already started on this device: the existing CA is left alone (its root still has keyCertSign; it only expires soon, so replace this CA before it does)"
        return 0 ;;
      needs-rotation)
        tn_ca_warn "$TN_CA_MSG"
        if [ "${TN_K3S_CA_AUTO_REMEDIATE:-true}" != true ]; then
          tn_ca_fatal "TN_K3S_CA_AUTO_REMEDIATE is not true: run 'sh tn-k3s-ca.sh remediate' and then this script again, or the k3s installer below will break the device"
        fi
        tn_ca_info "Repairing the CA of this device before k3s is installed or updated"
        _tn_rr=0
        tn_ca_remediate all || _tn_rr=$?
        case $_tn_rr in
          0) return 0 ;;
          5) tn_ca_fatal "the k3s CA of this device WAS changed (see above: the datastore of k3s holds the new CA), but a later step failed. Do not roll back and do not run the k3s installer yet: fix that step, make sure k3s is running, then run this script again" ;;
          *) tn_ca_fatal "CA remediation failed; the device was not changed beyond the saved backup. See the messages above" ;;
        esac ;;
      *) tn_ca_fatal "unexpected CA state '$TN_CA_STATE': $TN_CA_MSG" ;;
    esac
  fi

  # not initialised here. If k3s is nevertheless up and ready, it runs from another data dir.
  tn_ca_guard_unknown_datadir "$DATA_DIR" || tn_ca_fatal "cannot tell which data dir k3s uses (see above)"
  if tn_ca_pki_current "$DATA_DIR"; then
    tn_ca_clean_stages "$DATA_DIR"
    tn_ca_info "A valid CA already exists in ${DATA_DIR}/server/tls (k3s has not started yet): keeping it"
    return 0
  fi
  tn_ca_install_pki "$DATA_DIR" "$(tn_ca_suffix "$DATA_DIR")" || tn_ca_fatal "Cannot generate the k3s CA"
  return 0
}

# ----------------------------------------------------------------- remediation

# A staged rotation lives in DATA_DIR/server/.tn-ca-rotate-<ts>/ (mode 700):
#   server/tls/   the new bootstrap data for `k3s certificate rotate-ca --path`
#   trust.pem     the server-ca of the running server, trusted via SSL_CERT_FILE for the call
#   ROTATE_CMD    the exact command; touches ROTATED when it succeeds
# `finalize` copies the new files into server/tls once ROTATED exists, and removes the dir.

tn_ca_newest_stage() { # DATA_DIR -> prints the newest staging dir, or nothing
  find "$1/server" -maxdepth 1 -type d -name '.tn-ca-rotate-*' 2>/dev/null | LC_ALL=C sort | tail -n 1
}

# tn_ca_clean_stages DATA_DIR: remove every staging dir of a rotation (.tn-ca-rotate-*) and every work
# dir of a regeneration that was interrupted (.tn-tls-old.* and .tn-tls-new.*: the previous server/tls
# that was moved aside, and the new one before it was moved in). Only when the CA is fine: they all
# hold copies of the CA keys, and a crashed run leaves them behind.
tn_ca_clean_stages() {
  find "$1/server" -maxdepth 1 -type d \( -name '.tn-ca-rotate-*' -o -name '.tn-tls-old.*' -o -name '.tn-tls-new.*' \) 2>/dev/null |
    while read -r _tn_cs; do
      rm -rf "$_tn_cs" && tn_ca_info "Removed the leftover staging dir $_tn_cs (it held copies of the CA keys)"
    done
  return 0
}

tn_ca_k3s_bin() { echo "${K3S_BIN:-k3s}"; }

# tn_ca_rotate_supported: 0 if the k3s binary has `certificate rotate-ca` or its version cannot be
# told, 1 if it is too old (added in v1.24.12, v1.25.8, v1.26.3; every v1.27+ has it). Sets
# TN_CA_K3S_VERSION.
tn_ca_rotate_supported() {
  TN_CA_K3S_VERSION=
  _tn_vo=$($(tn_ca_k3s_bin) --version 2>/dev/null | sed -n '1s/^.*version v\{0,1\}\([0-9][0-9]*\)\.\([0-9][0-9]*\)\.\([0-9][0-9]*\).*$/\1 \2 \3/p') || return 0
  [ -n "$_tn_vo" ] || return 0
  IFS=' ' read -r _tn_vma _tn_vmi _tn_vpa <<EOV
$_tn_vo
EOV
  TN_CA_K3S_VERSION=v$_tn_vma.$_tn_vmi.$_tn_vpa
  [ "$_tn_vma" -eq 1 ] || return 0
  case $_tn_vmi in
    24) [ "$_tn_vpa" -ge 12 ] ;;
    25) [ "$_tn_vpa" -ge 8 ] ;;
    26) [ "$_tn_vpa" -ge 3 ] ;;
    *) [ "$_tn_vmi" -ge 27 ] ;;
  esac
}

tn_ca_warn_rotate_unsupported() {
  tn_ca_warn "k3s $TN_CA_K3S_VERSION has no 'k3s certificate rotate-ca' (it came with v1.24.12, v1.25.8 and v1.26.3; every v1.27+ has it), so the root CA cannot be replaced on this k3s. The CA and k3s were not changed."
  tn_ca_warn "This k3s keeps running with its current CA (the keyCertSign check came with v1.34.12/v1.35.9/v1.36.5/v1.37.1). Do not upgrade it to one of those yet: upgrade it one minor version at a time to a release that is older than that but has rotate-ca (v1.27 to v1.34.11), then run remediate again, and only then go on."
}

# prepare: stage a rotation (initialised), or regenerate the PKI in place (never started)
tn_ca_prepare() {
  _tn_dd=$1
  tn_ca_state "$_tn_dd" || _tn_rc=$?
  _tn_rc=${_tn_rc:-0}
  case $TN_CA_STATE in
    not-a-server) tn_ca_info "$TN_CA_MSG"; return 0 ;;
    ok) tn_ca_clean_stages "$_tn_dd"; tn_ca_info "root CA already fine: nothing to do"; return 0 ;;
    foreign|no-server-data) tn_ca_warn "$TN_CA_MSG"; return 4 ;;
    needs-regeneration)
      tn_ca_warn "$TN_CA_MSG"
      tn_ca_guard_unknown_datadir "$_tn_dd" || return 4
      tn_ca_info "Regenerating the PKI in $_tn_dd/server/tls (k3s never finished a start on this device)"
      tn_ca_install_pki "$_tn_dd" "$(tn_ca_suffix "$_tn_dd")" || return 1
      tn_ca_info "Done. k3s starts from the new CA. Nothing to rotate."
      return 0 ;;
    needs-rotation) ;;
    *) tn_ca_warn "$TN_CA_MSG"; return 1 ;;
  esac

  if ! tn_ca_rotate_supported; then tn_ca_warn_rotate_unsupported; return 1; fi
  if [ -d "$_tn_dd/server/db/etcd" ]; then
    tn_ca_warn "this server uses embedded etcd: with several servers, read https://docs.k3s.io/cli/certificate first; this tool was tested with one server (sqlite)"
  fi
  _tn_tls=$_tn_dd/server/tls
  _tn_ts=$(tn_ca_ts)
  _tn_s=$_tn_dd/server/.tn-ca-rotate-$_tn_ts
  # earlier unfinished stagings hold copies of the keys: drop them
  find "$_tn_dd/server" -maxdepth 1 -type d -name '.tn-ca-rotate-*' -exec rm -rf {} + 2>/dev/null || true
  ( umask 077; mkdir -p "$_tn_s/server/tls/etcd" "$_tn_s/w" ) || return 1
  tn_ca_split "$_tn_tls/server-ca.crt" "$_tn_s/w" >/dev/null || { rm -rf "$_tn_s"; return 1; }
  tn_ca_lastcert "$_tn_tls/server-ca.crt" "$_tn_s/oldroot.pem" || { rm -rf "$_tn_s"; return 1; }
  tn_ca_find_key "$_tn_dd" "$_tn_s/oldroot.pem" || { rm -rf "$_tn_s"; return 1; }
  _tn_key=$TN_CA_KEY
  tn_ca_reissue_root "$_tn_s/oldroot.pem" "$_tn_key" "$_tn_s/newroot.pem" || { rm -rf "$_tn_s"; return 1; }
  _tn_oldfp=$(tn_ca_fp "$_tn_s/oldroot.pem")

  # a copy of everything k3s keeps there (keys, service.key, bundles)
  ( cd "$_tn_tls" && tar -cf - . ) | ( cd "$_tn_s/server/tls" && tar -xf - ) || { rm -rf "$_tn_s"; return 1; }
  rm -rf "$_tn_s/server/tls/.ca"
  # every bundle: the old root replaced by the new one, all other certificates kept. This function
  # runs in an `||` context, where `set -e` does not exist: EVERY write is checked, and each rebuilt
  # bundle is verified before anything is handed to rotate-ca.
  _tn_newfp=$(tn_ca_fp "$_tn_s/newroot.pem") || { rm -rf "$_tn_s"; return 1; }
  _tn_rbok=1
  for _tn_b in $TN_CA_BUNDLES; do
    [ -f "$_tn_tls/$_tn_b" ] || continue
    rm -rf "$_tn_s/b" "$_tn_s/v"
    mkdir -p "$_tn_s/b" "$_tn_s/v" || { _tn_rbok=0; break; }
    _tn_n=$(tn_ca_split "$_tn_tls/$_tn_b" "$_tn_s/b") || { _tn_rbok=0; break; }
    if [ "${_tn_n:-0}" -lt 1 ]; then tn_ca_warn "$_tn_b holds no certificate"; _tn_rbok=0; break; fi
    : > "$_tn_s/server/tls/$_tn_b" || { _tn_rbok=0; break; }
    _tn_i=0
    while [ "$_tn_i" -lt "$_tn_n" ]; do
      if [ "$(tn_ca_fp "$_tn_s/b/c$_tn_i.pem")" = "$_tn_oldfp" ]; then
        cat "$_tn_s/newroot.pem" >> "$_tn_s/server/tls/$_tn_b" || { _tn_rbok=0; break; }
      else
        cat "$_tn_s/b/c$_tn_i.pem" >> "$_tn_s/server/tls/$_tn_b" || { _tn_rbok=0; break; }
      fi
      _tn_i=$((_tn_i + 1))
    done
    [ "$_tn_rbok" = 1 ] || break
    # the rebuilt bundle: as many certificates as before, and the new root last
    _tn_m=$(tn_ca_split "$_tn_s/server/tls/$_tn_b" "$_tn_s/v") || _tn_m=
    if [ "${_tn_m:-x}" != "$_tn_n" ]; then
      tn_ca_warn "the rebuilt $_tn_b holds ${_tn_m:-no} certificates, the original $_tn_n"; _tn_rbok=0; break
    fi
    if [ "$(tn_ca_fp "$_tn_s/v/c$((_tn_n - 1)).pem")" != "$_tn_newfp" ]; then
      tn_ca_warn "the last certificate of the rebuilt $_tn_b is not the new root (unexpected layout of the original bundle)"
      _tn_rbok=0; break
    fi
  done
  if [ "$_tn_rbok" != 1 ]; then
    tn_ca_warn "cannot rebuild the CA bundles (disk full? unexpected layout?): the CA and k3s were not changed"
    rm -rf "$_tn_s"; return 1
  fi
  rm -rf "$_tn_s/b" "$_tn_s/v"
  cp "$_tn_s/newroot.pem" "$_tn_s/server/tls/root-ca.pem" || { rm -rf "$_tn_s"; return 1; }

  # every leaf CA must verify through the new root
  for _tn_b in server-ca.crt client-ca.crt request-header-ca.crt etcd/peer-ca.crt etcd/server-ca.crt; do
    [ -f "$_tn_s/server/tls/$_tn_b" ] || continue
    rm -rf "$_tn_s/b"; mkdir -p "$_tn_s/b"
    tn_ca_split "$_tn_s/server/tls/$_tn_b" "$_tn_s/b" >/dev/null
    if ! "$OPENSSL" verify -CAfile "$_tn_s/newroot.pem" -untrusted "$_tn_s/server/tls/intermediate-ca.crt" "$_tn_s/b/c0.pem" >/dev/null 2>&1; then
      tn_ca_warn "the new root does not verify $_tn_b: the CA and k3s were not changed"
      rm -rf "$_tn_s"; return 1
    fi
  done
  rm -rf "$_tn_s/b"

  # k3s only accepts the new bundle if the OLD root can still vouch for the chain (it verifies the
  # new leaf CAs with the old chain). An old root that is expired or not yet valid, that has no
  # basicConstraints or CA:FALSE, or that has a keyUsage without keyCertSign (the device-controller
  # certificates) cannot (Go refuses such a parent), however good the new bundle is. Verified above
  # with openssl; then k3s's own check is skipped (--force). The explicit tests are followed by a
  # general one: if openssl cannot verify the OLD chain against the old root, but the new chain
  # verifies (done above), the old root is no voucher either.
  _tn_force=; _tn_fwhy=
  _tn_otxt=$("$OPENSSL" x509 -in "$_tn_s/oldroot.pem" -noout -text 2>/dev/null)
  _tn_obc=$(printf '%s\n' "$_tn_otxt" | awk '/X509v3 Basic Constraints/ { getline; print; exit }')
  if ! "$OPENSSL" x509 -in "$_tn_s/oldroot.pem" -noout -checkend 0 >/dev/null 2>&1; then
    _tn_fwhy="the old root has expired"
  elif [ -z "$_tn_obc" ]; then
    _tn_fwhy="the old root has no basicConstraints"
  elif printf '%s\n' "$_tn_obc" | grep -q 'CA:FALSE'; then
    _tn_fwhy="the old root has CA:FALSE"
  elif printf '%s\n' "$_tn_otxt" | grep -q 'X509v3 Key Usage' && ! tn_ca_has_certsign "$_tn_s/oldroot.pem"; then
    _tn_fwhy="the old root has a key usage without keyCertSign"
  else
    _tn_vout=$("$OPENSSL" verify -CAfile "$_tn_s/oldroot.pem" -untrusted "$_tn_tls/intermediate-ca.crt" "$_tn_s/w/c0.pem" 2>&1) ||
      _tn_fwhy="openssl cannot verify the old chain against the old root ($(printf '%s\n' "$_tn_vout" | grep -i 'error' | head -n 1 | sed 's/^error [0-9]* at [0-9]* depth lookup: *//'))"
  fi
  if [ -n "$_tn_fwhy" ]; then
    _tn_force=' --force'
    tn_ca_info "rotate-ca will use --force ($_tn_fwhy, so k3s cannot verify the new CA bundle against it); the new bundle was verified here"
  fi
  tn_ca_backup_tls "$_tn_dd" || { rm -rf "$_tn_s"; return 1; }
  cp "$_tn_s/w/c0.pem" "$_tn_s/trust.pem" || { rm -rf "$_tn_s"; return 1; }
  rm -rf "$_tn_s/w" "$_tn_s/oldroot.pem"
  # SSL_CERT_FILE: a post-fix k3s that refuses the old root still has to talk to its own
  # supervisor. Trusting the running server's own leaf CA for this one call allows that.
  printf '%s\n' "SSL_CERT_FILE='$_tn_s/trust.pem' $(tn_ca_k3s_bin) certificate rotate-ca --data-dir '$_tn_dd' --path '$_tn_s/server'$_tn_force && touch '$_tn_s/ROTATED'" > "$_tn_s/ROTATE_CMD" ||
    { rm -rf "$_tn_s"; return 1; }
  tn_ca_info "Staged $_tn_s"
  tn_ca_info "Next: run its ROTATE_CMD against the running k3s server (remediate --phase rotate), then --phase finalize and restart k3s"
}

# rotate: run ROTATE_CMD of the newest staging
tn_ca_rotate() {
  _tn_s=$(tn_ca_newest_stage "$1")
  if [ -z "$_tn_s" ]; then
    tn_ca_state "$1" >/dev/null || true
    case $TN_CA_STATE in needs-regeneration|ok|not-a-server) tn_ca_info "nothing to rotate"; return 0 ;; esac
    tn_ca_warn "no staged rotation in $1/server: run 'remediate --phase prepare' first"; return 1
  fi
  [ -f "$_tn_s/ROTATE_CMD" ] || { tn_ca_warn "$_tn_s has no ROTATE_CMD"; return 1; }
  if ! tn_ca_rotate_supported; then tn_ca_warn_rotate_unsupported; return 1; fi
  tn_ca_info "Rotating the CA in the running k3s server (rotate-ca)"
  # rotate-ca talks to the k3s server. One that is starting right now (a unit that was just restarted, or
  # one that restarts itself in a loop, like a k3s that a cgroup v1 host made exit) does not answer yet:
  # try again for a while, but only when systemd says that the unit is there and starting or running
  _tn_tries=1
  case $(tn_ca_unit_state) in active|activating|reloading) _tn_tries=${TN_K3S_ROTATE_TRIES:-6} ;; esac
  _tn_try=1
  while ! sh -c "$(cat "$_tn_s/ROTATE_CMD")"; do
    if [ "$_tn_try" -ge "$_tn_tries" ]; then
      tn_ca_warn "k3s certificate rotate-ca failed. Is the k3s server running (systemctl status k3s)? A server that refuses the old root is fine, it only has to be up."
      tn_ca_warn "The CA of k3s and its files in $1/server/tls were not changed. To go back to a working state: stop k3s and install the previous k3s patch release (v1.34.11, v1.35.8, v1.36.4 or v1.37.0), then run remediate again."
      return 1
    fi
    tn_ca_info "rotate-ca failed (attempt $_tn_try of $_tn_tries): the k3s server may still be starting; trying again in ${TN_K3S_ROTATE_WAIT:-10}s"
    sleep "${TN_K3S_ROTATE_WAIT:-10}"
    _tn_try=$((_tn_try + 1))
  done
  tn_ca_changed "the CA was rotated in the datastore of k3s (k3s certificate rotate-ca)"
}

# finalize: put the rotated files on disk (k3s keeps them in step with its datastore)
tn_ca_finalize() {
  _tn_dd=$1
  _tn_s=$(tn_ca_newest_stage "$_tn_dd")
  if [ -z "$_tn_s" ]; then tn_ca_info "nothing to finalize"; return 0; fi
  [ -f "$_tn_s/ROTATED" ] || { tn_ca_warn "the rotation in $_tn_s has not completed (no ROTATED marker): not touching $_tn_dd/server/tls"; return 1; }
  for _tn_f in $TN_CA_BUNDLES root-ca.pem; do
    [ -f "$_tn_s/server/tls/$_tn_f" ] || continue
    cp "$_tn_s/server/tls/$_tn_f" "$_tn_dd/server/tls/$_tn_f.tn-new" && mv "$_tn_dd/server/tls/$_tn_f.tn-new" "$_tn_dd/server/tls/$_tn_f" || return 1
  done
  rm -rf "$_tn_s"
  tn_ca_changed "the CA files in $_tn_dd/server/tls were replaced"
  tn_ca_info "CA files updated in $_tn_dd/server/tls"
}

# tn_ca_restart: restart k3s, but not forever (TN_K3S_SERVICE_TIMEOUT, 600 s): the unit waits for a
# k3s that may never become ready. `systemctl restart` that is given up on leaves the unit starting,
# so the unit is watched for TN_K3S_READY_GRACE (120) more seconds: a slow device that gets there is a
# success. Otherwise a timeout is a failure (return 124): the caller reports that the CA WAS changed
# and k3s did not come back in time.
tn_ca_restart() {
  _tn_rt=$(tn_ca_svc_timeout)
  _tn_rrc=0 _tn_watched=""
  _tn_gw=0
  if [ -n "${TN_K3S_RESTART_CMD:-}" ]; then
    tn_ca_info "Restarting k3s: $TN_K3S_RESTART_CMD"
    tn_ca_run_bounded "$_tn_rt" sh -c "$TN_K3S_RESTART_CMD" || _tn_rrc=$?
  elif command -v systemctl >/dev/null 2>&1 && systemctl cat "$(tn_ca_unit_name)" >/dev/null 2>&1; then
    tn_ca_info "Restarting k3s"
    tn_ca_run_bounded "$_tn_rt" systemctl restart "$(tn_ca_unit_name)" || _tn_rrc=$?
  else
    tn_ca_warn "restart k3s yourself now (no systemd unit found and TN_K3S_RESTART_CMD is not set)"
    return 0
  fi
  if [ "$_tn_rrc" -eq 124 ] && [ -z "${TN_K3S_RESTART_CMD:-}" ]; then
    _tn_gr=${TN_K3S_READY_GRACE:-120} _tn_gp=${TN_K3S_READY_POLL:-5} _tn_watched=1
    tn_ca_info "the restart of k3s did not finish within ${_tn_rt}s: watching the unit for up to ${_tn_gr}s more"
    while :; do
      if tn_ca_k3s_ready; then
        tn_ca_info "k3s is active now (${_tn_gw}s after the restart gave up)"
        _tn_rrc=0
        break
      fi
      [ "$_tn_gw" -lt "$_tn_gr" ] || break
      sleep "$_tn_gp"
      _tn_gw=$((_tn_gw + _tn_gp))
    done
  fi
  if [ "$_tn_rrc" -eq 124 ]; then
    if [ -n "${_tn_watched:-}" ]; then
      tn_ca_warn "the restart of k3s did not finish within ${_tn_rt}s (TN_K3S_SERVICE_TIMEOUT), and the unit was not active ${_tn_gw}s later either: it is still starting, or it cannot start"
    else
      tn_ca_warn "the restart of k3s did not finish within ${_tn_rt}s (TN_K3S_SERVICE_TIMEOUT): it is still starting, or it cannot start"
    fi
  elif [ "$_tn_rrc" -eq 0 ]; then
    tn_ca_changed "k3s was restarted"
  fi
  return "$_tn_rrc"
}

# After this point the CA has been changed (rotated in the datastore, or the PKI regenerated):
# a failure must not claim that the device is unchanged. Return code 5.
tn_ca_warn_changed_finalize() {
  tn_ca_warn "k3s certificate rotate-ca SUCCEEDED: the datastore of k3s already holds the new CA. Only putting the new files into $1/server/tls failed (see above: disk space? permissions?). Do not roll back. Fix that, then run: sh tn-k3s-ca.sh remediate --phase finalize, and restart k3s."
}
tn_ca_warn_changed_restart() { # DATA_DIR WAS
  if [ "$2" = needs-rotation ]; then
    tn_ca_warn "k3s certificate rotate-ca SUCCEEDED: the datastore of k3s holds the new CA, and so does $1/server/tls, but k3s could not be restarted."
  else
    tn_ca_warn "The PKI in $1/server/tls was regenerated (the previous one is in $1/server/tls-backup-tn-*.tar), but k3s could not be restarted."
  fi
  tn_ca_warn "Look at: systemctl status $(tn_ca_unit_name); journalctl -u $(tn_ca_unit_name) -n 50. Then restart it: systemctl restart $(tn_ca_unit_name). Do not roll back files: restoring the backup tar does not undo a rotation (see 'Rollback limits' at the top of this file)."
}

# tn_ca_remediate PHASE   (prepare | rotate | finalize | all)
tn_ca_remediate() {
  tn_ca_openssl || return 1
  tn_ca_resolve_datadir
  _tn_dd=$DATA_DIR
  case $1 in
    prepare) tn_ca_prepare "$_tn_dd" ;;
    rotate) tn_ca_rotate "$_tn_dd" ;;
    finalize) tn_ca_finalize "$_tn_dd" ;;
    all)
      tn_ca_state "$_tn_dd" >/dev/null || true
      case $TN_CA_STATE in
        ok) tn_ca_clean_stages "$_tn_dd"; tn_ca_info "$TN_CA_MSG"; return 0 ;;
        not-a-server) tn_ca_info "$TN_CA_MSG"; return 0 ;;
      esac
      _tn_was=$TN_CA_STATE
      _tn_s=$(tn_ca_newest_stage "$_tn_dd")
      if [ "$_tn_was" = needs-rotation ] && [ -n "$_tn_s" ] && [ -f "$_tn_s/ROTATED" ]; then
        # an earlier run rotated the CA in the datastore and stopped before the files were copied:
        # do not stage and rotate again
        tn_ca_info "A completed rotation is waiting in $_tn_s: finalizing it"
        tn_ca_finalize "$_tn_dd" || { tn_ca_warn_changed_finalize "$_tn_dd"; return 5; }
      else
        tn_ca_prepare "$_tn_dd" || return $?
        if [ "$_tn_was" = needs-rotation ]; then
          tn_ca_rotate "$_tn_dd" || return 1
          tn_ca_finalize "$_tn_dd" || { tn_ca_warn_changed_finalize "$_tn_dd"; return 5; }
        fi
      fi
      tn_ca_restart || { tn_ca_warn_changed_restart "$_tn_dd" "$_tn_was"; return 5; }
      ;;
    *) tn_ca_warn "unknown phase '$1' (prepare, rotate, finalize or all)"; return 2 ;;
  esac
}

# tn_ca_check: print the state, exit code as documented at the top
tn_ca_check() {
  tn_ca_openssl || return 1
  tn_ca_resolve_datadir
  echo "tn-k3s-ca: data dir ${DATA_DIR} (${TN_CA_DATADIR_SRC})" >&2
  _tn_rc=0
  tn_ca_state "$DATA_DIR" || _tn_rc=$?
  echo "tn-k3s-ca: ${TN_CA_STATE}: ${TN_CA_MSG}"
  return "$_tn_rc"
}

# ----------------------------------------------------------------- program mode

tn_ca_usage() {
  cat <<'EOF'
usage: sh tn-k3s-ca.sh check [--data-dir DIR] [--config-dir DIR]
       sh tn-k3s-ca.sh remediate [--phase prepare|rotate|finalize|all] [--data-dir DIR] [--config-dir DIR]

check       Is the k3s root CA of this device acceptable to k3s v1.34.12/v1.35.9/v1.36.5/v1.37.1+?
            exit 0 ok, 2 needs rotation, 3 needs regeneration, 4 foreign CA or no server data found,
            1 error. Without --data-dir the data dir is resolved the way k3s does (ExecStart of the
            k3s unit, config.yaml.d/*.yaml, config.yaml, default /var/lib/rancher/k3s)
remediate   Re-issue the root with keyCertSign (same key and subject) and put it in place.
            Phases: prepare (offline, stages the new files), rotate (k3s certificate rotate-ca against
            the running server), finalize (copy the files), all (those three plus a k3s restart).
            A device whose k3s never finished its first start gets its PKI regenerated instead.
Run as root on the device. Environment: K3S_BIN, TN_K3S_RESTART_CMD, TN_K3S_STOP_CMD,
TN_K3S_START_CMD, TN_K3S_SERVICE_TIMEOUT (seconds for a stop/start/restart of k3s, 600), CONFIG_PATH, DATA_DIR.
EOF
}

tn_ca_main() {
  _tn_cmd=$1; shift
  _tn_phase=all
  while [ $# -gt 0 ]; do
    case $1 in
      --phase|--data-dir|--config-dir)
        if [ $# -lt 2 ] || [ -z "$2" ]; then tn_ca_warn "$1 needs a value"; tn_ca_usage >&2; return 2; fi
        case $1 in
          --phase) _tn_phase=$2 ;;
          --data-dir) DATA_DIR=$2 ;;
          --config-dir) CONFIG_PATH=$2 ;;
        esac
        shift 2 ;;
      -h|--help) tn_ca_usage; return 0 ;;
      *) tn_ca_usage >&2; return 2 ;;
    esac
  done
  [ -z "${CONFIG_PATH:-}" ] || export CONFIG_PATH
  [ -z "${DATA_DIR:-}" ] || export DATA_DIR
  case $_tn_cmd in
    check) tn_ca_check ;;
    remediate)
      [ "$(id -u)" -eq 0 ] || { tn_ca_warn "run as root"; return 1; }
      tn_ca_remediate "$_tn_phase" ;;
  esac
}

# Only the command words start the program: sourcing this file defines functions and nothing
# else. (A bootstrap script passes no arguments of its own; the template that embeds this file
# sets TN_K3S_CA_NO_MAIN anyway.)
case "${1:-}" in
  check|remediate)
    if [ -z "${TN_K3S_CA_NO_MAIN:-}" ]; then
      CONFIG_PATH=${CONFIG_PATH:-/etc/teknoir}
      tn_ca_main "$@"
      exit $?
    fi
    ;;
esac

# The k3s release policy (a re-run keeps the installed k3s) and the cgroup v1 kubelet flag.
#!/bin/sh
# tn-k3s-install.sh - what a Teknoir bootstrap script decides before it runs the k3s installer:
# which k3s release (a re-run keeps what is installed), and whether the kubelet must be told that
# cgroup v1 is acceptable. Used by the server and the agent script (sourced), next to
# tn-k3s-ca.sh (the server only).
#
#   . tn-k3s-install.sh; tn_inst_prepare server|agent       (once, right before the installer)
#   tn_inst_finish                                           (once, right after the installer)
#   tn_inst_config_ensure FILE KEY TEXT                      (config.yaml without clobbering it)
#
# POSIX sh. It runs under `set -e` and `set -o noglob` (as the bootstrap scripts do) and does not
# change shell options, traps or the working directory of the caller. Sourcing defines functions only.
#
# ---------------------------------------------------------------- version policy
# A bootstrap script is meant to be re-run (it repairs the CA, refreshes the manifests). It must not
# upgrade k3s as a side effect: the pin in the script (INSTALL_K3S_VERSION, `tnctl bootstrap
# --k3s-version`) is the release for a NEW device.
#   - k3s is not installed:   the pin is installed (a SERVER; an agent: see below).
#   - the version cannot be read although a k3s binary or DATA_DIR/server/db exists: REFUSED (the
#                             installer would install or replace k3s blind), unless TN_K3S_FORCE=true.
#   - k3s is installed:       its version is KEPT. The exact installed release is handed to the
#                             installer (which then only rewrites the unit and the symlinks), and
#                             a different pin is reported and ignored.
#   - TN_K3S_UPGRADE=true     (tnctl bootstrap --k3s-upgrade) lets the pin replace the installed
#                             release. A pin below the installed release is refused (k3s does not
#                             downgrade: the datastore has been migrated), and so is a jump of more
#                             than one minor version (v1.34 -> v1.36) unless TN_K3S_FORCE=true
#                             (tnctl bootstrap --force): Kubernetes supports one minor at a time.
#                             A channel is checked too: a minor (v1.35) by its major.minor against the
#                             installed one (lower or other major: refused; more than +1: needs force);
#                             stable/latest/testing is resolved at update.k3s.io like the installer does
#                             and then checked as an exact release. A channel that cannot be resolved
#                             is refused unless TN_K3S_FORCE=true (then it goes on unchecked).
#
# An AGENT follows its server. A kubelet must never be newer than the apiserver it joins, so a new
# agent (and an agent that TN_K3S_UPGRADE=true replaces) does not install the pin of the script: it
# reads the release of the server and installs exactly that one. Where the release comes from: k3s
# does NOT answer /version to an anonymous caller (its apiserver runs with anonymous-auth=false: 401,
# verified with v1.30, v1.34 and v1.36), and the join token is only good for the supervisor
# endpoints. So the Teknoir SERVER script publishes the release it runs (tn_inst_publish_release) as
# <DATA_DIR>/server/static/teknoir/k3s-version, which k3s serves without authentication at
#     https://<K3S_URL>/static/teknoir/k3s-version
# and the agent reads that file (then, for a server that allows anonymous access, /version). The
# pin only counts when the person who runs the script asked for it (`tnctl bootstrap --k3s-version`, or
# INSTALL_K3S_VERSION in the environment; TN_K3S_PIN_SOURCE=flag|env, the template sets it): then it is
# installed if it is not newer than the server (older gets a warning), and refused if it is. When the
# server's release cannot be read (down, unreachable, bootstrapped by a script that does not publish it
# yet): an explicit exact pin is installed with a warning, and otherwise the script REFUSES, unless
# TN_K3S_FORCE=true. An OS image build (OS_BUILD) cannot reach the server and installs the pin. The
# cgroup logic below is keyed on the release that is installed.
#
# ---------------------------------------------------------------- cgroup v1
# Kubernetes >= 1.35: the kubelet refuses to start on a cgroup v1 host (failCgroupV1 defaults to
# true) and k3s does not change that. Jetson L4T r32/r35 and Ubuntu 18.04/20.04 are cgroup v1. When
# the host is on cgroup v1 (stat -fc %T /sys/fs/cgroup is not cgroup2fs), or cannot be told (an OS
# build runs elsewhere than the device), and the k3s that will run is >= v1.31 (the kubelet knows
# the flag since 1.31; it only matters from 1.35, but a later upgrade, by hand or by the k3s
# installer alone, must not stop the kubelet), the script writes (atomically, only when it differs)
#     /etc/rancher/k3s/config.yaml.d/99-teknoir-cgroupv1.yaml:   kubelet-arg+: ["fail-cgroupv1=false"]
# `kubelet-arg+` APPENDS to the kubelet-arg of config.yaml and the other files (verified with
# rancher/k3s v1.36.5, a kubelet-arg already in config.yaml is kept); the flag is harmless on a
# cgroup v2 host. A fail-cgroupv1 that the device sets itself is respected. cgroup v1 is
# deprecated upstream: move the device to cgroup v2 (kernel command line
# systemd.unified_cgroup_hierarchy=1, or a newer OS release).
#
# Order of effects. Everything before the installer (tn_inst_prepare, the CA step, the download) can
# be interrupted by a restart of k3s (the CA step restarts it) or by a reboot of the device, and the
# running k3s then reads the configuration again. So tn_inst_prepare only makes changes that are safe
# for the k3s that is running NOW, and never takes a protection away before its replacement is in place:
#   - the drop-in is REMOVED early only when the running k3s does not need it: the host is on cgroup v2;
#     the release that will run is older than v1.31 (an older kubelet fails on the unknown flag); or the
#     device sets fail-cgroupv1 in a PERSISTENT config file that k3s already reads (config.yaml or another
#     config.yaml.d/*.yaml). A fail-cgroupv1 only in INSTALL_K3S_EXEC reaches the unit when the installer
#     has run, so until then the drop-in stays (or is written): the same value twice is harmless, and
#     the command line wins when they differ.
#   - the drop-in is WRITTEN early, except when the k3s that is installed now is older than v1.31 (an
#     upgrade): its kubelet would fail on the flag if it restarted before the installer replaced it.
#     That write waits for tn_inst_finish, after the installer. An existing drop-in is removed at once in
#     that case, because the old kubelet cannot take it.
#   - but the installer starts the NEW release as soon as it has rewritten the unit, and a release >= v1.35
#     refuses to start on cgroup v1 without the flag (install.sh's `systemctl restart` fails, and under
#     `set -e` the script stops before tn_inst_finish: a k3s that crash-loops). So when the write is held
#     back and the release the installer will start is not KNOWN to be older than v1.35 (a channel such as
#     stable or latest, or an exact v1.35 or newer, also with TN_K3S_FORCE=true), the flag is appended to
#     INSTALL_K3S_EXEC (--kubelet-arg=fail-cgroupv1=false, once, and exported): it is then in the NEW unit
#     from its first start and the old, running kubelet never sees it. An exact v1.31 - v1.34 does not need
#     it on its first start (it accepts it, it only ignores it on cgroup v1), so it gets none. tn_inst_finish
#     still writes the drop-in afterwards, which is what keeps later runs and upgrades safe.
#
# What counts as "the device sets fail-cgroupv1 itself": an uncommented line that sets it to false in
# config.yaml, config.yaml.d/*.yaml or *.yml (not our own file), or in INSTALL_K3S_EXEC. A comment does
# not count, and neither does fail-cgroupv1=true: then the drop-in is written (and kept) as usual, and
# it wins, because kubelet-arg+ comes after. A device that sets fail-cgroupv1=true on a cgroup v1 host
# on purpose is not supported (the kubelet of v1.35 and newer cannot start there anyway); delete the
# drop-in and set cgroup v2 instead.
#
# Environment (all optional):
#   INSTALL_K3S_VERSION, INSTALL_K3S_CHANNEL   the pin of the script (read, and set to what the installer must get)
#   TN_K3S_UPGRADE      true: the pin may replace an installed k3s
#   TN_K3S_FORCE        true: allow an upgrade across more than one minor version; an agent newer than its
#                       server, or without knowing the version of its server; a k3s whose version cannot be read
#   TN_K3S_PIN_SOURCE   who chose INSTALL_K3S_VERSION: flag / env (the person who runs the script) or
#                       default (the template); the template sets it, an agent follows the server unless it is flag/env
#   K3S_URL             the server of an agent (https://host:6443)
#   TN_K3S_SERVER_VERSION_TRIES, TN_K3S_SERVER_VERSION_WAIT   tries (3) and seconds between them (3) to reach the
#                       server; a server that answers without a release is not asked again
#   DATA_DIR            the k3s data dir (/var/lib/rancher/k3s), where a datastore says that k3s is there
#   TN_K3S_CGROUPV1     true / false: do not detect, take this as the answer
#   TN_CGROUP_FSTYPE    the file system type of /sys/fs/cgroup, instead of `stat` (tests)
#   K3S_BIN             the k3s command used to read the installed version (k3s, then /usr/local/bin/k3s)
#   TN_K3S_CONFIG_DIR   /etc/rancher/k3s
#   OS_BUILD            set by the OS image builds: the host is not the device, cgroup cannot be detected

TN_K3S_INSTALL_LIB_VERSION=3
export TN_K3S_INSTALL_LIB_VERSION

# ----------------------------------------------------------------- messages
# Own functions, in the format of header.sh.tmpl: a caller's info/warn/fatal may not exist, and `info`
# can be the GNU info reader.
tn_inst_info() { echo '[INFO] ' "$@"; }
tn_inst_warn() { echo '[WARN] ' "$@" >&2; }
tn_inst_fatal() { echo '[ERROR] ' "$@" >&2; exit 1; }

# tn_inst_state_note: how a refusal describes the device. This library has changed nothing when it
# refuses; the CA step (tn-k3s-ca.sh) may have, earlier in the same run, and then says exactly what.
tn_inst_state_note() {
  if [ -n "${TN_K3S_CA_CHANGED:-}" ]; then
    printf 'The k3s release was not changed, but earlier in this run: %s' "$TN_K3S_CA_CHANGED"
  else
    printf 'Nothing was changed'
  fi
}

# ----------------------------------------------------------------- versions

# tn_inst_parse VERSION: sets TN_INST_MAJ TN_INST_MIN TN_INST_PAT TN_INST_REV from vMAJ.MIN.PAT[+k3sREV]
# (REV 0 when there is none). Fails for anything else (stable, latest, a commit).
tn_inst_parse() {
  TN_INST_MAJ=; TN_INST_MIN=; TN_INST_PAT=; TN_INST_REV=
  _tn_pv=$(printf '%s\n' "$1" | sed -n 's/^v\([0-9][0-9]*\)\.\([0-9][0-9]*\)\.\([0-9][0-9]*\)\(+k3s\([0-9][0-9]*\)\)\{0,1\}$/\1 \2 \3 \5/p')
  [ -n "$_tn_pv" ] || return 1
  IFS=' ' read -r TN_INST_MAJ TN_INST_MIN TN_INST_PAT TN_INST_REV <<EOV
$_tn_pv
EOV
  TN_INST_REV=${TN_INST_REV:-0}
}

# tn_inst_cmp A B: sets TN_INST_CMP to -1, 0 or 1 (A older, equal, newer than B). Fails if either
# is not an exact release.
tn_inst_cmp() {
  tn_inst_parse "$1" || return 1
  _tn_a="$TN_INST_MAJ $TN_INST_MIN $TN_INST_PAT $TN_INST_REV"
  tn_inst_parse "$2" || return 1
  _tn_b="$TN_INST_MAJ $TN_INST_MIN $TN_INST_PAT $TN_INST_REV"
  TN_INST_CMP=$(printf '%s\n%s\n' "$_tn_a" "$_tn_b" | awk 'NR == 1 { for (i = 1; i <= 4; i++) a[i] = $i } NR == 2 {
    for (i = 1; i <= 4; i++) { if (a[i] + 0 < $i + 0) { print -1; exit } if (a[i] + 0 > $i + 0) { print 1; exit } } print 0 }')
}

# the k3s command that reads the installed version
tn_inst_k3s_cmd() {
  if [ -n "${K3S_BIN:-}" ]; then echo "$K3S_BIN"
  elif command -v k3s >/dev/null 2>&1; then echo k3s
  elif [ -x /usr/local/bin/k3s ]; then echo /usr/local/bin/k3s
  else echo k3s; fi
}

# tn_inst_installed_version: sets TN_INST_INSTALLED to the release of the installed k3s
# (v1.34.11+k3s1) or to nothing when there is none.
tn_inst_installed_version() {
  TN_INST_INSTALLED=$($(tn_inst_k3s_cmd) --version 2>/dev/null | sed -n '1s/^.*version \(v[0-9][0-9]*\.[0-9][0-9]*\.[0-9][0-9]*\(+k3s[0-9][0-9]*\)\{0,1\}\).*$/\1/p') || TN_INST_INSTALLED=
}

# tn_inst_k3s_present: 0 if there is a k3s binary or a server datastore, whatever its version is.
# Sets TN_INST_PRESENT to what was found.
tn_inst_k3s_present() {
  TN_INST_PRESENT=
  _tn_first=
  for _tn_w in $(tn_inst_k3s_cmd); do _tn_first=$_tn_w; break; done
  if [ -n "$_tn_first" ] && _tn_found=$(command -v "$_tn_first" 2>/dev/null) && [ -n "$_tn_found" ]; then
    TN_INST_PRESENT="the k3s command $_tn_found"; return 0
  fi
  if [ -d "${DATA_DIR:-/var/lib/rancher/k3s}/server/db" ]; then
    TN_INST_PRESENT="the datastore ${DATA_DIR:-/var/lib/rancher/k3s}/server/db"; return 0
  fi
  return 1
}

# tn_inst_http_get URL: the body on stdout. Returns 0 for 2xx, 1 when the server ANSWERED with an error
# (404: no such file, 401: no anonymous access), 2 when it could not be reached (or there is neither curl
# nor wget). No certificate check: the answer decides which release to install and nothing else (the
# server is the one this device joins with its token anyway).
tn_inst_http_get() {
  _tn_hc=0
  if command -v curl >/dev/null 2>&1; then
    curl -fsk --max-time 10 "$1" || _tn_hc=$?
    case $_tn_hc in 0) return 0 ;; 22) return 1 ;; *) return 2 ;; esac
  elif command -v wget >/dev/null 2>&1; then
    wget -qO- --no-check-certificate -T 10 "$1" || _tn_hc=$?
    case $_tn_hc in 0) return 0 ;; 8) return 1 ;; *) return 2 ;; esac
  fi
  return 2
}

# tn_inst_server_version URL: sets TN_INST_SERVER to the k3s release the server at URL runs
# (v1.34.11+k3s1) and TN_INST_SERVER_SRC to where it was read, or leaves them empty (and sets
# TN_INST_SERVER_WHY to unreachable or no-release). Fails when it cannot tell. Two places, in this order:
#   <URL>/static/teknoir/k3s-version   the file that the Teknoir server script publishes (anonymous)
#   <URL>/version                      gitVersion, for a server that allows anonymous access (k3s does not by default)
tn_inst_server_version() {
  TN_INST_SERVER=; TN_INST_SERVER_SRC=; TN_INST_SERVER_WHY=unreachable
  _tn_su=${1%/}
  _tn_sv_max=${TN_K3S_SERVER_VERSION_TRIES:-3}
  _tn_sv_try=0
  while [ "$_tn_sv_try" -lt "$_tn_sv_max" ]; do
    _tn_sv_try=$((_tn_sv_try + 1))
    _tn_unreach=0
    _tn_rc=0
    _tn_body=$(tn_inst_http_get "$_tn_su/static/teknoir/k3s-version" 2>/dev/null) || _tn_rc=$?
    if [ "$_tn_rc" = 0 ]; then
      TN_INST_SERVER=$(printf '%s' "$_tn_body" | tr -d ' \t\r\n' | sed -n 's/^\(v[0-9][0-9]*\.[0-9][0-9]*\.[0-9][0-9]*+k3s[0-9][0-9]*\)$/\1/p')
      if [ -n "$TN_INST_SERVER" ]; then TN_INST_SERVER_SRC=$_tn_su/static/teknoir/k3s-version; return 0; fi
    elif [ "$_tn_rc" = 2 ]; then
      _tn_unreach=1
    fi
    if [ "$_tn_unreach" = 0 ]; then
      _tn_rc=0
      _tn_body=$(tn_inst_http_get "$_tn_su/version" 2>/dev/null) || _tn_rc=$?
      if [ "$_tn_rc" = 0 ]; then
        # {"major":"1","minor":"34","gitVersion":"v1.34.11+k3s1",...}, over several lines or one
        TN_INST_SERVER=$(printf '%s\n' "$_tn_body" | tr -d '\n' | sed -n 's/^.*"gitVersion"[[:space:]]*:[[:space:]]*"\(v[0-9][0-9]*\.[0-9][0-9]*\.[0-9][0-9]*+k3s[0-9][0-9]*\)".*$/\1/p')
        if [ -n "$TN_INST_SERVER" ]; then TN_INST_SERVER_SRC=$_tn_su/version; return 0; fi
      elif [ "$_tn_rc" = 2 ]; then
        _tn_unreach=1
      fi
    fi
    # the server answered, but has no release to give: asking again does not help
    if [ "$_tn_unreach" = 0 ]; then TN_INST_SERVER_WHY=no-release; return 1; fi
    if [ "$_tn_sv_try" -lt "$_tn_sv_max" ]; then sleep "${TN_K3S_SERVER_VERSION_WAIT:-3}"; fi
  done
  return 1
}

# tn_inst_publish_release: a SERVER script calls it after the k3s installer. Publishes the installed
# release as <DATA_DIR>/server/static/teknoir/k3s-version; k3s serves server/static without
# authentication (https://<server>:6443/static/teknoir/k3s-version), and an agent script reads it to
# install the release of its server. Written atomically; unchanged when it is right; it never fails the
# script. An out-of-band upgrade of k3s leaves it saying the older release, which is the safe direction
# (an agent may be older than its server, never newer): a re-run of the server script updates it.
tn_inst_publish_release() {
  tn_inst_installed_version
  if [ -z "$TN_INST_INSTALLED" ]; then
    tn_inst_warn "cannot read the release of the installed k3s: it is not published, so agents cannot follow this server (they need INSTALL_K3S_VERSION)"
    return 0
  fi
  _tn_pd=${DATA_DIR:-/var/lib/rancher/k3s}/server/static/teknoir
  _tn_pf=$_tn_pd/k3s-version
  if [ -f "$_tn_pf" ] && [ "$(cat "$_tn_pf" 2>/dev/null)" = "$TN_INST_INSTALLED" ]; then
    tn_inst_info "the k3s release $TN_INST_INSTALLED is already published for the agents ($_tn_pf)"
    return 0
  fi
  _tn_pt=$_tn_pd/.k3s-version.tmp.$$
  if mkdir -p "$_tn_pd" && printf '%s\n' "$TN_INST_INSTALLED" > "$_tn_pt" && mv -f "$_tn_pt" "$_tn_pf"; then
    tn_inst_info "Published the k3s release $TN_INST_INSTALLED for the agents: $_tn_pf (served at /static/teknoir/k3s-version)"
  else
    rm -f "$_tn_pt" 2>/dev/null || true
    tn_inst_warn "cannot publish the k3s release to $_tn_pf: agents cannot follow this server (they need INSTALL_K3S_VERSION)"
  fi
  return 0
}

# ----------------------------------------------------------------- the release to install

# tn_inst_agent_target: for an agent that is to be installed (or replaced): which release. Sets and
# exports INSTALL_K3S_VERSION (and unsets INSTALL_K3S_CHANNEL) or exits through tn_inst_fatal.
tn_inst_agent_target() {
  _tn_pin=${INSTALL_K3S_VERSION:-}
  _tn_exp=0
  case ${TN_K3S_PIN_SOURCE:-default} in flag|env) _tn_exp=1 ;; esac
  if [ -n "${OS_BUILD:-}" ]; then
    tn_inst_warn "OS build: the k3s server is not reachable from here, so the release of the agent cannot follow it. ${_tn_pin:-The release the installer picks} is installed: the server must run this release or a newer one"
    return 0
  fi
  TN_INST_SERVER=
  if [ -z "${K3S_URL:-}" ]; then
    tn_inst_warn "K3S_URL is not set: the k3s release of the server cannot be read"
  else
    tn_inst_info "Reading the k3s release of the server at ${K3S_URL%/}"
    tn_inst_server_version "$K3S_URL" || true
  fi
  if [ -n "$TN_INST_SERVER" ]; then
    tn_inst_info "the k3s server at $K3S_URL runs $TN_INST_SERVER (read from $TN_INST_SERVER_SRC)"
    if [ "$_tn_exp" = 1 ] && [ -n "$_tn_pin" ] && tn_inst_cmp "$_tn_pin" "$TN_INST_SERVER"; then
      case $TN_INST_CMP in
        0) tn_inst_info "the requested release $_tn_pin is the release of the server" ;;
        -1) tn_inst_warn "the requested release $_tn_pin is OLDER than the release of the server ($TN_INST_SERVER): it is installed as asked. An agent should run the release of its server (Kubernetes supports a kubelet up to three minor versions older than the apiserver)" ;;
        *) if [ "${TN_K3S_FORCE:-}" = true ]; then
             tn_inst_warn "the requested release $_tn_pin is NEWER than the release of the server ($TN_INST_SERVER); TN_K3S_FORCE=true: it is installed anyway. A kubelet newer than its apiserver is not supported"
           else
             tn_inst_fatal "refusing to install k3s $_tn_pin on this agent: the server at $K3S_URL runs the older $TN_INST_SERVER, and a kubelet must not be newer than the apiserver it joins. Without --k3s-version (INSTALL_K3S_VERSION) the agent gets the release of its server, $TN_INST_SERVER. Or upgrade the server first, or set TN_K3S_FORCE=true. $(tn_inst_state_note)"
           fi ;;
      esac
      return 0
    fi
    if [ "$_tn_exp" = 1 ]; then
      tn_inst_warn "the requested k3s release (${_tn_pin:-${INSTALL_K3S_CHANNEL:-stable}}) is not an exact release, so it cannot be compared with the server: the release of the server is installed instead"
    elif [ -n "$_tn_pin" ] && [ "$_tn_pin" != "$TN_INST_SERVER" ]; then
      tn_inst_info "the script's default $_tn_pin is not used: an agent runs the release of its server"
    fi
    INSTALL_K3S_VERSION=$TN_INST_SERVER
    export INSTALL_K3S_VERSION
    unset INSTALL_K3S_CHANNEL
    tn_inst_info "the agent gets the release of its server: $TN_INST_SERVER"
    return 0
  fi
  # the server cannot be asked
  if [ "$_tn_exp" = 1 ] && [ -n "$_tn_pin" ] && tn_inst_parse "$_tn_pin"; then
    tn_inst_warn "cannot read the k3s release of the server at ${K3S_URL:-(K3S_URL is not set)}: the requested $_tn_pin is installed as asked. The server must run this release or a newer one"
    return 0
  fi
  if [ "${TN_K3S_FORCE:-}" = true ]; then
    tn_inst_warn "cannot read the k3s release of the server at ${K3S_URL:-(K3S_URL is not set)}; TN_K3S_FORCE=true: ${_tn_pin:-the release the installer picks} is installed anyway. If the server runs an older release, this agent will not work"
    return 0
  fi
  case ${TN_INST_SERVER_WHY:-unreachable} in
    no-release) _tn_why="The server answered, but does not publish its release: it was bootstrapped by a script that does not yet (run the server script again: it keeps k3s as it is and publishes its release), and k3s does not allow an anonymous /version." ;;
    *) _tn_why="Is the server up and reachable from this device? Start it and run this script again." ;;
  esac
  tn_inst_fatal "cannot read the k3s release of the server at ${K3S_URL:-(K3S_URL is not set)} (${K3S_URL:-https://<server>:6443}/static/teknoir/k3s-version): an agent must not run a newer k3s than its server, so this script does not guess. $_tn_why Or say which release the server runs: INSTALL_K3S_VERSION=v1.xx.y+k3sN (tnctl bootstrap --k3s-version), or set TN_K3S_FORCE=true to install ${_tn_pin:-the release the installer picks} anyway. $(tn_inst_state_note)"
}

# tn_inst_normalize_pin: the k3s installer takes INSTALL_K3S_VERSION as an EXACT release (it downloads that
# tag, so "stable" would be a 404) and INSTALL_K3S_CHANNEL as a channel. A channel name given as the version
# (stable, latest, testing, or a minor such as v1.34) is moved to INSTALL_K3S_CHANNEL and the version is
# unset, so that every later check sees what the installer will see. An exact release is not touched.
tn_inst_normalize_pin() {
  [ -n "${INSTALL_K3S_VERSION:-}" ] || return 0
  case $INSTALL_K3S_VERSION in
    stable|latest|testing) ;;
    v[0-9]*) printf '%s\n' "$INSTALL_K3S_VERSION" | grep -Eq '^v[0-9]+\.[0-9]+$' || return 0 ;;
    *) return 0 ;;
  esac
  if [ -n "${INSTALL_K3S_CHANNEL:-}" ] && [ "$INSTALL_K3S_CHANNEL" != "$INSTALL_K3S_VERSION" ]; then
    tn_inst_fatal "conflicting k3s requests: INSTALL_K3S_VERSION=$INSTALL_K3S_VERSION is a channel and INSTALL_K3S_CHANNEL=$INSTALL_K3S_CHANNEL is another. Set only one of them. $(tn_inst_state_note)"
  fi
  INSTALL_K3S_CHANNEL=$INSTALL_K3S_VERSION
  export INSTALL_K3S_CHANNEL
  unset INSTALL_K3S_VERSION
  tn_inst_info "$INSTALL_K3S_CHANNEL is a k3s channel, not a release: the installer gets INSTALL_K3S_CHANNEL=$INSTALL_K3S_CHANNEL"
}

# tn_inst_plan: applies the policy at the top. Reads INSTALL_K3S_VERSION / INSTALL_K3S_CHANNEL,
# sets and exports them for the installer. Sets TN_INST_INSTALLED, TN_INST_ACTION
# (install|same|keep|upgrade) and TN_INST_EFFECTIVE (the exact release that will run, or nothing
# when only a channel is known). Exits through tn_inst_fatal when it refuses.
tn_inst_plan() {
  tn_inst_normalize_pin
  tn_inst_installed_version
  if [ -z "$TN_INST_INSTALLED" ] && tn_inst_k3s_present; then
    if [ "${TN_K3S_FORCE:-}" = true ]; then
      tn_inst_warn "$TN_INST_PRESENT exists, but its k3s version cannot be read; TN_K3S_FORCE=true: going on as if k3s were not installed. The installer may replace it with ${INSTALL_K3S_VERSION:-${INSTALL_K3S_CHANNEL:-the release the installer picks}}, without the upgrade and downgrade checks"
    else
      tn_inst_fatal "$TN_INST_PRESENT exists, but its k3s version cannot be read ('$(tn_inst_k3s_cmd) --version' gave no release), so this script cannot tell whether the installer would upgrade it, downgrade it or break its datastore, and does not install anything. Look at '$(tn_inst_k3s_cmd) --version' and at the datastore; repair or remove k3s on purpose, or set TN_K3S_FORCE=true to install anyway. $(tn_inst_state_note)"
    fi
  fi
  if [ "${TN_INST_ROLE:-server}" = agent ] && { [ -z "$TN_INST_INSTALLED" ] || [ "${TN_K3S_UPGRADE:-}" = true ]; }; then
    tn_inst_agent_target
  fi
  _tn_t=${INSTALL_K3S_VERSION:-}
  _tn_want=${_tn_t:-${INSTALL_K3S_CHANNEL:-the default channel of the k3s installer (stable)}}
  TN_INST_ACTION=install
  TN_INST_EFFECTIVE=
  if [ -z "$TN_INST_INSTALLED" ]; then
    TN_INST_EFFECTIVE=$_tn_t
    if [ -n "$_tn_t" ]; then tn_inst_info "k3s is not installed: installing $_tn_t"; else tn_inst_info "k3s is not installed: installing $_tn_want"; fi
    return 0
  fi
  tn_inst_info "k3s $TN_INST_INSTALLED is installed on this device"

  if [ "${TN_K3S_UPGRADE:-}" != true ]; then
    # keep: an exact release is not "newer", it is "this one"
    TN_INST_EFFECTIVE=$TN_INST_INSTALLED
    if [ "$_tn_t" = "$TN_INST_INSTALLED" ]; then
      TN_INST_ACTION=same
      tn_inst_info "k3s $TN_INST_INSTALLED is the requested release: it is installed again, nothing is upgraded"
      return 0
    fi
    TN_INST_ACTION=keep
    INSTALL_K3S_VERSION=$TN_INST_INSTALLED
    export INSTALL_K3S_VERSION
    unset INSTALL_K3S_CHANNEL
    if tn_inst_cmp "$_tn_t" "$TN_INST_INSTALLED" && [ "$TN_INST_CMP" = -1 ]; then
      tn_inst_warn "the requested k3s release $_tn_t is OLDER than the installed $TN_INST_INSTALLED: k3s cannot be downgraded, $TN_INST_INSTALLED is kept"
    else
      tn_inst_warn "k3s $TN_INST_INSTALLED is kept; the requested $_tn_want is NOT installed. A re-run of this script repairs the device, it does not upgrade k3s. To upgrade, run it with TN_K3S_UPGRADE=true (tnctl bootstrap --k3s-upgrade), one minor version at a time"
    fi
    return 0
  fi

  # TN_K3S_UPGRADE=true
  if [ -z "$_tn_t" ]; then
    # a channel: the installer picks the release, so find out which, or compare the minor
    _tn_ch=${INSTALL_K3S_CHANNEL:-stable}
    if printf '%s\n' "$_tn_ch" | grep -Eq '^v[0-9]+\.[0-9]+$'; then
      tn_inst_parse "${_tn_ch}.0"; _tn_cmaj=$TN_INST_MAJ; _tn_cmin=$TN_INST_MIN
      tn_inst_parse "$TN_INST_INSTALLED"; _tn_imaj=$TN_INST_MAJ; _tn_imin=$TN_INST_MIN
      if [ "$_tn_cmaj" != "$_tn_imaj" ] || [ "$_tn_cmin" -lt "$_tn_imin" ]; then
        tn_inst_fatal "refusing to downgrade k3s from $TN_INST_INSTALLED to the channel $_tn_ch (v$_tn_cmaj.$_tn_cmin is not v$_tn_imaj.$_tn_imin or a later minor of the same major): k3s cannot go back (the datastore has been migrated by the newer release). $(tn_inst_state_note). Use a channel of v$_tn_imaj.$_tn_imin or $_tn_imaj.$((_tn_imin + 1)), or restore the device from a backup"
      fi
      if [ $((_tn_cmin - _tn_imin)) -gt 1 ]; then
        if [ "${TN_K3S_FORCE:-}" = true ]; then
          tn_inst_warn "upgrading k3s from $TN_INST_INSTALLED to the channel $_tn_ch skips minor versions (TN_K3S_FORCE=true). Kubernetes supports one minor version at a time: this can leave the cluster in a state that does not start. Have a backup of the datastore"
        else
          tn_inst_fatal "refusing to upgrade k3s from $TN_INST_INSTALLED to the channel $_tn_ch: that is more than one minor version (v$_tn_imaj.$_tn_imin -> v$_tn_cmaj.$_tn_cmin). Upgrade one minor at a time (channel v$_tn_imaj.$((_tn_imin + 1)) first), or set TN_K3S_FORCE=true (tnctl bootstrap --force) if you know what you are doing. $(tn_inst_state_note)"
        fi
      fi
      TN_INST_ACTION=upgrade
      tn_inst_info "upgrading k3s from $TN_INST_INSTALLED to the latest release of the channel $_tn_ch (TN_K3S_UPGRADE=true). A server must be upgraded before its agents"
      return 0
    fi
    # stable, latest, testing: resolve like the installer does (the last path segment of the redirect)
    TN_INST_RESOLVED=$(curl -w '%{url_effective}' -L -s -S -o /dev/null "https://update.k3s.io/v1-release/channels/$_tn_ch" 2>/dev/null | sed 's|^.*/||; s|%2[Bb]|+|') || TN_INST_RESOLVED=
    if tn_inst_parse "$TN_INST_RESOLVED" 2>/dev/null; then
      _tn_t=$TN_INST_RESOLVED
      tn_inst_info "the k3s channel $_tn_ch resolves to $_tn_t: it is pinned, so that the installer gets what was checked"
      INSTALL_K3S_VERSION=$_tn_t
      export INSTALL_K3S_VERSION
      unset INSTALL_K3S_CHANNEL
    elif [ "${TN_K3S_FORCE:-}" = true ]; then
      TN_INST_ACTION=upgrade
      tn_inst_warn "TN_K3S_UPGRADE=true with the channel $_tn_ch, which cannot be resolved from here (update.k3s.io): the k3s installer picks the release, so a downgrade or a jump over several minor versions cannot be checked. TN_K3S_FORCE=true: going on without those checks. Pin an exact release (INSTALL_K3S_VERSION=v1.xx.y+k3sN) to get them"
      return 0
    else
      tn_inst_fatal "refusing to upgrade k3s from $TN_INST_INSTALLED to the channel $_tn_ch: the channel cannot be resolved to a release from here (update.k3s.io is not reachable), so a downgrade or a jump over several minor versions cannot be checked. Pin an exact release (INSTALL_K3S_VERSION=v1.xx.y+k3sN, tnctl bootstrap --k3s-version), or set TN_K3S_FORCE=true to go on without the checks. $(tn_inst_state_note)"
    fi
  fi
  if ! tn_inst_cmp "$_tn_t" "$TN_INST_INSTALLED"; then
    tn_inst_fatal "cannot compare the requested k3s release '$_tn_t' with the installed $TN_INST_INSTALLED. Pin an exact release (INSTALL_K3S_VERSION=v1.xx.y+k3sN). $(tn_inst_state_note)"
  fi
  TN_INST_EFFECTIVE=$_tn_t
  case $TN_INST_CMP in
    0) TN_INST_ACTION=same; tn_inst_info "k3s $TN_INST_INSTALLED is the requested release: nothing to upgrade"; return 0 ;;
    -1) tn_inst_fatal "refusing to downgrade k3s from $TN_INST_INSTALLED to $_tn_t: k3s cannot go back (the datastore has been migrated by the newer release). $(tn_inst_state_note). Pin $TN_INST_INSTALLED or a newer release, or restore the device from a backup" ;;
  esac
  tn_inst_parse "$TN_INST_INSTALLED"; _tn_imaj=$TN_INST_MAJ; _tn_imin=$TN_INST_MIN
  tn_inst_parse "$_tn_t"
  if [ "$TN_INST_MAJ" != "$_tn_imaj" ] || [ $((TN_INST_MIN - _tn_imin)) -gt 1 ]; then
    if [ "${TN_K3S_FORCE:-}" = true ]; then
      tn_inst_warn "upgrading k3s from $TN_INST_INSTALLED to $_tn_t skips minor versions (TN_K3S_FORCE=true). Kubernetes supports one minor version at a time: this can leave the cluster in a state that does not start. Have a backup of the datastore"
    else
      tn_inst_fatal "refusing to upgrade k3s from $TN_INST_INSTALLED to $_tn_t: that is more than one minor version (v$_tn_imaj.$_tn_imin -> v$TN_INST_MAJ.$TN_INST_MIN). Upgrade one minor at a time (to v$_tn_imaj.$((_tn_imin + 1)).x first), or set TN_K3S_FORCE=true (tnctl bootstrap --force) if you know what you are doing. $(tn_inst_state_note)"
    fi
  fi
  TN_INST_ACTION=upgrade
  tn_inst_info "upgrading k3s from $TN_INST_INSTALLED to $_tn_t (TN_K3S_UPGRADE=true). A server must be upgraded before its agents"
}

# ----------------------------------------------------------------- cgroup v1

TN_INST_CGROUP_FILE=99-teknoir-cgroupv1.yaml

# tn_inst_cgroup_state: prints v1 (cgroup v1 or hybrid), v2 or unknown
tn_inst_cgroup_state() {
  case ${TN_K3S_CGROUPV1:-} in true) echo v1; return 0 ;; false) echo v2; return 0 ;; esac
  # an OS image is built on another machine than the one it will run on
  if [ -n "${OS_BUILD:-}" ]; then echo unknown; return 0; fi
  _tn_cg=${TN_CGROUP_FSTYPE:-}
  [ -n "$_tn_cg" ] || _tn_cg=$(stat -fc %T /sys/fs/cgroup 2>/dev/null) || _tn_cg=
  case $_tn_cg in
    cgroup2fs) echo v2 ;;
    '') echo unknown ;;
    *) echo v1 ;;   # tmpfs: cgroup v1, or the hybrid layout
  esac
}

# tn_inst_cgroup_flag_in_text: reads TEXT on stdin; 0 if an uncommented part of it sets fail-cgroupv1 to
# false (kubelet-arg: ["fail-cgroupv1=false"], - fail-cgroupv1=false, --kubelet-arg=fail-cgroupv1=false).
# A whole-line or trailing YAML comment is dropped first; fail-cgroupv1=true does not match.
tn_inst_cgroup_flag_in_text() {
  sed 's/^[[:space:]]*#.*$//; s/[[:space:]]#.*$//' | grep -Eiq 'fail-cgroupv1[[:space:]]*=[[:space:]]*false([^A-Za-z0-9_]|$)'
}

# tn_inst_cgroup_configured_elsewhere: 0 if the device sets fail-cgroupv1=false itself (not in our file).
# Sets TN_INST_CGROUP_WHERE to where, and TN_INST_CGROUP_PERSISTENT to 1 when that is a config file that
# the running k3s reads (config.yaml, config.yaml.d/*.yaml and *.yml), and to 0 when it is only INSTALL_K3S_EXEC
# (which reaches the unit when the installer has run). Files win over INSTALL_K3S_EXEC. A comment, and
# fail-cgroupv1=true, do not count.
tn_inst_cgroup_configured_elsewhere() {
  _tn_cd=${TN_K3S_CONFIG_DIR:-/etc/rancher/k3s}
  TN_INST_CGROUP_WHERE=; TN_INST_CGROUP_PERSISTENT=0
  for _tn_cf in "$_tn_cd/config.yaml" $(find "$_tn_cd/config.yaml.d" -maxdepth 1 -type f \( -iname '*.yaml' -o -iname '*.yml' \) 2>/dev/null | LC_ALL=C sort); do
    [ -r "$_tn_cf" ] || continue
    [ "$_tn_cf" != "$_tn_cd/config.yaml.d/$TN_INST_CGROUP_FILE" ] || continue
    if tn_inst_cgroup_flag_in_text < "$_tn_cf"; then TN_INST_CGROUP_WHERE=$_tn_cf; TN_INST_CGROUP_PERSISTENT=1; return 0; fi
  done
  if printf '%s\n' "${INSTALL_K3S_EXEC:-}" | tn_inst_cgroup_flag_in_text; then TN_INST_CGROUP_WHERE=INSTALL_K3S_EXEC; return 0; fi
  return 1
}

tn_inst_cgroup_remove() { # FILE WHY
  [ -f "$1" ] || return 0
  rm -f "$1" || { tn_inst_warn "cannot remove $1"; return 0; }
  tn_inst_info "Removed $1 ($2)"
}

# tn_inst_cgroup: writes or removes the drop-in, see the top. Uses TN_INST_EFFECTIVE (the release
# that will run; nothing = a channel, taken as new enough: stable and latest are >= v1.35; a channel
# named vMAJ.MIN is read as that minor). The flag goes in from v1.31, where the kubelet learned it (an
# older kubelet would not start with an unknown argument).
tn_inst_cgroup() {
  _tn_cdir=${TN_K3S_CONFIG_DIR:-/etc/rancher/k3s}/config.yaml.d
  _tn_cfile=$_tn_cdir/$TN_INST_CGROUP_FILE
  TN_INST_CGROUP_DEFERRED=
  _tn_cst=$(tn_inst_cgroup_state)
  if [ "$_tn_cst" = v2 ]; then
    tn_inst_cgroup_remove "$_tn_cfile" "this host uses cgroup v2"
    return 0
  fi
  _tn_eff=${TN_INST_EFFECTIVE:-}
  if [ -z "$_tn_eff" ]; then
    case ${INSTALL_K3S_CHANNEL:-} in v[0-9]*.[0-9]*) tn_inst_parse "${INSTALL_K3S_CHANNEL}.0" && _tn_eff=${INSTALL_K3S_CHANNEL}.0 ;; esac
  fi
  _tn_flagok=1 _tn_must=1
  if [ -n "$_tn_eff" ] && tn_inst_cmp "$_tn_eff" v1.31.0 && [ "$TN_INST_CMP" = -1 ]; then _tn_flagok=0; fi
  if [ -n "$_tn_eff" ] && tn_inst_cmp "$_tn_eff" v1.35.0 && [ "$TN_INST_CMP" = -1 ]; then _tn_must=0; fi
  if [ "$_tn_cst" = v1 ]; then
    tn_inst_warn "this host uses cgroup v1 (or the hybrid layout). cgroup v1 is deprecated: Kubernetes >= 1.35 refuses to start the kubelet on it. Move the device to cgroup v2 (kernel command line systemd.unified_cgroup_hierarchy=1, or a newer OS release)"
  else
    tn_inst_info "cannot tell whether the device uses cgroup v1 (OS build, or no /sys/fs/cgroup): the kubelet flag is set to be safe, it is harmless on cgroup v2"
  fi
  if [ "$_tn_flagok" = 0 ]; then
    tn_inst_info "k3s $_tn_eff is older than v1.31 (its kubelet has no fail-cgroupv1 flag) and starts on cgroup v1 without help"
    tn_inst_cgroup_remove "$_tn_cfile" "k3s is older than v1.31"
    return 0
  fi
  if [ "$_tn_must" = 0 ]; then
    tn_inst_info "k3s $_tn_eff starts on cgroup v1 without help; the kubelet flag is set anyway, so that an upgrade to v1.35 or newer (also one made by hand) does not stop the kubelet"
  fi
  if tn_inst_cgroup_configured_elsewhere; then
    if [ "$TN_INST_CGROUP_PERSISTENT" = 1 ]; then
      tn_inst_info "fail-cgroupv1=false is set by the device itself ($TN_INST_CGROUP_WHERE): left as it is"
      tn_inst_cgroup_remove "$_tn_cfile" "the device sets fail-cgroupv1=false itself, in a file that k3s reads"
      return 0
    fi
    # only on the command line of the installer: k3s does not read it until the unit is rewritten, so the
    # drop-in must keep protecting a k3s that restarts before that. The same value twice does no harm.
    tn_inst_info "fail-cgroupv1=false is set by the device itself ($TN_INST_CGROUP_WHERE): left as it is. The drop-in $_tn_cfile stays until the installer has put it in the unit; the same value twice is harmless, and the one on the command line wins"
  fi
  # an installed k3s older than v1.31 (about to be upgraded) cannot start with the flag
  if [ -n "${TN_INST_INSTALLED:-}" ] && tn_inst_cmp "$TN_INST_INSTALLED" v1.31.0 && [ "$TN_INST_CMP" = -1 ]; then
    TN_INST_CGROUP_DEFERRED=1
    tn_inst_info "the installed k3s $TN_INST_INSTALLED is older than v1.31: the kubelet flag is set after the installer has upgraded it, not before"
    tn_inst_cgroup_remove "$_tn_cfile" "the installed k3s is older than v1.31 and cannot take the flag until it is upgraded"
    # The installer starts the new release as soon as it has written the unit. From v1.35 that kubelet
    # does not start on cgroup v1 without the flag, and tn_inst_finish would never run (the installer's
    # restart fails under set -e). So the unit gets the flag from its first start, through INSTALL_K3S_EXEC:
    # the old kubelet, which is running now, never reads it. Not needed for an exact v1.31 - v1.34.
    if [ "$_tn_must" = 1 ]; then
      if printf '%s\n' "${INSTALL_K3S_EXEC:-}" | tn_inst_cgroup_flag_in_text; then
        tn_inst_info "INSTALL_K3S_EXEC already has fail-cgroupv1=false: the new unit carries it from its first start"
      else
        INSTALL_K3S_EXEC="${INSTALL_K3S_EXEC:+$INSTALL_K3S_EXEC }--kubelet-arg=fail-cgroupv1=false"
        export INSTALL_K3S_EXEC
        tn_inst_info "k3s ${_tn_eff:-from a channel} may need the flag on its first start: --kubelet-arg=fail-cgroupv1=false is added to INSTALL_K3S_EXEC, so that the new unit has it. The old kubelet does not read it"
      fi
    fi
    return 0
  fi
  tn_inst_cgroup_write
}

# tn_inst_cgroup_write: the drop-in, written atomically and only when it differs
tn_inst_cgroup_write() {
  _tn_cdir=${TN_K3S_CONFIG_DIR:-/etc/rancher/k3s}/config.yaml.d
  _tn_cfile=$_tn_cdir/$TN_INST_CGROUP_FILE
  _tn_want=$(printf '%s\n' \
    '# Written by the Teknoir bootstrap script. Kubernetes >= 1.35 refuses to start the kubelet on cgroup v1' \
    '# unless this is false. cgroup v1 is deprecated: move the device to cgroup v2 and delete this file.' \
    '# "kubelet-arg+" appends to the kubelet-arg of config.yaml; harmless on cgroup v2.' \
    'kubelet-arg+:' \
    '  - "fail-cgroupv1=false"')
  if [ -f "$_tn_cfile" ] && [ "$(cat "$_tn_cfile")" = "$_tn_want" ]; then
    tn_inst_info "kubelet-arg fail-cgroupv1=false is already in place ($_tn_cfile)"
    return 0
  fi
  # atomically: k3s may be restarting in a loop right now and must never read half a file. The temporary
  # name does not end in .yaml, so k3s does not read it either.
  _tn_tmp=$_tn_cdir/.$TN_INST_CGROUP_FILE.tmp.$$
  if mkdir -p "$_tn_cdir" && find "$_tn_cdir" -maxdepth 1 -name ".$TN_INST_CGROUP_FILE.tmp.*" -exec rm -f {} + &&
     printf '%s\n' "$_tn_want" > "$_tn_tmp" && mv -f "$_tn_tmp" "$_tn_cfile"; then
    :
  else
    rm -f "$_tn_tmp" 2>/dev/null || true
    tn_inst_fatal "cannot write $_tn_cfile: k3s >= v1.35 would not start on this cgroup v1 host"
  fi
  tn_inst_info "Set the kubelet argument fail-cgroupv1=false in $_tn_cfile (k3s ${TN_INST_EFFECTIVE:-from a channel} on cgroup v1)"
}

# ----------------------------------------------------------------- config.yaml

# tn_inst_yaml_has_key FILE KEY: is there a top-level KEY (or KEY+) in FILE
tn_inst_yaml_has_key() {
  [ -r "$1" ] || return 1
  awk -v k="$2" '{ sub(/\r$/, "") } $0 ~ ("^" k "\\+?[ \t]*:") { f = 1 } END { exit f ? 0 : 1 }' "$1"
}

# tn_inst_config_ensure FILE KEY TEXT: appends TEXT (the YAML of the key, may be several lines) to
# FILE unless FILE has the top-level KEY already. A config.yaml that the device owner has edited is
# never rewritten: a re-run of a bootstrap script used to replace it, and lost every key it did not know.
tn_inst_config_ensure() {
  if tn_inst_yaml_has_key "$1" "$2"; then
    tn_inst_info "$1 already sets '$2': kept"
    return 0
  fi
  mkdir -p "$(dirname "$1")" || return 1
  if [ -s "$1" ] && [ -n "$(tail -c 1 "$1")" ]; then echo >> "$1" || return 1; fi
  printf '%s\n' "$3" >> "$1" || return 1
  tn_inst_info "$1: added '$2'"
}

# ----------------------------------------------------------------- the entry point

# tn_inst_prepare ROLE: the release policy, then cgroup v1. Call it after INSTALL_K3S_VERSION /
# INSTALL_K3S_CHANNEL (and INSTALL_K3S_EXEC, if the device sets fail-cgroupv1 there) are set and
# before the k3s installer. A SERVER script calls it BEFORE tn_k3s_ca_setup: the kubelet flag must be in
# place before the CA step restarts k3s, or a k3s that a cgroup v1 host made exit would not be up for
# rotate-ca (it restarts itself every few seconds and reads the flag then). The CA step must in turn
# run before the installer. An agent has no CA step.
tn_inst_prepare() {
  TN_INST_ROLE=${1:-server}
  tn_inst_info "Checking the k3s release and the cgroup version ($TN_INST_ROLE)"
  tn_inst_plan
  tn_inst_cgroup
}

# tn_inst_finish: call it right after the k3s installer succeeded. Writes the kubelet flag that
# tn_inst_prepare held back because the k3s that was installed then could not take it (see "Order of
# effects"). Nothing to do otherwise.
tn_inst_finish() {
  if [ -n "${TN_INST_CGROUP_DEFERRED:-}" ]; then
    TN_INST_CGROUP_DEFERRED=
    tn_inst_info "The installer has run: setting the kubelet flag that was held back"
    tn_inst_cgroup_write
  fi
}


# Who chose the k3s release: the person who runs this script (INSTALL_K3S_VERSION / INSTALL_K3S_CHANNEL in the
# environment, or `tnctl bootstrap --k3s-version`) or the default of this script. An agent installs the
# release of its server unless it was asked for one (it is then refused if it is newer than the server).
if [ -n "${INSTALL_K3S_VERSION:-}${INSTALL_K3S_CHANNEL:-}" ]; then TN_K3S_PIN_SOURCE=env; else TN_K3S_PIN_SOURCE=default; fi
export TN_K3S_PIN_SOURCE
# k3s is pinned to v1.36.5+k3s1 (tnctl bootstrap --k3s-version). Override when running the script:
#   INSTALL_K3S_VERSION=v1.xx.y+k3sN sh bootstrap_*.sh      (an agent must run the version of its server)
# This is the release for a NEW device: a re-run on a device with k3s installed keeps the installed version
# (TN_K3S_UPGRADE=true, tnctl bootstrap --k3s-upgrade, replaces it; never a downgrade).
export INSTALL_K3S_VERSION="${INSTALL_K3S_VERSION:-v1.36.5+k3s1}"

# Order: (0) the arguments of the k3s installer, (1) the k3s release policy and the cgroup v1 kubelet
# flag, (2) the CA, (3) the k3s installer.
# (0) comes first because (1) reads INSTALL_K3S_EXEC: it must see what the installer will get, or a
# fail-cgroupv1 that the caller put there would be taken as "set by the device" (the drop-in removed)
# and then lost when the variable is rebuilt. A value the caller gave is kept, after ours.
# (1) comes before the CA step: it refuses before anything on the device is changed, and the kubelet
# flag is in place when the CA step restarts k3s (a k3s that a cgroup v1 host made exit restarts itself
# in a loop, and has to be up for rotate-ca). The data dir is looked up first, the way the CA step does
# it, because a k3s datastore there decides whether a k3s whose version cannot be read is installed; a
# data dir that disagrees with config.yaml is refused here, before (1) changes anything.
# BEGIN install-k3s-exec
export INSTALL_K3S_EXEC="server --token ${_K3S_TOKEN} --tls-san ${_DEVICE_ID}.${_DOMAIN} --tls-san 10.0.0.10${INSTALL_K3S_EXEC:+ ${INSTALL_K3S_EXEC}}"
if [ "${USE_DOCKER}" = true ]; then
    info "Use docker container-runtime for K3s"
    export INSTALL_K3S_EXEC="${INSTALL_K3S_EXEC} --docker"
fi
# END install-k3s-exec
tn_ca_resolve_datadir
tn_ca_require_consistent_datadir
# BEGIN keep-k3s-token
# A k3s that has started before keeps its cluster token: its datastore is encrypted with it, and
# k3s refuses to start with another one ("bootstrap data already found and encrypted with different
# token"). tnctl generates a new token for every set of scripts, so a re-run takes the passphrase of
# the existing server/token instead. Agents that join later need that token too (K3S_TOKEN).
if $SUDO test -s "${DATA_DIR}/server/token"; then
    _tn_existing_token=$($SUDO cat "${DATA_DIR}/server/token")
    _tn_existing_token=${_tn_existing_token##*:}
    if [ -n "${_tn_existing_token}" ] && [ "${_tn_existing_token}" != "${_K3S_TOKEN}" ]; then
        INSTALL_K3S_EXEC=$(printf '%s' "${INSTALL_K3S_EXEC}" | sed "s|--token ${_K3S_TOKEN}|--token ${_tn_existing_token}|")
        export INSTALL_K3S_EXEC
        _K3S_TOKEN=${_tn_existing_token}
        info "k3s has started on this device before: keeping its cluster token (agents joining it need K3S_TOKEN from ${DATA_DIR}/server/token)"
    fi
    unset _tn_existing_token
fi
# END keep-k3s-token
tn_inst_prepare server
tn_k3s_ca_setup

info "Install Rancher K3s"
export K3S_CONFIG_PATH=/etc/rancher/k3s
$SUDO mkdir -p ${K3S_CONFIG_PATH}
# A re-run must not replace a config.yaml that already exists (it may hold keys of the device owner,
# like a kubelet-arg): the Teknoir keys are added when they are missing.
tn_inst_config_ensure ${K3S_CONFIG_PATH}/config.yaml disable "disable:
  - traefik"
tn_inst_config_ensure ${K3S_CONFIG_PATH}/config.yaml node-name "node-name: teknoir-master"
# The CA above was made in ${DATA_DIR}, the data dir that k3s uses on this device. The file just
# written and the unit that the installer writes (it has no --data-dir) must keep it there.
if [ "${DATA_DIR}" != "/var/lib/rancher/k3s" ]; then
    tn_inst_config_ensure ${K3S_CONFIG_PATH}/config.yaml data-dir "data-dir: ${DATA_DIR}"
fi
cat >/etc/sysctl.d/teknoir-k3s.conf << EOF
fs.inotify.max_user_instances=1024
EOF
sysctl --system

download k3s_installer.sh https://get.k3s.io
$SUDO chmod +x k3s_installer.sh

if [ ${OS_BUILD} ] || [ ${INSECURE} ]; then
    info "Running installation without verifying ssl certs on URLs"
    $SUDO sed -i "s#curl -w#curl --insecure -w#g" k3s_installer.sh
    $SUDO sed -i "s#curl -o#curl --insecure -o#g" k3s_installer.sh
fi

if [ ${OS_BUILD} ]; then
    info "OS Build specifics"
    export INSTALL_K3S_SKIP_START=true
    $SUDO sed -i "s#-d /run/systemd#true#g" k3s_installer.sh
fi

# The release (INSTALL_K3S_VERSION) and the kubelet flag were decided by tn_inst_prepare above.
export INSTALL_K3S_SYMLINK=force
$SUDO ./k3s_installer.sh
# The kubelet flag that had to wait for the installer (an installed k3s older than v1.31 that was upgraded)
tn_inst_finish

# Tell the agents which k3s release this server runs: they install exactly that one (a kubelet must not be
# newer than its apiserver), and k3s does not answer /version to an anonymous caller. Served by k3s at
# https://<server>:6443/static/teknoir/k3s-version.
tn_inst_publish_release

# The registry pull secret for teknoir-registry-secret (tnctl bootstrap --docker-secret). Without one,
# a re-run keeps the credential the device already has: teknoir-registry-secret, or
# artifact-registry-secret from older installers (the default ServiceAccounts now point at
# teknoir-registry-secret only), skipping an empty {"auths":{}}. A first run gets an empty but valid
# config: an empty .dockerconfigjson is invalid JSON and makes k3s reject the whole Teknoir manifest.
_TN_EMPTY_DOCKER_SECRET=eyJhdXRocyI6e319 # {"auths":{}}
_TN_DOCKER_SECRET="TEST-ONLY-docker-secret"
if [ -z "${_TN_DOCKER_SECRET}" ]; then
    for _tn_s in teknoir-registry-secret artifact-registry-secret; do
        _TN_DOCKER_SECRET=$($SUDO k3s kubectl -n default get secret "${_tn_s}" -o jsonpath='{.data.\.dockerconfigjson}' 2>/dev/null || true)
        if [ -n "${_TN_DOCKER_SECRET}" ] && [ "${_TN_DOCKER_SECRET}" != "${_TN_EMPTY_DOCKER_SECRET}" ]; then
            info "Keeping the registry pull secret the device already has (${_tn_s})"
            break
        fi
        _TN_DOCKER_SECRET=
    done
    [ -n "${_TN_DOCKER_SECRET}" ] || _TN_DOCKER_SECRET=${_TN_EMPTY_DOCKER_SECRET}
    unset _tn_s
fi
info "Install Teknoir Manifests"
$SUDO mkdir -p ${DATA_DIR}/server/manifests
$SUDO tee ${DATA_DIR}/server/manifests/teknoir.yaml > /dev/null << EOL
---
apiVersion: node.k8s.io/v1
kind: RuntimeClass
metadata:
  name: nvidia
handler: nvidia

---
apiVersion: v1
kind: Secret
metadata:
  name: teknoir-registry-secret
  namespace: kube-system
type: kubernetes.io/dockerconfigjson
data:
  .dockerconfigjson: "${_TN_DOCKER_SECRET}"

---
apiVersion: v1
kind: Secret
metadata:
  name: teknoir-registry-secret
  namespace: default
type: kubernetes.io/dockerconfigjson
data:
  .dockerconfigjson: "${_TN_DOCKER_SECRET}"

---
apiVersion: v1
kind: ServiceAccount
metadata:
  name: default
  namespace: default
imagePullSecrets:
- name: teknoir-registry-secret

---
apiVersion: v1
kind: ServiceAccount
metadata:
  name: default
  namespace: kube-system
imagePullSecrets:
- name: teknoir-registry-secret

---
apiVersion: scheduling.k8s.io/v1
kind: PriorityClass
metadata:
  name: high-priority-mqtt
  namespace: kube-system
value: 900000000
globalDefault: false
description: "This priority class should be used for mqtt service pods only."

---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: mqtt
  namespace: kube-system
spec:
  replicas: 1
  selector:
    matchLabels:
      app: mqtt
  template:
    metadata:
      labels:
        app: mqtt
    spec:
      priorityClassName: high-priority-mqtt
      containers:
        - name: mqtt
          image: emqx/emqx-edge:latest
          imagePullPolicy: Always
          env:
           - name: EMQX_MQTT__MAX_PACKET_SIZE
             value: 3MB
          ports:
            - containerPort: 1883
            - containerPort: 18083
          resources:
            requests:
              memory: "128Mi"
              cpu: "150m"
            limits:
              memory: "512Mi"
              cpu: "1000m"

---
apiVersion: v1
kind: Service
metadata:
  name: mqtt
  namespace: kube-system
spec:
  type: NodePort
  ports:
    - nodePort: 31883
      port: 1883
      protocol: TCP
      targetPort: 1883
  selector:
    app: mqtt

---
apiVersion: v1
kind: Service
metadata:
  name: mqtt-dashboard
  namespace: kube-system
spec:
  type: NodePort
  selector:
    app: mqtt
  ports:
    - nodePort: 31083
      port: 18083
      protocol: TCP
      targetPort: 18083

---
apiVersion: scheduling.k8s.io/v1
kind: PriorityClass
metadata:
  name: high-priority-toe
  namespace: kube-system
value: 1000000000
globalDefault: false
description: "This priority class should be used for toe service pods only."

---
apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  annotations:
    controller-gen.kubebuilder.io/version: v0.12.0
  name: toes.teknoir.org
spec:
  group: teknoir.org
  names:
    kind: TOE
    listKind: TOEList
    plural: toes
    singular: toe
  scope: Cluster
  versions:
    - name: v1
      schema:
        openAPIV3Schema:
          properties:
            apiVersion:
              type: string
            kind:
              type: string
            metadata:
              type: object
            spec:
              properties:
                config:
                  properties:
                    apiVersion:
                      type: string
                    items:
                      items:
                        properties:
                          apiVersion:
                            type: string
                          binaryData:
                            additionalProperties:
                              format: byte
                              type: string
                            type: object
                          data:
                            additionalProperties:
                              type: string
                            type: object
                          kind:
                            type: string
                          metadata:
                            properties:
                              annotations:
                                additionalProperties:
                                  type: string
                                type: object
                              creationTimestamp:
                                format: date-time
                                type: string
                              deletionGracePeriodSeconds:
                                format: int64
                                type: integer
                              deletionTimestamp:
                                format: date-time
                                type: string
                              finalizers:
                                items:
                                  type: string
                                type: array
                              generateName:
                                type: string
                              generation:
                                format: int64
                                type: integer
                              labels:
                                additionalProperties:
                                  type: string
                                type: object
                              managedFields:
                                items:
                                  properties:
                                    apiVersion:
                                      type: string
                                    fieldsType:
                                      type: string
                                    fieldsV1:
                                      type: object
                                    manager:
                                      type: string
                                    operation:
                                      type: string
                                    subresource:
                                      type: string
                                    time:
                                      format: date-time
                                      type: string
                                  type: object
                                type: array
                              name:
                                type: string
                              namespace:
                                type: string
                              ownerReferences:
                                items:
                                  properties:
                                    apiVersion:
                                      type: string
                                    blockOwnerDeletion:
                                      type: boolean
                                    controller:
                                      type: boolean
                                    kind:
                                      type: string
                                    name:
                                      type: string
                                    uid:
                                      type: string
                                  required:
                                    - apiVersion
                                    - kind
                                    - name
                                    - uid
                                  type: object
                                  x-kubernetes-map-type: atomic
                                type: array
                              resourceVersion:
                                type: string
                              selfLink:
                                type: string
                              uid:
                                type: string
                            type: object
                          spec:
                            x-kubernetes-preserve-unknown-fields: true
                          status:
                            x-kubernetes-preserve-unknown-fields: true
                          type:
                            type: string
                        required:
                          - metadata
                        type: object
                        x-kubernetes-preserve-unknown-fields: true
                      type: array
                    kind:
                      type: string
                    metadata:
                      properties:
                        continue:
                          type: string
                        remainingItemCount:
                          format: int64
                          type: integer
                        resourceVersion:
                          type: string
                        selfLink:
                          type: string
                      type: object
                  required:
                    - items
                  type: object
                  x-kubernetes-preserve-unknown-fields: true
              type: object
            status:
              properties:
                last_applied_config:
                  properties:
                    apiVersion:
                      type: string
                    items:
                      items:
                        properties:
                          apiVersion:
                            type: string
                          binaryData:
                            additionalProperties:
                              format: byte
                              type: string
                            type: object
                          data:
                            additionalProperties:
                              type: string
                            type: object
                          kind:
                            type: string
                          metadata:
                            properties:
                              annotations:
                                additionalProperties:
                                  type: string
                                type: object
                              creationTimestamp:
                                format: date-time
                                type: string
                              deletionGracePeriodSeconds:
                                format: int64
                                type: integer
                              deletionTimestamp:
                                format: date-time
                                type: string
                              finalizers:
                                items:
                                  type: string
                                type: array
                              generateName:
                                type: string
                              generation:
                                format: int64
                                type: integer
                              labels:
                                additionalProperties:
                                  type: string
                                type: object
                              managedFields:
                                items:
                                  properties:
                                    apiVersion:
                                      type: string
                                    fieldsType:
                                      type: string
                                    fieldsV1:
                                      type: object
                                    manager:
                                      type: string
                                    operation:
                                      type: string
                                    subresource:
                                      type: string
                                    time:
                                      format: date-time
                                      type: string
                                  type: object
                                type: array
                              name:
                                type: string
                              namespace:
                                type: string
                              ownerReferences:
                                items:
                                  properties:
                                    apiVersion:
                                      type: string
                                    blockOwnerDeletion:
                                      type: boolean
                                    controller:
                                      type: boolean
                                    kind:
                                      type: string
                                    name:
                                      type: string
                                    uid:
                                      type: string
                                  required:
                                    - apiVersion
                                    - kind
                                    - name
                                    - uid
                                  type: object
                                  x-kubernetes-map-type: atomic
                                type: array
                              resourceVersion:
                                type: string
                              selfLink:
                                type: string
                              uid:
                                type: string
                            type: object
                          spec:
                            x-kubernetes-preserve-unknown-fields: true
                          status:
                            x-kubernetes-preserve-unknown-fields: true
                          type:
                            type: string
                        required:
                          - metadata
                        type: object
                        x-kubernetes-preserve-unknown-fields: true
                      type: array
                    kind:
                      type: string
                    metadata:
                      properties:
                        continue:
                          type: string
                        remainingItemCount:
                          format: int64
                          type: integer
                        resourceVersion:
                          type: string
                        selfLink:
                          type: string
                      type: object
                  required:
                    - items
                  type: object
                  x-kubernetes-preserve-unknown-fields: true
              type: object
          type: object
      served: true
      storage: true
      subresources:
        status: {}

---
apiVersion: v1
kind: ServiceAccount
metadata:
  labels:
    app.kubernetes.io/component: rbac
    app.kubernetes.io/created-by: teknoir
    app.kubernetes.io/instance: controller-manager-sa
    app.kubernetes.io/managed-by: kustomize
    app.kubernetes.io/name: serviceaccount
    app.kubernetes.io/part-of: teknoir
  name: teknoir-controller-manager
  namespace: kube-system
#imagePullSecrets:
#- name: teknoir-registry-secret

---
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  labels:
    app.kubernetes.io/component: rbac
    app.kubernetes.io/created-by: teknoir
    app.kubernetes.io/instance: leader-election-role
    app.kubernetes.io/managed-by: kustomize
    app.kubernetes.io/name: role
    app.kubernetes.io/part-of: teknoir
  name: teknoir-leader-election-role
  namespace: kube-system
rules:
- apiGroups:
  - ""
  resources:
  - configmaps
  verbs:
  - get
  - list
  - watch
  - create
  - update
  - patch
  - delete
- apiGroups:
  - coordination.k8s.io
  resources:
  - leases
  verbs:
  - get
  - list
  - watch
  - create
  - update
  - patch
  - delete
- apiGroups:
  - ""
  resources:
  - events
  verbs:
  - create
  - patch

---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: teknoir-manager-role
rules:
- apiGroups:
  - teknoir.org
  resources:
  - toes
  verbs:
  - create
  - delete
  - get
  - list
  - patch
  - update
  - watch
- apiGroups:
  - teknoir.org
  resources:
  - toes/finalizers
  verbs:
  - update
- apiGroups:
  - teknoir.org
  resources:
  - toes/status
  verbs:
  - get
  - patch
  - update

---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  labels:
    app.kubernetes.io/component: kube-rbac-proxy
    app.kubernetes.io/created-by: teknoir
    app.kubernetes.io/instance: metrics-reader
    app.kubernetes.io/managed-by: kustomize
    app.kubernetes.io/name: clusterrole
    app.kubernetes.io/part-of: teknoir
  name: teknoir-metrics-reader
rules:
- nonResourceURLs:
  - /metrics
  verbs:
  - get

---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  labels:
    app.kubernetes.io/component: kube-rbac-proxy
    app.kubernetes.io/created-by: teknoir
    app.kubernetes.io/instance: proxy-role
    app.kubernetes.io/managed-by: kustomize
    app.kubernetes.io/name: clusterrole
    app.kubernetes.io/part-of: teknoir
  name: teknoir-proxy-role
rules:
- apiGroups:
  - authentication.k8s.io
  resources:
  - tokenreviews
  verbs:
  - create
- apiGroups:
  - authorization.k8s.io
  resources:
  - subjectaccessreviews
  verbs:
  - create

---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  labels:
    app.kubernetes.io/component: rbac
    app.kubernetes.io/created-by: teknoir
    app.kubernetes.io/instance: manager-rolebinding
    app.kubernetes.io/managed-by: kustomize
    app.kubernetes.io/name: clusterrolebinding
    app.kubernetes.io/part-of: teknoir
  name: teknoir-admin-rolebinding
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: cluster-admin
subjects:
- kind: ServiceAccount
  name: teknoir-controller-manager
  namespace: kube-system

---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  labels:
    app.kubernetes.io/component: rbac
    app.kubernetes.io/created-by: teknoir
    app.kubernetes.io/instance: leader-election-rolebinding
    app.kubernetes.io/managed-by: kustomize
    app.kubernetes.io/name: rolebinding
    app.kubernetes.io/part-of: teknoir
  name: teknoir-leader-election-rolebinding
  namespace: kube-system
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: Role
  name: teknoir-leader-election-role
subjects:
- kind: ServiceAccount
  name: teknoir-controller-manager
  namespace: kube-system

---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  labels:
    app.kubernetes.io/component: rbac
    app.kubernetes.io/created-by: teknoir
    app.kubernetes.io/instance: manager-rolebinding
    app.kubernetes.io/managed-by: kustomize
    app.kubernetes.io/name: clusterrolebinding
    app.kubernetes.io/part-of: teknoir
  name: teknoir-manager-rolebinding
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: teknoir-manager-role
subjects:
- kind: ServiceAccount
  name: teknoir-controller-manager
  namespace: kube-system

---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  labels:
    app.kubernetes.io/component: kube-rbac-proxy
    app.kubernetes.io/created-by: teknoir
    app.kubernetes.io/instance: proxy-rolebinding
    app.kubernetes.io/managed-by: kustomize
    app.kubernetes.io/name: clusterrolebinding
    app.kubernetes.io/part-of: teknoir
  name: teknoir-proxy-rolebinding
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: teknoir-proxy-role
subjects:
- kind: ServiceAccount
  name: teknoir-controller-manager
  namespace: kube-system

---
apiVersion: v1
kind: Service
metadata:
  labels:
    app.kubernetes.io/component: kube-rbac-proxy
    app.kubernetes.io/created-by: teknoir
    app.kubernetes.io/instance: controller-manager-metrics-service
    app.kubernetes.io/managed-by: kustomize
    app.kubernetes.io/name: service
    app.kubernetes.io/part-of: teknoir
    control-plane: controller-manager
  name: toe-metrics-service
  namespace: kube-system
spec:
  ports:
  - name: https
    port: 8443
    protocol: TCP
    targetPort: https
  selector:
    control-plane: controller-manager

---
apiVersion: apps/v1
kind: Deployment
metadata:
  labels:
    app.kubernetes.io/component: manager
    app.kubernetes.io/created-by: teknoir
    app.kubernetes.io/instance: controller-manager
    app.kubernetes.io/managed-by: kustomize
    app.kubernetes.io/name: deployment
    app.kubernetes.io/part-of: teknoir
    control-plane: controller-manager
  name: toe
  namespace: kube-system
spec:
  replicas: 1
  strategy:
    type: Recreate
  selector:
    matchLabels:
      control-plane: controller-manager
  template:
    metadata:
      annotations:
        kubectl.kubernetes.io/default-container: manager
      labels:
        teknoir.org/app: toe
        app: toe
        control-plane: controller-manager
    spec:
      priorityClassName: high-priority-toe
      containers:
      - args:
        - --health-probe-bind-address=:8081
        - --metrics-bind-address=127.0.0.1:8080
        env:
        - name: CLIENT_ID
          value: "golden-device"
        - name: TOE_PROJECT
          value: "teknoir"
        - name: TOE_DOMAIN
          value: "example.test"
        - name: TOE_IOT_REGISTRY
          value: "golden-ns"
        - name: TOE_DEVICE
          value: "golden-device"
        - name: TOE_CA_CERT
          value: "/etc/teknoir/roots.pem"
        - name: TOE_PRIVATE_KEY
          value: "/etc/teknoir/rsa_private.pem"
        volumeMounts:
        - name: toe-volume
          mountPath: /etc/teknoir
        command:
        - /manager
        image: docker.io/tekn0ir/toe:toe_v4
        imagePullPolicy: Always
        livenessProbe:
          httpGet:
            path: /healthz
            port: 8081
          initialDelaySeconds: 15
          periodSeconds: 20
        name: toe
        readinessProbe:
          httpGet:
            path: /readyz
            port: 8081
          initialDelaySeconds: 5
          periodSeconds: 10
        resources:
          limits:
            cpu: 500m
            memory: 512Mi
          requests:
            cpu: 10m
            memory: 64Mi
        securityContext:
          allowPrivilegeEscalation: false
          capabilities:
            drop:
            - ALL
      securityContext:
        runAsNonRoot: true
        runAsUser: 65532
        runAsGroup: 65532
      serviceAccountName: teknoir-controller-manager
      terminationGracePeriodSeconds: 10
      volumes:
      - name: toe-volume
        hostPath:
          # directory location on host
          path: "/etc/teknoir"

EOL

INSTALL_WATCHDOG=${INSTALL_WATCHDOG:-false}
if [ "${INSTALL_WATCHDOG}" = true ]; then
    info "Install Teknoir Watchdog"
    $SUDO mkdir -p /usr/local/lib/teknoir
    $SUDO chmod 755 /usr/local/lib/teknoir
    info "Install apt dependencies"
    $SUDO apt install -y python3-pip libsystemd-dev
    $SUDO apt autoremove --purge -y || true
    $SUDO apt clean || true
    $SUDO pip3 install -U pip netifaces kubernetes systemd-watchdog python-statemachine

    $SUDO tee /usr/local/lib/teknoir/watchdog.py > /dev/null << EOL
import argparse
import logging
import socket
import subprocess
import sys
import time
import yaml
import json

import kubernetes
from kubernetes.stream import stream
from kubernetes.stream.ws_client import ERROR_CHANNEL
import systemd_watchdog
from statemachine import StateMachine, State

INTERVAL = 60
ERRORS = 20

class WatchdogSM(StateMachine):

    def __init__(self, **kwargs):
        for k, v in kwargs.items():
            setattr(self, k, v)
        super(WatchdogSM, self).__init__()
        self.wait_count = 0

    uninitialized = State('Uninitialized', initial=True)

    running = State('Idle')
    checkping = State('CheckPing')
    checktoe = State('CheckTOE')
    checkkubernetesapi = State('CheckK8sAPI')

    reboot = State('Reboot')

    progress = uninitialized.to(running)

    run = running.to.itself()

    healthcheck = running.to(checkping) | \
                  checkping.to(checktoe) | \
                  checktoe.to(checkkubernetesapi) | \
                  checkkubernetesapi.to(running)

    degress = reboot.to.itself()

    error = checkping.to(checktoe) | \
            checktoe.to(checkkubernetesapi) | \
            checkkubernetesapi.to(running) | \
            uninitialized.to.itself() | \
            reboot.to.itself()

    escalate = reboot.from_(checkping, checktoe, checkkubernetesapi)

    def on_run(self):
        # self.logger.info(f'Healtcheck wait count: {self.wait_count}')
        self.wait_count += 1


class Watchdog(object):

    def __init__(self, **kwargs):
        for k, v in kwargs.items():
            setattr(self, k, v)
        self.sm = WatchdogSM(**kwargs)
        self.e_checkping = 0
        self.e_checktoe = 0
        self.e_checkkubernetesapi = 0

    def tick(self):
        self.logger.info(self.sm.current_state.name)

        if self.sm.current_state == self.sm.uninitialized:
            self.sm.progress()

        #######################################################################################################################

        elif self.sm.current_state == self.sm.running:
            if self.sm.wait_count >= self.interval:
                self.sm.wait_count = 0
                self.sm.healthcheck()
            else:
                self.sm.run()

        elif self.sm.current_state == self.sm.checkping:
            if self.wrap_action(self.checkping):
                self.sm.healthcheck()
                self.e_checkping = 0
            else:
                self.e_checkping += 1
                self.logger.warning(f'{self.sm.current_state.name} error no {self.e_checkping}')
                self.sm.escalate() if self.e_checkping >= self.errors else self.sm.error()

        elif self.sm.current_state == self.sm.checktoe:
            if self.wrap_action(self.checktoe):
                self.sm.healthcheck()
                self.e_checktoe = 0
            else:
                self.e_checktoe += 1
                self.logger.warning(f'{self.sm.current_state.name} error no {self.e_checktoe}')
                self.sm.escalate() if self.e_checktoe >= self.errors else self.sm.error()

        elif self.sm.current_state == self.sm.checkkubernetesapi:
            if self.wrap_action(self.checkkubernetesapi):
                self.sm.healthcheck()
                self.e_checkkubernetesapi = 0
            else:
                self.e_checkkubernetesapi += 1
                self.logger.warning(f'{self.sm.current_state.name} error no {self.e_checkkubernetesapi}')
                self.sm.escalate() if self.e_checkkubernetesapi >= self.errors else self.sm.error()

        #######################################################################################################################

        elif self.sm.current_state == self.sm.reboot:
            if self.wrap_action(self.reboot):
                self.sm.degress()
            else:
                self.sm.error()

    def wrap_action(self, action):
        try:
            return action()
        except:
            e = sys.exc_info()[0]
            self.logger.warning("Exception: %s" % e)
            return False

    def reboot(self):
        self.logger.warning(f'Rebooting the device')
        proc = subprocess.run(['reboot'], stdout=subprocess.PIPE, stderr=subprocess.PIPE)
        if proc.returncode != 0:
            self.logger.warning(f'The exit code was: {proc.returncode}')
            return False
        return True

    def checkping(self):
        try:
            socket.create_connection(("1.1.1.1", 53))
            return True
        except OSError:
            pass
        self.logger.warning('Cannot ping IP: 1.1.1.1 on port 53')
        return False

    def checktoe(self):
        kubernetes.config.load_kube_config("/etc/rancher/k3s/k3s.yaml")
        try:
            api_instance = kubernetes.client.CoreV1Api()
            label_selector = "app=toe"
            api_response = api_instance.list_pod_for_all_namespaces(watch=False, label_selector=label_selector)
            toe = next(iter(api_response.items), None)
            if toe == None:
                self.logger.warning(f'There is no TOE POD')
                return False
            toe_container_status = next(iter(toe.status.container_statuses), None)
            if toe_container_status == None:
                self.logger.warning(f'There is no container status for TOE POD')
                return False
            self.logger.info(f'TOE POD NAME: {toe_container_status.name}, Ready: {str(toe_container_status.ready)}')
            return toe_container_status.ready
        except kubernetes.client.rest.ApiException as e:
            self.logger.warning('Found exception in reading TOE pod')
        return False

    def checkkubernetesapi(self):
        kubernetes.config.load_kube_config("/etc/rancher/k3s/k3s.yaml")
        try:
            api_instance = kubernetes.client.CoreV1Api()
            name = 'healtcheck'
            namespace = 'kube-system'
            resp = None
            try:
                resp = api_instance.read_namespaced_pod(name=name, namespace=namespace)
            except kubernetes.client.rest.ApiException as e:
                if e.status != 404:
                    logger.info(f"Unknown error: {e}")
                    return False
            if not resp:
                logger.info(f"Pod {name} does not exist. Creating it...")
                pod_manifest = {
                    'apiVersion': 'v1',
                    'kind': 'Pod',
                    'metadata': {
                        'name': name
                    },
                    'spec': {
                        'containers': [{
                            'image': 'busybox',
                            'name': 'sleep',
                            "args": [
                                "/bin/sh",
                                "-c",
                                "while true;do date;sleep 5; done"
                            ]
                        }]
                    }
                }
                resp = api_instance.create_namespaced_pod(body=pod_manifest, namespace=namespace)
                while True:
                    resp = api_instance.read_namespaced_pod(name=name, namespace=namespace)
                    if resp.status.phase != 'Pending':
                        break
                    time.sleep(1)
                logger.info(f"Pod {name} created")

            # Calling exec and waiting for response
            exec_command = [
                'nc', '-zvn', '-w', '2', 'kubernetes.default', '443']
            client = stream(api_instance.connect_get_namespaced_pod_exec,
                            name,
                            namespace,
                            command=exec_command,
                            stderr=True, stdin=False,
                            stdout=True, tty=False, _preload_content=False)

            client.run_forever(timeout=10)
            err = client.read_channel(ERROR_CHANNEL)
            result = yaml.safe_load(err)
            if result['status'] != "Success":
                logger.error(f'Failed to run command "{" ".join(exec_command)}"')
                logger.error('Reason: ' + result['reason'])
                logger.error('Message: ' + result['message'])
                logger.error('Details: ' + ';'.join(map(lambda x: json.dumps(x), result['details']['causes'])))
                return False

        except kubernetes.client.rest.ApiException as e:
            logger.warning(f'Exception during healthcheck of kubernetes API: {e}')
            return False

        return True

def parse_args():
    # Parse input arguments
    desc = 'Teknoir Device Watchdog'
    parser = argparse.ArgumentParser(description=desc)
    parser.add_argument('--interval', dest='interval',
                        help='Set interval in seconds between check',
                        default=INTERVAL, type=int)
    parser.add_argument('--errors', dest='errors',
                        help='Set number of errors to reboot',
                        default=ERRORS, type=int)
    args = parser.parse_args()
    return args


if __name__ == '__main__':
    args = parse_args()
    logger = logging.getLogger('tnw')
    ch = logging.StreamHandler(sys.stdout)
    ch.setLevel(logging.INFO)
    formatter = logging.Formatter('[%(name)s] [%(levelname)s] %(message)s')
    ch.setFormatter(formatter)
    logger.addHandler(ch)
    logger.setLevel(logging.INFO)

    logger.info("TΞꓘN01R")
    logger.info("TΞꓘN01R")
    logger.info("TΞꓘN01R")
    logger.info("TΞꓘN01R")

    wd = systemd_watchdog.watchdog()
    if not wd.is_enabled:
        # Then it's probably not running is systemd with watchdog enabled
        raise Exception("Watchdog not enabled")

    watchdog = Watchdog(logger=logger, wd=wd, **args.__dict__)

    # Report that the program init is complete
    wd.ready()
    wd.status("Init is complete...")
    wd.notify()

    wd.status("Starting up watchdog...")
    while True:
        watchdog.tick()
        time.sleep(1)
        wd.notify()
EOL
  $SUDO chmod 644 /usr/local/lib/teknoir/watchdog.py

  $SUDO tee /lib/systemd/system/tn-watchdog.service > /dev/null << EOL
[Unit]
Description=Teknoir Device Watchdog
After=network.target
StartLimitBurst=5
StartLimitIntervalSec=600

[Service]
Type=simple
Restart=always
RestartSec=10
ExecStart=/usr/bin/python3 /usr/local/lib/teknoir/watchdog.py --interval 60 --errors 20
Type=notify
WatchdogSec=300

[Install]
WantedBy=multi-user.target
EOL


  $SUDO chmod 644 /lib/systemd/system/tn-watchdog.service
  info "Enable and start watchdog service"
  if [ ${OS_BUILD} ]; then
      info "OS Build specifics"
      $SUDO ln -s /lib/systemd/system/tn-watchdog.service /etc/systemd/system/multi-user.target.wants/tn-watchdog.service
  else
      $SUDO systemctl enable --now tn-watchdog.service
  fi
fi

info "Device bootstrapped successfully!"

info ""
info " _____ _____ _  ___   _  ___ ___ ____ "
info "|_   _| ____| |/ / \ | |/ _ \_ _|  _ \ "
info "  | | |  _| | ' /|  \| | | | | || |_) | "
info "  | | | |___| . \| |\  | |_| | ||  _ <  "
info "  |_| |_____|_|\_\_| \_|\___/___|_| \_\ "
info ""

info "Notice!"
info "No networking or user settings has been changed to match settings in the platform."

warn "To enable tunneling, the user ${_FIRST_USER_NAME} has to be created."

on_sudo() {
  $SUDO sh -- "$@"
}

setup_user() {
  $SUDO mkdir -p "/home/${_FIRST_USER_NAME}"
  if ! id -u "${_FIRST_USER_NAME}" >/dev/null 2>&1; then
    if command -v useradd >/dev/null 2>&1 && command -v groupadd >/dev/null 2>&1; then
      if ! getent group "${_FIRST_USER_NAME}" >/dev/null 2>&1; then
        $SUDO groupadd "${_FIRST_USER_NAME}"
      fi
      $SUDO useradd \
        --gid "${_FIRST_USER_NAME}" \
        --groups users \
        --home-dir "/home/${_FIRST_USER_NAME}" \
        --shell /bin/bash \
        "${_FIRST_USER_NAME}"
    else
      $SUDO adduser --disabled-password --gecos "" "${_FIRST_USER_NAME}"
    fi
  fi

  if [ -z "${_FIRST_USER_PASS}" ]; then
    warn "No password for ${_FIRST_USER_NAME} in the platform: key-only login"
  elif command -v chpasswd &> /dev/null; then
    echo "${_FIRST_USER_NAME}:${_FIRST_USER_PASS}" | $SUDO chpasswd
  elif command -v passwd &> /dev/null; then
    echo -e "${_FIRST_USER_PASS}\n${_FIRST_USER_PASS}" | passwd "${_FIRST_USER_NAME}"
  elif command -v usermod &> /dev/null && command -v openssl &> /dev/null; then
    usermod --password $(openssl passwd -1 "${_FIRST_USER_PASS}") "${_FIRST_USER_NAME}"
  else
      warn "Could not set password for user ${_FIRST_USER_NAME}"
      info "Skipping tunneling setup"
      exit
  fi

  on_sudo << EOF
mkdir -p "/home/${_FIRST_USER_NAME}/.ssh"
cp "${CONFIG_PATH}/rsa_private.pem" "/home/${_FIRST_USER_NAME}/.ssh/id_rsa"
ssh-keygen -y -f "${CONFIG_PATH}/rsa_private.pem" > "/home/${_FIRST_USER_NAME}/.ssh/id_rsa.pub"
cat "/home/${_FIRST_USER_NAME}/.ssh/id_rsa.pub" > "/home/${_FIRST_USER_NAME}/.ssh/authorized_keys"
if [ -z "\${_FIRST_USER_KEY+x}" ] || [ "\${_FIRST_USER_KEY}" = "" ]; then
  echo "[INFO] _FIRST_USER_KEY is not set, will continue with only password auth";
else
  echo "\${_FIRST_USER_KEY}" >> "/home/${_FIRST_USER_NAME}/.ssh/authorized_keys"
fi
chmod 600 /home/${_FIRST_USER_NAME}/.ssh/*
chmod 744 /home/${_FIRST_USER_NAME}/.ssh
chown -R "${_FIRST_USER_NAME}:${_FIRST_USER_NAME}" "/home/${_FIRST_USER_NAME}"

# Add root user to users and sudo groups
if [ -e /etc/group ]; then
    sed -i "/^users:/s/\(.*\)/\1,${_FIRST_USER_NAME}/;s/:,/:/" /etc/group
    sed -i "/^sudo:/s/\(.*\)/\1,${_FIRST_USER_NAME}/;s/:,/:/" /etc/group
fi
EOF
}


CREATE_USER=${CREATE_USER:-ask}
if [ "${OS_BUILD}" ] || [ "${CREATE_USER}" = true ]; then
  setup_user
else
  if id -u "${_FIRST_USER_NAME}" >/dev/null 2>&1; then
    warn "The user ${_FIRST_USER_NAME} already exist! This operation will change the password and OVERWRITE ssh keys for this users."
  fi
  warn "Do you want to add \"${_FIRST_USER_NAME}\" as a user and enable tunneling? [yY]"
  read REPLY

  case ${REPLY} in
    [Yy]* )
      setup_user
      ;;
    * )
      info "Skipping tunneling setup"
      ;;
  esac
fi


cd ..
info "Device bootstrapped successfully!"


