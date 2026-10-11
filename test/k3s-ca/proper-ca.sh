#!/bin/sh
# proper-ca.sh - the "proper" control: a k3s-style root WITH keyCertSign over the
# device key, then exactly the legacy intermediate/leaf generation. Proves the
# harness itself is sound: this must be READY on every k3s version.
#
#   tn_proper_ca <tls_dir> <cert> <key> [<suffix>]    (cert is only used to name the root)
# Needs legacy-ca.sh sourced first.

tn_proper_ca() {
  _tp_tls=$1 _tp_key=$3 _tp_suffix=${4:-device-0.test.local}
  _tp_w=$(mktemp -d)
  cat > "$_tp_w/root.cnf" <<'CNF'
[req]
distinguished_name = dn
[dn]
[v3_root]
subjectKeyIdentifier = hash
basicConstraints = critical, CA:true
keyUsage = critical, digitalSignature, keyEncipherment, keyCertSign
CNF
  openssl req -x509 -new -key "$_tp_key" -subj "/CN=teknoir-proper-root@$_tp_suffix" -days 7300 -sha256 \
    -config "$_tp_w/root.cnf" -extensions v3_root -out "$_tp_w/root.pem"
  tn_legacy_ca "$_tp_tls" "$_tp_w/root.pem" "$_tp_key" "$_tp_suffix"
  _tp_rc=$?
  rm -rf "$_tp_w"
  return $_tp_rc
}
