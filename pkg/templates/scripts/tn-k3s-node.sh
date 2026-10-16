# tn-k3s-node.sh: the node name, role labels and taints of a k3s node (tnctl bootstrap --role ...).
#
# Embedded in the server and the agent bootstrap script (only when the script was generated with a node
# role, label, taint or name), and usable stand-alone from a test. Usage:
#
#   . tn-k3s-install.sh; . tn-k3s-node.sh
#   tn_node_name NAME                 (the node name; optional)
#   tn_node_label key=value           (once per label; the role and robot labels come first)
#   tn_node_taint key[=value]:Effect  (once per taint)
#   tn_node_apply server|agent        (writes the k3s configuration; once, before the k3s installer)
#
# It needs tn_inst_config_ensure, tn_inst_yaml_has_key and tn_inst_info/warn from tn-k3s-install.sh.
# POSIX sh. It runs under `set -e` and `set -o noglob` and does not change shell options.
#
# What it writes (TN_K3S_CONFIG_DIR, /etc/rancher/k3s by default):
#   config.yaml                       node-name, added only when config.yaml has no node-name already: a
#                                     name the device owner set is kept (a different one is reported)
#   config.yaml.d/98-teknoir-node.yaml  node-label+ and node-taint+: the labels and taints of the role.
#                                     "+" APPENDS to the node-label and node-taint lists of config.yaml and
#                                     the other drop-ins, so the entries of the device owner are kept. The
#                                     file is ours: it is rewritten (atomically) when it differs and left
#                                     alone when it does not, so a re-run changes nothing.
#
# k3s applies node-label and node-taint ONLY when a node registers for the first time. When the node has
# registered already and the file changes (or is new), the script says so and prints the kubectl commands
# that apply the labels and taints to the running node; run them on the k3s server.
#
# Environment:
#   TN_K3S_CONFIG_DIR   /etc/rancher/k3s
#   DATA_DIR            the k3s data dir (/var/lib/rancher/k3s): <DATA_DIR>/agent/client-kubelet.crt exists
#                       once the kubelet of this node has registered
#   TN_NODE_REGISTERED  true / false: do not look at the data dir, take this as the answer (tests)

TN_K3S_NODE_LIB_VERSION=1
export TN_K3S_NODE_LIB_VERSION

TN_NODE_NAME=
TN_NODE_LABELS=
TN_NODE_TAINTS=
TN_NODE_DROPIN=98-teknoir-node.yaml

tn_node_name() { TN_NODE_NAME=$1; }
tn_node_label() { TN_NODE_LABELS="${TN_NODE_LABELS:+$TN_NODE_LABELS
}$1"; }
tn_node_taint() { TN_NODE_TAINTS="${TN_NODE_TAINTS:+$TN_NODE_TAINTS
}$1"; }

# tn_node_yaml_value FILE KEY: the value of the top-level scalar KEY (quotes and a trailing comment removed)
tn_node_yaml_value() {
  [ -r "$1" ] || return 0
  awk -v k="$2" '
    { sub(/\r$/, "") }
    $0 ~ ("^" k "[ \t]*:") {
      v = $0
      sub("^" k "[ \t]*:[ \t]*", "", v)
      sub(/[ \t]+#.*$/, "", v)
      gsub(/^["\047]|["\047]$/, "", v)
      print v
      exit
    }' "$1"
}

# tn_node_registered: has the kubelet of this node a client certificate, i.e. has the node registered
tn_node_registered() {
  case ${TN_NODE_REGISTERED:-} in
    true) return 0 ;;
    false) return 1 ;;
  esac
  [ -s "${DATA_DIR:-/var/lib/rancher/k3s}/agent/client-kubelet.crt" ]
}

# tn_node_dropin_text: the content of the labels and taints file
tn_node_dropin_text() {
  printf '%s\n' \
    '# Written by the Teknoir bootstrap script (tnctl bootstrap --role ...). Do not edit: a re-run rewrites it.' \
    '# "+" appends to the node-label and node-taint lists of config.yaml and the other drop-ins.' \
    '# k3s applies them only when the node registers for the first time.'
  if [ -n "$TN_NODE_LABELS" ]; then
    echo 'node-label+:'
    printf '%s\n' "$TN_NODE_LABELS" | while IFS= read -r _tn_l; do printf '  - "%s"\n' "$_tn_l"; done
  fi
  if [ -n "$TN_NODE_TAINTS" ]; then
    echo 'node-taint+:'
    printf '%s\n' "$TN_NODE_TAINTS" | while IFS= read -r _tn_t; do printf '  - "%s"\n' "$_tn_t"; done
  fi
}

# tn_node_kubectl_help NODE: how to apply the labels and taints to a node that has registered already
tn_node_kubectl_help() {
  _tn_kl=
  if [ -n "$TN_NODE_LABELS" ]; then
    _tn_kl=$(printf '%s\n' "$TN_NODE_LABELS" | tr '\n' ' ')
    tn_inst_warn "  k3s kubectl label node $1 ${_tn_kl}--overwrite"
  fi
  if [ -n "$TN_NODE_TAINTS" ]; then
    _tn_kl=$(printf '%s\n' "$TN_NODE_TAINTS" | tr '\n' ' ')
    tn_inst_warn "  k3s kubectl taint node $1 ${_tn_kl}--overwrite"
  fi
}

# tn_node_apply ROLE: write the node name, and the labels and taints, into the k3s configuration
tn_node_apply() {
  _tn_nrole=${1:-server}
  _tn_ncfg=${TN_K3S_CONFIG_DIR:-/etc/rancher/k3s}
  _tn_nyaml=$_tn_ncfg/config.yaml
  mkdir -p "$_tn_ncfg" || tn_inst_fatal "cannot create $_tn_ncfg"

  # the name of the node: a node-name in config.yaml wins (it is the owner's), a different one is reported
  if [ -n "$TN_NODE_NAME" ]; then
    if tn_inst_yaml_has_key "$_tn_nyaml" node-name; then
      _tn_have=$(tn_node_yaml_value "$_tn_nyaml" node-name)
      if [ "$_tn_have" != "$TN_NODE_NAME" ]; then
        tn_inst_warn "$_tn_nyaml sets node-name '$_tn_have', not '$TN_NODE_NAME' as asked for this $_tn_nrole: kept. The node keeps its name; edit the file to rename it (k3s registers a renamed node as a new one)"
      fi
    fi
    tn_inst_config_ensure "$_tn_nyaml" node-name "node-name: $TN_NODE_NAME"
  fi

  [ -n "$TN_NODE_LABELS$TN_NODE_TAINTS" ] || return 0

  _tn_nfile=$_tn_ncfg/config.yaml.d/$TN_NODE_DROPIN
  _tn_nwant=$(tn_node_dropin_text)
  if [ -f "$_tn_nfile" ] && [ "$(cat "$_tn_nfile")" = "$_tn_nwant" ]; then
    tn_inst_info "node labels and taints are already in place ($_tn_nfile)"
    return 0
  fi
  # atomically, and under a name that k3s does not read (it does not end in .yaml)
  _tn_ntmp=$_tn_ncfg/config.yaml.d/.$TN_NODE_DROPIN.tmp.$$
  if mkdir -p "$_tn_ncfg/config.yaml.d" && printf '%s\n' "$_tn_nwant" > "$_tn_ntmp" && mv -f "$_tn_ntmp" "$_tn_nfile"; then
    :
  else
    rm -f "$_tn_ntmp" 2>/dev/null || true
    tn_inst_fatal "cannot write $_tn_nfile"
  fi
  tn_inst_info "Wrote the node labels and taints of this $_tn_nrole to $_tn_nfile"

  if tn_node_registered; then
    _tn_nname=$(tn_node_yaml_value "$_tn_nyaml" node-name)
    _tn_nname=${_tn_nname:-${TN_NODE_NAME:-$(hostname | tr 'A-Z' 'a-z')}}
    tn_inst_warn "This node has registered with the cluster already, and k3s applies node-label and node-taint only when a node registers for the first time: the labels and taints above will NOT show up on it by themselves."
    tn_inst_warn "Apply them to the running node from the k3s server (this changes the Node object only; the file above keeps them if the node ever registers again):"
    tn_node_kubectl_help "$_tn_nname"
  fi
}
