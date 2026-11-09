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
Exit codes: 0 it ran, 1 a tool is missing, 2 usage, 6 whoami itself was refused.
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
for s in whoami robot problems config inspect; do
  : > "$tmp/$s.lines"
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

call() {  # <command...>: never fails the script; sets CALLRC, CALLOUT and CALLERR
  CALLRC=0
  CALLOUT=""
  CALLERR=""
  : > "$tmp/stderr"
  CALLOUT=$("$@" 2>"$tmp/stderr") || CALLRC=$?
  CALLERR=$(head -n 1 "$tmp/stderr" 2>/dev/null || true)
  CALLERR=${CALLERR#"Error: "}
  return 0
}

is_json() {  # <text>: true when it parses as JSON
  printf '%s' "$1" | jq -e . >/dev/null 2>&1
}

notrun=""
who_ok=0
who_rc=0
who_err=""
who_data=null
mode=""
access=""
rob_ok=0
rob_rc=0
rob_err=""
rob_data=null
pro_ok=0
pro_rc=0
pro_err=""
pro_data=null
con_ok=0
con_rc=0
con_err=""
con_data=null
ins_ok=0
ins_rc=0
ins_err=""
ins_data=null

step_obj() {  # <step> <ok 0|1> <exit code> <error> <data json>
  local err=$4
  # A step that never ran (the script stopped at whoami) says so.
  if [ -z "$err" ] && [ "$2" = "0" ] && [ "$3" = "0" ] && [ -n "$notrun" ]; then
    err=$notrun
  fi
  jq -n --arg name "$1" --arg ok "$2" --argjson rc "$3" --arg err "$err" \
    --rawfile summary "$tmp/$1.lines" --argjson data "$5" \
    '{ok: ($ok == "1"), exitCode: $rc, error: (if $err == "" then null else $err end),
      summary: ($summary | split("\n") | map(select(length > 0))), data: $data}'
}

print_json() {
  local who rob pro con ins
  who=$(step_obj whoami "$who_ok" "$who_rc" "$who_err" "$who_data")
  rob=$(step_obj robot "$rob_ok" "$rob_rc" "$rob_err" "$rob_data")
  pro=$(step_obj problems "$pro_ok" "$pro_rc" "$pro_err" "$pro_data")
  con=$(step_obj config "$con_ok" "$con_rc" "$con_err" "$con_data")
  ins=$(step_obj inspect "$ins_ok" "$ins_rc" "$ins_err" "$ins_data")
  jq -n --arg target "$robot" --arg teamspace "$ts" \
    --argjson whoami "$who" --argjson robot "$rob" --argjson problems "$pro" \
    --argjson config "$con" --argjson inspect "$ins" \
    '{target: $target, teamspace: $teamspace, whoami: $whoami, robot: $robot,
      problems: $problems, config: $config, inspect: $inspect}'
}

# ---------------------------------------------------------------- 1. whoami

section whoami "whoami"
call "$TNCTL" whoami -o json
who_rc=$CALLRC
who_err=$CALLERR

if [ "$who_rc" -ne 0 ]; then
  line "failed: tnctl whoami exited $who_rc: $who_err"
elif ! is_json "$CALLOUT"; then
  line "failed: tnctl whoami did not answer JSON"
  who_rc=1
  who_err="whoami did not answer JSON"
else
  who_ok=1
  who_data=$(printf '%s' "$CALLOUT" | jq -c .)
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
call "$TNCTL" robot "$robot" "${TARGS[@]}" -o json
rob_rc=$CALLRC
rob_err=$CALLERR
if [ "$rob_rc" -ne 0 ]; then
  line "failed: tnctl robot exited $rob_rc: $rob_err"
elif ! is_json "$CALLOUT"; then
  line "failed: tnctl robot did not answer JSON"
  rob_rc=1
  rob_err="robot did not answer JSON"
else
  rob_ok=1
  rob_data=$(printf '%s' "$CALLOUT" | jq -c .)
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
    call "$TNCTL" api GET "$ts/robots/$robot/access" "${TARGS[@]}" -o json
    if [ "$CALLRC" -eq 0 ] && is_json "$CALLOUT"; then
      while IFS= read -r l; do
        line "$l"
      done < <(printf '%s' "$CALLOUT" | jq -r '
        "tunnel: \(.tunnel.state // "?"), connected: \(.tunnel.connected // false)",
        "shell: \(.terminal.state // "?")"
      ')
    else
      line "failed: tnctl api GET robots/$robot/access exited $CALLRC: $CALLERR"
    fi
  fi
fi

# ---------------------------------------------------------------- 3. the problems

section problems "problems"
pro_call_ok=0
if [ -z "$ts" ]; then
  line "skipped: the teamspace is unknown (whoami failed and -n was not given)"
else
  call "$TNCTL" api GET "$ts/problems?robot=$robot" "${TARGS[@]}" -o json
  pro_rc=$CALLRC
  pro_err=$CALLERR
  if [ "$pro_rc" -ne 0 ]; then
    line "failed: tnctl api GET problems exited $pro_rc: $pro_err"
  elif ! is_json "$CALLOUT"; then
    line "failed: the problems answer is not JSON"
    pro_rc=1
    pro_err="the problems answer is not JSON"
  else
    pro_ok=1
    pro_call_ok=1
    pro_data=$(printf '%s' "$CALLOUT" | jq -c .)
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
      (([.problems[]? | problem] | .[0:8])[]),
      (if ((.problems // []) | length) > 8 then "and \(((.problems // []) | length) - 8) more" else empty end)
    ')
  fi
fi

# ---------------------------------------------------------------- 4. the config

section config "config $robot"
call "$TNCTL" config get "$robot" "${TARGS[@]}" -o json
con_rc=$CALLRC
con_err=$CALLERR
if [ "$con_rc" -ne 0 ]; then
  line "failed: tnctl config get exited $con_rc: $con_err"
elif ! is_json "$CALLOUT"; then
  line "failed: the configuration answer is not JSON"
  con_rc=1
  con_err="the configuration answer is not JSON"
else
  con_ok=1
  con_data=$(printf '%s' "$CALLOUT" | jq -c .)
  while IFS= read -r l; do
    line "$l"
  done < <(printf '%s' "$CALLOUT" | jq -r '
    def top(list; n; f): ([list[] | f] | .[0:n] | join("; "));
    def rest(list; n): if (list | length) > n then " (+\((list | length) - n) more)" else "" end;
    . as $v
    | ($v.configuration.deviations // []) as $dev
    | ($v.configuration.warnings // []) as $warn
    | ([$v.overrideStatus[]? | select(.state == "invalid" or .state == "no-target")]) as $bad
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
elif [ "$pro_call_ok" -eq 0 ]; then
  line "skipped: the problems of the robot are unknown, so no failing pod is named"
else
  mapfile -t podlist < <(printf '%s' "$pro_data" | jq -r '
    [.problems[]?
     | select(.kind == "image-pull" or .kind == "crash-loop" or .kind == "not-running" or .kind == "not-ready")
     | . as $p
     | ($p.pods // [])[]
     | "\($p.namespace // "default")/\(.)"]
    | unique | .[0:3] | .[]')
  if [ "${#podlist[@]}" -eq 0 ]; then
    line "nothing to read: no image-pull, crash-loop, not-running or not-ready problem names a pod"
  fi
  pod_count=0
  ins_files=()
  for pod in "${podlist[@]}"; do
    [ -n "$pod" ] || continue
    pod_count=$((pod_count + 1))
    call "$TNCTL" inspect pod "$robot" "$pod" "${TARGS[@]}" -o json
    if [ "$CALLRC" -ne 0 ]; then
      line "$pod: failed: tnctl inspect pod exited $CALLRC: $CALLERR"
      ins_rc=$CALLRC
      ins_err=$CALLERR
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
  if [ "$pod_count" -eq 0 ]; then
    line "no failing pod to read"
  fi
  if [ "${#ins_files[@]}" -gt 0 ]; then
    ins_data=$(jq -s '.' "${ins_files[@]}")
  fi
fi

if [ "$json" -eq 1 ]; then
  print_json
fi
exit 0
