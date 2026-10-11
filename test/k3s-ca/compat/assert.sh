#!/bin/sh
# compat/assert.sh - runs INSIDE a distro/arch container under /bin/sh (dash on
# Debian/Ubuntu). For every device-cert generator it runs the library's
# tn_k3s_ca_setup twice on a scratch data dir and asserts what the k3s fix and
# our promises require. It never prints key material.
#
# Environment (set by run.sh):
#   LIB   path of the CA library (TN_K3S_CA_LIB), sourced as the bootstrap script does
#   GENS  space separated generator names; certs are in /gens/<name>/rsa_{public,private}.pem
#   BIN   directory with the k3sverify binary built for this architecture
# Output: one line per generator: "RESULT <gen> PASS|FAIL <note>"
:

LIB=${LIB:-/tn/tn-k3s-ca.sh}
BIN=${BIN:-/tnbin}
GENDIR=${GENDIR:-/gens}

command -v openssl >/dev/null 2>&1 || { echo "RESULT * FAIL openssl missing in image"; exit 0; }
echo "# $(uname -m) $(openssl version) sh=$(readlink -f /bin/sh 2>/dev/null || echo sh)"

sum() { sha256sum "$1" | cut -d' ' -f1; }

one() { # GEN
  g=$1
  src=$GENDIR/$g
  w=$(mktemp -d)
  mkdir -p "$w/etc" "$w/data"
  cp "$src/rsa_public.pem" "$src/rsa_private.pem" "$w/etc/"
  pub0=$(sum "$w/etc/rsa_public.pem"); prv0=$(sum "$w/etc/rsa_private.pem")
  # 1+2: run the setup twice (the second run must be idempotent in its guarantees)
  for run in 1 2; do
    (
      set -e
      set -o noglob
      umask 027
      info() { echo '[INFO] ' "$@"; }
      warn() { echo '[WARN] ' "$@" >&2; }
      fatal() { echo '[ERROR] ' "$@" >&2; exit 1; }
      CONFIG_PATH=$w/etc DATA_DIR=$w/data PRODUCT=k3s _DEVICE_ID=compat _DOMAIN=test.local
      export CONFIG_PATH DATA_DIR PRODUCT _DEVICE_ID _DOMAIN
      . "$LIB"
      tn_k3s_ca_setup
    ) > "$w/setup$run.log" 2>&1 || { echo "RESULT $g FAIL setup run $run failed: $(tail -3 "$w/setup$run.log" | tr '\n' ' ' | cut -c1-300)"; rm -rf "$w"; return; }
    # k3sverify: root properties vs original, chain linkage, k3s pruned-pool verification
    if ! out=$("$BIN/k3sverify" -orig "$w/etc/rsa_public.pem" -key "$w/etc/rsa_private.pem" -days 7300 "$w/data/server/tls" 2>&1); then
      echo "RESULT $g FAIL run $run k3sverify: $(echo "$out" | grep '^FAIL' | head -3 | tr '\n' ' ' | cut -c1-400)"; rm -rf "$w"; return
    fi
    # openssl-side assertions (text parsing differs between 1.1.1 and 3.x, so keep it coarse)
    t=$w/data/server/tls
    txt=$(openssl x509 -in "$t/root-ca.pem" -noout -text)
    case $txt in *"CA:TRUE"*) ;; *) echo "RESULT $g FAIL run $run: openssl sees no CA:TRUE"; rm -rf "$w"; return ;; esac
    case $txt in *"Certificate Sign"*) ;; *) echo "RESULT $g FAIL run $run: openssl sees no Certificate Sign"; rm -rf "$w"; return ;; esac
    openssl verify -CAfile "$t/root-ca.pem" "$t/root-ca.pem" >/dev/null 2>&1 ||
      { echo "RESULT $g FAIL run $run: openssl verify root"; rm -rf "$w"; return; }
    openssl verify -CAfile "$t/root-ca.pem" -untrusted "$t/intermediate-ca.pem" "$t/server-ca.pem" >/dev/null 2>&1 ||
      { echo "RESULT $g FAIL run $run: openssl verify server-ca"; rm -rf "$w"; return; }
    pk1=$(openssl x509 -in "$t/root-ca.pem" -noout -pubkey | sha256sum | cut -d' ' -f1)
    pk2=$(openssl pkey -in "$w/etc/rsa_private.pem" -pubout 2>/dev/null | sha256sum | cut -d' ' -f1)
    [ "$pk1" = "$pk2" ] || { echo "RESULT $g FAIL run $run: root public key != device key"; rm -rf "$w"; return; }
    # the device files must never change
    [ "$(sum "$w/etc/rsa_public.pem")" = "$pub0" ] && [ "$(sum "$w/etc/rsa_private.pem")" = "$prv0" ] ||
      { echo "RESULT $g FAIL run $run: rsa_public.pem/rsa_private.pem were modified"; rm -rf "$w"; return; }
  done
  na=$(openssl x509 -in "$w/data/server/tls/root-ca.pem" -noout -enddate | cut -d= -f2)
  echo "RESULT $g PASS root notAfter=$na"
  rm -rf "$w"
}

# remed GEN: a device bootstrapped with the old scripts (the device certificate as root) is checked,
# prepared, "rotated" with a fake k3s, finalized and checked again, with the same tools and
# openssl as setup. Only for certificates that need it (no keyCertSign).
remed() { # GEN
  g=$1
  w=$(mktemp -d)
  mkdir -p "$w/etc" "$w/data/server/tls"
  cp "$GENDIR/$g/rsa_public.pem" "$GENDIR/$g/rsa_private.pem" "$w/etc/"
  printf '#!/bin/sh\necho "$*" >> "%s/k3s.calls"\n' "$w" > "$w/fake-k3s"
  chmod 755 "$w/fake-k3s"
  if ! out=$( (
        set -e; set -o noglob; umask 027
        info() { echo '[INFO] ' "$@"; }; warn() { echo '[WARN] ' "$@" >&2; }; fatal() { echo '[ERROR] ' "$@" >&2; exit 1; }
        CONFIG_PATH=$w/etc DATA_DIR=$w/data PRODUCT=k3s _DEVICE_ID=compat _DOMAIN=test.local
        K3S_BIN=$w/fake-k3s TN_K3S_RESTART_CMD=true
        export CONFIG_PATH DATA_DIR PRODUCT _DEVICE_ID _DOMAIN K3S_BIN TN_K3S_RESTART_CMD
        . /h/legacy-ca.sh
        tn_legacy_ca "$w/data/server/tls" "$w/etc/rsa_public.pem" "$w/etc/rsa_private.pem" compat.test.local >/dev/null 2>&1
        echo 'K10fake::server:fake' > "$w/data/server/token"
        . "$LIB"
        rc=0; tn_ca_check || rc=$?
        [ "$rc" = 2 ] || { echo "check before: rc=$rc (want 2)"; exit 1; }
        tn_ca_remediate all
        if [ ! -f "$w/k3s.calls" ] || ! grep -q 'certificate rotate-ca' "$w/k3s.calls"; then echo "rotate-ca was not called"; exit 1; fi
        tn_ca_check
      ) 2>&1 ); then
    echo "RESULT $g-remediate FAIL $(echo "$out" | tail -3 | tr '\n' ' ' | cut -c1-300)"; rm -rf "$w"; return
  fi
  if ! out=$("$BIN/k3sverify" -orig "$w/etc/rsa_public.pem" -key "$w/etc/rsa_private.pem" -days 7300 "$w/data/server/tls" 2>&1); then
    echo "RESULT $g-remediate FAIL k3sverify after remediation: $(echo "$out" | grep '^FAIL' | head -3 | tr '\n' ' ' | cut -c1-300)"; rm -rf "$w"; return
  fi
  if ls -a "$w/data/server" | grep -q '^\.tn-ca-rotate'; then echo "RESULT $g-remediate FAIL staging dir left behind"; rm -rf "$w"; return; fi
  echo "RESULT $g-remediate PASS check 2 -> remediate -> check 0"
  rm -rf "$w"
}

for g in $GENS; do
  if [ ! -s "$GENDIR/$g/rsa_public.pem" ]; then echo "RESULT $g FAIL no generated cert"; continue; fi
  one "$g"
  if openssl x509 -in "$GENDIR/$g/rsa_public.pem" -noout -text | grep -q 'Certificate Sign'; then :; else remed "$g"; fi
done
