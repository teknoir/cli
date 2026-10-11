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
#   - k3s is not installed:   the pin is installed.
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
# ---------------------------------------------------------------- cgroup v1
# Kubernetes >= 1.35: the kubelet refuses to start on a cgroup v1 host (failCgroupV1 defaults to
# true) and k3s does not change that. Jetson L4T r32/r35 and Ubuntu 18.04/20.04 are cgroup v1. When
# the host is on cgroup v1 (stat -fc %T /sys/fs/cgroup is not cgroup2fs), or cannot be told (an OS
# build runs elsewhere than the device), and the k3s that will run is >= v1.35, the script writes
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
#   TN_K3S_FORCE        true: allow an upgrade across more than one minor version
#   TN_K3S_CGROUPV1     true / false: do not detect, take this as the answer
#   TN_CGROUP_FSTYPE    the file system type of /sys/fs/cgroup, instead of `stat` (tests)
#   K3S_BIN             the k3s command used to read the installed version (k3s, then /usr/local/bin/k3s)
#   TN_K3S_CONFIG_DIR   /etc/rancher/k3s
#   OS_BUILD            set by the OS image builds: the host is not the device, cgroup cannot be detected

TN_K3S_INSTALL_LIB_VERSION=1
export TN_K3S_INSTALL_LIB_VERSION

# ----------------------------------------------------------------- messages
# Own functions, in the format of header.sh.tmpl: a caller's info/warn/fatal may not exist, and `info`
# can be the GNU info reader.
tn_inst_info() { echo '[INFO] ' "$@"; }
tn_inst_warn() { echo '[WARN] ' "$@" >&2; }
tn_inst_fatal() { echo '[ERROR] ' "$@" >&2; exit 1; }

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

# ----------------------------------------------------------------- the release to install

# tn_inst_plan: applies the policy at the top. Reads INSTALL_K3S_VERSION / INSTALL_K3S_CHANNEL,
# sets and exports them for the installer. Sets TN_INST_INSTALLED, TN_INST_ACTION
# (install|same|keep|upgrade) and TN_INST_EFFECTIVE (the exact release that will run, or nothing
# when only a channel is known). Exits through tn_inst_fatal when it refuses.
tn_inst_plan() {
  tn_inst_installed_version
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
    -1) tn_inst_fatal "refusing to downgrade k3s from $TN_INST_INSTALLED to $_tn_t: k3s cannot go back (the datastore has been migrated by the newer release). Nothing was changed. Pin $TN_INST_INSTALLED or a newer release, or restore the device from a backup" ;;
  esac
  tn_inst_parse "$TN_INST_INSTALLED"; _tn_imaj=$TN_INST_MAJ; _tn_imin=$TN_INST_MIN
  tn_inst_parse "$_tn_t"
  if [ "$TN_INST_MAJ" != "$_tn_imaj" ] || [ $((TN_INST_MIN - _tn_imin)) -gt 1 ]; then
    if [ "${TN_K3S_FORCE:-}" = true ]; then
      tn_inst_warn "upgrading k3s from $TN_INST_INSTALLED to $_tn_t skips minor versions (TN_K3S_FORCE=true). Kubernetes supports one minor version at a time: this can leave the cluster in a state that does not start. Have a backup of the datastore"
    else
      tn_inst_fatal "refusing to upgrade k3s from $TN_INST_INSTALLED to $_tn_t: that is more than one minor version (v$_tn_imaj.$_tn_imin -> v$TN_INST_MAJ.$TN_INST_MIN). Upgrade one minor at a time (to v$_tn_imaj.$((_tn_imin + 1)).x first), or set TN_K3S_FORCE=true (tnctl bootstrap --force) if you know what you are doing. Nothing was changed"
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
# that will run; nothing = a channel, taken as new enough: stable and latest are >= v1.35).
tn_inst_cgroup() {
  _tn_cdir=${TN_K3S_CONFIG_DIR:-/etc/rancher/k3s}/config.yaml.d
  _tn_cfile=$_tn_cdir/$TN_INST_CGROUP_FILE
  _tn_cst=$(tn_inst_cgroup_state)
  if [ "$_tn_cst" = v2 ]; then
    tn_inst_cgroup_remove "$_tn_cfile" "this host uses cgroup v2"
    return 0
  fi
  _tn_newk3s=1
  if [ -n "${TN_INST_EFFECTIVE:-}" ] && tn_inst_cmp "$TN_INST_EFFECTIVE" v1.35.0 && [ "$TN_INST_CMP" = -1 ]; then _tn_newk3s=0; fi
  if [ "$_tn_cst" = v1 ]; then
    tn_inst_warn "this host uses cgroup v1 (or the hybrid layout). cgroup v1 is deprecated: Kubernetes >= 1.35 refuses to start the kubelet on it. Move the device to cgroup v2 (kernel command line systemd.unified_cgroup_hierarchy=1, or a newer OS release)"
  else
    tn_inst_info "cannot tell whether the device uses cgroup v1 (OS build, or no /sys/fs/cgroup): the kubelet flag is set to be safe, it is harmless on cgroup v2"
  fi
  if [ "$_tn_newk3s" = 0 ]; then
    tn_inst_info "k3s $TN_INST_EFFECTIVE is older than v1.35 and starts on cgroup v1 without help"
    tn_inst_cgroup_remove "$_tn_cfile" "k3s is older than v1.35"
    return 0
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
  mkdir -p "$_tn_cdir" && printf '%s\n' "$_tn_want" > "$_tn_cfile" ||
    tn_inst_fatal "cannot write $_tn_cfile: k3s >= v1.35 would not start on this cgroup v1 host"
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

# tn_inst_prepare ROLE: the release policy, then cgroup v1. Call it right before the k3s installer,
# after INSTALL_K3S_VERSION / INSTALL_K3S_CHANNEL / INSTALL_K3S_EXEC are set.
tn_inst_prepare() {
  tn_inst_info "Checking the k3s release and the cgroup version (${1:-server})"
  tn_inst_plan
  tn_inst_cgroup
}
