#!/bin/sh
set -e

POSITIONAL=()
while [[ $# -gt 0 ]]
do
key="$1"

case $key in
    -n|--namespace)
    NAMESPACE="$2"
    shift # past argument
    shift # past value
    ;;
    -d|--device)
    DEVICE="$2"
    shift # past argument
    shift # past value
    ;;
    -c|--context)
    CONTEXT=$2
    shift # past argument
    shift # past value
    ;;
    -h|--help|*)
    echo "$0 -c(--context) <context> -n(--namespace) <namespace> -d(--device) <device-name> -s(--source) <source-path> -t(--target) <target-path>"
    exit 0
    ;;
esac
done

DEVICE_RESOURCE="device"

if [[ -z "${CONTEXT}" ]]; then
  export DEVICE_MANIFEST="$(kubectl -n $NAMESPACE get $DEVICE_RESOURCE $DEVICE -o yaml)"
else
  export DEVICE_MANIFEST="$(kubectl --context ${CONTEXT} -n $NAMESPACE get $DEVICE_RESOURCE $DEVICE -o yaml)"
fi

USERNAME="$(printf "%s\n" "${DEVICE_MANIFEST}" | yq e .spec.keys.data.username - | base64 --decode -i -)"
PASSWORD="$(printf "%s\n" "${DEVICE_MANIFEST}" | yq e .spec.keys.data.userpassword - | base64 --decode -i -)"

REMOTE_ACCESS_ACTIVE="$(printf "%s\n" "${DEVICE_MANIFEST}" | yq e .subresources.status.remote_access.active -)"
REMOTE_ACCESS_PORT="$(printf "%s\n" "${DEVICE_MANIFEST}" | yq e .subresources.status.remote_access.port -)"

if ! [[ ${REMOTE_ACCESS_ACTIVE} == true ]] ; then
   echo "error: The device ${DEVICE}, has not enabled remote access, please go to the GUI and enable remote access for the device!" >&2
   exit 1
fi

RSA_KEY_FILE=$(mktemp -t "$(basename $0)")
printf "%s\n" "${DEVICE_MANIFEST}" | yq e .spec.keys.data.rsa_private - | base64 --decode -i - | tee ${RSA_KEY_FILE} >/dev/null
cat ${RSA_KEY_FILE}
#RSA_KEY_FILE="/Volumes/GIT/ai/cli/rtx2000-ada-64gb-se_rsa_private.pem"

trap "exit" INT TERM ERR
trap "kill 0" EXIT

case "${CONTEXT}" in
  *teknoir-dev*|*teknoir-poc*)
    DOMAIN="teknoir.dev"
    ;;
  *rtx2000-pro-bw-se.teknoir*)
    DOMAIN="teknoir.online"
    ;;
  *r415*)
    DOMAIN="teknoir.cloud"
    ;;
esac
DEADENDUSER='teknoir'
DEADENDHOST="${DEADENDHOST:-deadend-${NAMESPACE}.${DOMAIN}}"
DEADENDHTTPSPORT="${DEADENDHTTPSPORT:-2222}"
REVERSETUNNEL="${REMOTE_ACCESS_PORT}:localhost:22"


echo "NAMESPACE             = ${NAMESPACE}"
echo "DEVICE                = ${DEVICE}"
echo "USERNAME              = ${USERNAME}"
echo "PASSWORD              = ${PASSWORD}"
echo "REMOTE_ACCESS_ACTIVE  = ${REMOTE_ACCESS_ACTIVE}"
echo "REMOTE_ACCESS_PORT    = ${REMOTE_ACCESS_PORT}"
echo "DEADENDHOST           = ${DEADENDHOST}"

#if ! command -v ncat >/dev/null 2>&1; then
#  echo "error: ncat is required for HTTPS proxy tunneling (missing ncat)" >&2
#  exit 1
#fi

#PROXY_CMD="ncat --ssl --ssl-servername ${DEADENDHOST} %h %p"
PROXY_CMD="openssl s_client -quiet -connect %h:%p -servername ${DEADENDHOST}"
ssh -v -o "ProxyCommand ${PROXY_CMD}" -o 'PubkeyAcceptedAlgorithms=+ssh-rsa' -o IdentitiesOnly=yes -o 'UserKnownHostsFile=/dev/null' -o 'StrictHostKeyChecking=no' -o 'ExitOnForwardFailure=yes' -o 'ServerAliveInterval=60' -i ${RSA_KEY_FILE} -N -R ${REVERSETUNNEL} ${DEADENDUSER}@${DEADENDHOST} -p ${DEADENDHTTPSPORT}

rm ${RSA_KEY_FILE}

# printf '' | nc deadend-test-organisation.teknoir.online 2222
# If you see SSH-2.0-OpenSSH_..., then TLS termination is not happening on 2222 (it’s raw SSH). If it hangs or you only see TLS handshake bytes (gibberish), it’s likely TLS‑terminated.

# openssl s_client -connect deadend-test-organisation.teknoir.online:2222 -servername deadend-test-organisation.teknoir.online -showcerts < /dev/null