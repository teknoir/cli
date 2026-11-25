#!/usr/bin/env bash
# Real-sudo tests of tnctl's root wrapper (cmd/devexec_root.go): see README.md.
#
#   test/sudo-root/run.sh [go test flags]     for example: run.sh -v, run.sh -run 'TestSudoRig/pw'
#   test/sudo-root/run.sh --matrix [flags]    the four device systems of the README, one after the other
#   test/sudo-root/run.sh --clean             remove the images this script built (they are kept between runs)
#
# Builds a throwaway container with sshd and one login user per sudo state, runs the TestSudoRig
# tests of ./cmd against it with the real ssh client, and removes the container. Needs docker, go
# and ssh. The normal unit suite (scripts/test) does not need any of this: without the
# TN_SUDO_RIG_* variables the TestSudoRig tests are skipped.
#
#   TN_SUDO_RIG_BASE  the device system (default debian:trixie-slim, sudo 1.9.16)
#   TN_SUDO_RIG_SUDO  the package that provides sudo there (default sudo; sudo-rs with ubuntu:26.04)
set -euo pipefail

here=$(cd "$(dirname "$0")" && pwd)
repo=$(cd "$here/../.." && pwd)

if [ "${1:-}" = --clean ]; then
  # Not forced: an image a running rig still uses stays.
  images=$(docker images --format '{{.Repository}}:{{.Tag}}' 'tnctl-sudo-root-test')
  [ -z "$images" ] || docker image rm $images
  exit 0
fi

if [ "${1:-}" = --matrix ]; then
  shift
  rc=0
  for pair in debian:trixie-slim=sudo ubuntu:20.04=sudo ubuntu:22.04=sudo ubuntu:26.04=sudo-rs; do
    echo "== ${pair%=*} with ${pair#*=}"
    TN_SUDO_RIG_BASE=${pair%=*} TN_SUDO_RIG_SUDO=${pair#*=} "$0" "$@" || rc=$?
  done
  exit "$rc"
fi

base=${TN_SUDO_RIG_BASE:-debian:trixie-slim}
sudo=${TN_SUDO_RIG_SUDO:-sudo}
image=${TN_SUDO_RIG_IMAGE:-tnctl-sudo-root-test:$(printf '%s' "$base-$sudo" | tr ':/' '--')}
name=tnctl-sudo-root-$$

for tool in docker go ssh ssh-keygen; do
  command -v "$tool" >/dev/null || { echo "run.sh: $tool is needed" >&2; exit 2; }
done

work=$(mktemp -d "${TMPDIR:-/tmp}/tnctl-sudo-rig.XXXXXX")
cleanup() {
  docker rm -f "$name" >/dev/null 2>&1 || true
  rm -rf "$work"
}
trap cleanup EXIT

docker build -q --build-arg BASE="$base" --build-arg SUDO="$sudo" -t "$image" "$here" >/dev/null

ssh-keygen -q -t ed25519 -N '' -C tnctl-sudo-rig -f "$work/id"
# Every character a shell, printf or sudo could trip over, and a random part so that the
# "is the password in the output?" checks cannot match by accident.
password="tn-$(od -An -N8 -tx1 /dev/urandom | tr -d ' \n') 'q' \"d\" \$HOME \\n -n %s #"

docker run -d --name "$name" -p 127.0.0.1::22 \
  -e TN_PASSWORD="$password" -e TN_PUBKEY="$(cat "$work/id.pub")" "$image" >/dev/null

port=
for _ in $(seq 1 100); do
  port=$(docker port "$name" 22/tcp 2>/dev/null | sed -n 's/.*://p' | head -1) || port=
  # The host key is taken from the container itself, so ssh checks it strictly.
  if [ -n "$port" ] && key=$(docker exec "$name" cat /etc/ssh/ssh_host_ed25519_key.pub 2>/dev/null) &&
    docker exec "$name" sh -c 'kill -0 "$(cat /run/sshd.pid)"' 2>/dev/null; then
    echo "[127.0.0.1]:$port ${key% *}" > "$work/known_hosts"
    break
  fi
  if [ "$(docker inspect -f '{{.State.Running}}' "$name" 2>/dev/null)" != true ]; then
    echo "run.sh: the container stopped:" >&2
    docker logs "$name" >&2 || true
    exit 1
  fi
  sleep 0.2
done
[ -s "$work/known_hosts" ] || { echo "run.sh: sshd did not start" >&2; docker logs "$name" >&2; exit 1; }

cd "$repo"
TN_SUDO_RIG_CONTAINER=$name TN_SUDO_RIG_PORT=$port TN_SUDO_RIG_DIR=$work TN_SUDO_RIG_PASSWORD=$password \
  "${GO:-go}" test ./cmd -run '^TestSudoRig' -count=1 "$@"
