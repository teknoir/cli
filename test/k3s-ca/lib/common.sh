# shellcheck shell=bash
# Common helpers for the k3s custom-CA harness: logging, k3s version maths,
# expected outcomes, results and the job pool. Sourced by run.sh; also sourced
# by harness_test.go to unit-test the pure functions.

log()  { printf '%s [%s] %s\n' "$(date +%H:%M:%S)" "${CASE:-run}" "$*" >&2; }
warn() { log "WARN: $*"; }
die()  { log "ERROR: $*"; exit 2; }

# ---------------------------------------------------------------- versions

# Strip +k3sN / -k3sN and a leading v: v1.34.11+k3s1 -> 1.34.11
ver_core() { local v=${1#v}; v=${v%%+*}; v=${v%%-k3s*}; printf '%s' "$v"; }

# Docker tag of rancher/k3s: v1.34.11, 1.34.11, v1.34.11+k3s1, v1.34.11-k3s1 -> v1.34.11-k3s1
ver_tag() {
  local in=$1 core suffix=k3s1
  core=$(ver_core "$in")
  case $in in
    *+k3s*) suffix=k3s${in##*+k3s} ;;
    *-k3s*) suffix=k3s${in##*-k3s} ;;
  esac
  printf 'v%s-%s' "$core" "$suffix"
}

# Short DNS/label safe form: v1.34.11 -> 1-34-11
ver_slug() { ver_core "$1" | tr . -; }

ver_minor() { local c; c=$(ver_core "$1"); c=${c#*.}; printf '%s' "${c%%.*}"; }
ver_patch() { local c; c=$(ver_core "$1"); printf '%s' "${c##*.}"; }

# First patch release of each minor that carries the CA hash validation fix
# (k3s-io/k3s#14699, backports #14708). Minors above the table are fixed,
# minors below are end of life and unfixed.
ver_fixed_patch() {
  case $1 in
    34) echo 12 ;; 35) echo 9 ;; 36) echo 5 ;; 37) echo 1 ;;
    *) echo "" ;;
  esac
}

# ver_is_postfix VERSION: exit 0 if VERSION rejects CAs without keyCertSign,
# 1 if it does not, 2 if unknown (minor below 1.34: end of life, no backport known).
ver_is_postfix() {
  local minor patch fix
  minor=$(ver_minor "$1"); patch=$(ver_patch "$1")
  fix=$(ver_fixed_patch "$minor")
  if [ -n "$fix" ]; then
    [ "$patch" -ge "$fix" ] && return 0 || return 1
  fi
  [ "$minor" -gt 37 ] && return 0
  return 2
}

# The pre-fix patch of the same minor, used by the downgrade fallback. A pinned
# map beats guessing: it is the last release before the fix.
ver_prefix_of() {
  case $(ver_minor "$1") in
    34) echo v1.34.11 ;; 35) echo v1.35.8 ;; 36) echo v1.36.4 ;; 37) echo v1.37.0 ;;
    *) return 1 ;;
  esac
}

# Resolve names to versions. stable/latest come from update.k3s.io, vMAJOR.MINOR
# (e.g. v1.33) to the newest patch of that channel.
resolve_version() {
  local v=$1 loc out
  case $v in
    stable|latest)
      if [ "$v" = stable ]; then
        loc=$(curl -fsS -m 20 -D- -o /dev/null https://update.k3s.io/v1-release/channels/stable | tr -d '\r' | sed -n 's/^[Ll]ocation: //p')
        out=${loc##*/tag/}; out=${out//%2B/+}; out=${out//%2b/+}
      else
        out=$(channel_latest latest)
      fi
      [ -n "$out" ] || die "cannot resolve k3s channel '$v' (network?)"
      printf '%s\n' "$(ver_tag "$out" | sed 's/-k3s/+k3s/')" ;;
    v[0-9]*.[0-9]*|[0-9]*.[0-9]*)
      case $(ver_core "$v") in
        *.*.*) printf '%s\n' "$v" ;;
        *) out=$(channel_latest "v${v#v}")
           [ -n "$out" ] || die "cannot resolve k3s channel '$v'"
           printf '%s\n' "$out" ;;
      esac ;;
    *) printf '%s\n' "$v" ;;
  esac
}

channel_latest() {
  curl -fsS -m 20 https://update.k3s.io/v1-release/channels 2>/dev/null |
    tr '{' '\n' | grep "\"name\":\"$1\"" | sed -n 's/.*"latest":"\([^"]*\)".*/\1/p' | head -1
}

# Resolve and deduplicate a list of version words. Echoes one tag per line.
resolve_versions() {
  local v r seen=" "
  for v in "$@"; do
    r=$(resolve_version "$v") || return 1
    r=$(ver_tag "$r")
    case $seen in *" $r "*) continue ;; esac
    seen="$seen$r "
    printf '%s\n' "$r"
  done
}

# ---------------------------------------------------------------- expectations

# expected_outcome CA_MODE GEN K3S_VERSION -> READY | FAIL | ANY
#  - proper and lib roots carry keyCertSign: READY everywhere.
#  - legacy copies the device cert verbatim: READY on pre-fix k3s, FAIL on post-fix k3s,
#    unless the device cert is one of the fixed generators (it then has keyCertSign).
expected_outcome() {
  local ca=$1 gen=$2 ver=$3 rc=0
  case $ca in
    proper|lib) echo READY; return ;;
  esac
  case $gen in
    fixed-*) echo READY; return ;;
  esac
  ver_is_postfix "$ver" || rc=$?
  case $rc in
    0) echo FAIL ;;
    1) echo READY ;;
    *) echo ANY ;;
  esac
}

verdict_for() { # expect outcome
  case $1 in
    ANY) echo INFO ;;
    *) [ "$1" = "$2" ] && echo PASS || echo MISMATCH ;;
  esac
}

# ---------------------------------------------------------------- results

RESULT_HEADER=$'suite\tcase\tk3s\tgen\tca\texpect\toutcome\tverdict\tsecs\tnote'

# result_write SUITE CASE K3S GEN CA EXPECT OUTCOME SECS [NOTE]
# One file per call so parallel jobs never interleave.
result_write() {
  local suite=$1 name=$2 k3s=$3 gen=$4 ca=$5 expect=$6 outcome=$7 secs=$8 note=${9:-} verdict
  case $outcome in
    SKIP) verdict=SKIP ;;
    ERROR) verdict=ERROR ;;
    *) verdict=$(verdict_for "$expect" "$outcome") ;;
  esac
  note=${note//$'\t'/ }; note=${note//$'\n'/ }
  mkdir -p "$OUT/results.d"
  printf '%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n' \
    "$suite" "$name" "$k3s" "$gen" "$ca" "$expect" "$outcome" "$verdict" "$secs" "$note" \
    > "$OUT/results.d/$(date +%s%N)-$name.tsv"
  log "result $name: expect=$expect outcome=$outcome verdict=$verdict ${note:+($note)}"
}

results_merge() {
  { printf '%s\n' "$RESULT_HEADER"
    local f
    for f in "$OUT"/results.d/*.tsv; do [ -e "$f" ] && cat "$f"; done | sort -t$'\t' -k1,1 -k2,2
  } > "$OUT/results.tsv"
}

now() { date +%s; }

# ---------------------------------------------------------------- job pool

JOBS=${JOBS:-4}
DRY=${DRY:-0}
JOB_NAMES=() JOB_PIDS=()

# pool_wait: wait for the cases only (a bare `wait` would also wait for the tee of run.log)
pool_wait() { [ "${#JOB_PIDS[@]}" -eq 0 ] || wait "${JOB_PIDS[@]}" 2>/dev/null || true; }

# pool_run NAME FUNCTION ARGS... : run FUNCTION in a background subshell, at most $JOBS at a time.
# Output goes to $OUT/logs/NAME.log. With DRY=1 (--list) it only prints the case.
pool_run() {
  local name=$1; shift
  if [ "$DRY" = 1 ]; then log "would run: $name ($*)"; return 0; fi
  while [ "$(jobs -rp | wc -l)" -ge "$JOBS" ]; do wait -n 2>/dev/null || true; done
  mkdir -p "$OUT/logs"
  (
    set +e
    CASE=$name
    "$@"
  ) > "$OUT/logs/$name.log" 2>&1 &
  JOB_NAMES+=("$name")
  JOB_PIDS+=("$!")
  log "started $name"
}
