#!/usr/bin/env bash
# Tests for job B's script: skills/teknoir-debug/scripts/triage.sh.
# No platform and no cluster: a stub tnctl on a temporary PATH logs every argv and answers from the
# fixture JSON of the case, chosen by the stub's first arguments.
#
# Usage: bash agentkit/test/scripts-b.sh
set -uo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
agentkit="$(cd "$here/.." && pwd)"
script="$agentkit/skills/teknoir-debug/scripts/triage.sh"

work="$(mktemp -d "${TMPDIR:-/tmp}/triage-test.XXXXXX")"
trap 'rm -rf "$work"' EXIT

bin="$work/bin"
mkdir -p "$bin"
export PATH="$bin:$PATH"

fails=0
passes=0
ok() { printf 'ok   %s\n' "$*"; passes=$((passes + 1)); }
fail() { printf 'FAIL %s\n' "$*"; fails=$((fails + 1)); }

check_eq() {  # label want got
  if [ "$2" = "$3" ]; then ok "$1"; else fail "$1: want [$2], got [$3]"; fi
}

check_contains() {  # label file needle
  if grep -qF -- "$3" "$2" 2>/dev/null; then ok "$1"; else fail "$1: no [$3] in $2"; fi
}

check_absent() {  # label file needle
  if grep -qF -- "$3" "$2" 2>/dev/null; then fail "$1: found [$3] in $2"; else ok "$1"; fi
}

# ---------------------------------------------------------------- the stub tnctl

cat > "$bin/tnctl" <<'STUB'
#!/usr/bin/env bash
# One line per invocation: the argv joined with tabs.
{
  first=1
  for a in "$@"; do
    [ "$first" -eq 1 ] || printf '\t'
    printf '%s' "$a"
    first=0
  done
  printf '\n'
} >> "${TNCTL_LOG:?}"
key=""
case "${1:-} ${2:-}" in
  "whoami -o") key=whoami ;;
  "robot "*)   key=robot ;;
  "api GET")   key=problems ;;
  "config "*)  key=config ;;
  "inspect "*) key=inspect ;;
  *)           key=other ;;
esac
if [ -n "${FIX:-}" ] && [ -f "$FIX/$key.json" ]; then
  cat "$FIX/$key.json"
fi
if [ -n "${FIX:-}" ] && [ -f "$FIX/$key.err" ]; then
  cat "$FIX/$key.err" >&2
fi
if [ -n "${FIX:-}" ] && [ -f "$FIX/$key.rc" ]; then
  exit "$(cat "$FIX/$key.rc")"
fi
exit 0
STUB
chmod +x "$bin/tnctl"

# ---------------------------------------------------------------- fixtures

fix="$work/fix"
mkdir -p "$fix"

write_user() {  # file teamspace
  cat > "$1" <<JSON
{"mode":"user","email":"a@b.c","domain":"example.test","namespace":"$2"}
JSON
}

write_bot() {  # file teamspace access
  cat > "$1" <<JSON
{"mode":"bot","bot":{"namespace":"$2","name":"helper","unit":"site-a","unitId":"u-1","access":"$3"},"creator":{"email":"c@d.e"},"domain":"example.test"}
JSON
}

healthy_robot='{
  "name":"vs-01","namespace":"ts1","type":"vision","labels":{},"device":"vs-01",
  "phase":"Online","nodes":[{"role":"server","nodeName":"vs-01","joined":true,"ready":true,"k3s":"server"}],
  "layers":[],"deviations":0,"fleets":[]
}'

broken_robot='{
  "name":"vs-01","namespace":"ts1","type":"vision","labels":{},"device":"vs-01",
  "phase":"Degraded","phaseMessage":"Device vs-01 has 2 problems.",
  "nodes":[{"role":"server","nodeName":"vs-01","joined":true,"ready":true,"k3s":"server"}],
  "layers":[],"deviations":1,"overrides":2,"fleets":["gpu-nvidia"]
}'

healthy_config='{
  "target":{"kind":"robot","name":"vs-01","device":"vs-01"},
  "configuration":{"resources":[],"effective":[],"deviations":[],"warnings":[]},
  "overrides":{"resources":{}},
  "overrideStatus":[],
  "published":{"hash":"abc123","lastChange":"2026-11-09T10:00:00Z"},
  "overrideCount":0
}'

bad_config='{
  "target":{"kind":"robot","name":"vs-01","device":"vs-01"},
  "configuration":{
    "resources":[],
    "effective":[
      {"identity":"HelmChart-notebook-default","kind":"HelmChart","name":"notebook","namespace":"default","layer":"fleet","source":"gpu-nvidia","status":"pending"}
    ],
    "deviations":[{"identity":"Deployment-chatty-default","reason":"off"}],
    "warnings":["The override Deployment/chatty/chatty is not applied: it belongs to the platform."]
  },
  "overrides":{"resources":{}},
  "overrideStatus":[
    {"target":"HelmChart/default/notebook","state":"invalid","message":"valuesContent is not valid YAML"},
    {"target":"Deployment/default/whoami","state":"active"}
  ],
  "published":{"hash":"def456","lastChange":"2026-11-09T11:00:00Z","message":"2 items are held back"}
}'

# Five failing pods, to prove the script stops at three.
five_pods='{
  "problems":[
    {"id":"app:vs-01:w1","kind":"image-pull","severity":"critical","title":"w1","device":"vs-01","robot":"vs-01",
     "app":"web","namespace":"default","pods":["web-1"]},
    {"id":"app:vs-01:w2","kind":"crash-loop","severity":"critical","title":"w2","device":"vs-01","robot":"vs-01",
     "app":"api","namespace":"default","pods":["api-1"],"restarts":7,"reason":"CrashLoopBackOff"},
    {"id":"app:vs-01:w3","kind":"not-running","severity":"critical","title":"w3","device":"vs-01","robot":"vs-01",
     "app":"db","namespace":"default","pods":["db-1"]},
    {"id":"app:vs-01:w4","kind":"not-ready","severity":"warning","title":"w4","device":"vs-01","robot":"vs-01",
     "app":"cache","namespace":"default","pods":["cache-1"]},
    {"id":"app:vs-01:w5","kind":"image-pull","severity":"critical","title":"w5","device":"vs-01","robot":"vs-01",
     "app":"job","namespace":"default","pods":["job-1"]}
  ],
  "source":"prometheus"
}'

one_pod='{
  "problems":[
    {"id":"app:vs-01:w2","kind":"crash-loop","severity":"critical","title":"api restarts","device":"vs-01","robot":"vs-01",
     "app":"api","namespace":"default","pods":["api-1"],"restarts":7,"reason":"CrashLoopBackOff"}
  ],
  "source":"prometheus"
}'

inspect_answer='{
  "robot":"vs-01","device":"vs-01",
  "pod":{"name":"api-1","namespace":"default","node":"vs-01","phase":"Running","restarts":7,
         "conditions":[],
         "containers":[{"name":"api","init":false,"image":"registry.example.test/api:1","ready":false,
                        "restarts":7,"state":{"kind":"waiting","reason":"CrashLoopBackOff"},
                        "lastTerminated":{"kind":"terminated","reason":"Error","exitCode":1},"oomKilled":false}]},
  "events":[{"type":"Warning","reason":"BackOff","message":"Back-off restarting failed container"}],
  "fetchedAt":"2026-11-09T11:00:00Z"
}'

# ---------------------------------------------------------------- helpers over the stub's log

# run_case <label> <fixture dir> <args...>: runs triage.sh and records last_rc, $work/out, $work/err
run_case() {
  local fixdir=$2
  shift 2
  last_rc=0
  : > "$work/log"
  : > "$work/out"
  : > "$work/err"
  TNCTL=tnctl TNCTL_LOG="$work/log" FIX="$fixdir" \
    bash "$script" "$@" > "$work/out" 2> "$work/err" || last_rc=$?
}

calls_of() {  # log name -> number of invocations whose first argv word is name
  awk -F'\t' -v n="$2" '$1 == n' "$1" | wc -l | tr -d ' '
}

# ---------------------------------------------------------------- 1. a healthy robot

hf="$work/healthy"
mkdir -p "$hf"
write_user "$hf/whoami.json" ts1
printf '%s' "$healthy_robot" > "$hf/robot.json"
printf '{"problems":[],"source":"prometheus"}' > "$hf/problems.json"
printf '%s' "$healthy_config" > "$hf/config.json"

run_case "healthy" "$hf" vs-01
check_eq "healthy: exit 0" 0 "$last_rc"
for heading in "== whoami" "== robot" "== problems" "== config" "== inspect"; do
  check_contains "healthy: section $heading" "$work/out" "$heading"
done
check_contains "healthy: phase reported" "$work/out" "phase: Online"
check_contains "healthy: published reported" "$work/out" "published: abc123"
check_eq "healthy: no inspect call (no failing pod)" 0 "$(calls_of "$work/log" inspect)"
check_contains "healthy: the teamspace from whoami" "$work/out" "teamspace: ts1"

# ---------------------------------------------------------------- 2. failing pods, person and bot

pf="$work/pods"
mkdir -p "$pf"
write_user "$pf/whoami.json" ts1
printf '%s' "$broken_robot" > "$pf/robot.json"
printf '%s' "$five_pods" > "$pf/problems.json"
printf '%s' "$bad_config" > "$pf/config.json"
printf '%s' "$inspect_answer" > "$pf/inspect.json"

run_case "person, 5 failing pods" "$pf" vs-01
check_eq "person: exit 0" 0 "$last_rc"
n=$(calls_of "$work/log" inspect)
check_eq "person: at most 3 inspect calls" 1 "$([ "$n" -ge 1 ] && [ "$n" -le 3 ] && echo 1 || echo 0)"
check_contains "person: a pod was read" "$work/out" "default/api-1: phase Running"
check_contains "person: the inspect summary" "$work/out" "containers 0/1 ready, restarts 7"
check_contains "person: the Warning event" "$work/out" "BackOff"
check_contains "problems: the critical count" "$work/out" "total: 5, 4 critical"
check_contains "problems: a problem row" "$work/out" "crash-loop: api"
check_contains "config: invalid override" "$work/out" "(invalid)"
check_contains "config: pending row" "$work/out" "pending in the published config: 1"
check_contains "config: deviation" "$work/out" "Deployment-chatty-default (off)"

# A bot at creator may inspect, and is capped the same way.
bf="$work/bot-creator"
mkdir -p "$bf"
cp "$pf"/*.json "$bf/"
write_bot "$bf/whoami.json" ts1 creator

run_case "bot creator, 5 failing pods" "$bf" vs-01
check_eq "bot creator: exit 0" 0 "$last_rc"
n=$(calls_of "$work/log" inspect)
check_eq "bot creator: at most 3 inspect calls" 1 "$([ "$n" -ge 1 ] && [ "$n" -le 3 ] && echo 1 || echo 0)"
check_contains "bot creator: mode in the output" "$work/out" "mode: bot, access: creator"

# One failing pod: exactly one inspect call, and the pod is read once.
of="$work/one-pod"
mkdir -p "$of"
cp "$pf"/*.json "$of/"
printf '%s' "$one_pod" > "$of/problems.json"

run_case "one failing pod" "$of" vs-01
check_eq "one pod: exit 0" 0 "$last_rc"
check_eq "one pod: exactly one inspect call" 1 "$(calls_of "$work/log" inspect)"

# ---------------------------------------------------------------- 3. the teamspace

check_teamspace() {  # label expected-ts
  local want=$2 bad=0 line idx val
  while IFS= read -r line; do
    case "$line" in
      $'whoami\t'*) continue ;;
    esac
    idx=$(awk -F'\t' '{ for (i = 1; i <= NF; i++) if ($i == "-n") { print i; exit } }' <<< "$line")
    if [ -z "$idx" ]; then
      fail "$1: an invocation has no -n: $line"
      bad=1
      continue
    fi
    if [ "$idx" -lt 3 ]; then
      fail "$1: -n is not after the subcommand: $line"
      bad=1
    fi
    val=$(awk -F'\t' -v i="$idx" '{ print $(i + 1) }' <<< "$line")
    if [ "$val" != "$want" ]; then
      fail "$1: -n is [$val], want [$want]: $line"
      bad=1
    fi
  done < "$work/log"
  [ "$bad" -eq 0 ] && ok "$1"
}

run_case "person without -n" "$pf" vs-01
check_teamspace "person: every call after whoami carries -n ts1" ts1

run_case "bot without -n" "$bf" vs-01
check_teamspace "bot: every call after whoami carries -n ts1" ts1

mf="$work/bot-other"
mkdir -p "$mf"
cp "$pf"/*.json "$mf/"
write_bot "$mf/whoami.json" ts1 creator
# The bot's own teamspace is ts1; -n x would be refused by a real tnctl, so use the same name to
# test the plumbing: -n ts1 must be passed through, not dropped.
run_case "with -n" "$pf" vs-01 -n ts1
check_teamspace "with -n ts1: every call carries -n ts1" ts1

# A person's whoami says ts1; -n ts2 must win over it.
run_case "with -n ts2" "$pf" vs-01 -n ts2
check_teamspace "with -n ts2: every call carries -n ts2" ts2
check_contains "with -n ts2: the teamspace in the output" "$work/out" "teamspace: ts2"

# Problems with Prometheus down: not "healthy".
uf="$work/unavailable"
mkdir -p "$uf"
cp "$pf"/*.json "$uf/"
printf '{"problems":[],"source":"unavailable","message":"Prometheus could not be asked, so problems are not shown right now. Try again in a minute."}' > "$uf/problems.json"

run_case "prometheus unavailable" "$uf" vs-01
check_eq "unavailable: exit 0" 0 "$last_rc"
check_contains "unavailable: says the problems are unknown" "$work/out" \
  "source: unavailable - Prometheus could not be asked"
check_eq "unavailable: no inspect (no named pod)" 0 "$(calls_of "$work/log" inspect)"

# The Shell is off: inspect fails with exit 5, and the script still finishes.
sf="$work/shell-off"
mkdir -p "$sf"
cp "$pf"/*.json "$sf/"
printf 'The Shell of vs-01 is off. Ask a person to turn it on in the portal.\n' > "$sf/inspect.err"
printf '5' > "$sf/inspect.rc"

run_case "shell off" "$sf" vs-01
check_eq "shell off: exit 0 (it ran)" 0 "$last_rc"
check_contains "shell off: the exit code and the message" "$work/out" \
  "failed: tnctl inspect pod exited 5: The Shell of vs-01 is off"
check_contains "shell off: the config was still read" "$work/out" "published: def456"

# ---------------------------------------------------------------- 4. a bot at read

rf="$work/bot-read"
mkdir -p "$rf"
cp "$pf"/*.json "$rf/"
write_bot "$rf/whoami.json" ts1 read

run_case "bot at read" "$rf" vs-01
check_eq "bot read: exit 0" 0 "$last_rc"
check_eq "bot read: inspect not called" 0 "$(calls_of "$work/log" inspect)"
check_contains "bot read: says why" "$work/out" "skipped: inspect needs a person"
check_contains "bot read: still reads the problems" "$work/out" "crash-loop: api"

# ---------------------------------------------------------------- 5. whoami refused

wf="$work/whoami-refused"
mkdir -p "$wf"
printf 'not logged in: run tnctl login\n' > "$wf/whoami.err"
printf '6' > "$wf/whoami.rc"

run_case "whoami refused" "$wf" vs-01
check_eq "whoami refused: exit 6" 6 "$last_rc"
check_contains "whoami refused: the reason" "$work/err" "whoami was refused (exit 6): not logged in"
check_eq "whoami refused: nothing else was called" 1 "$(wc -l < "$work/log" | tr -d ' ')"

run_case "whoami refused, --json" "$wf" vs-01 --json
check_eq "whoami refused (json): exit 6" 6 "$last_rc"
if jq -e . < "$work/out" > /dev/null 2>&1; then ok "whoami refused (json): valid JSON"; else fail "whoami refused (json): not valid JSON"; fi
check_eq "whoami refused (json): the whoami step" "6" "$(jq -r '.whoami.exitCode' < "$work/out")"
check_eq "whoami refused (json): the later steps say why" "not run: whoami was refused (exit 6)" \
  "$(jq -r '.robot.error' < "$work/out")"

# Without jq the script stops with a clear message, before it calls anything.
noq="$work/no-jq"
mkdir -p "$noq"
for tool in bash mktemp head cat rm; do
  p=$(command -v "$tool" 2>/dev/null) && ln -sf "$p" "$noq/$tool"
done
cp "$bin/tnctl" "$noq/tnctl"
last_rc=0
: > "$work/log"
PATH="$noq" TNCTL=tnctl TNCTL_LOG="$work/log" FIX="$pf" \
  bash "$script" vs-01 > "$work/out" 2> "$work/err" || last_rc=$?
check_eq "no jq: exit 1" 1 "$last_rc"
check_contains "no jq: a clear message" "$work/err" "jq is required"
check_eq "no jq: tnctl was not called" 0 "$(wc -l < "$work/log" | tr -d ' ')"

# ---------------------------------------------------------------- 6. --json

run_case "json" "$pf" vs-01 --json
check_eq "json: exit 0" 0 "$last_rc"
if jq -e . < "$work/out" > /dev/null 2>&1; then ok "json: valid JSON"; else fail "json: not valid JSON"; fi
check_eq "json: one key per thing" "config inspect problems robot target teamspace whoami" \
  "$(jq -r 'keys | join(" ")' < "$work/out")"
check_eq "json: each step has the same shape" "true true true true true" \
  "$(jq -r '[.whoami, .robot, .problems, .config, .inspect]
            | map((keys | sort) == ["data","error","exitCode","ok","summary"])
            | join(" ")' < "$work/out")"
check_eq "json: robot data is an object" "vs-01" "$(jq -r '.robot.data.name' < "$work/out")"
check_eq "json: whoami data carries the mode" "user" "$(jq -r '.whoami.data.mode' < "$work/out")"
check_eq "json: three inspect answers" "3" "$(jq -r '.inspect.data | length' < "$work/out")"
check_eq "json: the summary has lines" "true" "$(jq -r '.problems.summary | length > 0' < "$work/out")"
check_absent "json: no section headings on stdout" "$work/out" "== problems"

# A step that fails still gives JSON, with its exit code and error, and the script goes on.
jf="$work/json-fail"
mkdir -p "$jf"
cp "$pf"/*.json "$jf/"
printf 'the platform refused the manifest (exit 5)\n' > "$jf/config.err"
printf '5' > "$jf/config.rc"

run_case "json with a failing step" "$jf" vs-01 --json
check_eq "json failure: exit 0 (it ran)" 0 "$last_rc"
if jq -e . < "$work/out" > /dev/null 2>&1; then ok "json failure: valid JSON"; else fail "json failure: not valid JSON"; fi
check_eq "json failure: the failing step's exit code" "5" "$(jq -r '.config.exitCode' < "$work/out")"
check_eq "json failure: the failing step is not ok" "false" "$(jq -r '.config.ok' < "$work/out")"
check_eq "json failure: the first error line" "the platform refused the manifest (exit 5)" "$(jq -r '.config.error' < "$work/out")"
check_eq "json failure: later steps ran" "true" "$(jq -r '.problems.ok' < "$work/out")"

# The same failure without --json: the exit code and the first error line in the section, and the
# later steps still run.
run_case "a failing step, human output" "$jf" vs-01
check_eq "failing step: exit 0 (it ran)" 0 "$last_rc"
check_contains "failing step: the exit code and the first line" "$work/out" \
  "failed: tnctl config get exited 5: the platform refused the manifest (exit 5)"
check_contains "failing step: a later step still ran" "$work/out" "crash-loop: api"

# ---------------------------------------------------------------- 7. no writes

for case_fix in "$pf" "$bf" "$rf"; do
  run_case "no writes ($case_fix)" "$case_fix" vs-01
  badwords=""
  for w in set unset on off add remove reset promote delete try untry edit; do
    if awk -F'\t' -v w="$w" \
        '{ if ($1 == w) f = 1; for (i = 2; i <= NF; i++) if ($i == w && $1 == "config") f = 1 } END { exit !f }' \
        "$work/log"; then
      badwords="$badwords $w"
    fi
  done
  if awk -F'\t' '$1 == "api" && $2 != "GET"' "$work/log" | grep -q .; then
    badwords="$badwords api-nonGET"
  fi
  # Every config call is one of the read subcommands.
  if awk -F'\t' '$1 == "config" && $2 != "get" && $2 != "diff" && $2 != "links" && $2 != "kinds" && $2 != "wait"' \
      "$work/log" | grep -q .; then
    badwords="$badwords config-write"
  fi
  check_eq "no writes: no write command, no api beyond GET ($(basename "$case_fix"))" "" "$badwords"
done

# ---------------------------------------------------------------- 8. usage errors

usage_rc() {  # args... -> last_rc
  last_rc=0
  : > "$work/log"
  TNCTL=tnctl TNCTL_LOG="$work/log" FIX="$pf" bash "$script" "$@" > /dev/null 2>&1 || last_rc=$?
}

usage_rc
check_eq "usage: no robot exits 2" 2 "$last_rc"

usage_rc vs-01 vs-02
check_eq "usage: two robots exits 2" 2 "$last_rc"

usage_rc vs-01 --bogus
check_eq "usage: an unknown option exits 2" 2 "$last_rc"

usage_rc vs-01 -n
check_eq "usage: -n without a value exits 2" 2 "$last_rc"

usage_rc --help
check_eq "usage: --help exits 0" 0 "$last_rc"

check_eq "usage: no tnctl call on a usage error" 0 "$(wc -l < "$work/log" | tr -d ' ')"

# ---------------------------------------------------------------- report

printf '\n%d ok, %d failed\n' "$passes" "$fails"
[ "$fails" -eq 0 ]
