#!/bin/sh
set -e

function list_users {
  node list_users.js
}

function set_environment {
if [[ -z "${ENVIRONMENT}" ]]; then
  export ENVIRONMENT="teknoir-poc"
fi

if [[ "${ENVIRONMENT}" == "teknoir" ]]; then
  export GOOGLE_CLOUD_PROJECT=teknoir
  export GOOGLE_APPLICATION_CREDENTIALS=/Volumes/GIT/ai/teknoir_scripts/teknoir-admin-credentials.json
else
  export GOOGLE_CLOUD_PROJECT=teknoir-poc
  export GOOGLE_APPLICATION_CREDENTIALS=/Volumes/GIT/ai/teknoir_scripts/teknoir-poc-admin-credentials.json
fi
}

POSITIONAL=()
while [[ $# -gt 0 ]]
do
key="$1"

case $key in
    -e|--environment)
    ENVIRONMENT="$2"
    shift # past argument
    shift # past value
    ;;
    -h|--help|*)
    echo "$0 -e(--environment) <teknoir | teknoir-poc>"
    echo "\t environment defaults to teknoir-poc"
    exit 0
    ;;
esac
done

echo "ENVIRONMENT     = ${ENVIRONMENT}"

set_environment
list_users