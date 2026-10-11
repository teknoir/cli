# shellcheck shell=bash
# Device certificate generators. Each variant writes rsa_private.pem and
# rsa_public.pem (a self-signed certificate) into $OUT/gen/<name>/ - the pair a
# Device CR carries in spec.settings.
#
#   bs-o111    Backstage validator-create-device.ts as shipped, OpenSSL 1.1.1 (debian:bullseye)
#   bs-o3      the same command with OpenSSL 3.x (debian:bookworm)
#   go-sha1    device-controller generateRSAKeyAndCert (6c718b6), RFC 5280 SHA-1 SKI
#   go-sha256  the same with the truncated SHA-256 SKI Go >= 1.25 produces
#   expired    Backstage command, validity 2024-2025 (debian:trixie, OpenSSL 3.5 -not_before/-not_after)
#   fixed-bs   Backstage generator after the fix: explicit -config with keyCertSign, 7300 days
#   fixed-go   device-controller generator after the fix (gencert-go -fixed)
#   ext        --device-cert/--device-key supplied from outside (WP3-WP5 use this)

GEN_LEGACY="bs-o111 bs-o3 go-sha1 go-sha256 expired"
GEN_FIXED="fixed-bs fixed-go"
BS_SUBJ='/C=US/ST=Texas/L=SHouston/O=Teknoir LLC/CN=teknoir.ai'

gen_dir() { printf '%s/gen/%s' "$OUT" "$1"; }

# The exact openssl invocation of Backstage, minus the working directory.
_bs_cmd() { # IMAGE DIR [extra req args replacing -days 365]
  local img=$1 dir=$2; shift 2
  local validity=("$@"); [ "${#validity[@]}" -gt 0 ] || validity=(-days 365)
  d_helper "$img" --user "$(id -u):$(id -g)" -e HOME=/tmp -v "$dir:/out" -w /out -- \
    openssl req -x509 -nodes -newkey rsa:2048 -keyout rsa_private.pem -keyform PEM \
    -pkeyopt rsa_keygen_pubexp:65537 -out rsa_public.pem "${validity[@]}" \
    -subj "$BS_SUBJ" -sha256 -batch
}

gen_make() { # NAME
  local g=$1 dir img; dir=$(gen_dir "$g")
  mkdir -p "$dir"
  case $g in
    bs-o111)  img=$(helper_image debian:bullseye) || die "helper image"; _bs_cmd "$img" "$dir" ;;
    bs-o3)    img=$(helper_image debian:bookworm) || die "helper image"; _bs_cmd "$img" "$dir" ;;
    expired)  img=$(helper_image debian:trixie) || die "helper image"; _bs_cmd "$img" "$dir" -not_before 20240301000000Z -not_after 20250301000000Z ;;
    go-sha1)  "$OUT/bin/gencert-go" -out "$dir" -ski sha1 ;;
    go-sha256) "$OUT/bin/gencert-go" -out "$dir" -ski sha256trunc ;;
    fixed-go) "$OUT/bin/gencert-go" -out "$dir" -ski sha256trunc -fixed ;;
    fixed-bs)
      cat > "$dir/fixed.cnf" <<'CNF'
[req]
distinguished_name = req_dn
x509_extensions = v3_device
[req_dn]
[v3_device]
basicConstraints = critical, CA:TRUE
keyUsage = critical, digitalSignature, keyEncipherment, keyCertSign, cRLSign
subjectKeyIdentifier = hash
authorityKeyIdentifier = keyid:always
CNF
      img=$(helper_image debian:bullseye) || die "helper image"
      d_helper "$img" --user "$(id -u):$(id -g)" -e HOME=/tmp -v "$dir:/out" -w /out -- \
        openssl req -x509 -nodes -newkey rsa:2048 -keyout rsa_private.pem -keyform PEM \
        -pkeyopt rsa_keygen_pubexp:65537 -out rsa_public.pem -config fixed.cnf -days 7300 \
        -subj "$BS_SUBJ" -sha256 -batch
      rm -f "$dir/fixed.cnf" ;;
    ext)
      [ -f "${DEVICE_CERT:-}" ] && [ -f "${DEVICE_KEY:-}" ] || die "gen 'ext' needs --device-cert and --device-key"
      cp "$DEVICE_CERT" "$dir/rsa_public.pem"; cp "$DEVICE_KEY" "$dir/rsa_private.pem" ;;
    *) die "unknown device cert generator '$g'" ;;
  esac
  [ -s "$dir/rsa_public.pem" ] && [ -s "$dir/rsa_private.pem" ] || die "generator $g produced no key pair"
  chmod 600 "$dir/rsa_private.pem"; chmod 644 "$dir/rsa_public.pem"
}

# Sanity: the generator must have produced what its name promises.
gen_check() { # NAME
  local g=$1 dir txt; dir=$(gen_dir "$g")
  local img; img=$(helper_image debian:bookworm) || die "helper image"
  txt=$(d_helper "$img" -v "$dir:/out:ro" -- openssl x509 -in /out/rsa_public.pem -noout -text -dates 2>/dev/null) || die "cannot read $g certificate"
  case $g in
    bs-*|go-*|expired)
      if grep -q 'Certificate Sign' <<<"$txt"; then die "generator $g unexpectedly has keyCertSign"; fi ;;
    fixed-*)
      grep -q 'Certificate Sign' <<<"$txt" || die "generator $g lacks keyCertSign" ;;
    ext)
      # the real WP3-WP5 certificate: what the legacy scripts do with it depends on keyCertSign
      if grep -q 'Certificate Sign' <<<"$txt"; then EXT_CERTSIGN=1; else EXT_CERTSIGN=0; fi
      export EXT_CERTSIGN
      log "device cert 'ext': keyCertSign=$([ "$EXT_CERTSIGN" = 1 ] && echo yes || echo no) -> legacy scripts expected $(expected_outcome legacy ext v1.36.5-k3s1) on post-fix k3s${EXPECT_OVERRIDE:+ (--expect $EXPECT_OVERRIDE)}" ;;
  esac
  if [ "$g" = expired ]; then
    d_helper "$img" -v "$dir:/out:ro" -- openssl x509 -in /out/rsa_public.pem -noout -checkend 0 >/dev/null 2>&1 &&
      die "generator expired is still valid"
  fi
  return 0
}
