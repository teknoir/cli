#!/usr/bin/env bash
# Real-sudo tests of tnctl's root wrapper (cmd/devexec_root.go): see README.md.
#
#   test/sudo-root/run.sh [go test flags]     for example: run.sh -v, run.sh -run 'TestSudoRig/pw'
#
# Builds a throwaway Debian container with sshd and one login user per sudo state and login
# shell, runs the TestSudoRig tests of ./cmd against it with the real ssh client, and removes
# the container. Needs docker, go and ssh. The normal unit suite (scripts/test) does not need
# any of this: without the TN_SUDO_RIG_* variables the TestSudoRig tests are skipped.
set -euo pipefail

here=$(cd "$(dirname "$0")" && pwd)
repo=$(cd "$here/../.." && pwd)
image=${TN_SUDO_RIG_IMAGE:-tnctl-sudo-root-test:local}
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

docker build -q -t "$image" "$here" >/dev/null

ssh-keygen -q -t ed25519 -N '' -C tnctl-sudo-rig -f "$work/id"
# Every character a shell, printf or sudo could trip over, and a random part so that the
# "is the password in the output?" checks cannot match by accident.
password="tn-$(od -An -N8 -tx1 /dev/urandom | tr -d ' \n') 'q' \"d\" \$HOME \\n -n %s #"

docker run -d --name "$name" -p 127.0.0.1::22 \
  -e TN_PASSWORD="$password" -e TN_PUBKEY="$(cat "$work/id.pub")" "$image" >/dev/null

port=
for _ in $(seq 1 100); do
  port=$(docker port "$name" 22/tcp 2>/dev/null | sed -n 's/.*://p' | head -1)
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
