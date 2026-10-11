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
#   DATA_DIR       k3s data dir (/var/lib/rancher/k3s)
#   PRODUCT        k3s
#   _DEVICE_ID, _DOMAIN   used in the CN of the generated CAs (taken from the
#                  existing CAs when not set)
#   K3S_BIN        k3s command used by `rotate-ca` (k3s)
#   TN_K3S_RESTART_CMD    restarts k3s after a remediation (systemctl restart k3s)
#   TN_K3S_CA_AUTO_REMEDIATE  true (default) / false: a re-run of a bootstrap script
#                  repairs an initialised data dir itself; false stops with advice
#
# check exit codes:
#   0  ok (the root is acceptable to k3s, or this is not a server, or k3s made its own CA)
#   2  needs-rotation: initialised data dir, root unusable => `remediate`
#   3  needs-regeneration: data dir never initialised, PKI unusable => `remediate`
#      (or re-run the bootstrap script)
#   4  foreign: the root is unusable but we do not hold its key (not a Teknoir CA)
#   1  error

TN_K3S_CA_LIB_VERSION=1
export TN_K3S_CA_LIB_VERSION

# ----------------------------------------------------------------- messages
# info/warn/fatal come from the bootstrap script header. Fallbacks for a bare run.
command -v info >/dev/null 2>&1 || info() { echo '[INFO] ' "$@"; }
command -v warn >/dev/null 2>&1 || warn() { echo '[WARN] ' "$@" >&2; }
command -v fatal >/dev/null 2>&1 || fatal() { echo '[ERROR] ' "$@" >&2; exit 1; }

# ----------------------------------------------------------------- small helpers

# Select the openssl binary (3.x if installed side by side, as on some distros).
tn_ca_openssl() {
  if command -v openssl-3 >/dev/null 2>&1; then
    OPENSSL=openssl-3
  elif command -v openssl >/dev/null 2>&1; then
    OPENSSL=openssl
  else
    warn "openssl not found"
    return 1
  fi
  if ! "$OPENSSL" ecparam -name prime256v1 -genkey -noout -out /dev/null >/dev/null 2>&1; then
    warn "openssl missing Elliptic Curve (ecparam) support"
    return 1
  fi
  return 0
}

tn_ca_ts() { date +%Y%m%dT%H%M%S; }

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

# ----------------------------------------------------------------- the root certificate

# tn_ca_reissue_root SRC_CERT KEY OUT_CERT
# New self-signed root from the SAME key and subject as SRC_CERT (and the same subject key
# identifier), with CA:TRUE and keyUsage keyCertSign, valid 7300 days. The extensions of
# SRC_CERT are cleared first (no duplicates, no extendedKeyUsage).
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
  "$OPENSSL" x509 -in "$1" -signkey "$2" -clrext -extfile "$_tn_rw/root.cnf" -extensions v3_root \
    -set_serial "0x$_tn_serial" -days 7300 -sha256 -out "$3" >/dev/null 2>&1
  _tn_rc=$?
  rm -rf "$_tn_rw"
  if [ "$_tn_rc" -ne 0 ]; then warn "cannot re-issue the root certificate from $1"; return 1; fi
  tn_ca_has_certsign "$3" || { warn "re-issued root has no keyCertSign"; return 1; }
}

# ----------------------------------------------------------------- PKI generation

# tn_ca_generate_pki TLS_DIR ROOT_CERT ROOT_KEY SUFFIX
# TLS_DIR must exist and be empty. Creates root-ca.{pem,key,crt}, intermediate-ca.*, the five
# leaf CAs and service.key in it exactly as the old bootstrap scripts did, below ROOT_CERT.
# Runs in a subshell: the caller's directory and traps are not touched.
tn_ca_generate_pki() {
  (
    set -e
    tls=$1 root=$2 rkey=$3 suffix=$4
    product=${PRODUCT:-k3s}
    cd "$tls"
    mkdir -p etcd .ca/certs
    cp "$root" root-ca.pem
    cp "$rkey" root-ca.key
    : > .ca/index
    "$OPENSSL" rand -hex 8 > .ca/serial
    cat > .ca/config <<'EOF'
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

    info "Generating Kubernetes service account issuer RSA key"
    # shellcheck disable=SC2086
    quiet "$OPENSSL" genrsa $GENRSA_FLAGS -out service.key 2048

    cat root-ca.pem > root-ca.crt

    info "Generating intermediate certificate authority RSA key and certificate"
    # shellcheck disable=SC2086
    quiet "$OPENSSL" genrsa $GENRSA_FLAGS -out intermediate-ca.key 4096
    "$OPENSSL" req -new -nodes -subj "/CN=$(cn "${product}-intermediate-ca@${suffix}")" \
      -key intermediate-ca.key 2>/dev/null |
    quiet "$OPENSSL" ca -batch -notext -days 3700 -in /dev/stdin -out intermediate-ca.pem \
      -keyfile root-ca.key -cert root-ca.pem -config .ca/config -extensions v3_ca
    cat intermediate-ca.pem root-ca.pem > intermediate-ca.crt

    # a new leaf CA for every control-plane and etcd component
    for TYPE in client server request-header etcd/peer etcd/server; do
      name="${product}-$(echo "$TYPE" | tr / -)-ca"
      info "Generating ${name} leaf certificate authority EC key and certificate"
      quiet "$OPENSSL" ecparam -name prime256v1 -genkey -noout -out "${TYPE}-ca.key"
      "$OPENSSL" req -new -nodes -subj "/CN=$(cn "${name}@${suffix}")" -key "${TYPE}-ca.key" 2>/dev/null |
      quiet "$OPENSSL" ca -batch -notext -days 3700 -in /dev/stdin -out "${TYPE}-ca.pem" \
        -keyfile intermediate-ca.key -cert intermediate-ca.pem -config .ca/config -extensions v3_ca
      cat "${TYPE}-ca.pem" intermediate-ca.pem root-ca.pem > "${TYPE}-ca.crt"
    done
    rm -rf .ca
  )
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
#   not-a-server        no server/tls/server-ca.crt (an agent node, or nothing installed yet)
#   ok                  the root has keyCertSign and is valid for more than 90 days
#   needs-rotation      initialised, root unusable, we hold the key
#   needs-regeneration  never initialised, PKI unusable, we hold the key
#   foreign             root unusable and we do not hold its key
tn_ca_state() {
  TN_CA_STATE=; TN_CA_MSG=
  _tn_tls=$1/server/tls
  if [ ! -f "$_tn_tls/server-ca.crt" ]; then
    TN_CA_STATE=not-a-server; TN_CA_MSG="no $_tn_tls/server-ca.crt: nothing to check"; return 0
  fi
  _tn_sw=$(mktemp -d) || return 1
  if ! tn_ca_lastcert "$_tn_tls/server-ca.crt" "$_tn_sw/root.pem"; then
    rm -rf "$_tn_sw"; TN_CA_STATE=error; TN_CA_MSG="cannot read the root certificate from $_tn_tls/server-ca.crt"; return 1
  fi
  _tn_ncert=$(tn_ca_split "$_tn_tls/server-ca.crt" "$_tn_sw/b")
  _tn_why=
  if [ "$_tn_ncert" -gt 1 ]; then
    tn_ca_has_certsign "$_tn_sw/root.pem" || _tn_why="the root certificate has no keyCertSign"
  fi
  if [ -z "$_tn_why" ] && ! "$OPENSSL" x509 -in "$_tn_sw/root.pem" -noout -checkend 7776000 >/dev/null 2>&1; then
    _tn_why="the root certificate is expired or expires within 90 days"
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

# tn_ca_backup_tls DATA_DIR: tar server/tls (if it holds anything) to server/tls-backup-tn-<ts>.tar
tn_ca_backup_tls() {
  [ -d "$1/server/tls" ] || return 0
  _tn_bk="$1/server/tls-backup-tn-$(tn_ca_ts).tar"
  ( umask 077; tar -C "$1/server" -cf "$_tn_bk" tls ) || { warn "cannot back up $1/server/tls"; return 1; }
  chmod 600 "$_tn_bk" 2>/dev/null || true
  # the tar holds keys: keep only the newest one
  for _tn_o in $(find "$1/server" -maxdepth 1 -type f -name 'tls-backup-tn-*.tar' 2>/dev/null | sort | sed '$d'); do rm -f "$_tn_o"; done
  info "Previous CA files saved to $_tn_bk"
}

# tn_ca_install_pki DATA_DIR SUFFIX: build a complete new PKI from the device certificate and
# replace server/tls with it (the old one is saved first). The data dir must not be initialised.
tn_ca_install_pki() {
  _tn_dd=$1 _tn_sfx=$2
  _tn_cfg=${CONFIG_PATH:-/etc/teknoir}
  [ -s "$_tn_cfg/rsa_public.pem" ] && [ -s "$_tn_cfg/rsa_private.pem" ] ||
    { warn "device certificate or key missing in $_tn_cfg"; return 1; }
  mkdir -p "$_tn_dd/server" || return 1
  _tn_new=$(mktemp -d "$_tn_dd/server/.tn-tls-new.XXXXXX") || return 1
  _tn_rt=$(mktemp -d) || { rm -rf "$_tn_new"; return 1; }
  info "Re-issuing the device certificate as the k3s root CA (keyCertSign)"
  if ! tn_ca_reissue_root "$_tn_cfg/rsa_public.pem" "$_tn_cfg/rsa_private.pem" "$_tn_rt/root.pem"; then
    rm -rf "$_tn_new" "$_tn_rt"; return 1
  fi
  mkdir -p "$_tn_new/etcd"
  if ! tn_ca_generate_pki "$_tn_new" "$_tn_rt/root.pem" "$_tn_cfg/rsa_private.pem" "$_tn_sfx"; then
    warn "PKI generation failed"
    rm -rf "$_tn_new" "$_tn_rt"; return 1
  fi
  rm -rf "$_tn_rt"
  if [ -d "$_tn_dd/server/tls" ]; then
    tn_ca_backup_tls "$_tn_dd" || { rm -rf "$_tn_new"; return 1; }
    # keep an old service account key so tokens issued before stay valid
    if [ -s "$_tn_dd/server/tls/service.key" ]; then
      cat "$_tn_dd/server/tls/service.key" >> "$_tn_new/service.key"
    fi
    rm -rf "$_tn_dd/server/tls"
  fi
  mv "$_tn_new" "$_tn_dd/server/tls" || return 1
  chmod 750 "$_tn_dd/server/tls" 2>/dev/null || true
}

# tn_k3s_ca_setup: the bootstrap step. Uses CONFIG_PATH, DATA_DIR, PRODUCT, _DEVICE_ID, _DOMAIN.
#   never started   -> create the PKI below the re-issued root
#   started, ok     -> leave everything alone (re-running a bootstrap script is safe)
#   started, old    -> repair it first (TN_K3S_CA_AUTO_REMEDIATE=false: stop with advice)
tn_k3s_ca_setup() {
  info "Prepare K3s CA Certs"
  export PRODUCT="${PRODUCT:-k3s}"
  export DATA_DIR="${DATA_DIR:-/var/lib/rancher/${PRODUCT}}"
  tn_ca_openssl || fatal "openssl not found or missing Elliptic Curve (ecparam) support."
  info "Using ${OPENSSL}: $("$OPENSSL" version)"
  _tn_cfg=${CONFIG_PATH:-/etc/teknoir}
  [ -s "$_tn_cfg/rsa_public.pem" ] && [ -s "$_tn_cfg/rsa_private.pem" ] ||
    fatal "device certificate or key missing in $_tn_cfg"

  if tn_ca_initialised "$DATA_DIR"; then
    tn_ca_state "$DATA_DIR" || true
    case $TN_CA_STATE in
      ok|not-a-server|foreign)
        info "k3s has already started on this device: the existing CA is left alone ($TN_CA_STATE: $TN_CA_MSG)"
        return 0 ;;
      needs-rotation)
        warn "$TN_CA_MSG"
        if [ "${TN_K3S_CA_AUTO_REMEDIATE:-true}" != true ]; then
          fatal "TN_K3S_CA_AUTO_REMEDIATE is not true: run 'sh tn-k3s-ca.sh remediate' and then this script again, or the k3s installer below will break the device"
        fi
        info "Repairing the CA of this device before k3s is installed or updated"
        tn_ca_remediate all || fatal "CA remediation failed; the device was not changed beyond the saved backup. See the messages above"
        return 0 ;;
      *) fatal "unexpected CA state '$TN_CA_STATE': $TN_CA_MSG" ;;
    esac
  fi

  if tn_ca_pki_current "$DATA_DIR"; then
    info "A valid CA already exists in ${DATA_DIR}/server/tls (k3s has not started yet): keeping it"
    return 0
  fi
  tn_ca_install_pki "$DATA_DIR" "$(tn_ca_suffix "$DATA_DIR")" || fatal "Cannot generate the k3s CA"
  return 0
}

# ----------------------------------------------------------------- remediation

# A staged rotation lives in DATA_DIR/server/.tn-ca-rotate-<ts>/ (mode 700):
#   server/tls/   the new bootstrap data for `k3s certificate rotate-ca --path`
#   trust.pem     the server-ca of the running server, trusted via SSL_CERT_FILE for the call
#   ROTATE_CMD    the exact command; touches ROTATED when it succeeds
# `finalize` copies the new files into server/tls once ROTATED exists, and removes the dir.

tn_ca_newest_stage() { # DATA_DIR -> prints the newest staging dir, or nothing
  find "$1/server" -maxdepth 1 -type d -name '.tn-ca-rotate-*' 2>/dev/null | sort | tail -n 1
}

tn_ca_k3s_bin() { echo "${K3S_BIN:-k3s}"; }

# prepare: stage a rotation (initialised), or regenerate the PKI in place (never started)
tn_ca_prepare() {
  _tn_dd=$1
  tn_ca_state "$_tn_dd" || _tn_rc=$?
  _tn_rc=${_tn_rc:-0}
  case $TN_CA_STATE in
    not-a-server) info "$TN_CA_MSG"; return 0 ;;
    ok) info "root CA already fine: nothing to do"; return 0 ;;
    foreign) warn "$TN_CA_MSG"; return 4 ;;
    needs-regeneration)
      warn "$TN_CA_MSG"
      info "Regenerating the PKI in $_tn_dd/server/tls (k3s never finished a start on this device)"
      tn_ca_install_pki "$_tn_dd" "$(tn_ca_suffix "$_tn_dd")" || return 1
      # whatever the hung start left behind was made with the old CA
      info "Done. Restart k3s: it starts from the new CA. Nothing to rotate."
      return 0 ;;
    needs-rotation) ;;
    *) warn "$TN_CA_MSG"; return 1 ;;
  esac

  if [ -d "$_tn_dd/server/db/etcd" ]; then
    warn "this server uses embedded etcd: with several servers, read https://docs.k3s.io/cli/certificate first; this tool was tested with one server (sqlite)"
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
  # every bundle: the old root replaced by the new one, all other certificates kept
  for _tn_b in $TN_CA_BUNDLES; do
    [ -f "$_tn_tls/$_tn_b" ] || continue
    rm -rf "$_tn_s/b"; mkdir -p "$_tn_s/b"
    _tn_n=$(tn_ca_split "$_tn_tls/$_tn_b" "$_tn_s/b")
    : > "$_tn_s/server/tls/$_tn_b"
    _tn_i=0
    while [ "$_tn_i" -lt "$_tn_n" ]; do
      if [ "$(tn_ca_fp "$_tn_s/b/c$_tn_i.pem")" = "$_tn_oldfp" ]; then
        cat "$_tn_s/newroot.pem" >> "$_tn_s/server/tls/$_tn_b"
      else
        cat "$_tn_s/b/c$_tn_i.pem" >> "$_tn_s/server/tls/$_tn_b"
      fi
      _tn_i=$((_tn_i + 1))
    done
  done
  rm -rf "$_tn_s/b"
  cp "$_tn_s/newroot.pem" "$_tn_s/server/tls/root-ca.pem"

  # every leaf CA must verify through the new root
  for _tn_b in server-ca.crt client-ca.crt request-header-ca.crt etcd/peer-ca.crt etcd/server-ca.crt; do
    [ -f "$_tn_s/server/tls/$_tn_b" ] || continue
    rm -rf "$_tn_s/b"; mkdir -p "$_tn_s/b"
    tn_ca_split "$_tn_s/server/tls/$_tn_b" "$_tn_s/b" >/dev/null
    if ! "$OPENSSL" verify -CAfile "$_tn_s/newroot.pem" -untrusted "$_tn_s/server/tls/intermediate-ca.crt" "$_tn_s/b/c0.pem" >/dev/null 2>&1; then
      warn "the new root does not verify $_tn_b: nothing was changed"
      rm -rf "$_tn_s"; return 1
    fi
  done
  rm -rf "$_tn_s/b"

  # k3s only accepts the new bundle if the OLD root can still vouch for the chain (it verifies the
  # new leaf CAs with the old chain). An old root that has a keyUsage without keyCertSign (the
  # device-controller certificates) or that has expired cannot (Go refuses such a parent), however
  # good the new bundle is. Verified above with openssl; then k3s's own check is skipped (--force).
  _tn_force=
  if ! "$OPENSSL" x509 -in "$_tn_s/oldroot.pem" -noout -checkend 0 >/dev/null 2>&1; then
    _tn_force=' --force'; _tn_fwhy="the old root has expired"
  elif "$OPENSSL" x509 -in "$_tn_s/oldroot.pem" -noout -text | grep -q 'X509v3 Key Usage' && ! tn_ca_has_certsign "$_tn_s/oldroot.pem"; then
    _tn_force=' --force'; _tn_fwhy="the old root has a key usage without keyCertSign"
  fi
  [ -z "$_tn_force" ] || info "rotate-ca will use --force ($_tn_fwhy, so k3s cannot verify the new CA bundle against it); the new bundle was verified here"
  tn_ca_backup_tls "$_tn_dd" || { rm -rf "$_tn_s"; return 1; }
  cp "$_tn_s/w/c0.pem" "$_tn_s/trust.pem" || { rm -rf "$_tn_s"; return 1; }
  rm -rf "$_tn_s/w" "$_tn_s/oldroot.pem"
  # SSL_CERT_FILE: a post-fix k3s that refuses the old root still has to talk to its own
  # supervisor. Trusting the running server's own leaf CA for this one call allows that.
  printf '%s\n' "SSL_CERT_FILE='$_tn_s/trust.pem' $(tn_ca_k3s_bin) certificate rotate-ca --data-dir '$_tn_dd' --path '$_tn_s/server'$_tn_force && touch '$_tn_s/ROTATED'" > "$_tn_s/ROTATE_CMD" ||
    { rm -rf "$_tn_s"; return 1; }
  info "Staged $_tn_s"
  info "Next: run its ROTATE_CMD against the running k3s server (remediate --phase rotate), then --phase finalize and restart k3s"
}

# rotate: run ROTATE_CMD of the newest staging
tn_ca_rotate() {
  _tn_s=$(tn_ca_newest_stage "$1")
  if [ -z "$_tn_s" ]; then
    tn_ca_state "$1" >/dev/null || true
    case $TN_CA_STATE in needs-regeneration|ok|not-a-server) info "nothing to rotate"; return 0 ;; esac
    warn "no staged rotation in $1/server: run 'remediate --phase prepare' first"; return 1
  fi
  [ -f "$_tn_s/ROTATE_CMD" ] || { warn "$_tn_s has no ROTATE_CMD"; return 1; }
  info "Rotating the CA in the running k3s server (rotate-ca)"
  if ! sh -c "$(cat "$_tn_s/ROTATE_CMD")"; then
    warn "k3s certificate rotate-ca failed. Is the k3s server running (systemctl status k3s)? A server that refuses the old root is fine, it only has to be up."
    warn "Nothing was changed on disk. To go back to a working state: stop k3s and install the previous k3s patch release (v1.34.11, v1.35.8, v1.36.4 or v1.37.0), then run remediate again."
    return 1
  fi
}

# finalize: put the rotated files on disk (k3s keeps them in step with its datastore)
tn_ca_finalize() {
  _tn_dd=$1
  _tn_s=$(tn_ca_newest_stage "$_tn_dd")
  if [ -z "$_tn_s" ]; then info "nothing to finalize"; return 0; fi
  [ -f "$_tn_s/ROTATED" ] || { warn "the rotation in $_tn_s has not completed (no ROTATED marker): not touching $_tn_dd/server/tls"; return 1; }
  for _tn_f in $TN_CA_BUNDLES root-ca.pem; do
    [ -f "$_tn_s/server/tls/$_tn_f" ] || continue
    cp "$_tn_s/server/tls/$_tn_f" "$_tn_dd/server/tls/$_tn_f.tn-new" && mv "$_tn_dd/server/tls/$_tn_f.tn-new" "$_tn_dd/server/tls/$_tn_f" || return 1
  done
  rm -rf "$_tn_s"
  info "CA files updated in $_tn_dd/server/tls"
}

tn_ca_restart() {
  if [ -n "${TN_K3S_RESTART_CMD:-}" ]; then
    info "Restarting k3s: $TN_K3S_RESTART_CMD"
    sh -c "$TN_K3S_RESTART_CMD"
  elif command -v systemctl >/dev/null 2>&1 && systemctl cat k3s.service >/dev/null 2>&1; then
    info "Restarting k3s"
    systemctl restart k3s
  else
    warn "restart k3s yourself now (no systemd unit found and TN_K3S_RESTART_CMD is not set)"
  fi
}

# tn_ca_remediate PHASE   (prepare | rotate | finalize | all)
tn_ca_remediate() {
  tn_ca_openssl || return 1
  _tn_dd=${DATA_DIR:-/var/lib/rancher/k3s}
  case $1 in
    prepare) tn_ca_prepare "$_tn_dd" ;;
    rotate) tn_ca_rotate "$_tn_dd" ;;
    finalize) tn_ca_finalize "$_tn_dd" ;;
    all)
      tn_ca_state "$_tn_dd" >/dev/null || true
      case $TN_CA_STATE in
        ok|not-a-server) info "$TN_CA_MSG"; return 0 ;;
      esac
      _tn_was=$TN_CA_STATE
      tn_ca_prepare "$_tn_dd" || return 1
      if [ "$_tn_was" = needs-rotation ]; then
        tn_ca_rotate "$_tn_dd" || return 1
        tn_ca_finalize "$_tn_dd" || return 1
      fi
      tn_ca_restart
      ;;
    *) warn "unknown phase '$1' (prepare, rotate, finalize or all)"; return 2 ;;
  esac
}

# tn_ca_check: print the state, exit code as documented at the top
tn_ca_check() {
  tn_ca_openssl || return 1
  _tn_rc=0
  tn_ca_state "${DATA_DIR:-/var/lib/rancher/k3s}" || _tn_rc=$?
  echo "tn-k3s-ca: ${TN_CA_STATE}: ${TN_CA_MSG}"
  return "$_tn_rc"
}

# ----------------------------------------------------------------- program mode

tn_ca_usage() {
  cat <<'EOF'
usage: sh tn-k3s-ca.sh check [--data-dir DIR] [--config-dir DIR]
       sh tn-k3s-ca.sh remediate [--phase prepare|rotate|finalize|all] [--data-dir DIR] [--config-dir DIR]

check       Is the k3s root CA of this device acceptable to k3s v1.34.12/v1.35.9/v1.36.5/v1.37.1+?
            exit 0 ok, 2 needs rotation, 3 needs regeneration, 4 foreign CA, 1 error
remediate   Re-issue the root with keyCertSign (same key and subject) and put it in place.
            Phases: prepare (offline, stages the new files), rotate (k3s certificate rotate-ca against
            the running server), finalize (copy the files), all (those three plus a k3s restart).
            A device whose k3s never finished its first start gets its PKI regenerated instead.
Run as root on the device. Environment: K3S_BIN, TN_K3S_RESTART_CMD, CONFIG_PATH, DATA_DIR.
EOF
}

tn_ca_main() {
  _tn_cmd=$1; shift
  _tn_phase=all
  while [ $# -gt 0 ]; do
    case $1 in
      --phase) _tn_phase=${2:-}; shift 2 ;;
      --data-dir) DATA_DIR=${2:-}; shift 2 ;;
      --config-dir) CONFIG_PATH=${2:-}; shift 2 ;;
      -h|--help) tn_ca_usage; return 0 ;;
      *) tn_ca_usage >&2; return 2 ;;
    esac
  done
  export DATA_DIR CONFIG_PATH
  case $_tn_cmd in
    check) tn_ca_check ;;
    remediate)
      [ "$(id -u)" -eq 0 ] || { warn "run as root"; return 1; }
      tn_ca_remediate "$_tn_phase" ;;
  esac
}

# Only the command words start the program: sourcing this file defines functions and nothing
# else. (A bootstrap script passes no arguments of its own; the template that embeds this file
# sets TN_K3S_CA_NO_MAIN anyway.)
case "${1:-}" in
  check|remediate)
    if [ -z "${TN_K3S_CA_NO_MAIN:-}" ]; then
      DATA_DIR=${DATA_DIR:-/var/lib/rancher/k3s}
      CONFIG_PATH=${CONFIG_PATH:-/etc/teknoir}
      tn_ca_main "$@"
      exit $?
    fi
    ;;
esac
