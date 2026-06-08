#!/bin/sh
set -e
#set -x

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
    -k|--kubeflow)
    KUBEFLOW="true"
    shift # past argument
    ;;
    -s|--source)
    SOURCE="$2"
    shift # past argument
    shift # past value
    ;;
    -D|--destination)
    DESTINATION="$2"
    shift # past argument
    shift # past value
    ;;
    -o|--opts)
    RSYNC_OPTS="$2"
    shift # past argument
    shift # past value
    ;;
    -p|--pull)
    PULL="true"
    shift # past argument
    ;;
    -h|--help|*)
    echo "$0 -c(--context) <context> -n(--namespace) <namespace> -d(--device) <device-name> -s(--source) <source-path> -D(--destination) <destination-path> -o(--opts) <rsync-options> [-p|--pull]"
    echo "\nrsync to device example (default):"
    echo "$0 --context teknoir-prod --namespace teknoir-ai --device orin-agx-64gb-se --source /local/path/ --destination /remote/path/"
    echo "\nrsync from device example (with pull flag):"
    echo "$0 --context teknoir-prod --namespace teknoir-ai --device orin-agx-64gb-se --source /remote/path/ --destination /local/path/ --pull"
    echo "\nWith custom rsync options example:"
    echo "$0 --context teknoir-prod --namespace teknoir-ai --device orin-agx-64gb-se --source /remote/path/ --destination /local/path/ --pull --opts \"--include='*.mp4' --exclude='*'\""
    echo ""
    echo ""
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
#PROXY_CMD="ssh -o UserKnownHostsFile=/dev/null -o StrictHostKeyChecking=no -o ExitOnForwardFailure=yes -o ServerAliveInterval=60 -i ${RSA_KEY_FILE} -N -W %h:%p ${DEADENDUSER}@${DEADENDHOST} -p ${DEADENDPORT}"
PROXY_PROXY_CMD="ncat --ssl ${DEADENDHOST} ${DEADENDPORT}"
PROXY_CMD="ssh -o ProxyCommand='${PROXY_PROXY_CMD}' -o UserKnownHostsFile=/dev/null -o StrictHostKeyChecking=no -o ExitOnForwardFailure=yes -o ServerAliveInterval=60 -i ${RSA_KEY_FILE} -N -W %h:%p ${DEADENDUSER}@${DEADENDHOST} -p ${DEADENDPORT}"

echo "NAMESPACE             = ${NAMESPACE}"
echo "DEVICE                = ${DEVICE}"
echo "USERNAME              = ${USERNAME}"
echo "PASSWORD              = ${PASSWORD}"
echo "REMOTE_ACCESS_ACTIVE  = ${REMOTE_ACCESS_ACTIVE}"
echo "REMOTE_ACCESS_PORT    = ${REMOTE_ACCESS_PORT}"
echo "DEADENDHOST           = ${DEADENDHOST}"

# Determine sync direction based on PULL flag
if [[ -z "${PULL}" ]]; then
  # Default: Local to Remote
  echo "Syncing files from ${SOURCE} to ${USERNAME}@${DEVICE}:${DESTINATION}"
  echo "Using rsync options: ${RSYNC_OPTS}"
  echo "ctrl+C to quit"

  # Use eval to properly handle quoted rsync options
  eval "rsync -avz --progress --stats ${RSYNC_OPTS} -e \"ssh -o \\\"ProxyCommand ${PROXY_CMD}\\\" -o \\\"UserKnownHostsFile=/dev/null\\\" -o \\\"StrictHostKeyChecking=no\\\" -o \\\"ServerAliveInterval=60\\\" -i ${RSA_KEY_FILE} -p ${REMOTE_ACCESS_PORT}\" ${SOURCE} ${USERNAME}@127.0.0.1:${DESTINATION}"
else
  # Pull: Remote to Local
  echo "Syncing files from ${USERNAME}@${DEVICE}:${SOURCE} to ${DESTINATION}"
  echo "Using rsync options: ${RSYNC_OPTS}"
  echo "ctrl+C to quit"

  # Use eval to properly handle quoted rsync options
  eval "rsync -avz --progress --stats ${RSYNC_OPTS} -e \"ssh -o \\\"ProxyCommand ${PROXY_CMD}\\\" -o \\\"UserKnownHostsFile=/dev/null\\\" -o \\\"StrictHostKeyChecking=no\\\" -o \\\"ServerAliveInterval=60\\\" -i ${RSA_KEY_FILE} -p ${REMOTE_ACCESS_PORT}\" ${USERNAME}@127.0.0.1:${SOURCE} ${DESTINATION}"
fi

rm ${RSA_KEY_FILE}

#rsync_device.sh --context teknoir-prod --namespace victra-poc --device victra-poc-02 \
#  --source /opt/teknoir/video/segments/ --destination /Volumes/VIDEOS/victra-poc-02/ \
#  --pull --opts "--include='nc0009-front-door*.mp4' --exclude='*'"
