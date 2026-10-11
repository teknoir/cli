# shellcheck shell=bash
# Docker helpers. Every container and network the harness creates is labelled
# tn-k3sca=<runid> and named tn-k3sca-<short>-*; cleanup only ever removes
# objects carrying THIS run's label. No docker prune, no touching anything else
# (k3d-teknoir-dev in particular).

LABEL_KEY=tn-k3sca
CLEAN_IMAGE=${TN_CLEAN_IMAGE:-alpine:3.20}
K3S_IMAGE=${TN_K3S_IMAGE:-rancher/k3s}

docker_init() { # RUNID
  RUNID=$1
  SHORT=${RUNID##*-}
  LABEL="$LABEL_KEY=$RUNID"
  NET="tn-k3sca-$SHORT"
  PREFIX="tn-k3sca-$SHORT"
}

d_net_create() {
  docker network inspect "$NET" >/dev/null 2>&1 ||
    docker network create --label "$LABEL" "$NET" >/dev/null || die "cannot create network $NET"
}

# Remove this run's containers and networks. Safe to call repeatedly.
d_cleanup() {
  [ -n "${LABEL:-}" ] || return 0
  local ids
  ids=$(docker ps -aq --filter "label=$LABEL" 2>/dev/null) || true
  # shellcheck disable=SC2086
  [ -z "$ids" ] || docker rm -f -v $ids >/dev/null 2>&1 || true
  ids=$(docker network ls -q --filter "label=$LABEL" 2>/dev/null) || true
  # shellcheck disable=SC2086
  [ -z "$ids" ] || docker network rm $ids >/dev/null 2>&1 || true
}

# Remove a directory tree that may hold root-owned files, via a throwaway container.
d_rm_root() { # DIR...
  local d
  for d in "$@"; do
    [ -e "$d" ] || continue
    rm -rf "$d" 2>/dev/null && continue
    docker run --rm --label "$LABEL" --name "$PREFIX-rm-$RANDOM" -v "$(dirname "$d"):/p" \
      "$CLEAN_IMAGE" rm -rf "/p/$(basename "$d")" >/dev/null 2>&1 ||
      docker run --rm --label "$LABEL" -v "$(dirname "$d"):/p" "$(helper_image debian:bookworm)" \
        rm -rf "/p/$(basename "$d")" >/dev/null 2>&1 || true
    rm -rf "$d" 2>/dev/null || warn "could not remove $d"
  done
}

# helper_image BASE [PLATFORM]: a cached image BASE + openssl + ca-certificates (+ gawk/mawk as shipped).
# Built once, tagged tn-k3sca-helper:<base>[-<arch>], labelled tn-k3sca-cache=1 (images are
# a cache and survive runs; remove them with: docker image rm $(docker images -q 'tn-k3sca-helper'))
helper_image() {
  local base=$1 platform=${2:-} tag
  case $base in tn-k3sca-helper:*) printf '%s' "$base"; return 0 ;; esac
  tag="tn-k3sca-helper:$(printf '%s' "$base" | tr ':/' '--')${platform:+-$(printf '%s' "$platform" | tr '/' '-')}"
  if ! docker image inspect "$tag" >/dev/null 2>&1; then
    log "building helper image $tag" >&2
    docker build -q ${platform:+--platform "$platform"} --label tn-k3sca-cache=1 -t "$tag" - >&2 <<DOCKERFILE || die "cannot build $tag"
FROM $base
ENV DEBIAN_FRONTEND=noninteractive
RUN set -e; \
    apt-get update >/dev/null 2>&1 || true; \
    if ! apt-get install -y --no-install-recommends openssl ca-certificates >/dev/null 2>&1; then \
      echo "apt mirror stale, falling back to archive.debian.org"; \
      for f in /etc/apt/sources.list /etc/apt/sources.list.d/*; do \
        [ -f "\$f" ] || continue; \
        sed -i -e 's|deb.debian.org/debian-security|archive.debian.org/debian-security|g' -e 's|security.debian.org[^ ]*|archive.debian.org/debian-security|g' -e 's|deb.debian.org/debian|archive.debian.org/debian|g' -e '/-updates/d' "\$f"; \
      done; \
      echo 'Acquire::Check-Valid-Until "false";' > /etc/apt/apt.conf.d/99archive; \
      apt-get update; \
      apt-get install -y --no-install-recommends openssl ca-certificates; \
    fi; \
    rm -rf /var/lib/apt/lists/*
DOCKERFILE
  fi
  printf '%s' "$tag"
}

# helper_curl_image: helper_image debian:bookworm plus curl (the install library asks the server of an
# agent for its release with curl). Cached like the other helper images.
helper_curl_image() {
  local base tag=tn-k3sca-helper:debian-bookworm-curl
  base=$(helper_image debian:bookworm)
  if ! docker image inspect "$tag" >/dev/null 2>&1; then
    log "building helper image $tag" >&2
    docker build -q --label tn-k3sca-cache=1 -t "$tag" - >&2 <<DOCKERFILE || die "cannot build $tag"
FROM $base
ENV DEBIAN_FRONTEND=noninteractive
RUN apt-get update >/dev/null 2>&1; apt-get install -y --no-install-recommends curl && rm -rf /var/lib/apt/lists/*
DOCKERFILE
  fi
  printf '%s' "$tag"
}

# d_helper IMAGE [docker run args...] -- COMMAND...: run a short-lived labelled container as root.
d_helper() {
  local image=$1; shift
  local args=()
  while [ "$#" -gt 0 ] && [ "$1" != -- ]; do args+=("$1"); shift; done
  shift
  docker run --rm --label "$LABEL" --name "$PREFIX-h-$RANDOM$RANDOM" "${args[@]}" "$image" "$@"
}

# k3s container. Mirrors what the device templates run: raw hex token, traefik off.
# d_k3s_server NAME IMAGE_TAG WORKDIR HOSTNAME TOKEN [extra k3s args...]
d_k3s_server() {
  local name=$1 tag=$2 work=$3 host=$4 token=$5; shift 5
  local dd=()
  if [ "${DATA_CT:-/var/lib/rancher/k3s}" != /var/lib/rancher/k3s ]; then dd=(--data-dir "$DATA_CT"); fi
  docker run -d --label "$LABEL" --name "$name" --hostname "$host" --network "$NET" --network-alias "$host" \
    --privileged --tmpfs /run --tmpfs /var/run \
    -v "$work/k3s:${DATA_CT:-/var/lib/rancher/k3s}" -v "$work/etc-rancher:/etc/rancher" \
    "$K3S_IMAGE:$tag" server --token "$token" --disable traefik --tls-san "$host" \
    "${dd[@]}" "$@" >/dev/null
}

# d_k3s_agent NAME IMAGE_TAG WORKDIR HOSTNAME SERVER_HOST TOKEN [TRUST_BUNDLE]
d_k3s_agent() {
  local name=$1 tag=$2 work=$3 host=$4 srv=$5 token=$6 trust=${7:-}
  local extra=()
  if [ -n "$trust" ]; then extra=(-v "$trust:/tn/os-trust.pem:ro" -e SSL_CERT_FILE=/tn/os-trust.pem); fi
  docker run -d --label "$LABEL" --name "$name" --hostname "$host" --network "$NET" \
    --privileged --tmpfs /run --tmpfs /var/run \
    -v "$work/agent:/var/lib/rancher/k3s" \
    -e "K3S_URL=https://$srv:6443" -e "K3S_TOKEN=$token" "${extra[@]}" \
    "$K3S_IMAGE:$tag" agent >/dev/null
}

d_running() { [ "$(docker inspect -f '{{.State.Running}}' "$1" 2>/dev/null)" = true ]; }

d_pull() { # IMAGE: pull unless present
  docker image inspect "$1" >/dev/null 2>&1 || docker pull -q "$1" >/dev/null
}
