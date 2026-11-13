#!/usr/bin/env bash
# triage.sh - read-only triage of one Teknoir robot, in five steps:
#   1. whoami            who you are and which teamspace you work in
#   2. the robot         phase, computers, disabled state, device
#   3. the problems      the failing apps and nodes of that robot
#   4. the config        published, deviations, overrides not applied, pending rows
#   5. the pods          up to three failing pods, when the access allows it
#
# Every command is a read: no write, no method other than GET, and inspect only through
# `tnctl inspect` (which never turns the robot's Shell on).
#
# Usage: triage.sh <robot> [-n <teamspace>] [--json]
set -euo pipefail

TNCTL="${TNCTL:-tnctl}"

usage() {
  cat <<'EOF'
triage.sh <robot> [-n <teamspace>] [--json]

Read-only triage of one Teknoir robot, in five steps:
  1. whoami                    who you are, and the teamspace
  2. the robot                 phase, computers, disabled state, device
  3. the problems              the failing apps and nodes of that robot
  4. the config                published, deviations, overrides not applied, pending rows
  5. the pods                  up to three failing pods, when the access allows it

  -n, --namespace <teamspace>  the teamspace (default: the one whoami reports)
  --json                       print one JSON object with a key per step
  -h, --help                   this text

Every command is a read. It needs tnctl on the PATH (override with TNCTL) and jq.
Exit codes: 0 it ran, 1 jq is missing, 2 usage, 6 whoami itself was refused.
EOF
}

die_usage() {
  printf 'triage: %s\n' "$*" >&2
  printf 'usage: triage.sh <robot> [-n <teamspace>] [--json]  (try --help)\n' >&2
  exit 2
}

robot=""
ts=""
json=0
while [ $# -gt 0 ]; do
  case "$1" in
    -h|--help)
      usage
      exit 0
      ;;
    --json)
      json=1
      shift
      ;;
    -n|--namespace)
      [ $# -ge 2 ] || die_usage "$1 needs a teamspace"
      ts=$2
      shift 2
      ;;
    -n=*|--namespace=*)
      ts=${1#*=}
      shift
      ;;
    --)
      shift
      [ $# -eq 1 ] || die_usage "give exactly one robot"
      robot=$1
      shift
      ;;
    -*)
      die_usage "unknown option: $1"
      ;;
    *)
      [ -z "$robot" ] || die_usage "give exactly one robot, not \"$robot\" and \"$1\""
      robot=$1
      shift
      ;;
  esac
done
[ -n "$robot" ] || die_usage "give a robot"

if ! command -v jq >/dev/null 2>&1; then
  printf 'triage: jq is required and is not on the PATH; install jq, or put it on the PATH.\n' >&2
  exit 1
fi

tmp="$(mktemp -d "${TMPDIR:-/tmp}/triage.XXXXXX")"
trap 'rm -rf "$tmp"' EXIT
# Each step's answer lives in a file, never in an argv string: a config answer can be far
# bigger than the 128 KiB Linux allows in one argument.
for s in whoami robot problems config inspect; do
  : > "$tmp/$s.lines"
  printf 'null' > "$tmp/$s.data"
done

# The -n arguments every command after whoami carries, once the teamspace is known.
TARGS=()

step=""
lines=0

section() {  # <step> <heading>
  step=$1
  lines=0
  : > "$tmp/$1.lines"
  if [ "$json" -eq 0 ]; then
    printf '\n== %s\n' "$2"
  fi
}

line() {  # <text>: at most ten lines per section
  [ "$lines" -lt 10 ] || return 0
  lines=$((lines + 1))
  printf '%s\n' "$1" >> "$tmp/$step.lines"
  if [ "$json" -eq 0 ]; then
    printf '%s\n' "$1"
  fi
}

CALLRC=0
CALLOUT=""
CALLERR=""
CALLCODE=""
CALLRETRY=""

call() {  # <command...>: never fails the script; sets CALLRC, CALLOUT, CALLERR, CALLCODE and CALLRETRY
  CALLRC=0
  CALLOUT=""
  CALLERR=""
  CALLCODE=""
  CALLRETRY=""
  : > "$tmp/stderr"
  CALLOUT=$("$@" 2>"$tmp/stderr") || CALLRC=$?
  # Every call passes -o json, so tnctl writes its error as one JSON object on stderr. Read the
  # last non-blank line: a notice printed before it (a default configuration file being created)
  # must not hide the error.
  last=$(grep -v '^[[:space:]]*$' "$tmp/stderr" 2>/dev/null | tail -n 1 || true)
  if printf '%s' "$last" | jq -e '.error | type == "object"' >/dev/null 2>&1; then
    CALLERR=$(printf '%s' "$last" | jq -r '.error.message // ""')
    CALLCODE=$(printf '%s' "$last" | jq -r '.error.code // ""')
    CALLRETRY=$(printf '%s' "$last" | jq -r '.error.retryAfter // ""')
  else
    first=$(grep -m 1 '^Error: ' "$tmp/stderr" 2>/dev/null || true)
    CALLERR=${first:-$last}
    CALLERR=${CALLERR#"Error: "}
  fi
  return 0
}

failed_text() {  # <command> <rc> <code> <retryAfter> <message>
  local m
  m="failed: $1 exited $2"
  [ -z "$3" ] || m="$m ($3)"
  m="$m: $5"
  [ -z "$4" ] || m="$m - retry after ${4}s"
  printf '%s' "$m"
}

is_json() {  # <text>: true when it parses as JSON
  printf '%s' "$1" | jq -e . >/dev/null 2>&1
}

notrun=""
who_ok=0
who_rc=0
who_err=""
who_code=""
who_retry=""
mode=""
access=""
rob_ok=0
rob_rc=0
rob_err=""
rob_code=""
rob_retry=""
pro_ok=0
pro_rc=0
pro_err=""
pro_code=""
pro_retry=""
con_ok=0
con_rc=0
con_err=""
con_code=""
con_retry=""
ins_ok=0
ins_rc=0
ins_err=""
ins_code=""
ins_retry=""

step_obj() {  # <step> <ok 0|1> <exit code> <error> <code> <retryAfter>
  local err=$4
  # A step that never ran (the script stopped at whoami) says so.
  if [ -z "$err" ] && [ "$2" = "0" ] && [ "$3" = "0" ] && [ -n "$notrun" ]; then
    err=$notrun
  fi
  jq -n --arg name "$1" --arg ok "$2" --argjson rc "$3" --arg err "$err" \
    --arg code "$5" --arg retry "$6" \
    --rawfile summary "$tmp/$1.lines" --slurpfile data "$tmp/$1.data" \
    '{ok: ($ok == "1"), exitCode: $rc, error: (if $err == "" then null else $err end),
      code: (if $code == "" then null else $code end),
      retryAfter: (if $retry == "" then null else ($retry | tonumber? // null) end),
      summary: ($summary | split("\n") | map(select(length > 0))), data: $data[0]}'
}

print_json() {
  step_obj whoami "$who_ok" "$who_rc" "$who_err" "$who_code" "$who_retry" > "$tmp/whoami.step"
  step_obj robot "$rob_ok" "$rob_rc" "$rob_err" "$rob_code" "$rob_retry" > "$tmp/robot.step"
  step_obj problems "$pro_ok" "$pro_rc" "$pro_err" "$pro_code" "$pro_retry" > "$tmp/problems.step"
  step_obj config "$con_ok" "$con_rc" "$con_err" "$con_code" "$con_retry" > "$tmp/config.step"
  step_obj inspect "$ins_ok" "$ins_rc" "$ins_err" "$ins_code" "$ins_retry" > "$tmp/inspect.step"
  jq -n --arg target "$robot" --arg teamspace "$ts" \
    --slurpfile whoami "$tmp/whoami.step" --slurpfile robot "$tmp/robot.step" \
    --slurpfile problems "$tmp/problems.step" --slurpfile config "$tmp/config.step" \
    --slurpfile inspect "$tmp/inspect.step" \
    '{target: $target, teamspace: $teamspace, whoami: $whoami[0], robot: $robot[0],
      problems: $problems[0], config: $config[0], inspect: $inspect[0]}'
}

# ---------------------------------------------------------------- 1. whoami

section whoami "whoami"
call "$TNCTL" whoami -o json
who_rc=$CALLRC
who_err=$CALLERR
who_code=$CALLCODE
who_retry=$CALLRETRY

if [ "$who_rc" -ne 0 ]; then
  line "$(failed_text 'tnctl whoami' "$who_rc" "$who_code" "$who_retry" "$who_err")"
elif ! is_json "$CALLOUT"; then
  line "failed: tnctl whoami did not answer JSON"
  who_rc=1
  who_err="whoami did not answer JSON"
else
  who_ok=1
  printf '%s' "$CALLOUT" | jq -c . > "$tmp/whoami.data"
  mode=$(printf '%s' "$CALLOUT" | jq -r '.mode // ""')
  case "$mode" in
    bot)
      access=$(printf '%s' "$CALLOUT" | jq -r '.bot.access // ""')
      bot_ts=$(printf '%s' "$CALLOUT" | jq -r '.bot.namespace // ""')
      bot_name=$(printf '%s' "$CALLOUT" | jq -r '.bot.name // ""')
      bot_unit=$(printf '%s' "$CALLOUT" | jq -r '.bot.unit // "none"')
      creator=$(printf '%s' "$CALLOUT" | jq -r '.creator.email // ""')
      [ -n "$ts" ] || ts=$bot_ts
      line "mode: bot, access: ${access:-unknown}"
      line "bot: ${bot_name:-?} for ${creator:-?}, unit: ${bot_unit:-none}"
      ;;
    user)
      user_ts=$(printf '%s' "$CALLOUT" | jq -r '.namespace // ""')
      email=$(printf '%s' "$CALLOUT" | jq -r '.email // ""')
      [ -n "$ts" ] || ts=$user_ts
      line "mode: user, email: ${email:-?}"
      ;;
    *)
      line "mode: ${mode:-unknown}"
      ;;
  esac
  line "teamspace: ${ts:-unknown}"
fi

if [ "$who_rc" -eq 6 ]; then
  if [ "$json" -eq 1 ]; then
    notrun="not run: whoami was refused (exit 6)"
    print_json
  fi
  printf 'triage: whoami was refused (exit 6): %s\n' "$who_err" >&2
  exit 6
fi

if [ -n "$ts" ]; then
  TARGS=(-n "$ts")
fi

# ---------------------------------------------------------------- 2. the robot

section robot "robot $robot"
call "$TNCTL" robot "$robot" ${TARGS[@]+"${TARGS[@]}"} -o json
rob_rc=$CALLRC
rob_err=$CALLERR
rob_code=$CALLCODE
rob_retry=$CALLRETRY
if [ "$rob_rc" -ne 0 ]; then
  line "$(failed_text 'tnctl robot' "$rob_rc" "$rob_code" "$rob_retry" "$rob_err")"
elif ! is_json "$CALLOUT"; then
  line "failed: tnctl robot did not answer JSON"
  rob_rc=1
  rob_err="robot did not answer JSON"
else
  rob_ok=1
  printf '%s' "$CALLOUT" | jq -c . > "$tmp/robot.data"
  while IFS= read -r l; do
    line "$l"
  done < <(printf '%s' "$CALLOUT" | jq -r '
    "phase: \(.phase // "unknown")",
    (if (.phaseMessage // "") == "" then empty else "message: \(.phaseMessage)" end),
    "nodes: \(([.nodes[]? | select(.ready == true)] | length))/\((.nodes // []) | length) ready, \(([.nodes[]? | select(.joined == true)] | length)) joined",
    "device: \(.device // "-"), type: \(.type // "-")",
    (if .disabled then "disabled: \(.disabled.state // "yes"), by \(.disabled.by // "?")\(if (.disabled.reason // "") != "" then ": " + .disabled.reason else "" end)" else "disabled: no" end),
    "overrides: \(.overrides // 0), deviations: \(.deviations // 0)",
    "fleets: \(if ((.fleets // []) | length) == 0 then "none" else (.fleets | join(", ")) end)"
  ')
  if [ "${#TARGS[@]}" -gt 0 ]; then
    call "$TNCTL" api GET "$ts/robots/$robot/access" ${TARGS[@]+"${TARGS[@]}"} -o json
    if [ "$CALLRC" -eq 0 ] && is_json "$CALLOUT"; then
      while IFS= read -r l; do
        line "$l"
      done < <(printf '%s' "$CALLOUT" | jq -r '
        "tunnel: \(.tunnel.state // "?"), connected: \(.tunnel.connected // false)",
        "shell: \(.terminal.state // "?")"
      ')
    else
      line "$(failed_text "tnctl api GET $ts/robots/$robot/access" "$CALLRC" "$CALLCODE" "$CALLRETRY" "$CALLERR")"
    fi
  fi
fi

# ---------------------------------------------------------------- 3. the problems

section problems "problems"
pro_call_ok=0
if [ -z "$ts" ]; then
  line "skipped: the teamspace is unknown (whoami failed and -n was not given)"
  pro_err="skipped: the teamspace is unknown"
else
  call "$TNCTL" api GET "$ts/problems?robot=$robot" ${TARGS[@]+"${TARGS[@]}"} -o json
  pro_rc=$CALLRC
  pro_err=$CALLERR
  pro_code=$CALLCODE
  pro_retry=$CALLRETRY
  if [ "$pro_rc" -ne 0 ]; then
    line "$(failed_text 'tnctl api GET problems' "$pro_rc" "$pro_code" "$pro_retry" "$pro_err")"
  elif ! is_json "$CALLOUT"; then
    line "failed: the problems answer is not JSON"
    pro_rc=1
    pro_err="the problems answer is not JSON"
  else
    pro_ok=1
    pro_call_ok=1
    printf '%s' "$CALLOUT" | jq -c . > "$tmp/problems.data"
    while IFS= read -r l; do
      line "$l"
    done < <(printf '%s' "$CALLOUT" | jq -r '
      def problem:
        [.severity, (.kind + ":"), (.app // .node // .device // "?"), (.namespace // ""),
         (if ((.pods // []) | length) > 0 then "pods=" + (.pods | join(",")) else "" end),
         ((.reason // .detail) // "")]
        | map(select(. != "")) | join(" ");
      (if (.source // "prometheus") == "unavailable"
        then "source: unavailable - Prometheus could not be asked, so the problems are unknown, not absent"
        else "source: \(.source // "prometheus")" end),
      "total: \(.problems | length), \(([.problems[] | select(.severity == "critical")] | length)) critical",
      (([.problems[]? | problem] | .[0:7])[]),
      (if ((.problems // []) | length) > 7 then "and \(((.problems // []) | length) - 7) more" else empty end)
    ')
  fi
fi

# ---------------------------------------------------------------- 4. the config

section config "config $robot"
call "$TNCTL" config get "$robot" ${TARGS[@]+"${TARGS[@]}"} -o json
con_rc=$CALLRC
con_err=$CALLERR
con_code=$CALLCODE
con_retry=$CALLRETRY
if [ "$con_rc" -ne 0 ]; then
  line "$(failed_text 'tnctl config get' "$con_rc" "$con_code" "$con_retry" "$con_err")"
elif ! is_json "$CALLOUT"; then
  line "failed: the configuration answer is not JSON"
  con_rc=1
  con_err="the configuration answer is not JSON"
else
  con_ok=1
  printf '%s' "$CALLOUT" | jq -c . > "$tmp/config.data"
  while IFS= read -r l; do
    line "$l"
  done < <(printf '%s' "$CALLOUT" | jq -r '
    def top(list; n; f): ([list[] | f] | .[0:n] | join("; "));
    def rest(list; n): if (list | length) > n then " (+\((list | length) - n) more)" else "" end;
    . as $v
    | ($v.configuration.deviations // []) as $dev
    | ($v.configuration.warnings // []) as $warn
    | ([$v.overrideStatus[]? | select(.state == "invalid" or .state == "no-target" or .state == "not-applicable")]) as $bad
    | ([$v.configuration.effective[]? | select(.status == "pending")]) as $pend
    | [
        (if $v.published != null then
           "published: \($v.published.hash // "?") at \($v.published.lastChange // "?")"
           + (if (($v.published.message // "") | length) > 0 then " - " + $v.published.message else "" end)
         else
           "published: never - the controller has not published a configuration (the device is not connected, or has none)"
         end),
        "deviations: \($dev | length)"
          + (if ($dev | length) > 0 then " - " + top($dev; 3; "\(.identity) (\(.reason))") + rest($dev; 3) else "" end),
        "warnings: \($warn | length)"
          + (if ($warn | length) > 0 then " - " + top($warn; 3; .) + rest($warn; 3) else "" end),
        "overrides not applied: \($bad | length)"
          + (if ($bad | length) > 0 then " - " + top($bad; 3; "\(.target) (\(.state))\(if (.message // "") != "" then ": " + .message else "" end)") + rest($bad; 3) else "" end),
        "pending in the published config: \($pend | length)"
          + (if ($pend | length) > 0 then " - " + top($pend; 3; .identity) + rest($pend; 3) else "" end)
      ][]
  ')
fi

# ---------------------------------------------------------------- 5. the pods

section inspect "inspect"
may_inspect=0
if [ "$mode" = "user" ]; then
  may_inspect=1
elif [ "$mode" = "bot" ] && [ "$access" = "creator" ]; then
  may_inspect=1
fi

if [ "$may_inspect" -eq 0 ]; then
  line "skipped: inspect needs a person (mode user) or a bot at access creator (mode ${mode:-unknown}, access ${access:-unknown})"
  ins_err="skipped: inspect needs a person or a bot at access creator"
elif [ "$pro_call_ok" -eq 0 ]; then
  line "skipped: the problems of the robot are unknown, so no failing pod is named"
  ins_err="skipped: the problems are unknown"
else
  podlist=()
  while IFS= read -r p; do
    if [ -n "$p" ]; then podlist+=("$p"); fi
  done < <(jq -r '
    [.problems[]?
     | select(.kind == "image-pull" or .kind == "crash-loop" or .kind == "not-running" or .kind == "not-ready")
     | . as $p
     | ($p.pods // [])[]
     | "\($p.namespace // "default")/\(.)"]
    | unique | .[0:3] | .[]' "$tmp/problems.data")
  if [ "${#podlist[@]}" -eq 0 ]; then
    ins_ok=1
    line "nothing to read: no image-pull, crash-loop, not-running or not-ready problem names a pod"
  fi
  pod_count=0
  ins_files=()
  for pod in ${podlist[@]+"${podlist[@]}"}; do
    [ -n "$pod" ] || continue
    pod_count=$((pod_count + 1))
    call "$TNCTL" inspect pod "$robot" "$pod" ${TARGS[@]+"${TARGS[@]}"} -o json
    if [ "$CALLRC" -ne 0 ]; then
      line "$pod: $(failed_text 'tnctl inspect pod' "$CALLRC" "$CALLCODE" "$CALLRETRY" "$CALLERR")"
      ins_rc=$CALLRC
      ins_err=$CALLERR
      ins_code=$CALLCODE
      ins_retry=$CALLRETRY
      continue
    fi
    if ! is_json "$CALLOUT"; then
      line "$pod: failed: the inspect answer is not JSON"
      ins_rc=1
      ins_err="the inspect answer is not JSON"
      continue
    fi
    ins_ok=1
    printf '%s' "$CALLOUT" > "$tmp/inspect-$pod_count.json"
    ins_files+=("$tmp/inspect-$pod_count.json")
    while IFS= read -r l; do
      line "$pod: $l"
    done < <(printf '%s' "$CALLOUT" | jq -r '
      def readyc: ([.pod.containers[]? | select(.ready == true)] | length);
      def names: ([.pod.containers[]? | select(.ready != true) | .name] | join(","));
      "phase \(.pod.phase // "?"), containers \(readyc)/\((.pod.containers // []) | length) ready, restarts \(.pod.restarts // 0)"
        + (if (names | length) > 0 then ", not ready: " + names else "" end),
      (([.events[]? | select(.type == "Warning") | "\(.reason // "?")\(if (.message // "") != "" then ": " + .message else "" end)"] | .[0:2] | join("; ")) as $w
        | if ($w | length) > 0 then "warnings: " + $w else empty end)
    ')
  done
  if [ "${#ins_files[@]}" -gt 0 ]; then
    jq -s '.' ${ins_files[@]+"${ins_files[@]}"} > "$tmp/inspect.data"
  else
    printf '[]' > "$tmp/inspect.data"
  fi
fi

if [ "$json" -eq 1 ]; then
  print_json
fi
exit 0
