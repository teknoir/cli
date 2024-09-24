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
    -s|--source)
    SOURCE=$2
    shift # past argument
    shift # past value
    ;;
    -t|--target)
    TARGET=$2
    shift # past argument
    shift # past value
    ;;
    -k|--kubeflow)
    KUBEFLOW="true"
    shift # past argument
    # shift # past value
    ;;
    -h|--help|*)
    echo "$0 -c(--context) <context> -n(--namespace) <namespace> -d(--device) <device-name>"
    exit 0
    ;;
esac
done

if [[ -z "${KUBEFLOW}" ]]; then
  DEVICE_RESOURCE="device.teknoir.org"
else
  DEVICE_RESOURCE="device"
fi

if [[ -z "${CONTEXT}" ]]; then
  export DEVICE_MANIFEST="$(kubectl -n $NAMESPACE get $DEVICE_RESOURCE $DEVICE -o yaml)"
else
  export DEVICE_MANIFEST="$(kubectl --context ${CONTEXT} -n $NAMESPACE get $DEVICE_RESOURCE $DEVICE -o yaml)"
fi

USERNAME="$(echo "$DEVICE_MANIFEST" | yq e .spec.keys.data.username - | base64 --decode -i -)"
PASSWORD="$(echo "$DEVICE_MANIFEST" | yq e .spec.keys.data.userpassword - | base64 --decode -i -)"

REMOTE_ACCESS_ACTIVE="$(echo "$DEVICE_MANIFEST" | yq e .subresources.status.remote_access.active -)"
REMOTE_ACCESS_PORT="$(echo "$DEVICE_MANIFEST" | yq e .subresources.status.remote_access.port -)"

echo "NAMESPACE             = ${NAMESPACE}"
echo "DEVICE                = ${DEVICE}"
echo "USERNAME              = ${USERNAME}"
echo "PASSWORD              = ${PASSWORD}"
echo "REMOTE_ACCESS_ACTIVE  = ${REMOTE_ACCESS_ACTIVE}"
echo "REMOTE_ACCESS_PORT    = ${REMOTE_ACCESS_PORT}"

if ! [[ ${REMOTE_ACCESS_ACTIVE} == true ]] ; then
   echo "error: The device ${DEVICE}, has not enabled remote access, please go to the GUI and enable remote access for the device!" >&2
   exit 1
fi

RSA_KEY_FILE=$(mktemp -t "$(basename $0)")
echo "$DEVICE_MANIFEST" | yq e .spec.keys.data.rsa_private - | base64 --decode -i - | tee ${RSA_KEY_FILE} >/dev/null

trap "exit" INT TERM ERR
trap "kill 0" EXIT

DOMAIN=$(if [[ ${CONTEXT} =~ ^.*teknoir-dev.*$ || ${CONTEXT} =~ ^.*teknoir-poc.*$ ]]; then echo "teknoir.dev"; else echo "teknoir.cloud"; fi)
DEADENDUSER='teknoir'
DEADENDHOST="deadend.${DOMAIN}"
DEADENDPORT='2222'
PROXY_CMD="ssh -o UserKnownHostsFile=/dev/null -o StrictHostKeyChecking=no -o ExitOnForwardFailure=yes -o ServerAliveInterval=60 -i ${RSA_KEY_FILE} -N -W %h:%p ${DEADENDUSER}@${DEADENDHOST} -p ${DEADENDPORT}"
scp -o "ProxyCommand ${PROXY_CMD}" -o 'UserKnownHostsFile=/dev/null' -o 'StrictHostKeyChecking=no' -o 'ServerAliveInterval=60' -i ${RSA_KEY_FILE} -P ${REMOTE_ACCESS_PORT} ${USERNAME}@127.0.0.1:${SOURCE} ${TARGET}

rm ${RSA_KEY_FILE}