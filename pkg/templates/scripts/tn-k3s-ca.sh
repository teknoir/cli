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
    for _tn_f in $(find "$_tn_cd/config.yaml.d" -maxdepth 1 -type f -name '*.yaml' 2>/dev/null | LC_ALL=C sort); do
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
    for _tn_f in $(find "$_tn_cd/config.yaml.d" -maxdepth 1 -type f -name '*.yaml' 2>/dev/null | LC_ALL=C sort); do
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
