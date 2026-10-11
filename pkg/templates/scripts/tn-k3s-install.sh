#!/bin/sh
# tn-k3s-install.sh - what a Teknoir bootstrap script decides before it runs the k3s installer:
# which k3s release (a re-run keeps what is installed), and whether the kubelet must be told that
# cgroup v1 is acceptable. Used by the server and the agent script (sourced), next to
# tn-k3s-ca.sh (the server only).
#
#   . tn-k3s-install.sh; tn_inst_prepare server|agent       (once, right before the installer)
#   tn_inst_config_ensure FILE KEY TEXT                      (config.yaml without clobbering it)
#
# POSIX sh. It runs under `set -e` and `set -o noglob` (as the bootstrap scripts do) and does not
# change shell options, traps or the working directory of the caller. Sourcing defines functions only.
#
# ---------------------------------------------------------------- version policy
# A bootstrap script is meant to be re-run (it repairs the CA, refreshes the manifests). It must not
# upgrade k3s as a side effect: the pin in the script (INSTALL_K3S_VERSION, `tnctl bootstrap
# --k3s-version`) is the release for a NEW device.
#   - k3s is not installed:   the pin is installed (a SERVER; an agent: see below).
#   - the version cannot be read although a k3s binary or DATA_DIR/server/db exists: REFUSED (the
#                             installer would install or replace k3s blind), unless TN_K3S_FORCE=true.
#   - k3s is installed:       its version is KEPT. The exact installed release is handed to the
#                             installer (which then only rewrites the unit and the symlinks), and
#                             a different pin is reported and ignored.
#   - TN_K3S_UPGRADE=true     (tnctl bootstrap --k3s-upgrade) lets the pin replace the installed
#                             release. A pin below the installed release is refused (k3s does not
#                             downgrade: the datastore has been migrated), and so is a jump of more
#                             than one minor version (v1.34 -> v1.36) unless TN_K3S_FORCE=true
#                             (tnctl bootstrap --force): Kubernetes supports one minor at a time.
#                             A channel (stable, latest) cannot be compared before the installer
#                             resolves it: that is only a warning, pin an exact release to get the checks.
#
# An AGENT follows its server. A kubelet must never be newer than the apiserver it joins, so a new
# agent (and an agent that TN_K3S_UPGRADE=true replaces) does not install the pin of the script: it
# reads the release of the server and installs exactly that one. Where the release comes from: k3s
# does NOT answer /version to an anonymous caller (its apiserver runs with anonymous-auth=false: 401,
# verified with v1.30, v1.34 and v1.36), and the join token is only good for the supervisor
# endpoints. So the Teknoir SERVER script publishes the release it runs (tn_inst_publish_release) as
# <DATA_DIR>/server/static/teknoir/k3s-version, which k3s serves without authentication at
#     https://<K3S_URL>/static/teknoir/k3s-version
# and the agent reads that file (then, for a server that allows anonymous access, /version). The
# pin only counts when the person who runs the script asked for it (`tnctl bootstrap --k3s-version`, or
# INSTALL_K3S_VERSION in the environment; TN_K3S_PIN_SOURCE=flag|env, the template sets it): then it is
# installed if it is not newer than the server (older gets a warning), and refused if it is. When the
# server's release cannot be read (down, unreachable, bootstrapped by a script that does not publish it
# yet): an explicit exact pin is installed with a warning, and otherwise the script REFUSES, unless
# TN_K3S_FORCE=true. An OS image build (OS_BUILD) cannot reach the server and installs the pin. The
# cgroup logic below is keyed on the release that is installed.
#
# ---------------------------------------------------------------- cgroup v1
# Kubernetes >= 1.35: the kubelet refuses to start on a cgroup v1 host (failCgroupV1 defaults to
# true) and k3s does not change that. Jetson L4T r32/r35 and Ubuntu 18.04/20.04 are cgroup v1. When
# the host is on cgroup v1 (stat -fc %T /sys/fs/cgroup is not cgroup2fs), or cannot be told (an OS
# build runs elsewhere than the device), and the k3s that will run is >= v1.31 (the kubelet knows
# the flag since 1.31; it only matters from 1.35, but a later upgrade, by hand or by the k3s
# installer alone, must not stop the kubelet), the script writes (atomically, only when it differs)
#     /etc/rancher/k3s/config.yaml.d/99-teknoir-cgroupv1.yaml:   kubelet-arg+: ["fail-cgroupv1=false"]
# `kubelet-arg+` APPENDS to the kubelet-arg of config.yaml and the other files (verified with
# rancher/k3s v1.36.5, a kubelet-arg already in config.yaml is kept); the flag is harmless on a
# cgroup v2 host. A fail-cgroupv1 that the device sets itself is respected. cgroup v1 is
# deprecated upstream: move the device to cgroup v2 (kernel command line
# systemd.unified_cgroup_hierarchy=1, or a newer OS release).
#
# Environment (all optional):
#   INSTALL_K3S_VERSION, INSTALL_K3S_CHANNEL   the pin of the script (read, and set to what the installer must get)
#   TN_K3S_UPGRADE      true: the pin may replace an installed k3s
#   TN_K3S_FORCE        true: allow an upgrade across more than one minor version; an agent newer than its
#                       server, or without knowing the version of its server; a k3s whose version cannot be read
#   TN_K3S_PIN_SOURCE   who chose INSTALL_K3S_VERSION: flag / env (the person who runs the script) or
#                       default (the template); the template sets it, an agent follows the server unless it is flag/env
#   K3S_URL             the server of an agent (https://host:6443)
#   TN_K3S_SERVER_VERSION_TRIES, TN_K3S_SERVER_VERSION_WAIT   tries (3) and seconds between them (3) to reach the
#                       server; a server that answers without a release is not asked again
#   DATA_DIR            the k3s data dir (/var/lib/rancher/k3s), where a datastore says that k3s is there
#   TN_K3S_CGROUPV1     true / false: do not detect, take this as the answer
#   TN_CGROUP_FSTYPE    the file system type of /sys/fs/cgroup, instead of `stat` (tests)
#   K3S_BIN             the k3s command used to read the installed version (k3s, then /usr/local/bin/k3s)
#   TN_K3S_CONFIG_DIR   /etc/rancher/k3s
#   OS_BUILD            set by the OS image builds: the host is not the device, cgroup cannot be detected

TN_K3S_INSTALL_LIB_VERSION=2
export TN_K3S_INSTALL_LIB_VERSION

# ----------------------------------------------------------------- messages
# Own functions, in the format of header.sh.tmpl: a caller's info/warn/fatal may not exist, and `info`
# can be the GNU info reader.
tn_inst_info() { echo '[INFO] ' "$@"; }
tn_inst_warn() { echo '[WARN] ' "$@" >&2; }
tn_inst_fatal() { echo '[ERROR] ' "$@" >&2; exit 1; }

# tn_inst_state_note: how a refusal describes the device. This library has changed nothing when it
# refuses; the CA step (tn-k3s-ca.sh) may have, earlier in the same run, and then says exactly what.
tn_inst_state_note() {
  if [ -n "${TN_K3S_CA_CHANGED:-}" ]; then
    printf 'The k3s release was not changed, but earlier in this run: %s' "$TN_K3S_CA_CHANGED"
  else
    printf 'Nothing was changed'
  fi
}

# ----------------------------------------------------------------- versions

# tn_inst_parse VERSION: sets TN_INST_MAJ TN_INST_MIN TN_INST_PAT TN_INST_REV from vMAJ.MIN.PAT[+k3sREV]
# (REV 0 when there is none). Fails for anything else (stable, latest, a commit).
tn_inst_parse() {
  TN_INST_MAJ=; TN_INST_MIN=; TN_INST_PAT=; TN_INST_REV=
  _tn_pv=$(printf '%s\n' "$1" | sed -n 's/^v\([0-9][0-9]*\)\.\([0-9][0-9]*\)\.\([0-9][0-9]*\)\(+k3s\([0-9][0-9]*\)\)\{0,1\}$/\1 \2 \3 \5/p')
  [ -n "$_tn_pv" ] || return 1
  IFS=' ' read -r TN_INST_MAJ TN_INST_MIN TN_INST_PAT TN_INST_REV <<EOV
$_tn_pv
EOV
  TN_INST_REV=${TN_INST_REV:-0}
}

# tn_inst_cmp A B: sets TN_INST_CMP to -1, 0 or 1 (A older, equal, newer than B). Fails if either
# is not an exact release.
tn_inst_cmp() {
  tn_inst_parse "$1" || return 1
  _tn_a="$TN_INST_MAJ $TN_INST_MIN $TN_INST_PAT $TN_INST_REV"
  tn_inst_parse "$2" || return 1
  _tn_b="$TN_INST_MAJ $TN_INST_MIN $TN_INST_PAT $TN_INST_REV"
  TN_INST_CMP=$(printf '%s\n%s\n' "$_tn_a" "$_tn_b" | awk 'NR == 1 { for (i = 1; i <= 4; i++) a[i] = $i } NR == 2 {
    for (i = 1; i <= 4; i++) { if (a[i] + 0 < $i + 0) { print -1; exit } if (a[i] + 0 > $i + 0) { print 1; exit } } print 0 }')
}

# the k3s command that reads the installed version
tn_inst_k3s_cmd() {
  if [ -n "${K3S_BIN:-}" ]; then echo "$K3S_BIN"
  elif command -v k3s >/dev/null 2>&1; then echo k3s
  elif [ -x /usr/local/bin/k3s ]; then echo /usr/local/bin/k3s
  else echo k3s; fi
}

# tn_inst_installed_version: sets TN_INST_INSTALLED to the release of the installed k3s
# (v1.34.11+k3s1) or to nothing when there is none.
tn_inst_installed_version() {
  TN_INST_INSTALLED=$($(tn_inst_k3s_cmd) --version 2>/dev/null | sed -n '1s/^.*version \(v[0-9][0-9]*\.[0-9][0-9]*\.[0-9][0-9]*\(+k3s[0-9][0-9]*\)\{0,1\}\).*$/\1/p') || TN_INST_INSTALLED=
}

# tn_inst_k3s_present: 0 if there is a k3s binary or a server datastore, whatever its version is.
# Sets TN_INST_PRESENT to what was found.
tn_inst_k3s_present() {
  TN_INST_PRESENT=
  _tn_first=
  for _tn_w in $(tn_inst_k3s_cmd); do _tn_first=$_tn_w; break; done
  if [ -n "$_tn_first" ] && _tn_found=$(command -v "$_tn_first" 2>/dev/null) && [ -n "$_tn_found" ]; then
    TN_INST_PRESENT="the k3s command $_tn_found"; return 0
  fi
  if [ -d "${DATA_DIR:-/var/lib/rancher/k3s}/server/db" ]; then
    TN_INST_PRESENT="the datastore ${DATA_DIR:-/var/lib/rancher/k3s}/server/db"; return 0
  fi
  return 1
}

# tn_inst_http_get URL: the body on stdout. Returns 0 for 2xx, 1 when the server ANSWERED with an error
# (404: no such file, 401: no anonymous access), 2 when it could not be reached (or there is neither curl
# nor wget). No certificate check: the answer decides which release to install and nothing else (the
# server is the one this device joins with its token anyway).
tn_inst_http_get() {
  _tn_hc=0
  if command -v curl >/dev/null 2>&1; then
    curl -fsk --max-time 10 "$1" || _tn_hc=$?
    case $_tn_hc in 0) return 0 ;; 22) return 1 ;; *) return 2 ;; esac
  elif command -v wget >/dev/null 2>&1; then
    wget -qO- --no-check-certificate -T 10 "$1" || _tn_hc=$?
    case $_tn_hc in 0) return 0 ;; 8) return 1 ;; *) return 2 ;; esac
  fi
  return 2
}

# tn_inst_server_version URL: sets TN_INST_SERVER to the k3s release the server at URL runs
# (v1.34.11+k3s1) and TN_INST_SERVER_SRC to where it was read, or leaves them empty (and sets
# TN_INST_SERVER_WHY to unreachable or no-release). Fails when it cannot tell. Two places, in this order:
#   <URL>/static/teknoir/k3s-version   the file that the Teknoir server script publishes (anonymous)
#   <URL>/version                      gitVersion, for a server that allows anonymous access (k3s does not by default)
tn_inst_server_version() {
  TN_INST_SERVER=; TN_INST_SERVER_SRC=; TN_INST_SERVER_WHY=unreachable
  _tn_su=${1%/}
  _tn_sv_max=${TN_K3S_SERVER_VERSION_TRIES:-3}
  _tn_sv_try=0
  while [ "$_tn_sv_try" -lt "$_tn_sv_max" ]; do
    _tn_sv_try=$((_tn_sv_try + 1))
    _tn_unreach=0
    _tn_rc=0
    _tn_body=$(tn_inst_http_get "$_tn_su/static/teknoir/k3s-version" 2>/dev/null) || _tn_rc=$?
    if [ "$_tn_rc" = 0 ]; then
      TN_INST_SERVER=$(printf '%s' "$_tn_body" | tr -d ' \t\r\n' | sed -n 's/^\(v[0-9][0-9]*\.[0-9][0-9]*\.[0-9][0-9]*+k3s[0-9][0-9]*\)$/\1/p')
      if [ -n "$TN_INST_SERVER" ]; then TN_INST_SERVER_SRC=$_tn_su/static/teknoir/k3s-version; return 0; fi
    elif [ "$_tn_rc" = 2 ]; then
      _tn_unreach=1
    fi
    if [ "$_tn_unreach" = 0 ]; then
      _tn_rc=0
      _tn_body=$(tn_inst_http_get "$_tn_su/version" 2>/dev/null) || _tn_rc=$?
      if [ "$_tn_rc" = 0 ]; then
        # {"major":"1","minor":"34","gitVersion":"v1.34.11+k3s1",...}, over several lines or one
        TN_INST_SERVER=$(printf '%s\n' "$_tn_body" | tr -d '\n' | sed -n 's/^.*"gitVersion"[[:space:]]*:[[:space:]]*"\(v[0-9][0-9]*\.[0-9][0-9]*\.[0-9][0-9]*+k3s[0-9][0-9]*\)".*$/\1/p')
        if [ -n "$TN_INST_SERVER" ]; then TN_INST_SERVER_SRC=$_tn_su/version; return 0; fi
      elif [ "$_tn_rc" = 2 ]; then
        _tn_unreach=1
      fi
    fi
    # the server answered, but has no release to give: asking again does not help
    if [ "$_tn_unreach" = 0 ]; then TN_INST_SERVER_WHY=no-release; return 1; fi
    if [ "$_tn_sv_try" -lt "$_tn_sv_max" ]; then sleep "${TN_K3S_SERVER_VERSION_WAIT:-3}"; fi
  done
  return 1
}

# tn_inst_publish_release: a SERVER script calls it after the k3s installer. Publishes the installed
# release as <DATA_DIR>/server/static/teknoir/k3s-version; k3s serves server/static without
# authentication (https://<server>:6443/static/teknoir/k3s-version), and an agent script reads it to
# install the release of its server. Written atomically; unchanged when it is right; it never fails the
# script. An out-of-band upgrade of k3s leaves it saying the older release, which is the safe direction
# (an agent may be older than its server, never newer): a re-run of the server script updates it.
tn_inst_publish_release() {
  tn_inst_installed_version
  if [ -z "$TN_INST_INSTALLED" ]; then
    tn_inst_warn "cannot read the release of the installed k3s: it is not published, so agents cannot follow this server (they need INSTALL_K3S_VERSION)"
    return 0
  fi
  _tn_pd=${DATA_DIR:-/var/lib/rancher/k3s}/server/static/teknoir
  _tn_pf=$_tn_pd/k3s-version
  if [ -f "$_tn_pf" ] && [ "$(cat "$_tn_pf" 2>/dev/null)" = "$TN_INST_INSTALLED" ]; then
    tn_inst_info "the k3s release $TN_INST_INSTALLED is already published for the agents ($_tn_pf)"
    return 0
  fi
  _tn_pt=$_tn_pd/.k3s-version.tmp.$$
  if mkdir -p "$_tn_pd" && printf '%s\n' "$TN_INST_INSTALLED" > "$_tn_pt" && mv -f "$_tn_pt" "$_tn_pf"; then
    tn_inst_info "Published the k3s release $TN_INST_INSTALLED for the agents: $_tn_pf (served at /static/teknoir/k3s-version)"
  else
    rm -f "$_tn_pt" 2>/dev/null || true
    tn_inst_warn "cannot publish the k3s release to $_tn_pf: agents cannot follow this server (they need INSTALL_K3S_VERSION)"
  fi
  return 0
}

# ----------------------------------------------------------------- the release to install

# tn_inst_agent_target: for an agent that is to be installed (or replaced): which release. Sets and
# exports INSTALL_K3S_VERSION (and unsets INSTALL_K3S_CHANNEL) or exits through tn_inst_fatal.
tn_inst_agent_target() {
  _tn_pin=${INSTALL_K3S_VERSION:-}
  _tn_exp=0
  case ${TN_K3S_PIN_SOURCE:-default} in flag|env) _tn_exp=1 ;; esac
  if [ -n "${OS_BUILD:-}" ]; then
    tn_inst_warn "OS build: the k3s server is not reachable from here, so the release of the agent cannot follow it. ${_tn_pin:-The release the installer picks} is installed: the server must run this release or a newer one"
    return 0
  fi
  TN_INST_SERVER=
  if [ -z "${K3S_URL:-}" ]; then
    tn_inst_warn "K3S_URL is not set: the k3s release of the server cannot be read"
  else
    tn_inst_info "Reading the k3s release of the server at ${K3S_URL%/}"
    tn_inst_server_version "$K3S_URL" || true
  fi
  if [ -n "$TN_INST_SERVER" ]; then
    tn_inst_info "the k3s server at $K3S_URL runs $TN_INST_SERVER (read from $TN_INST_SERVER_SRC)"
    if [ "$_tn_exp" = 1 ] && [ -n "$_tn_pin" ] && tn_inst_cmp "$_tn_pin" "$TN_INST_SERVER"; then
      case $TN_INST_CMP in
        0) tn_inst_info "the requested release $_tn_pin is the release of the server" ;;
        -1) tn_inst_warn "the requested release $_tn_pin is OLDER than the release of the server ($TN_INST_SERVER): it is installed as asked. An agent should run the release of its server (Kubernetes supports a kubelet up to three minor versions older than the apiserver)" ;;
        *) if [ "${TN_K3S_FORCE:-}" = true ]; then
             tn_inst_warn "the requested release $_tn_pin is NEWER than the release of the server ($TN_INST_SERVER); TN_K3S_FORCE=true: it is installed anyway. A kubelet newer than its apiserver is not supported"
           else
             tn_inst_fatal "refusing to install k3s $_tn_pin on this agent: the server at $K3S_URL runs the older $TN_INST_SERVER, and a kubelet must not be newer than the apiserver it joins. Without --k3s-version (INSTALL_K3S_VERSION) the agent gets the release of its server, $TN_INST_SERVER. Or upgrade the server first, or set TN_K3S_FORCE=true. $(tn_inst_state_note)"
           fi ;;
      esac
      return 0
    fi
    if [ "$_tn_exp" = 1 ]; then
      tn_inst_warn "the requested k3s release (${_tn_pin:-${INSTALL_K3S_CHANNEL:-stable}}) is not an exact release, so it cannot be compared with the server: the release of the server is installed instead"
    elif [ -n "$_tn_pin" ] && [ "$_tn_pin" != "$TN_INST_SERVER" ]; then
      tn_inst_info "the script's default $_tn_pin is not used: an agent runs the release of its server"
    fi
    INSTALL_K3S_VERSION=$TN_INST_SERVER
    export INSTALL_K3S_VERSION
    unset INSTALL_K3S_CHANNEL
    tn_inst_info "the agent gets the release of its server: $TN_INST_SERVER"
    return 0
  fi
  # the server cannot be asked
  if [ "$_tn_exp" = 1 ] && [ -n "$_tn_pin" ] && tn_inst_parse "$_tn_pin"; then
    tn_inst_warn "cannot read the k3s release of the server at ${K3S_URL:-(K3S_URL is not set)}: the requested $_tn_pin is installed as asked. The server must run this release or a newer one"
    return 0
  fi
  if [ "${TN_K3S_FORCE:-}" = true ]; then
    tn_inst_warn "cannot read the k3s release of the server at ${K3S_URL:-(K3S_URL is not set)}; TN_K3S_FORCE=true: ${_tn_pin:-the release the installer picks} is installed anyway. If the server runs an older release, this agent will not work"
    return 0
  fi
  case ${TN_INST_SERVER_WHY:-unreachable} in
    no-release) _tn_why="The server answered, but does not publish its release: it was bootstrapped by a script that does not yet (run the server script again: it keeps k3s as it is and publishes its release), and k3s does not allow an anonymous /version." ;;
    *) _tn_why="Is the server up and reachable from this device? Start it and run this script again." ;;
  esac
  tn_inst_fatal "cannot read the k3s release of the server at ${K3S_URL:-(K3S_URL is not set)} (${K3S_URL:-https://<server>:6443}/static/teknoir/k3s-version): an agent must not run a newer k3s than its server, so this script does not guess. $_tn_why Or say which release the server runs: INSTALL_K3S_VERSION=v1.xx.y+k3sN (tnctl bootstrap --k3s-version), or set TN_K3S_FORCE=true to install ${_tn_pin:-the release the installer picks} anyway. $(tn_inst_state_note)"
}

# tn_inst_plan: applies the policy at the top. Reads INSTALL_K3S_VERSION / INSTALL_K3S_CHANNEL,
# sets and exports them for the installer. Sets TN_INST_INSTALLED, TN_INST_ACTION
# (install|same|keep|upgrade) and TN_INST_EFFECTIVE (the exact release that will run, or nothing
# when only a channel is known). Exits through tn_inst_fatal when it refuses.
tn_inst_plan() {
  tn_inst_installed_version
  if [ -z "$TN_INST_INSTALLED" ] && tn_inst_k3s_present; then
    if [ "${TN_K3S_FORCE:-}" = true ]; then
      tn_inst_warn "$TN_INST_PRESENT exists, but its k3s version cannot be read; TN_K3S_FORCE=true: going on as if k3s were not installed. The installer may replace it with ${INSTALL_K3S_VERSION:-${INSTALL_K3S_CHANNEL:-the release the installer picks}}, without the upgrade and downgrade checks"
    else
      tn_inst_fatal "$TN_INST_PRESENT exists, but its k3s version cannot be read ('$(tn_inst_k3s_cmd) --version' gave no release), so this script cannot tell whether the installer would upgrade it, downgrade it or break its datastore, and does not install anything. Look at '$(tn_inst_k3s_cmd) --version' and at the datastore; repair or remove k3s on purpose, or set TN_K3S_FORCE=true to install anyway. $(tn_inst_state_note)"
    fi
  fi
  if [ "${TN_INST_ROLE:-server}" = agent ] && { [ -z "$TN_INST_INSTALLED" ] || [ "${TN_K3S_UPGRADE:-}" = true ]; }; then
    tn_inst_agent_target
  fi
  _tn_t=${INSTALL_K3S_VERSION:-}
  _tn_want=${_tn_t:-${INSTALL_K3S_CHANNEL:-the default channel of the k3s installer (stable)}}
  TN_INST_ACTION=install
  TN_INST_EFFECTIVE=
  if [ -z "$TN_INST_INSTALLED" ]; then
    TN_INST_EFFECTIVE=$_tn_t
    if [ -n "$_tn_t" ]; then tn_inst_info "k3s is not installed: installing $_tn_t"; else tn_inst_info "k3s is not installed: installing $_tn_want"; fi
    return 0
  fi
  tn_inst_info "k3s $TN_INST_INSTALLED is installed on this device"

  if [ "${TN_K3S_UPGRADE:-}" != true ]; then
    # keep: an exact release is not "newer", it is "this one"
    TN_INST_EFFECTIVE=$TN_INST_INSTALLED
    if [ "$_tn_t" = "$TN_INST_INSTALLED" ]; then
      TN_INST_ACTION=same
      tn_inst_info "k3s $TN_INST_INSTALLED is the requested release: it is installed again, nothing is upgraded"
      return 0
    fi
    TN_INST_ACTION=keep
    INSTALL_K3S_VERSION=$TN_INST_INSTALLED
    export INSTALL_K3S_VERSION
    unset INSTALL_K3S_CHANNEL
    if tn_inst_cmp "$_tn_t" "$TN_INST_INSTALLED" && [ "$TN_INST_CMP" = -1 ]; then
      tn_inst_warn "the requested k3s release $_tn_t is OLDER than the installed $TN_INST_INSTALLED: k3s cannot be downgraded, $TN_INST_INSTALLED is kept"
    else
      tn_inst_warn "k3s $TN_INST_INSTALLED is kept; the requested $_tn_want is NOT installed. A re-run of this script repairs the device, it does not upgrade k3s. To upgrade, run it with TN_K3S_UPGRADE=true (tnctl bootstrap --k3s-upgrade), one minor version at a time"
    fi
    return 0
  fi

  # TN_K3S_UPGRADE=true
  if ! tn_inst_cmp "$_tn_t" "$TN_INST_INSTALLED"; then
    TN_INST_ACTION=upgrade
    tn_inst_warn "TN_K3S_UPGRADE=true with $_tn_want, which is not an exact release: the k3s installer picks the release, so a downgrade or a jump over several minor versions cannot be checked. Pin an exact release (INSTALL_K3S_VERSION=v1.xx.y+k3sN) to get those checks"
    return 0
  fi
  TN_INST_EFFECTIVE=$_tn_t
  case $TN_INST_CMP in
    0) TN_INST_ACTION=same; tn_inst_info "k3s $TN_INST_INSTALLED is the requested release: nothing to upgrade"; return 0 ;;
    -1) tn_inst_fatal "refusing to downgrade k3s from $TN_INST_INSTALLED to $_tn_t: k3s cannot go back (the datastore has been migrated by the newer release). $(tn_inst_state_note). Pin $TN_INST_INSTALLED or a newer release, or restore the device from a backup" ;;
  esac
  tn_inst_parse "$TN_INST_INSTALLED"; _tn_imaj=$TN_INST_MAJ; _tn_imin=$TN_INST_MIN
  tn_inst_parse "$_tn_t"
  if [ "$TN_INST_MAJ" != "$_tn_imaj" ] || [ $((TN_INST_MIN - _tn_imin)) -gt 1 ]; then
    if [ "${TN_K3S_FORCE:-}" = true ]; then
      tn_inst_warn "upgrading k3s from $TN_INST_INSTALLED to $_tn_t skips minor versions (TN_K3S_FORCE=true). Kubernetes supports one minor version at a time: this can leave the cluster in a state that does not start. Have a backup of the datastore"
    else
      tn_inst_fatal "refusing to upgrade k3s from $TN_INST_INSTALLED to $_tn_t: that is more than one minor version (v$_tn_imaj.$_tn_imin -> v$TN_INST_MAJ.$TN_INST_MIN). Upgrade one minor at a time (to v$_tn_imaj.$((_tn_imin + 1)).x first), or set TN_K3S_FORCE=true (tnctl bootstrap --force) if you know what you are doing. $(tn_inst_state_note)"
    fi
  fi
  TN_INST_ACTION=upgrade
  tn_inst_info "upgrading k3s from $TN_INST_INSTALLED to $_tn_t (TN_K3S_UPGRADE=true). A server must be upgraded before its agents"
}

# ----------------------------------------------------------------- cgroup v1

TN_INST_CGROUP_FILE=99-teknoir-cgroupv1.yaml

# tn_inst_cgroup_state: prints v1 (cgroup v1 or hybrid), v2 or unknown
tn_inst_cgroup_state() {
  case ${TN_K3S_CGROUPV1:-} in true) echo v1; return 0 ;; false) echo v2; return 0 ;; esac
  # an OS image is built on another machine than the one it will run on
  if [ -n "${OS_BUILD:-}" ]; then echo unknown; return 0; fi
  _tn_cg=${TN_CGROUP_FSTYPE:-}
  [ -n "$_tn_cg" ] || _tn_cg=$(stat -fc %T /sys/fs/cgroup 2>/dev/null) || _tn_cg=
  case $_tn_cg in
    cgroup2fs) echo v2 ;;
    '') echo unknown ;;
    *) echo v1 ;;   # tmpfs: cgroup v1, or the hybrid layout
  esac
}

# tn_inst_cgroup_configured_elsewhere: 0 if the device sets fail-cgroupv1 itself (not in our file)
tn_inst_cgroup_configured_elsewhere() {
  _tn_cd=${TN_K3S_CONFIG_DIR:-/etc/rancher/k3s}
  case ${INSTALL_K3S_EXEC:-} in *fail-cgroupv1*) TN_INST_CGROUP_WHERE=INSTALL_K3S_EXEC; return 0 ;; esac
  for _tn_cf in "$_tn_cd/config.yaml" $(find "$_tn_cd/config.yaml.d" -maxdepth 1 -type f -name '*.yaml' 2>/dev/null | LC_ALL=C sort); do
    [ -r "$_tn_cf" ] || continue
    [ "$_tn_cf" != "$_tn_cd/config.yaml.d/$TN_INST_CGROUP_FILE" ] || continue
    if grep -q 'fail-cgroupv1' "$_tn_cf" 2>/dev/null; then TN_INST_CGROUP_WHERE=$_tn_cf; return 0; fi
  done
  return 1
}

tn_inst_cgroup_remove() { # FILE WHY
  [ -f "$1" ] || return 0
  rm -f "$1" || { tn_inst_warn "cannot remove $1"; return 0; }
  tn_inst_info "Removed $1 ($2)"
}

# tn_inst_cgroup: writes or removes the drop-in, see the top. Uses TN_INST_EFFECTIVE (the release
# that will run; nothing = a channel, taken as new enough: stable and latest are >= v1.35). The flag
# goes in from v1.31, where the kubelet learned it (an older kubelet would not start with an
# unknown argument).
tn_inst_cgroup() {
  _tn_cdir=${TN_K3S_CONFIG_DIR:-/etc/rancher/k3s}/config.yaml.d
  _tn_cfile=$_tn_cdir/$TN_INST_CGROUP_FILE
  _tn_cst=$(tn_inst_cgroup_state)
  if [ "$_tn_cst" = v2 ]; then
    tn_inst_cgroup_remove "$_tn_cfile" "this host uses cgroup v2"
    return 0
  fi
  _tn_flagok=1 _tn_must=1
  if [ -n "${TN_INST_EFFECTIVE:-}" ] && tn_inst_cmp "$TN_INST_EFFECTIVE" v1.31.0 && [ "$TN_INST_CMP" = -1 ]; then _tn_flagok=0; fi
  if [ -n "${TN_INST_EFFECTIVE:-}" ] && tn_inst_cmp "$TN_INST_EFFECTIVE" v1.35.0 && [ "$TN_INST_CMP" = -1 ]; then _tn_must=0; fi
  if [ "$_tn_cst" = v1 ]; then
    tn_inst_warn "this host uses cgroup v1 (or the hybrid layout). cgroup v1 is deprecated: Kubernetes >= 1.35 refuses to start the kubelet on it. Move the device to cgroup v2 (kernel command line systemd.unified_cgroup_hierarchy=1, or a newer OS release)"
  else
    tn_inst_info "cannot tell whether the device uses cgroup v1 (OS build, or no /sys/fs/cgroup): the kubelet flag is set to be safe, it is harmless on cgroup v2"
  fi
  if [ "$_tn_flagok" = 0 ]; then
    tn_inst_info "k3s $TN_INST_EFFECTIVE is older than v1.31 (its kubelet has no fail-cgroupv1 flag) and starts on cgroup v1 without help"
    tn_inst_cgroup_remove "$_tn_cfile" "k3s is older than v1.31"
    return 0
  fi
  if [ "$_tn_must" = 0 ]; then
    tn_inst_info "k3s $TN_INST_EFFECTIVE starts on cgroup v1 without help; the kubelet flag is set anyway, so that an upgrade to v1.35 or newer (also one made by hand) does not stop the kubelet"
  fi
  if tn_inst_cgroup_configured_elsewhere; then
    tn_inst_info "fail-cgroupv1 is set by the device itself ($TN_INST_CGROUP_WHERE): left as it is"
    tn_inst_cgroup_remove "$_tn_cfile" "the device sets fail-cgroupv1 itself"
    return 0
  fi
  _tn_want=$(printf '%s\n' \
    '# Written by the Teknoir bootstrap script. Kubernetes >= 1.35 refuses to start the kubelet on cgroup v1' \
    '# unless this is false. cgroup v1 is deprecated: move the device to cgroup v2 and delete this file.' \
    '# "kubelet-arg+" appends to the kubelet-arg of config.yaml; harmless on cgroup v2.' \
    'kubelet-arg+:' \
    '  - "fail-cgroupv1=false"')
  if [ -f "$_tn_cfile" ] && [ "$(cat "$_tn_cfile")" = "$_tn_want" ]; then
    tn_inst_info "kubelet-arg fail-cgroupv1=false is already in place ($_tn_cfile)"
    return 0
  fi
  # atomically: k3s may be restarting in a loop right now and must never read half a file. The temporary
  # name does not end in .yaml, so k3s does not read it either.
  _tn_tmp=$_tn_cdir/.$TN_INST_CGROUP_FILE.tmp.$$
  if mkdir -p "$_tn_cdir" && find "$_tn_cdir" -maxdepth 1 -name ".$TN_INST_CGROUP_FILE.tmp.*" -exec rm -f {} + &&
     printf '%s\n' "$_tn_want" > "$_tn_tmp" && mv -f "$_tn_tmp" "$_tn_cfile"; then
    :
  else
    rm -f "$_tn_tmp" 2>/dev/null || true
    tn_inst_fatal "cannot write $_tn_cfile: k3s >= v1.35 would not start on this cgroup v1 host"
  fi
  tn_inst_info "Set the kubelet argument fail-cgroupv1=false in $_tn_cfile (k3s ${TN_INST_EFFECTIVE:-from a channel} on cgroup v1)"
}

# ----------------------------------------------------------------- config.yaml

# tn_inst_yaml_has_key FILE KEY: is there a top-level KEY (or KEY+) in FILE
tn_inst_yaml_has_key() {
  [ -r "$1" ] || return 1
  awk -v k="$2" '{ sub(/\r$/, "") } $0 ~ ("^" k "\\+?[ \t]*:") { f = 1 } END { exit f ? 0 : 1 }' "$1"
}

# tn_inst_config_ensure FILE KEY TEXT: appends TEXT (the YAML of the key, may be several lines) to
# FILE unless FILE has the top-level KEY already. A config.yaml that the device owner has edited is
# never rewritten: a re-run of a bootstrap script used to replace it, and lost every key it did not know.
tn_inst_config_ensure() {
  if tn_inst_yaml_has_key "$1" "$2"; then
    tn_inst_info "$1 already sets '$2': kept"
    return 0
  fi
  mkdir -p "$(dirname "$1")" || return 1
  if [ -s "$1" ] && [ -n "$(tail -c 1 "$1")" ]; then echo >> "$1" || return 1; fi
  printf '%s\n' "$3" >> "$1" || return 1
  tn_inst_info "$1: added '$2'"
}

# ----------------------------------------------------------------- the entry point

# tn_inst_prepare ROLE: the release policy, then cgroup v1. Call it after INSTALL_K3S_VERSION /
# INSTALL_K3S_CHANNEL (and INSTALL_K3S_EXEC, if the device sets fail-cgroupv1 there) are set and
# before the k3s installer. A SERVER script calls it BEFORE tn_k3s_ca_setup: the kubelet flag must be in
# place before the CA step restarts k3s, or a k3s that a cgroup v1 host made exit would not be up for
# rotate-ca (it restarts itself every few seconds and reads the flag then). The CA step must in turn
# run before the installer. An agent has no CA step.
tn_inst_prepare() {
  TN_INST_ROLE=${1:-server}
  tn_inst_info "Checking the k3s release and the cgroup version ($TN_INST_ROLE)"
  tn_inst_plan
  tn_inst_cgroup
}
