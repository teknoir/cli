#!/bin/sh
set -e
#set -x

DIR="."

POSITIONAL=()
while [[ $# -gt 0 ]]
do
key="$1"

case $key in
    -c|--camera)
    CAMERA="$2"
    shift # past argument
    shift # past value
    ;;
    -d|--dir)
    DIR=$2
    shift # past argument
    shift # past value
    ;;
    -h|--help|*)
    echo "$0 -c(--camera) <peripheral id> -d(--dir) <segment directory>"
    echo "\nSimple example:"
    echo "$0 -d /Users/anders/git/staged-event -c nc0009-front-door"
    echo ""
    echo ""
    exit 0
    ;;
esac
done

if [[ -z "${CAMERA}" ]]; then
  echo "Camera model is required. Use -c or --camera to specify the camera model."
  exit 1
fi

pushd "$DIR" || exit 1
find . -maxdepth 1 -type f -name "$CAMERA*.mp4" ! -name "*.tmp.mp4" -print0 | sort -z | while IFS= read -r -d '' f; do printf "file '%s'\n" "${f##*/}"; done > "${CAMERA}_file_list"

ffmpeg -f concat -safe 0 -i "${CAMERA}_file_list" -c copy "${CAMERA}_concat.mp4"

popd || exit 1