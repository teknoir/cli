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
    -e|--email)
    export EMAIL="$2"
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
  export GOOGLE_APPLICATION_CREDENTIALS=/home/cris/work/teknoir/teknoir-admin-credentials.json
else
  export GOOGLE_CLOUD_PROJECT=teknoir-poc
  export GOOGLE_APPLICATION_CREDENTIALS=/home/cris/work/teknoir/teknoir-poc-admin-credentials.json
fi

NAME=$(echo "${EMAIL}" | cut -d '@' -f 1)
export FIRST_NAME=$(echo "$NAME" | cut -d '.' -f 1)
export LAST_NAME=$(echo "$NAME" | cut -d '.' -f 2)
export FIRST_NAME_CAPITALIZED=$(echo "$FIRST_NAME" | awk '{print toupper(substr($0, 1, 1)) tolower(substr($0, 2))}')
export LAST_NAME_CAPITALIZED=$(echo "$LAST_NAME" | awk '{print toupper(substr($0, 1, 1)) tolower(substr($0, 2))}')
export FULL_NAME="$FIRST_NAME_CAPITALIZED $LAST_NAME_CAPITALIZED"
export PASSWORD=$(LC_ALL=C  tr -dc 'A-Za-z0-9!@#$%^&*()-_=+[]{}|;:,.<>?/' < /dev/urandom | head -c 12 ; echo)

echo "GCP_PROJECT        = ${GCP_PROJECT}"
echo "NAMESPACE          = ${NAMESPACE}"
echo "EMAIL              = ${EMAIL}"
echo "FULL_NAME          = ${FULL_NAME}"
echo "PASSWORD           = ${PASSWORD}"

AUTHORIZATION_POLICY=$(cat <<EOF
---
apiVersion: security.istio.io/v1
kind: AuthorizationPolicy
metadata:
  annotations:
    role: surveillance-viewer
    user: $(echo ${EMAIL} | tr '[:upper:]' '[:lower:]')
  name: user-$(echo ${FIRST_NAME} | tr '[:upper:]' '[:lower:]')-$(echo ${LAST_NAME} | tr '[:upper:]' '[:lower:]')-$(echo ${NAMESPACE} | tr '[:upper:]' '[:lower:]')-clusterrole-surveillance-viewer
  namespace: ${NAMESPACE}
spec:
  action: ALLOW
  rules:
  - to:
    - operation:
        methods: ["GET", "PUT", "POST"]
        paths: [
          "/${NAMESPACE}/sdmc*", 
          "/${NAMESPACE}/ws*",
          "/${NAMESPACE}/media-service*",
          "/retrieve_events*",
          "/events*",
          "/feedbacks*",
          "/notifications*"
          ]
    when:
    - key: request.headers[X-Goog-Authenticated-User-Email]
      values:
      - securetoken.google.com/${GCP_PROJECT}:$(echo ${EMAIL} | tr '[:upper:]' '[:lower:]')
EOF
)

echo "${AUTHORIZATION_POLICY}" | kubectl --context ${CONTEXT} apply -f -


node create_surveillance_user.js "${EMAIL}" "${FULL_NAME}" "${PASSWORD}" "${NAMESPACE}"
