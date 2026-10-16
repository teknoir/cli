#!/bin/sh
set -e
set -o noglob
#set -x
umask 027

# --- helper functions ---
info() { echo '[INFO] ' "$@"; }
warn() { echo '[WARN] ' "$@" >&2; }
fatal() { echo '[ERROR] ' "$@" >&2; exit 1; }

# Exit if not running as root
[ $(id -u) -eq 0 ] || fatal 'You must run this script as root'

DOWNLOADER=
# --- download ---
download() {
    [ $# -eq 2 ] || fatal 'download needs exactly 2 arguments'

    case $DOWNLOADER in
        curl)
            if [ "${OS_BUILD}" ] || [ "${INSECURE}" ]; then
                info "Running installation without verifying ssl certs on URLs"
                $SUDO curl --insecure -o $1 -sSfL $2
            else
                $SUDO curl -o $1 -sSfL $2
            fi
            ;;
        wget)
            $SUDO wget -qO $1 $2
            ;;
        *)
            fatal "Incorrect executable '$DOWNLOADER'"
            ;;
    esac

    # Abort if download command failed
    [ $? -eq 0 ] || fatal 'Download failed'
}

# --- verify existence of network downloader executable ---
verify_downloader() {
    # Return failure if it doesn't exist or is no executable
    [ -x "$(command -v $1)" ] || return 1

    # Set verified executable as our downloader program and return success
    DOWNLOADER=$1
    return 0
}

verify_downloader curl || verify_downloader wget || fatal 'Can not find curl or wget for downloading files'


# This is the drop in device k3s agent(worker) bootstrap script for the Teknoir platform.
# It should work for most arm32/arm64/amd64 devices running Debian or Ubuntu.

# Environment variables:
#   K3S_URL=https://<IP or mDNS name>:6443 (default: generated at master node creation time)
#   K3S_TOKEN="32 char string" (default: generated at master node creation time)
#   CREATE_USER=false (default: ask, create a user for the device without asking)
#   INSTALL_K3S_VERSION=v1.xx.y+k3sN (default: the release of the SERVER, read from https://<K3S_URL>/static/teknoir/k3s-version: an
#                          agent never installs a k3s newer than its server. A release you ask for (here, or
#                          tnctl bootstrap --k3s-version) is installed if it is not newer than the server's, and
#                          refused if it is. If the server cannot be reached the script stops, unless the
#                          release was asked for or TN_K3S_FORCE=true)
#   TN_K3S_UPGRADE=true (default: false; a re-run KEEPS the installed k3s version. true lets the pin replace
#                          it: never a downgrade, never more than one minor version. Upgrade the server first)
#   TN_K3S_FORCE=true (default: false; allows an upgrade over more than one minor version, an agent that is
#                          newer than its server, and an install when the release of the server is unknown)
#   TN_K3S_CGROUPV1=true|false (default: detected; on cgroup v1 with k3s >= v1.31 the kubelet gets
#                          fail-cgroupv1=false, see /etc/rancher/k3s/config.yaml.d/99-teknoir-cgroupv1.yaml)

_DOMAIN="example.test"
_DEVICE_ID="golden-device"
_NAMESPACE="golden-ns"
_FIRST_USER_NAME="golden-user"
_FIRST_USER_PASS="TEST-ONLY-password"
_FIRST_USER_KEY="ssh-ed25519 TEST-ONLY-ssh-public golden@example.test"
_RSA_PUBLIC="TEST-ONLY-rsa-public
TEST-ONLY-rsa-public-line2"
_RSA_PRIVATE="TEST-ONLY-rsa-private
TEST-ONLY-rsa-private-line2"
_K3S_TOKEN="TEST-ONLY-k3s-token"


# The k3s release policy (a re-run keeps the installed k3s) and the cgroup v1 kubelet flag.
#!/bin/sh
# tn-k3s-install.sh - what a Teknoir bootstrap script decides before it runs the k3s installer:
# which k3s release (a re-run keeps what is installed), and whether the kubelet must be told that
# cgroup v1 is acceptable. Used by the server and the agent script (sourced), next to
# tn-k3s-ca.sh (the server only).
#
#   . tn-k3s-install.sh; tn_inst_prepare server|agent       (once, right before the installer)
#   tn_inst_finish                                           (once, right after the installer)
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
#                             A channel is checked too: a minor (v1.35) by its major.minor against the
#                             installed one (lower or other major: refused; more than +1: needs force);
#                             stable/latest/testing is resolved at update.k3s.io like the installer does
#                             and then checked as an exact release. A channel that cannot be resolved
#                             is refused unless TN_K3S_FORCE=true (then it goes on unchecked).
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
# Order of effects. Everything before the installer (tn_inst_prepare, the CA step, the download) can
# be interrupted by a restart of k3s (the CA step restarts it) or by a reboot of the device, and the
# running k3s then reads the configuration again. So tn_inst_prepare only makes changes that are safe
# for the k3s that is running NOW, and never takes a protection away before its replacement is in place:
#   - the drop-in is REMOVED early only when the running k3s does not need it: the host is on cgroup v2;
#     the release that will run is older than v1.31 (an older kubelet fails on the unknown flag); or the
#     device sets fail-cgroupv1 in a PERSISTENT config file that k3s already reads (config.yaml or another
#     config.yaml.d/*.yaml). A fail-cgroupv1 only in INSTALL_K3S_EXEC reaches the unit when the installer
#     has run, so until then the drop-in stays (or is written): the same value twice is harmless, and
#     the command line wins when they differ.
#   - the drop-in is WRITTEN early, except when the k3s that is installed now is older than v1.31 (an
#     upgrade): its kubelet would fail on the flag if it restarted before the installer replaced it.
#     That write waits for tn_inst_finish, after the installer. An existing drop-in is removed at once in
#     that case, because the old kubelet cannot take it.
#   - but the installer starts the NEW release as soon as it has rewritten the unit, and a release >= v1.35
#     refuses to start on cgroup v1 without the flag (install.sh's `systemctl restart` fails, and under
#     `set -e` the script stops before tn_inst_finish: a k3s that crash-loops). So when the write is held
#     back and the release the installer will start is not KNOWN to be older than v1.35 (a channel such as
#     stable or latest, or an exact v1.35 or newer, also with TN_K3S_FORCE=true), the flag is appended to
#     INSTALL_K3S_EXEC (--kubelet-arg=fail-cgroupv1=false, once, and exported): it is then in the NEW unit
#     from its first start and the old, running kubelet never sees it. An exact v1.31 - v1.34 does not need
#     it on its first start (it accepts it, it only ignores it on cgroup v1), so it gets none. tn_inst_finish
#     still writes the drop-in afterwards, which is what keeps later runs and upgrades safe.
#
# What counts as "the device sets fail-cgroupv1 itself": an uncommented line that sets it to false in
# config.yaml, config.yaml.d/*.yaml or *.yml (not our own file), or in INSTALL_K3S_EXEC. A comment does
# not count, and neither does fail-cgroupv1=true: then the drop-in is written (and kept) as usual, and
# it wins, because kubelet-arg+ comes after. A device that sets fail-cgroupv1=true on a cgroup v1 host
# on purpose is not supported (the kubelet of v1.35 and newer cannot start there anyway); delete the
# drop-in and set cgroup v2 instead.
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

TN_K3S_INSTALL_LIB_VERSION=3
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

# tn_inst_normalize_pin: the k3s installer takes INSTALL_K3S_VERSION as an EXACT release (it downloads that
# tag, so "stable" would be a 404) and INSTALL_K3S_CHANNEL as a channel. A channel name given as the version
# (stable, latest, testing, or a minor such as v1.34) is moved to INSTALL_K3S_CHANNEL and the version is
# unset, so that every later check sees what the installer will see. An exact release is not touched.
tn_inst_normalize_pin() {
  [ -n "${INSTALL_K3S_VERSION:-}" ] || return 0
  case $INSTALL_K3S_VERSION in
    stable|latest|testing) ;;
    v[0-9]*) printf '%s\n' "$INSTALL_K3S_VERSION" | grep -Eq '^v[0-9]+\.[0-9]+$' || return 0 ;;
    *) return 0 ;;
  esac
  if [ -n "${INSTALL_K3S_CHANNEL:-}" ] && [ "$INSTALL_K3S_CHANNEL" != "$INSTALL_K3S_VERSION" ]; then
    tn_inst_fatal "conflicting k3s requests: INSTALL_K3S_VERSION=$INSTALL_K3S_VERSION is a channel and INSTALL_K3S_CHANNEL=$INSTALL_K3S_CHANNEL is another. Set only one of them. $(tn_inst_state_note)"
  fi
  INSTALL_K3S_CHANNEL=$INSTALL_K3S_VERSION
  export INSTALL_K3S_CHANNEL
  unset INSTALL_K3S_VERSION
  tn_inst_info "$INSTALL_K3S_CHANNEL is a k3s channel, not a release: the installer gets INSTALL_K3S_CHANNEL=$INSTALL_K3S_CHANNEL"
}

# tn_inst_plan: applies the policy at the top. Reads INSTALL_K3S_VERSION / INSTALL_K3S_CHANNEL,
# sets and exports them for the installer. Sets TN_INST_INSTALLED, TN_INST_ACTION
# (install|same|keep|upgrade) and TN_INST_EFFECTIVE (the exact release that will run, or nothing
# when only a channel is known). Exits through tn_inst_fatal when it refuses.
tn_inst_plan() {
  tn_inst_normalize_pin
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
  if [ -z "$_tn_t" ]; then
    # a channel: the installer picks the release, so find out which, or compare the minor
    _tn_ch=${INSTALL_K3S_CHANNEL:-stable}
    if printf '%s\n' "$_tn_ch" | grep -Eq '^v[0-9]+\.[0-9]+$'; then
      tn_inst_parse "${_tn_ch}.0"; _tn_cmaj=$TN_INST_MAJ; _tn_cmin=$TN_INST_MIN
      tn_inst_parse "$TN_INST_INSTALLED"; _tn_imaj=$TN_INST_MAJ; _tn_imin=$TN_INST_MIN
      if [ "$_tn_cmaj" != "$_tn_imaj" ] || [ "$_tn_cmin" -lt "$_tn_imin" ]; then
        tn_inst_fatal "refusing to downgrade k3s from $TN_INST_INSTALLED to the channel $_tn_ch (v$_tn_cmaj.$_tn_cmin is not v$_tn_imaj.$_tn_imin or a later minor of the same major): k3s cannot go back (the datastore has been migrated by the newer release). $(tn_inst_state_note). Use a channel of v$_tn_imaj.$_tn_imin or $_tn_imaj.$((_tn_imin + 1)), or restore the device from a backup"
      fi
      if [ $((_tn_cmin - _tn_imin)) -gt 1 ]; then
        if [ "${TN_K3S_FORCE:-}" = true ]; then
          tn_inst_warn "upgrading k3s from $TN_INST_INSTALLED to the channel $_tn_ch skips minor versions (TN_K3S_FORCE=true). Kubernetes supports one minor version at a time: this can leave the cluster in a state that does not start. Have a backup of the datastore"
        else
          tn_inst_fatal "refusing to upgrade k3s from $TN_INST_INSTALLED to the channel $_tn_ch: that is more than one minor version (v$_tn_imaj.$_tn_imin -> v$_tn_cmaj.$_tn_cmin). Upgrade one minor at a time (channel v$_tn_imaj.$((_tn_imin + 1)) first), or set TN_K3S_FORCE=true (tnctl bootstrap --force) if you know what you are doing. $(tn_inst_state_note)"
        fi
      fi
      TN_INST_ACTION=upgrade
      tn_inst_info "upgrading k3s from $TN_INST_INSTALLED to the latest release of the channel $_tn_ch (TN_K3S_UPGRADE=true). A server must be upgraded before its agents"
      return 0
    fi
    # stable, latest, testing: resolve like the installer does (the last path segment of the redirect)
    TN_INST_RESOLVED=$(curl -w '%{url_effective}' -L -s -S -o /dev/null "https://update.k3s.io/v1-release/channels/$_tn_ch" 2>/dev/null | sed 's|^.*/||; s|%2[Bb]|+|') || TN_INST_RESOLVED=
    if tn_inst_parse "$TN_INST_RESOLVED" 2>/dev/null; then
      _tn_t=$TN_INST_RESOLVED
      tn_inst_info "the k3s channel $_tn_ch resolves to $_tn_t: it is pinned, so that the installer gets what was checked"
      INSTALL_K3S_VERSION=$_tn_t
      export INSTALL_K3S_VERSION
      unset INSTALL_K3S_CHANNEL
    elif [ "${TN_K3S_FORCE:-}" = true ]; then
      TN_INST_ACTION=upgrade
      tn_inst_warn "TN_K3S_UPGRADE=true with the channel $_tn_ch, which cannot be resolved from here (update.k3s.io): the k3s installer picks the release, so a downgrade or a jump over several minor versions cannot be checked. TN_K3S_FORCE=true: going on without those checks. Pin an exact release (INSTALL_K3S_VERSION=v1.xx.y+k3sN) to get them"
      return 0
    else
      tn_inst_fatal "refusing to upgrade k3s from $TN_INST_INSTALLED to the channel $_tn_ch: the channel cannot be resolved to a release from here (update.k3s.io is not reachable), so a downgrade or a jump over several minor versions cannot be checked. Pin an exact release (INSTALL_K3S_VERSION=v1.xx.y+k3sN, tnctl bootstrap --k3s-version), or set TN_K3S_FORCE=true to go on without the checks. $(tn_inst_state_note)"
    fi
  fi
  if ! tn_inst_cmp "$_tn_t" "$TN_INST_INSTALLED"; then
    tn_inst_fatal "cannot compare the requested k3s release '$_tn_t' with the installed $TN_INST_INSTALLED. Pin an exact release (INSTALL_K3S_VERSION=v1.xx.y+k3sN). $(tn_inst_state_note)"
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

# tn_inst_cgroup_flag_in_text: reads TEXT on stdin; 0 if an uncommented part of it sets fail-cgroupv1 to
# false (kubelet-arg: ["fail-cgroupv1=false"], - fail-cgroupv1=false, --kubelet-arg=fail-cgroupv1=false).
# A whole-line or trailing YAML comment is dropped first; fail-cgroupv1=true does not match.
tn_inst_cgroup_flag_in_text() {
  sed 's/^[[:space:]]*#.*$//; s/[[:space:]]#.*$//' | grep -Eiq 'fail-cgroupv1[[:space:]]*=[[:space:]]*false([^A-Za-z0-9_]|$)'
}

# tn_inst_cgroup_configured_elsewhere: 0 if the device sets fail-cgroupv1=false itself (not in our file).
# Sets TN_INST_CGROUP_WHERE to where, and TN_INST_CGROUP_PERSISTENT to 1 when that is a config file that
# the running k3s reads (config.yaml, config.yaml.d/*.yaml and *.yml), and to 0 when it is only INSTALL_K3S_EXEC
# (which reaches the unit when the installer has run). Files win over INSTALL_K3S_EXEC. A comment, and
# fail-cgroupv1=true, do not count.
tn_inst_cgroup_configured_elsewhere() {
  _tn_cd=${TN_K3S_CONFIG_DIR:-/etc/rancher/k3s}
  TN_INST_CGROUP_WHERE=; TN_INST_CGROUP_PERSISTENT=0
  for _tn_cf in "$_tn_cd/config.yaml" $(find "$_tn_cd/config.yaml.d" -maxdepth 1 -type f \( -iname '*.yaml' -o -iname '*.yml' \) 2>/dev/null | LC_ALL=C sort); do
    [ -r "$_tn_cf" ] || continue
    [ "$_tn_cf" != "$_tn_cd/config.yaml.d/$TN_INST_CGROUP_FILE" ] || continue
    if tn_inst_cgroup_flag_in_text < "$_tn_cf"; then TN_INST_CGROUP_WHERE=$_tn_cf; TN_INST_CGROUP_PERSISTENT=1; return 0; fi
  done
  if printf '%s\n' "${INSTALL_K3S_EXEC:-}" | tn_inst_cgroup_flag_in_text; then TN_INST_CGROUP_WHERE=INSTALL_K3S_EXEC; return 0; fi
  return 1
}

tn_inst_cgroup_remove() { # FILE WHY
  [ -f "$1" ] || return 0
  rm -f "$1" || { tn_inst_warn "cannot remove $1"; return 0; }
  tn_inst_info "Removed $1 ($2)"
}

# tn_inst_cgroup: writes or removes the drop-in, see the top. Uses TN_INST_EFFECTIVE (the release
# that will run; nothing = a channel, taken as new enough: stable and latest are >= v1.35; a channel
# named vMAJ.MIN is read as that minor). The flag goes in from v1.31, where the kubelet learned it (an
# older kubelet would not start with an unknown argument).
tn_inst_cgroup() {
  _tn_cdir=${TN_K3S_CONFIG_DIR:-/etc/rancher/k3s}/config.yaml.d
  _tn_cfile=$_tn_cdir/$TN_INST_CGROUP_FILE
  TN_INST_CGROUP_DEFERRED=
  _tn_cst=$(tn_inst_cgroup_state)
  if [ "$_tn_cst" = v2 ]; then
    tn_inst_cgroup_remove "$_tn_cfile" "this host uses cgroup v2"
    return 0
  fi
  _tn_eff=${TN_INST_EFFECTIVE:-}
  if [ -z "$_tn_eff" ]; then
    case ${INSTALL_K3S_CHANNEL:-} in v[0-9]*.[0-9]*) tn_inst_parse "${INSTALL_K3S_CHANNEL}.0" && _tn_eff=${INSTALL_K3S_CHANNEL}.0 ;; esac
  fi
  _tn_flagok=1 _tn_must=1
  if [ -n "$_tn_eff" ] && tn_inst_cmp "$_tn_eff" v1.31.0 && [ "$TN_INST_CMP" = -1 ]; then _tn_flagok=0; fi
  if [ -n "$_tn_eff" ] && tn_inst_cmp "$_tn_eff" v1.35.0 && [ "$TN_INST_CMP" = -1 ]; then _tn_must=0; fi
  if [ "$_tn_cst" = v1 ]; then
    tn_inst_warn "this host uses cgroup v1 (or the hybrid layout). cgroup v1 is deprecated: Kubernetes >= 1.35 refuses to start the kubelet on it. Move the device to cgroup v2 (kernel command line systemd.unified_cgroup_hierarchy=1, or a newer OS release)"
  else
    tn_inst_info "cannot tell whether the device uses cgroup v1 (OS build, or no /sys/fs/cgroup): the kubelet flag is set to be safe, it is harmless on cgroup v2"
  fi
  if [ "$_tn_flagok" = 0 ]; then
    tn_inst_info "k3s $_tn_eff is older than v1.31 (its kubelet has no fail-cgroupv1 flag) and starts on cgroup v1 without help"
    tn_inst_cgroup_remove "$_tn_cfile" "k3s is older than v1.31"
    return 0
  fi
  if [ "$_tn_must" = 0 ]; then
    tn_inst_info "k3s $_tn_eff starts on cgroup v1 without help; the kubelet flag is set anyway, so that an upgrade to v1.35 or newer (also one made by hand) does not stop the kubelet"
  fi
  if tn_inst_cgroup_configured_elsewhere; then
    if [ "$TN_INST_CGROUP_PERSISTENT" = 1 ]; then
      tn_inst_info "fail-cgroupv1=false is set by the device itself ($TN_INST_CGROUP_WHERE): left as it is"
      tn_inst_cgroup_remove "$_tn_cfile" "the device sets fail-cgroupv1=false itself, in a file that k3s reads"
      return 0
    fi
    # only on the command line of the installer: k3s does not read it until the unit is rewritten, so the
    # drop-in must keep protecting a k3s that restarts before that. The same value twice does no harm.
    tn_inst_info "fail-cgroupv1=false is set by the device itself ($TN_INST_CGROUP_WHERE): left as it is. The drop-in $_tn_cfile stays until the installer has put it in the unit; the same value twice is harmless, and the one on the command line wins"
  fi
  # an installed k3s older than v1.31 (about to be upgraded) cannot start with the flag
  if [ -n "${TN_INST_INSTALLED:-}" ] && tn_inst_cmp "$TN_INST_INSTALLED" v1.31.0 && [ "$TN_INST_CMP" = -1 ]; then
    TN_INST_CGROUP_DEFERRED=1
    tn_inst_info "the installed k3s $TN_INST_INSTALLED is older than v1.31: the kubelet flag is set after the installer has upgraded it, not before"
    tn_inst_cgroup_remove "$_tn_cfile" "the installed k3s is older than v1.31 and cannot take the flag until it is upgraded"
    # The installer starts the new release as soon as it has written the unit. From v1.35 that kubelet
    # does not start on cgroup v1 without the flag, and tn_inst_finish would never run (the installer's
    # restart fails under set -e). So the unit gets the flag from its first start, through INSTALL_K3S_EXEC:
    # the old kubelet, which is running now, never reads it. Not needed for an exact v1.31 - v1.34.
    if [ "$_tn_must" = 1 ]; then
      if printf '%s\n' "${INSTALL_K3S_EXEC:-}" | tn_inst_cgroup_flag_in_text; then
        tn_inst_info "INSTALL_K3S_EXEC already has fail-cgroupv1=false: the new unit carries it from its first start"
      else
        INSTALL_K3S_EXEC="${INSTALL_K3S_EXEC:+$INSTALL_K3S_EXEC }--kubelet-arg=fail-cgroupv1=false"
        export INSTALL_K3S_EXEC
        tn_inst_info "k3s ${_tn_eff:-from a channel} may need the flag on its first start: --kubelet-arg=fail-cgroupv1=false is added to INSTALL_K3S_EXEC, so that the new unit has it. The old kubelet does not read it"
      fi
    fi
    return 0
  fi
  tn_inst_cgroup_write
}

# tn_inst_cgroup_write: the drop-in, written atomically and only when it differs
tn_inst_cgroup_write() {
  _tn_cdir=${TN_K3S_CONFIG_DIR:-/etc/rancher/k3s}/config.yaml.d
  _tn_cfile=$_tn_cdir/$TN_INST_CGROUP_FILE
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

# tn_inst_finish: call it right after the k3s installer succeeded. Writes the kubelet flag that
# tn_inst_prepare held back because the k3s that was installed then could not take it (see "Order of
# effects"). Nothing to do otherwise.
tn_inst_finish() {
  if [ -n "${TN_INST_CGROUP_DEFERRED:-}" ]; then
    TN_INST_CGROUP_DEFERRED=
    tn_inst_info "The installer has run: setting the kubelet flag that was held back"
    tn_inst_cgroup_write
  fi
}


info "Install device specific keys"
export CONFIG_PATH=/etc/teknoir
$SUDO mkdir -p ${CONFIG_PATH}
$SUDO mkdir -p /toe_conf && $SUDO rm -rf /toe_conf && $SUDO ln -s ${CONFIG_PATH}/ /toe_conf # For backward compatibility
download ${CONFIG_PATH}/roots.pem https://pki.goog/roots.pem
$SUDO chmod 440 ${CONFIG_PATH}/roots.pem
$SUDO chown 65532:root ${CONFIG_PATH}/roots.pem
$SUDO tee ${CONFIG_PATH}/rsa_private.pem > /dev/null << EOL
${_RSA_PRIVATE}
EOL
$SUDO chmod 440 ${CONFIG_PATH}/rsa_private.pem
$SUDO chown 65532:root ${CONFIG_PATH}/rsa_private.pem
$SUDO tee ${CONFIG_PATH}/rsa_public.pem > /dev/null << EOL
${_RSA_PUBLIC}
EOL
$SUDO chmod 444 ${CONFIG_PATH}/rsa_public.pem
$SUDO chown 65532:root ${CONFIG_PATH}/rsa_public.pem
$SUDO chgrp 65532 /etc/teknoir 2>/dev/null || true


$SUDO cp ${CONFIG_PATH}/rsa_public.pem /usr/local/share/ca-certificates/${_DEVICE_ID}-${_DOMAIN}.crt
$SUDO update-ca-certificates

info "Install Rancher K3s Agent"
download k3s_installer.sh https://get.k3s.io
$SUDO chmod +x k3s_installer.sh

if [ "${OS_BUILD}" ] || [ "${INSECURE}" ]; then
    info "Running installation without verifying ssl certs on URLs"
    $SUDO sed -i "s#curl -w#curl --insecure -w#g" k3s_installer.sh
    $SUDO sed -i "s#curl -o#curl --insecure -o#g" k3s_installer.sh
fi

if [ "${OS_BUILD}" ]; then
    info "OS Build specifics"
    export INSTALL_K3S_SKIP_START=true
    $SUDO sed -i "s#-d /run/systemd#true#g" k3s_installer.sh
fi

if [ -z "${K3S_URL}" ]; then
    DEFAULT_K3S_URL="https://${_DEVICE_ID}.local:6443"
    read -p "Is ${DEFAULT_K3S_URL} the correct URL to the server/master node? [Y/n] " confirm
    confirm=${confirm:-"Y"}
    if echo "${confirm}" | grep -iq "^y$"; then
        export K3S_URL=${DEFAULT_K3S_URL}
    else
        read -p "Please enter the K3S_URL: " K3S_URL
        export K3S_URL=${K3S_URL}
    fi
fi
export K3S_URL=${K3S_URL}
export K3S_TOKEN="${K3S_TOKEN:-"${_K3S_TOKEN}"}"

export INSTALL_K3S_EXEC="agent"
if [ "${USE_DOCKER}" = true ]; then
    info "Use docker container-runtime for K3s"
    export INSTALL_K3S_EXEC="${INSTALL_K3S_EXEC} --docker"
fi

# Who chose the k3s release: the person who runs this script (INSTALL_K3S_VERSION / INSTALL_K3S_CHANNEL in the
# environment, or `tnctl bootstrap --k3s-version`) or the default of this script. An agent installs the
# release of its server unless it was asked for one (it is then refused if it is newer than the server).
if [ -n "${INSTALL_K3S_VERSION:-}${INSTALL_K3S_CHANNEL:-}" ]; then TN_K3S_PIN_SOURCE=env; else TN_K3S_PIN_SOURCE=default; fi
export TN_K3S_PIN_SOURCE
# k3s is pinned to v1.36.5+k3s1 (tnctl bootstrap --k3s-version). Override when running the script:
#   INSTALL_K3S_VERSION=v1.xx.y+k3sN sh bootstrap_*.sh      (an agent must run the version of its server)
# This is the release for a NEW device: a re-run on a device with k3s installed keeps the installed version
# (TN_K3S_UPGRADE=true, tnctl bootstrap --k3s-upgrade, replaces it; never a downgrade).
export INSTALL_K3S_VERSION="${INSTALL_K3S_VERSION:-v1.36.5+k3s1}"

# A new agent gets the k3s release of its server; a re-run keeps the installed k3s (unless
# TN_K3S_UPGRADE=true) and sets fail-cgroupv1=false on cgroup v1 hosts, which Kubernetes >= 1.35 would refuse
# (the flag goes in from k3s v1.31, where the kubelet learned it). INSTALL_K3S_EXEC is final above, before
# tn_inst_prepare reads it.
tn_inst_prepare agent
export INSTALL_K3S_SYMLINK=force
$SUDO ./k3s_installer.sh
# The kubelet flag that had to wait for the installer (an installed k3s older than v1.31 that was upgraded)
tn_inst_finish

info "K3s agent node joined successfully!"

warn "To enable tunneling, the user ${_FIRST_USER_NAME} has to be created."

on_sudo() {
  $SUDO sh -- "$@"
}

setup_user() {
  $SUDO mkdir -p "/home/${_FIRST_USER_NAME}"
  if ! id -u "${_FIRST_USER_NAME}" >/dev/null 2>&1; then
    if command -v useradd >/dev/null 2>&1 && command -v groupadd >/dev/null 2>&1; then
      if ! getent group "${_FIRST_USER_NAME}" >/dev/null 2>&1; then
        $SUDO groupadd "${_FIRST_USER_NAME}"
      fi
      $SUDO useradd \
        --gid "${_FIRST_USER_NAME}" \
        --groups users \
        --home-dir "/home/${_FIRST_USER_NAME}" \
        --shell /bin/bash \
        "${_FIRST_USER_NAME}"
    else
      $SUDO adduser --disabled-password --gecos "" "${_FIRST_USER_NAME}"
    fi
  fi

  if [ -z "${_FIRST_USER_PASS}" ]; then
    warn "No password for ${_FIRST_USER_NAME} in the platform: key-only login"
  elif command -v chpasswd &> /dev/null; then
    echo "${_FIRST_USER_NAME}:${_FIRST_USER_PASS}" | $SUDO chpasswd
  elif command -v passwd &> /dev/null; then
    echo -e "${_FIRST_USER_PASS}\n${_FIRST_USER_PASS}" | passwd "${_FIRST_USER_NAME}"
  elif command -v usermod &> /dev/null && command -v openssl &> /dev/null; then
    usermod --password $(openssl passwd -1 "${_FIRST_USER_PASS}") "${_FIRST_USER_NAME}"
  else
      warn "Could not set password for user ${_FIRST_USER_NAME}"
      info "Skipping tunneling setup"
      exit
  fi

  on_sudo << EOF
mkdir -p "/home/${_FIRST_USER_NAME}/.ssh"
cp "${CONFIG_PATH}/rsa_private.pem" "/home/${_FIRST_USER_NAME}/.ssh/id_rsa"
ssh-keygen -y -f "${CONFIG_PATH}/rsa_private.pem" > "/home/${_FIRST_USER_NAME}/.ssh/id_rsa.pub"
cat "/home/${_FIRST_USER_NAME}/.ssh/id_rsa.pub" > "/home/${_FIRST_USER_NAME}/.ssh/authorized_keys"
if [ -z "\${_FIRST_USER_KEY+x}" ] || [ "\${_FIRST_USER_KEY}" = "" ]; then
  echo "[INFO] _FIRST_USER_KEY is not set, will continue with only password auth";
else
  echo "\${_FIRST_USER_KEY}" >> "/home/${_FIRST_USER_NAME}/.ssh/authorized_keys"
fi
chmod 600 /home/${_FIRST_USER_NAME}/.ssh/*
chmod 744 /home/${_FIRST_USER_NAME}/.ssh
chown -R "${_FIRST_USER_NAME}:${_FIRST_USER_NAME}" "/home/${_FIRST_USER_NAME}"

# Add root user to users and sudo groups
if [ -e /etc/group ]; then
    sed -i "/^users:/s/\(.*\)/\1,${_FIRST_USER_NAME}/;s/:,/:/" /etc/group
    sed -i "/^sudo:/s/\(.*\)/\1,${_FIRST_USER_NAME}/;s/:,/:/" /etc/group
fi
EOF

  setup_user_sudo
}

# A key-only platform user (no password in the platform) cannot answer sudo's
# password prompt, so automation over SSH (Ansible become, tnctl) could never
# act as root. Give exactly that user passwordless sudo with a validated
# drop-in. This is no weaker than a password: the SSH key and any password
# live in the same platform record, and the key alone already logs in. With a
# password set, the drop-in is removed again and sudo asks for the password.
setup_user_sudo() {
  _tn_sudoers="/etc/sudoers.d/90-teknoir-${_FIRST_USER_NAME}"
  if [ -n "${_FIRST_USER_PASS}" ]; then
    $SUDO rm -f "${_tn_sudoers}"
    return 0
  fi
  if [ ! -d /etc/sudoers.d ]; then
    warn "No /etc/sudoers.d: ${_FIRST_USER_NAME} gets no passwordless sudo"
    return 0
  fi
  _tn_tmp=$($SUDO mktemp /etc/sudoers.d/.teknoir.XXXXXX)
  echo "${_FIRST_USER_NAME} ALL=(ALL:ALL) NOPASSWD: ALL" | $SUDO tee "${_tn_tmp}" > /dev/null
  $SUDO chmod 0440 "${_tn_tmp}"
  if command -v visudo > /dev/null 2>&1 && ! $SUDO visudo -cf "${_tn_tmp}" > /dev/null; then
    $SUDO rm -f "${_tn_tmp}"
    warn "The sudoers drop-in for ${_FIRST_USER_NAME} did not validate; no passwordless sudo"
    return 0
  fi
  $SUDO mv -f "${_tn_tmp}" "${_tn_sudoers}"
  info "Key-only login: ${_FIRST_USER_NAME} has passwordless sudo (${_tn_sudoers})"
}


CREATE_USER=${CREATE_USER:-ask}
if [ "${OS_BUILD}" ] || [ "${CREATE_USER}" = true ]; then
  setup_user
else
  if id -u "${_FIRST_USER_NAME}" >/dev/null 2>&1; then
    warn "The user ${_FIRST_USER_NAME} already exist! This operation will change the password and OVERWRITE ssh keys for this users."
  fi
  warn "Do you want to add \"${_FIRST_USER_NAME}\" as a user and enable tunneling? [yY]"
  read REPLY

  case ${REPLY} in
    [Yy]* )
      setup_user
      ;;
    * )
      info "Skipping tunneling setup"
      ;;
  esac
fi


cd ..
info "Device bootstrapped successfully!"


