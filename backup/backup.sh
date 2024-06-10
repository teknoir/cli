#!/bin/bash
set -e
#set -x

# This script require:
#   * yq and jq to be installed
#   * gcloud to be installed and configured
#   * gsutil to be installed and configured
#   * kubectl to be installed and configured
#   * kubectl to be installed and configured for the contexts teknoir-dev & teknoir-prod
#   * kubectl krew plugin manager with neat, ctx, and ns plugins installed
#   * ansible with Teknoir plugins installed


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
    -c|--context)
    CONTEXT=$2
    shift # past argument
    shift # past value
    ;;
    -h|--help|*)
    echo "$0 -c(--context) <context> -n(--namespace) <namespace>"
    exit 0
    ;;
esac
done

export ZONE=us-central1-c
export PROJECT=$(if [ "$CONTEXT" == "gke_teknoir-poc_us-central1-c_teknoir-dev-cluster" ]; then echo "teknoir-poc"; else echo "teknoir"; fi)
export DOMAIN=$(if [ "$CONTEXT" == "gke_teknoir-poc_us-central1-c_teknoir-dev-cluster" ]; then echo "teknoir.dev"; else echo "teknoir.cloud"; fi)

echo "NAMESPACE  = ${NAMESPACE}"
echo "CONTEXT    = ${CONTEXT}"
echo "ZONE       = ${ZONE}"
echo "PROJECT    = ${PROJECT}"
echo "DOMAIN     = ${DOMAIN}"

function backup_namespace_profile() {
  NAMESPACE=$1
  NOW=$2
  BACKUP_DIR="backup-data/${NAMESPACE}/${NOW}"
  mkdir -p ${BACKUP_DIR}
  kubectl --context ${CONTEXT} -n ${NAMESPACE} get profile ${NAMESPACE} -o yaml | kubectl neat > ${BACKUP_DIR}/${NAMESPACE}.yaml
}

function backup_namespace_device_devstudios() {
  DEVICE_MANIFEST=$1
  DEVICE=$2
  BACKUP_DIR=$3
  for DEVSTUDIO in `echo "${DEVICE_MANIFEST}" | yq eval '.spec.manifest.apps.items[] | select(.kind == "Deployment") | select(.metadata.name == "devstudio*") | .metadata.name' -`; do
    echo "Backup devstudio: ${DEVSTUDIO} on device: ${DEVICE}"
    for DEVSTUDIO_MOUNT_DIR in `echo "${DEVICE_MANIFEST}" | yq eval ".spec.manifest.apps.items[] | select(.kind == \"Deployment\") | select(.metadata.name == \"${DEVSTUDIO}\") | .spec.template.spec.volumes[] | select(.name == \"data\") | .hostPath.path" -`; do
      mkdir -p ${BACKUP_DIR}/devices/${DEVICE}/devstudios/${DEVSTUDIO}
      ansible ${DEVICE} -m fetch -a "src=${DEVSTUDIO_MOUNT_DIR}/flows.json dest=${BACKUP_DIR}/devices/${DEVICE}/devstudios/${DEVSTUDIO}/flows.json flat=yes" || true
      kill $(ps aux | grep 'kubectl' | grep ' 8118:8118' | awk '{print $2}') || true
      ansible ${DEVICE} -m fetch -a "src=${DEVSTUDIO_MOUNT_DIR}/flows_cred.json dest=${BACKUP_DIR}/devices/${DEVICE}/devstudios/${DEVSTUDIO}/flows_cred.json flat=yes" || true
      kill $(ps aux | grep 'kubectl' | grep ' 8118:8118' | awk '{print $2}') || true
      ansible ${DEVICE} -m fetch -a "src=${DEVSTUDIO_MOUNT_DIR}/.config.nodes.json dest=${BACKUP_DIR}/devices/${DEVICE}/devstudios/${DEVSTUDIO}/.config.nodes.json flat=yes" || true
      kill $(ps aux | grep 'kubectl' | grep ' 8118:8118' | awk '{print $2}') || true
      ansible ${DEVICE} -m fetch -a "src=${DEVSTUDIO_MOUNT_DIR}/.config.projects.json dest=${BACKUP_DIR}/devices/${DEVICE}/devstudios/${DEVSTUDIO}/.config.projects.json flat=yes" || true
      kill $(ps aux | grep 'kubectl' | grep ' 8118:8118' | awk '{print $2}') || true
      ansible ${DEVICE} -m fetch -a "src=${DEVSTUDIO_MOUNT_DIR}/.config.users.json dest=${BACKUP_DIR}/devices/${DEVICE}/devstudios/${DEVSTUDIO}/.config.users.json flat=yes" || true
      kill $(ps aux | grep 'kubectl' | grep ' 8118:8118' | awk '{print $2}') || true
    done
  done
}

function backup_namespace_devices() {
  NAMESPACE=$1
  NOW=$2
  BACKUP_DIR="backup-data/${NAMESPACE}/${NOW}"
  mkdir -p ${BACKUP_DIR}/devices
  for DEVICE in `kubectl --context ${CONTEXT} -n ${NAMESPACE} get device -o json |  jq '.items[] | .metadata.name' | sed 's/"//g'` ; do
    echo "Backup device: ${DEVICE}"
    mkdir -p ${BACKUP_DIR}/devices/${DEVICE}
    DEVICE_MANIFEST=$(kubectl --context ${CONTEXT} -n ${NAMESPACE} get device ${DEVICE} -o yaml | kubectl neat)
    echo "Writing file: ${BACKUP_DIR}/devices/${DEVICE}/${DEVICE}.yaml"
    echo "${DEVICE_MANIFEST}" > ${BACKUP_DIR}/devices/${DEVICE}/${DEVICE}.yaml
    backup_namespace_device_devstudios "${DEVICE_MANIFEST}" "${DEVICE}" "${BACKUP_DIR}"
  done
}

function backup_namespace_devstudios() {
  NAMESPACE=$1
  NOW=$2
  BACKUP_DIR="backup-data/${NAMESPACE}/${NOW}"
  for DEVSTUDIO in `kubectl --context ${CONTEXT} -n ${NAMESPACE} get devstudio -o json |  jq '.items[] | .metadata.name' | sed 's/"//g'` ; do
    echo "Backup devstudio: ${DEVSTUDIO}"
    mkdir -p ${BACKUP_DIR}/devstudios/${DEVSTUDIO}
    echo "Writing file: ${BACKUP_DIR}/devstudios/${DEVSTUDIO}/${DEVSTUDIO}.yaml"
    kubectl --context ${CONTEXT} -n ${NAMESPACE} get devstudio ${DEVSTUDIO} -o yaml | kubectl neat > ${BACKUP_DIR}/devstudios/${DEVSTUDIO}/${DEVSTUDIO}.yaml
    POD=$(kubectl --context ${CONTEXT} -n ${NAMESPACE} get pods -l devstudio-name=${DEVSTUDIO} -o json| jq -r '.items[].metadata.name')
    echo "Writing file: ${BACKUP_DIR}/devstudios/${DEVSTUDIO}/flows.json"
    kubectl --context ${CONTEXT} -n ${NAMESPACE} cp -c ${DEVSTUDIO} ${POD}:/data/flows.json ${BACKUP_DIR}/devstudios/${DEVSTUDIO}/flows.json || true
    echo "Writing file: ${BACKUP_DIR}/devstudios/${DEVSTUDIO}/flows_cred.json"
    kubectl --context ${CONTEXT} -n ${NAMESPACE} cp -c ${DEVSTUDIO} ${POD}:/data/flows_cred.json ${BACKUP_DIR}/devstudios/${DEVSTUDIO}/flows_cred.json || true
    echo "Writing file: ${BACKUP_DIR}/devstudios/${DEVSTUDIO}/.config.nodes.json"
    kubectl --context ${CONTEXT} -n ${NAMESPACE} cp -c ${DEVSTUDIO} ${POD}:/data/.config.nodes.json ${BACKUP_DIR}/devstudios/${DEVSTUDIO}/.config.nodes.json || true
    echo "Writing file: ${BACKUP_DIR}/devstudios/${DEVSTUDIO}/.config.projects.json"
    kubectl --context ${CONTEXT} -n ${NAMESPACE} cp -c ${DEVSTUDIO} ${POD}:/data/.config.projects.json ${BACKUP_DIR}/devstudios/${DEVSTUDIO}/.config.projects.json || true
    echo "Writing file: ${BACKUP_DIR}/devstudios/${DEVSTUDIO}/.config.users.json"
    kubectl --context ${CONTEXT} -n ${NAMESPACE} cp -c ${DEVSTUDIO} ${POD}:/data/.config.users.json ${BACKUP_DIR}/devstudios/${DEVSTUDIO}/.config.users.json || true
  done
}

function backup_namespace_notebooks() {
  NAMESPACE=$1
  NOW=$2
  BACKUP_DIR="backup-data/${NAMESPACE}/${NOW}"
  for NOTEBOOK in `kubectl --context ${CONTEXT} -n ${NAMESPACE} get notebooks -o json |  jq '.items[] | .metadata.name' | sed 's/"//g'` ; do
    echo "Backup notebook: ${NOTEBOOK}"
    mkdir -p ${BACKUP_DIR}/notebooks/${NOTEBOOK}/files
    echo "Writing file: ${BACKUP_DIR}/notebooks/${NOTEBOOK}/${NOTEBOOK}.yaml"
    kubectl --context ${CONTEXT} -n ${NAMESPACE} get notebooks ${NOTEBOOK} -o yaml | kubectl neat > ${BACKUP_DIR}/notebooks/${NOTEBOOK}/${NOTEBOOK}.yaml
    POD=$(kubectl --context ${CONTEXT} -n ${NAMESPACE} get pods -l notebook-name=${NOTEBOOK} -o json| jq -r '.items[].metadata.name')
    for FILE in `kubectl --context ${CONTEXT} -n ${NAMESPACE} exec -ti -c ${NOTEBOOK} ${POD} -- ls -1p | grep -v "/" | tr -d '\r'`; do
      echo "Writing file: ${BACKUP_DIR}/notebooks/${NOTEBOOK}/files/${FILE}"
      kubectl --context ${CONTEXT} -n ${NAMESPACE} cp -c ${NOTEBOOK} "${POD}:/home/jovyan/${FILE}" "${BACKUP_DIR}/notebooks/${NOTEBOOK}/files/${FILE}"
    done
  done
}


function compress_and_upload_namespace_backup() {
  NAMESPACE=$1
  NOW=$2
  BACKUP_DIR="backup-data/${NAMESPACE}/${NOW}"
  GS_URI="gs://${NAMESPACE}.${DOMAIN}/system-backup/${NOW}/${NAMESPACE}-${NOW}.tar"
  echo "Will backup upload backup into ${GS_URI}"
  #read -p "Proceed (y/n)?" choice
  #case "$choice" in
  #  y|Y ) echo "Proceeding";;
  #  * ) exit;;
  #esac
  TMP_DIR=$(mktemp -d)
  echo "temprorary folder ${TMP_DIR}"
  tar -cvf ${TMP_DIR}/${NAMESPACE}.tar -C ${BACKUP_DIR} .
  gsutil cp ${TMP_DIR}/${NAMESPACE}.tar ${GS_URI}
}



gcloud config set project ${PROJECT}
gcloud config set compute/zone ${ZONE}
kubectl ctx ${CONTEXT}

if [[ -z "${NAMESPACE}" ]]; then
  echo "error: namespace is not set, please use -n or --namespace to set the namespace, all for all namespaces" >&2
  exit 1
fi

NAMESPACES=( "${NAMESPACE}" )
if [[ "${NAMESPACE}" == "all" ]]; then
  NAMESPACES=`kubectl get profiles -o json |  jq '.items[] | .metadata.name' | sed 's/"//g'`
fi

for NAMESPACE in ${NAMESPACES[@]} ; do
  echo "Backup namespace: ${NAMESPACE}"
  kubectl ns ${NAMESPACE}
  NOW=$(date +"%Y-%m-%d")
  backup_namespace_profile ${NAMESPACE} ${NOW}
  backup_namespace_devices ${NAMESPACE} ${NOW}
  backup_namespace_devstudios ${NAMESPACE} ${NOW}
  backup_namespace_notebooks ${NAMESPACE} ${NOW}
  compress_and_upload_namespace_backup ${NAMESPACE} ${NOW}
done
