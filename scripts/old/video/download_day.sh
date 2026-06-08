#!/bin/sh
set -e
#set -x

POSITIONAL=()
while [[ $# -gt 0 ]]
do
key="$1"

case $key in
    -d|--day)
    DAY=$2
    shift # past argument
    shift # past value
    ;;
    -h|--help|*)
    echo "$0 -d(--day) <YYYY-MM-DD>"
    echo "\nSimple example:"
    echo "$0 -d 2025-07-08"
    echo ""
    echo ""
    exit 0
    ;;
esac
done

if [[ -z "${DAY}" ]]; then
  echo "Day is required. Use -d or --day to specify the day (YYYY-MM-DD)."
  exit 1
fi

# Pick a date (YYYY-MM-DD)
DEST=./videos_$DAY
mkdir -p "$DEST"

# Run for real:
gsutil -m rsync -r -x "^(?!.*$DAY).*" \
  gs://victra-poc.teknoir.cloud/media/videos "$DEST"

