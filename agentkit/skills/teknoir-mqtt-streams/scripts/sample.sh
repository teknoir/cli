#!/usr/bin/env bash
# sample.sh — take a short, bounded sample of the MQTT messages of Teknoir devices.
#
# Runs `tnctl mqtt stream <topic>... --json --seconds N --count N` and prints the JSON lines as
# they are, or counts them per topic with --summary. The defaults and hard limits keep a first
# look cheap: 10 seconds and 50 messages, at most 60 seconds and 500 messages.
set -euo pipefail

TNCTL="${TNCTL:-tnctl}"
JQ="${JQ:-jq}"

max_seconds=60
max_count=500

usage() {
  cat <<'EOF'
Sample device MQTT topics for a short time.

Usage:
  sample.sh [--seconds N] [--count N] [--summary] <topic>...

Arguments:
  <topic>...    1 to 8 topics: /devices/<device|+>/state/#,
                /devices/<device|+>/events/#, or a concrete topic below
                /devices/<device|+>/state/ or /devices/<device|+>/events/

Options:
  --seconds N   stop after N seconds (default 10, at most 60)
  --count N     stop after N messages (default 50, at most 500)
  --summary     count the messages per topic instead of printing them
  -h, --help    print this help

The messages come from `tnctl mqtt stream --json`; tnctl's exit code is passed through.
EOF
}

die() {
  echo "sample.sh: $*" >&2
  exit 2
}

seconds=10
count=50
summary=0
topics=()

while [ $# -gt 0 ]; do
  case "$1" in
    -h|--help)
      usage
      exit 0
      ;;
    --seconds)
      [ $# -ge 2 ] || die "--seconds needs a value"
      seconds="$2"
      shift 2
      ;;
    --seconds=*)
      seconds="${1#*=}"
      shift
      ;;
    --count)
      [ $# -ge 2 ] || die "--count needs a value"
      count="$2"
      shift 2
      ;;
    --count=*)
      count="${1#*=}"
      shift
      ;;
    --summary)
      summary=1
      shift
      ;;
    --)
      shift
      topics+=("$@")
      break
      ;;
    -*)
      die "unknown option $1 (see --help)"
      ;;
    *)
      topics+=("$1")
      shift
      ;;
  esac
done

case "$seconds" in
  ''|*[!0-9]*) die "--seconds must be a whole number of seconds" ;;
esac
case "$count" in
  ''|*[!0-9]*) die "--count must be a whole number of messages" ;;
esac
[ "$seconds" -ge 1 ] || die "--seconds must be at least 1"
[ "$seconds" -le "$max_seconds" ] || die "--seconds is at most $max_seconds"
[ "$count" -ge 1 ] || die "--count must be at least 1"
[ "$count" -le "$max_count" ] || die "--count is at most $max_count"
[ "${#topics[@]}" -ge 1 ] || die "name at least one topic (see --help)"
[ "${#topics[@]}" -le 8 ] || die "at most 8 topics"

rc=0
set +e
if [ "$summary" -eq 1 ]; then
  "$TNCTL" mqtt stream "${topics[@]}" --json --seconds "$seconds" --count "$count" \
    | "$JQ" -s -r 'group_by(.topic) | map({topic: .[0].topic, count: length})
                   | sort_by(-.count) | .[] | "\(.count)\t\(.topic)"'
  rc=${PIPESTATUS[0]}
else
  "$TNCTL" mqtt stream "${topics[@]}" --json --seconds "$seconds" --count "$count"
  rc=$?
fi
set -e

exit "$rc"
