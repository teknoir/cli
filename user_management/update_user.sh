#!/bin/sh
set -e

function update_user {
  node update_user.js $1 $2 $3 $4 $5
}

function set_environment {
if [[ -z "${PROJECTID}" ]]; then
  export PROJECTID="teknoir-poc"
fi

if [[ "${PROJECTID}" == "teknoir" ]]; then
  export GOOGLE_CLOUD_PROJECT=teknoir
  export GOOGLE_APPLICATION_CREDENTIALS=/Volumes/GIT/ai/teknoir_scripts/teknoir-admin-credentials.json
else
  export GOOGLE_CLOUD_PROJECT=teknoir-poc
  export GOOGLE_APPLICATION_CREDENTIALS=/Volumes/GIT/ai/teknoir_scripts/teknoir-poc-admin-credentials.json
fi

if [[ -z "${VIEWER}" ]]; then
  export VIEWER="[]"
fi

if [[ -z "${EDITOR}" ]]; then
  export EDITOR="[]"
fi

if [[ -z "${ADMIN}" ]]; then
  export ADMIN="[]"
fi

if [[ -z "${SUPERADMIN}" ]]; then
  export SUPERADMIN="false"
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
    -u|--user)
    USER="$2"
    shift # past argument
    shift # past value
    ;;
    -v|--viewer)
    VIEWER="$2"
    shift # past argument
    shift # past value
    ;;
    -e|--editor)
    EDITOR="$2"
    shift # past argument
    shift # past value
    ;;
    -a|--admin)
    ADMIN="$2"
    shift # past argument
    shift # past value
    ;;
    -s|--superadmin)
    SUPERADMIN="$2"
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

set_environment
echo "PROJECTID        = ${PROJECTID}"
echo "USER             = ${USER}"
update_user ${USER} ${VIEWER} ${EDITOR} ${ADMIN} ${SUPERADMIN}