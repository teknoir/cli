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

if [[ -z "${CONTEXT}" ]]; then
  export DEVICE_MANIFEST="$(kubectl -n $NAMESPACE get device $DEVICE -o yaml)"
else
  export DEVICE_MANIFEST="$(kubectl --context ${CONTEXT} -n $NAMESPACE get device $DEVICE -o yaml)"
fi

USERNAME="$(echo "$DEVICE_MANIFEST" | yq e .spec.keys.data.username - | base64 --decode -i -)"
PASSWORD="$(echo "$DEVICE_MANIFEST" | yq e .spec.keys.data.userpassword - | base64 --decode -i -)"

REMOTE_ACCESS_ACTIVE="$(echo "$DEVICE_MANIFEST" | yq e .subresources.status.remote_access.active -)"
REMOTE_ACCESS_PORT="$(echo "$DEVICE_MANIFEST" | yq e .subresources.status.remote_access.port -)"

if ! [[ ${REMOTE_ACCESS_ACTIVE} == true ]] ; then
   echo "error: The device ${DEVICE}, has not enabled remote access, please go to the GUI and enable remote access for the device!" >&2
   exit 1
fi

RSA_KEY_FILE="./$(basename $0)_deleteme.id_rsa"
echo "$DEVICE_MANIFEST" | yq e .spec.keys.data.rsa_private - | base64 --decode -i - | tee ${RSA_KEY_FILE} >/dev/null
chmod 600 ${RSA_KEY_FILE}

echo "Connecting to device: ${NAMESPACE}/${DEVICE}"
echo "Password to escalate permissions: ${PASSWORD}"
#echo "REMOTE_ACCESS_ACTIVE  = ${REMOTE_ACCESS_ACTIVE}"
#echo "REMOTE_ACCESS_PORT    = ${REMOTE_ACCESS_PORT}"
echo "Username to use in IntelliJ: ${USERNAME}"
echo "Private Key to use in IntelliJ: ${RSA_KEY_FILE}"
echo "Host to connect to in IntelliJ: 127.0.0.1:${REMOTE_ACCESS_PORT}"


trap "exit" INT TERM ERR
trap "kill 0" EXIT

DOMAIN=$(if [[ ${CONTEXT} =~ ^.*teknoir-dev.*$ || ${CONTEXT} =~ ^.*teknoir-poc.*$ ]]; then echo "teknoir.dev"; else echo "teknoir.cloud"; fi)
DEADENDUSER='teknoir'
DEADENDHOST="deadend.${DOMAIN}"
DEADENDPORT='2222'
ssh -o 'UserKnownHostsFile=/dev/null' -o 'StrictHostKeyChecking=no' -o 'ExitOnForwardFailure=yes' -o 'ServerAliveInterval=60' -i ${RSA_KEY_FILE} -N ${DEADENDUSER}@${DEADENDHOST} -p ${DEADENDPORT} -L ${REMOTE_ACCESS_PORT}:127.0.0.1:${REMOTE_ACCESS_PORT}
#ssh -o "ProxyCommand ${PROXY_CMD}" -o 'UserKnownHostsFile=/dev/null' -o 'StrictHostKeyChecking=no' -o 'ServerAliveInterval=60' -i ${RSA_KEY_FILE} ${USERNAME}@127.0.0.1 -p ${REMOTE_ACCESS_PORT}

rm ${RSA_KEY_FILE}