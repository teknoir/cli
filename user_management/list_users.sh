#!/bin/sh
set -e

# If using Windows WSL use /bin/bash instead of sh

function list_users {
  node list_users.js
}

function set_environment {
if [[ -z "${PROJECTID}" ]]; then
  export PROJECTID="teknoir-poc"
fi

if [[ "${PROJECTID}" == "teknoir" ]]; then
  export GOOGLE_CLOUD_PROJECT=teknoir
  export GOOGLE_APPLICATION_CREDENTIALS=$(pwd)/../teknoir-admin-credentials.json
else
  export GOOGLE_CLOUD_PROJECT=teknoir-poc
  export GOOGLE_APPLICATION_CREDENTIALS=$(pwd)/../teknoir-poc-admin-credentials.json
fi
}

POSITIONAL=()
while [[ $# -gt 0 ]]
do
key="$1"

case $key in
    -p|--projectid)
    PROJECTID="$2"
    shift # past argument
    shift # past value
    ;;
    -h|--help|*)
    echo "$0 -p(--projectid) <teknoir | teknoir-poc>"
    echo "\t environment defaults to teknoir-poc"
    exit 0
    ;;
esac
done

echo "ENVIRONMENT     = ${ENVIRONMENT}"

set_environment
list_users