#!/bin/sh
# legacy-ca.sh - the CA block of cli pkg/templates/server.sh.tmpl as it is on
# origin/main@7bb1bea (lines 37-141), wrapped in a function. It is the CONTROL
# case of the k3s custom-CA harness: it copies the device certificate verbatim
# to root-ca.pem, which k3s >= v1.34.12 / v1.35.9 / v1.36.5 / v1.37.1 rejects
# when the certificate lacks keyCertSign.
#
# The wrapped lines are a frozen, byte-identical copy of the template block
# (the template itself is being fixed); harness_test.go pins their sha256.
# Do NOT "fix" them here.
#
#   tn_legacy_ca <tls_dir> <cert> <key> [<suffix>]
#
#   tls_dir  DATA_DIR/server/tls (must end in /server/tls)
#   cert     device certificate (rsa_public.pem)
#   key      device private key (rsa_private.pem)
#   suffix   "<device-id>.<domain>" used in the CN of generated CAs
#
# Environment: PRODUCT (default k3s). OPENSSL is detected as in the template.
# Needs info() and fatal(); fallbacks are defined below.

command -v info >/dev/null 2>&1 || info() { echo '[INFO] ' "$@"; }
command -v fatal >/dev/null 2>&1 || fatal() { echo '[ERROR] ' "$@" >&2; exit 1; }

tn_legacy_ca() {
  _tn_tls=$1 _tn_cert=$2 _tn_key=$3 _tn_suffix=${4:-device-0.test.local}
  case $_tn_tls in */server/tls) ;; *) echo "tn_legacy_ca: tls_dir must end in /server/tls: $_tn_tls" >&2; return 2 ;; esac
  _tn_cfg=$(mktemp -d)
  cp "$_tn_cert" "$_tn_cfg/rsa_public.pem"
  cp "$_tn_key" "$_tn_cfg/rsa_private.pem"
  (
    set -e
    PRODUCT="${PRODUCT:-k3s}"
    CONFIG_PATH=$_tn_cfg
    DATA_DIR=${_tn_tls%/server/tls}
    _DEVICE_ID=${_tn_suffix%%.*}
    _DOMAIN=${_tn_suffix#*.}
    # ---- begin: server.sh.tmpl lines 37-141 -------------------------------
info "Prepare K3s CA Certs"
export PRODUCT="${PRODUCT:-k3s}"
export DATA_DIR="${DATA_DIR:-/var/lib/rancher/${PRODUCT}}"

if command -v openssl-3 >/dev/null 2>&1; then
  OPENSSL=openssl-3
else
  OPENSSL=openssl
fi

info "Using ${OPENSSL}: $(${OPENSSL} version)"

if ! ${OPENSSL} ecparam -name prime256v1 -genkey -noout -out /dev/null >/dev/null 2>&1; then
  fatal "openssl not found or missing Elliptic Curve (ecparam) support."
fi

${OPENSSL} version | grep -qF 'OpenSSL 3' && OPENSSL_GENRSA_FLAGS=-traditional

mkdir -p "${DATA_DIR}/server/tls/etcd"

cp ${CONFIG_PATH}/rsa_public.pem ${DATA_DIR}/server/tls/root-ca.pem
cp ${CONFIG_PATH}/rsa_private.pem ${DATA_DIR}/server/tls/root-ca.key

cd "${DATA_DIR}/server/tls"

# Set up temporary openssl configuration
mkdir -p ".ca/certs"
trap "rm -rf .ca" EXIT
touch .ca/index
openssl rand -hex 8 > .ca/serial
cat >.ca/config <<'EOF'
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

if [ -e service.key ]; then
  info "Generating additional Kubernetes service account issuer RSA key"
  OLD_SERVICE_KEY="$(cat service.key)"
else
  info "Generating Kubernetes service account issuer RSA key"
fi
${OPENSSL} genrsa ${OPENSSL_GENRSA_FLAGS:-} -out service.key 2048
echo "${OLD_SERVICE_KEY}" >> service.key

cat root-ca.pem > root-ca.crt

if [ ! -e root-ca.key ]; then
  fatal "Cannot generate intermediate certificate without root certificate private key"
fi

info "Generating intermediate certificate authority RSA key and certificate"
${OPENSSL} genrsa ${OPENSSL_GENRSA_FLAGS:-} -out intermediate-ca.key 4096
${OPENSSL} req -new -nodes \
               -subj "/CN=${PRODUCT}-intermediate-ca@${_DEVICE_ID}.${_DOMAIN}" \
               -key intermediate-ca.key |
${OPENSSL} ca  -batch -notext -days 3700 \
               -in /dev/stdin \
               -out intermediate-ca.pem \
               -keyfile root-ca.key \
               -cert root-ca.pem \
               -config .ca/config \
               -extensions v3_ca

cat intermediate-ca.pem root-ca.pem > intermediate-ca.crt

if [ ! -e intermediate-ca.key ]; then
  fatal "Cannot generate leaf certificates without intermediate certificate private key"
fi

# Generate new leaf CAs for all the control-plane and etcd components
for TYPE in client server request-header etcd/peer etcd/server; do
  CERT_NAME="${PRODUCT}-$(echo ${TYPE} | tr / -)-ca"
  info "Generating ${CERT_NAME} leaf certificate authority EC key and certificate"
  ${OPENSSL} ecparam -name prime256v1 -genkey -noout -out ${TYPE}-ca.key
  ${OPENSSL} req -new -nodes \
                 -subj "/CN=${CERT_NAME}@${_DEVICE_ID}.${_DOMAIN}" \
                 -key ${TYPE}-ca.key |
  ${OPENSSL} ca  -batch -notext -days 3700 \
                 -in /dev/stdin \
                 -out ${TYPE}-ca.pem \
                 -keyfile intermediate-ca.key \
                 -cert intermediate-ca.pem \
                 -config .ca/config \
                 -extensions v3_ca
  cat ${TYPE}-ca.pem \
      intermediate-ca.pem \
      root-ca.pem > ${TYPE}-ca.crt
done
    # ---- end: server.sh.tmpl lines 37-141 ---------------------------------
  )
  _tn_rc=$?
  rm -rf "$_tn_cfg"
  return $_tn_rc
}
