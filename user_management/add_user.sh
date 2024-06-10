#!/bin/bash
set -e

# If using Windows WSL use /bin/bash instead of sh

POSITIONAL=()
while [[ $# -gt 0 ]]
do
key="$1"

case $key in
    -c|--context)
    export CONTEXT="$2"
    shift # past argument
    shift # past value
    ;;
    -n|--namespace)
    export NAMESPACE="$2"
    shift # past argument
    shift # past value
    ;;
    -h|--help|*)
    echo "$0 -c(--context) <teknoir | teknoir-poc> -e(--email) <email> -n(--namespace) <namespace>"
    echo "\t GCP project defaults to teknoir"
    exit 0
    ;;
esac
done

export ZONE=us-central1-c
export GCP_PROJECT=$(if [ "$CONTEXT" == "gke_teknoir-poc_us-central1-c_teknoir-dev-cluster" ] || [ "$CONTEXT" == "teknoir-poc" ]; then echo "teknoir-poc"; else echo "teknoir"; fi)
export DOMAIN=$([ "$GCP_PROJECT" == 'teknoir' ] && echo "teknoir.cloud" || echo "teknoir.info")

gcloud config set project ${GCP_PROJECT}
gcloud config set compute/zone ${ZONE}

if [[ "${GCP_PROJECT}" == "teknoir" ]]; then
  export GOOGLE_CLOUD_PROJECT=teknoir
  export GOOGLE_APPLICATION_CREDENTIALS=/Volumes/GIT/ai/cli/teknoir-admin-credentials.json
else
  export GOOGLE_CLOUD_PROJECT=teknoir-poc
  export GOOGLE_APPLICATION_CREDENTIALS=/Volumes/GIT/ai/cli/teknoir-poc-admin-credentials.json
fi

NAME=$(echo "${EMAIL}" | cut -d '@' -f 1)
read -p "Enter your email: " EMAIL
read -p "Enter your first name: " FIRST_NAME
read -p "Enter your last name: " LAST_NAME
read -p "Enter users role (owner, editor, viewer): " ROLE
export FIRST_NAME_CAPITALIZED=$(echo "$FIRST_NAME" | awk '{print toupper(substr($0, 1, 1)) tolower(substr($0, 2))}')
export LAST_NAME_CAPITALIZED=$(echo "$LAST_NAME" | awk '{print toupper(substr($0, 1, 1)) tolower(substr($0, 2))}')
export FULL_NAME="$FIRST_NAME_CAPITALIZED $LAST_NAME_CAPITALIZED"
export PASSWORD=$(LC_ALL=C  tr -dc 'A-Za-z0-9!@#$%^&*()-_=+[]{}|;:,.<>?/' < /dev/urandom | head -c 12 ; echo)

echo "GCP_PROJECT        = ${GCP_PROJECT}"
echo "NAMESPACE          = ${NAMESPACE}"
echo "EMAIL              = ${EMAIL}"
echo "FULL_NAME          = ${FULL_NAME}"
echo "ROLE               = ${ROLE}"
echo "PASSWORD           = ${PASSWORD}"

warn()
{
    echo '[WARN] ' "$@" >&2
}

setup_user() {
  trap "exit" INT TERM ERR
  trap "kill 0" EXIT

  kubectl --context ${CONTEXT} -n teknoir port-forward $(kubectl --context ${CONTEXT} -n teknoir get pod -l kustomize.component=profiles -o name) 8081:8081 &

  # Get namespace owner email
  PROFILE_OWNER_EMAIL=$(kubectl --context ${CONTEXT} get profile ${NAMESPACE} -o jsonpath='{.spec.owner.name}')

  KFAM_BIND_BODY=$(cat <<EOF
  {
    "user": {
      "kind": "User",
      "name": "$(echo ${EMAIL} | tr '[:upper:]' '[:lower:]')"
    },
    "referredNamespace": "${NAMESPACE}",
    "RoleRef": {
      "kind": "ClusterRole",
      "name": "${ROLE}"
    }
  }
  EOF
  )
  echo "BODY:\n${KFAM_BIND_BODY}"

  sleep 5
  curl -v -X POST -H "Content-Type: application/json" -H "X-Goog-Authenticated-User-Email: securetoken.google.com/${GCP_PROJECT}:${PROFILE_OWNER_EMAIL}" -d "${KFAM_BIND_BODY//[$'\t\r\n ']}" http://localhost:8081/kfam/v1/bindings

  node create_user.js "${EMAIL}" "${FULL_NAME}" "${PASSWORD}" "${NAMESPACE}" "${ROLE}"
}

warn "Do you want to add \"${FULL_NAME}\" as a user to \"${NAMESPACE}\"? [yY]"
read REPLY

case ${REPLY} in
  [Yy]* )
    setup_user
    ;;
  * )
    info "Skipping..."
    ;;
esac