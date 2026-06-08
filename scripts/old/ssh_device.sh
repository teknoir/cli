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
    echo "$0 -c(--context) <context> -n(--namespace) <namespace> -d(--device) <device-name>"
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

USERNAME=$(printf "%s\n" "${DEVICE_MANIFEST}" | yq e .spec.keys.data.username - | base64 --decode -i -)
PASSWORD=$(printf "%s\n" "${DEVICE_MANIFEST}" | yq e .spec.keys.data.userpassword - | base64 --decode -i -)

REMOTE_ACCESS_ACTIVE=$(printf "%s\n" "${DEVICE_MANIFEST}" | yq e .subresources.status.remote_access.active -)
REMOTE_ACCESS_PORT=$(printf "%s\n" "${DEVICE_MANIFEST}" | yq e .subresources.status.remote_access.port -)

if ! [[ ${REMOTE_ACCESS_ACTIVE} == true ]] ; then
   echo "error: The device ${DEVICE}, has not enabled remote access, please go to the GUI and enable remote access for the device!" >&2
   exit 1
fi

RSA_KEY_FILE=$(mktemp -t "$(basename $0)")
printf "%s\n" "${DEVICE_MANIFEST}" | yq e .spec.keys.data.rsa_private - | base64 --decode -i - | tee ${RSA_KEY_FILE} >/dev/null

trap "exit" INT TERM ERR
trap "kill 0" EXIT

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
#DOMAIN=$(if [[ ${CONTEXT} =~ ^.*teknoir-dev.*$ || ${CONTEXT} =~ ^.*teknoir-poc.*$ ]]; then echo "teknoir.dev"; else echo "teknoir.cloud"; fi)
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

PROXY_PROXY_CMD="ncat --ssl ${DEADENDHOST} ${DEADENDPORT}"
FALLBACK_PROXY_PROXY_CMD="openssl s_client -quiet -connect ${DEADENDHOST}:${DEADENDPORT} -servername ${DEADENDHOST}"
#PROXY_PROXY_CMD="openssl s_client -quiet -proxy 127.0.0.1:${REMOTE_ACCESS_PORT} -connect %h:%p -servername %h"
PROXY_CMD="ssh -o ProxyCommand='${PROXY_PROXY_CMD}' -o UserKnownHostsFile=/dev/null -o StrictHostKeyChecking=no -o ExitOnForwardFailure=yes -o ServerAliveInterval=60 -i ${RSA_KEY_FILE} -N -W %h:%p ${DEADENDUSER}@${DEADENDHOST} -p ${DEADENDPORT}"
ssh -o "ProxyCommand=${PROXY_CMD}" -o 'UserKnownHostsFile=/dev/null' -o 'StrictHostKeyChecking=no' -o 'ServerAliveInterval=60' -i ${RSA_KEY_FILE} ${USERNAME}@127.0.0.1 -p ${REMOTE_ACCESS_PORT}
#ssh -o ProxyCommand="${PROXY_PROXY_CMD}" -o UserKnownHostsFile=/dev/null -o StrictHostKeyChecking=no -o ServerAliveInterval=60 -i ${RSA_KEY_FILE} ${DEADENDUSER}@${DEADENDHOST} -p ${DEADENDPORT}

rm ${RSA_KEY_FILE}
