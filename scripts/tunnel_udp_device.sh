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
    -t|--to)
    TO="$2"
    shift # past argument
    shift # past value
    ;;
    --udp-target)
    UDP_TARGET="$2"
    shift # past argument
    shift # past value
    ;;
    --udp-local-port)
    UDP_LOCAL_PORT="$2"
    shift # past argument
    shift # past value
    ;;
    -h|--help|*)
    echo "$0 -c(--context) <context> -n(--namespace) <namespace> -d(--device) <device-name> -p(--port) <local-port> -t(--to) <ip:port>"
    echo "UDP Tunneling example:"
    echo "$0 --context teknoir-prod --device my-device --port 5000 --udp-local-port 4201 --udp-target 10.202.1.4:4201"
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


echo "Browse to http://localhost:${PORT} or connect your service to 127.0.0.1:${PORT}"
echo "ctrl+C to quit"

#UDP Tunneling

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

#PROXY_CMD="ssh -o UserKnownHostsFile=/dev/null -o StrictHostKeyChecking=no -o ExitOnForwardFailure=yes -o ServerAliveInterval=60 -i ${RSA_KEY_FILE} -N -W %h:%p ${DEADENDUSER}@${DEADENDHOST} -p ${DEADENDPORT}"
#ssh -o "ProxyCommand ${PROXY_CMD}" -o 'UserKnownHostsFile=/dev/null' -o 'StrictHostKeyChecking=no' -o 'ServerAliveInterval=60' -i ${RSA_KEY_FILE} ${USERNAME}@127.0.0.1 -p ${REMOTE_ACCESS_PORT} -L ${PORT}:${TO} -N

PROXY_PROXY_CMD="ncat --ssl ${DEADENDHOST} ${DEADENDPORT}"
PROXY_CMD="ssh -o ProxyCommand='${PROXY_PROXY_CMD}' -o UserKnownHostsFile=/dev/null -o StrictHostKeyChecking=no -o ExitOnForwardFailure=yes -o ServerAliveInterval=60 -i ${RSA_KEY_FILE} -N -W %h:%p ${DEADENDUSER}@${DEADENDHOST} -p ${DEADENDPORT}"

# If UDP tunneling is requested, handle local/remote socat setups
if [[ -n "${UDP_TARGET}" ]]; then
    if [[ -z "${UDP_LOCAL_PORT}" ]]; then
        UDP_LOCAL_PORT="${PORT}"
    fi
    
    echo "Starting UDP tunnel: Local UDP port ${UDP_LOCAL_PORT} -> TCP port ${PORT} -> Remote UDP ${UDP_TARGET}"
    
    # Start local socat in the background
    socat "UDP4-LISTEN:${UDP_LOCAL_PORT},fork,reuseaddr" "TCP4:127.0.0.1:${PORT}" &
    
    # Execute remote socat command via SSH
    ssh -o "ProxyCommand=${PROXY_CMD}" -o 'UserKnownHostsFile=/dev/null' -o 'StrictHostKeyChecking=no' -o 'ServerAliveInterval=60' -i ${RSA_KEY_FILE} ${USERNAME}@127.0.0.1 -p ${REMOTE_ACCESS_PORT} -L "${PORT}:127.0.0.1:${PORT}" "socat TCP4-LISTEN:${PORT},fork UDP4:${UDP_TARGET}"
else
    # Standard TCP tunnel
    ssh -o "ProxyCommand=${PROXY_CMD}" -o 'UserKnownHostsFile=/dev/null' -o 'StrictHostKeyChecking=no' -o 'ServerAliveInterval=60' -i ${RSA_KEY_FILE} ${USERNAME}@127.0.0.1 -p ${REMOTE_ACCESS_PORT} -L "${PORT}:${TO}" -N
fi

rm ${RSA_KEY_FILE}