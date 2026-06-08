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
    -p|--port)
    PORT="$2"
    shift # past argument
    shift # past value
    ;;
    -h|--help|*)
    echo "$0 -c(--context) <context> -n(--namespace) <namespace> -d(--device) <device-name> [-p(--port) <port>]"
    echo "\nSOCKS proxy via device example:"
    echo "$0 --context teknoir-prod --namespace teknoir-ai --device orin-agx-64gb-se --port 1080"
    echo ""
    echo ""
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

trap "exit" INT TERM ERR
trap "kill 0" EXIT

if [[ -z "${PORT}" ]]; then
  PORT=$(jot -r 1  8000 65000)
fi

case "${CONTEXT}" in
  *teknoir-dev*)
    DOMAIN="teknoir.dev"
    ;;
  *teknoir-poc-eks*)
    DOMAIN="teknoir.online"
    ;;
  *r415*)
    DOMAIN="teknoir.cloud"
    ;;
esac

DEADENDUSER='teknoir'
DEADENDHOST="deadend-${NAMESPACE}.${DOMAIN}"
DEADENDPORT='2222'

echo "NAMESPACE             = ${NAMESPACE}"
echo "DEVICE                = ${DEVICE}"
echo "USERNAME              = ${USERNAME}"
echo "PASSWORD              = ${PASSWORD}"
echo "REMOTE_ACCESS_ACTIVE  = ${REMOTE_ACCESS_ACTIVE}"
echo "REMOTE_ACCESS_PORT    = ${REMOTE_ACCESS_PORT}"
echo "DEADENDHOST           = ${DEADENDHOST}"

echo "SOCKS proxy listening on 127.0.0.1:${PORT}"
echo "Configure your browser or application to use SOCKS5 proxy at 127.0.0.1:${PORT}"
echo "ctrl+C to quit"
echo "macOS:"
echo "  /Applications/Google\ Chrome.app/Contents/MacOS/Google\ Chrome --proxy-server="socks5://127.0.0.1:${PORT}" --user-data-dir=$(mktemp -d)"
echo "Linux"
echo "  google-chrome --proxy-server="socks5://127.0.0.1:${PORT}" --user-data-dir=$(mktemp -d)"

PROXY_PROXY_CMD="openssl s_client -quiet -connect ${DEADENDHOST}:${DEADENDPORT} -servername ${DEADENDHOST}"
PROXY_CMD="ssh -o ProxyCommand='${PROXY_PROXY_CMD}' -o UserKnownHostsFile=/dev/null -o StrictHostKeyChecking=no -o ExitOnForwardFailure=yes -o ServerAliveInterval=60 -i ${RSA_KEY_FILE} -N -W %h:%p ${DEADENDUSER}@${DEADENDHOST} -p ${DEADENDPORT}"
ssh -o "ProxyCommand=${PROXY_CMD}" -o 'UserKnownHostsFile=/dev/null' -o 'StrictHostKeyChecking=no' -o 'ServerAliveInterval=60' -i ${RSA_KEY_FILE} ${USERNAME}@127.0.0.1 -p ${REMOTE_ACCESS_PORT} -D ${PORT} -N


rm ${RSA_KEY_FILE}
