#!/bin/sh
set -e

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
    echo "$0 -p(--projectid) <teknoir | teknoir-poc> -u(--user) <user email> -v(--viewer) <viewer> -e(--editor) <editor> -a(--admin) <admin> -s(--superadmin) <true|false>"
    echo "\t environment defaults to teknoir-poc"
    echo "\t viewer, editor, admin are strings of json arrays i,e, '[\"teknoir-retail\", \"teknoir-ai\", \"teknoir-dashboards\", \"prime-communications\", \"boxer-property\"]'"
    exit 0
    ;;
esac
done

export ZONE=us-central1-c
export GCP_PROJECT=$(if [ "$CONTEXT" == "gke_teknoir-poc_us-central1-c_teknoir-dev-cluster" ]; then echo "teknoir-poc"; else echo "teknoir"; fi)
export DOMAIN=$([ "$GCP_PROJECT" == 'teknoir' ] && echo "teknoir.cloud" || echo "teknoir.info")

gcloud config set project ${GCP_PROJECT}
gcloud config set compute/zone ${ZONE}

if [[ "${GCP_PROJECT}" == "teknoir" ]]; then
  export GOOGLE_CLOUD_PROJECT=teknoir
  export GOOGLE_APPLICATION_CREDENTIALS=/Volumes/GIT/ai/teknoir_scripts/teknoir-admin-credentials.json
else
  export GOOGLE_CLOUD_PROJECT=teknoir-poc
  export GOOGLE_APPLICATION_CREDENTIALS=/Volumes/GIT/ai/teknoir_scripts/teknoir-poc-admin-credentials.json
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
    user: ${EMAIL}
  name: user-${FIRST_NAME}-${LAST_NAME}-${NAMESPACE}-clusterrole-surveillance-viewer
  namespace: ${NAMESPACE}
spec:
  action: ALLOW
  selector:
    matchLabels:
      app.kubernetes.io/part-of: sdmc
  rules:
  - to:
    - operation:
        methods: ["GET", "PUT", "POST"]
        paths: ["/${NAMESPACE}/sdmc*"]
    when:
    - key: request.headers[X-Goog-Authenticated-User-Email]
      values:
      - securetoken.google.com/${GCP_PROJECT}:${EMAIL}
EOF
)
echo "${AUTHORIZATION_POLICY}" | kubectl --context ${CONTEXT} apply -f -

node create_surveillance_user.js "${EMAIL}" "${FULL_NAME}" "${PASSWORD}" "${NAMESPACE}"
