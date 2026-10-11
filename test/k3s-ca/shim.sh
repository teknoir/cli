#!/bin/sh
# shim.sh - the environment the device bootstrap scripts run in: the relevant
# parts of pkg/templates/partials/header.sh.tmpl (strict mode, noglob, umask,
# info/warn/fatal). Sourced in the helper containers before the CA code, so the
# library is exercised under the same shell options as on a real device.
set -e
set -o noglob
umask 027
info() { echo '[INFO] ' "$@"; }
warn() { echo '[WARN] ' "$@" >&2; }
fatal() { echo '[ERROR] ' "$@" >&2; exit 1; }
