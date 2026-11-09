#!/usr/bin/env bash
# Tests for job C's scripts: sample.sh, mqtt-relay.py, teknoirbot-app-key, teknoirbot-build and
# templates/processor.py. No platform, no cluster, no real paho: the external commands are stubs on
# a temporary PATH, the stream route is a local Python server on 127.0.0.1, and paho is a fake
# module on a temporary PYTHONPATH.
#
# Usage: bash agentkit/test/scripts-c.sh
set -uo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
agentkit="$(cd "$here/.." && pwd)"
mqtt="$agentkit/skills/teknoir-mqtt-streams"
k3s="$agentkit/skills/teknoir-k3s-apps"
PYTHON="${PYTHON:-python3}"
orig_home="$HOME"

work="$(mktemp -d)"
trap 'kill $(jobs -p) 2>/dev/null; rm -rf "$work"' EXIT

bin="$work/bin"
mkdir -p "$bin"
export PATH="$bin:$PATH"

fails=0
ok() { printf 'ok   %s\n' "$*"; }
fail() { printf 'FAIL %s\n' "$*"; fails=$((fails + 1)); }

check_eq() {
  if [ "$2" = "$3" ]; then
    ok "$1"
  else
    fail "$1: want [$2], got [$3]"
  fi
}

check_contains() {  # label file needle
  if grep -qF -- "$3" "$2" 2>/dev/null; then
    ok "$1"
  else
    fail "$1: no [$3] in $2"
  fi
}

check_absent() {  # label file needle
  if grep -qF -- "$3" "$2" 2>/dev/null; then
    fail "$1: found [$3] in $2"
  else
    ok "$1"
  fi
}

arg_after() {  # file flag
  awk -F'\t' -v f="$2" '$2 == f { found = 1; next } found { print $2; exit }' "$1"
}

has_arg() {  # file value
  awk -F'\t' -v v="$2" '$2 == v { found = 1 } END { exit !found }' "$1"
}

wait_for() {  # file needle  -> 0 when the needle shows up
  for _ in $(seq 1 100); do
    if grep -qF -- "$2" "$1" 2>/dev/null; then
      return 0
    fi
    sleep 0.1
  done
  return 1
}

wait_for_nonempty() {  # file -> 0 when the file has a byte
  for _ in $(seq 1 100); do
    if [ -s "$1" ]; then
      return 0
    fi
    sleep 0.1
  done
  return 1
}

# ---------------------------------------------------------------- stubs

cat > "$bin/tnctl" <<'STUB'
#!/usr/bin/env bash
{
  i=1
  for a in "$@"; do
    printf '%s\t%s\n' "$i" "$a"
    i=$((i + 1))
  done
} >> "${TNCTL_LOG:?}"
if [ -n "${TNCTL_OUTPUT:-}" ] && [ -f "$TNCTL_OUTPUT" ]; then
  cat "$TNCTL_OUTPUT"
fi
exit "${TNCTL_EXIT:-0}"
STUB

cat > "$bin/buildctl" <<'STUB'
#!/usr/bin/env bash
printf '%s\n' "$@" > "${BUILDCTL_LOG:?}"
exit 0
STUB

cat > "$bin/kubectl" <<'STUB'
#!/usr/bin/env bash
{
  line=""
  for a in "$@"; do
    line="$line"$'\t'"$a"
  done
  printf '%s\n' "${line#$'\t'}"
} >> "${KUBECTL_LOG:?}"
case " $* " in
  *" create "*)
    keyfile=""
    name=""
    seen_generic=0
    for a in "$@"; do
      case "$a" in
        --from-file=key=*) keyfile="${a#--from-file=key=}" ;;
        generic) seen_generic=1 ;;
        *)
          if [ "$seen_generic" = 1 ] && [ -z "$name" ]; then
            name="$a"
          fi
          ;;
      esac
    done
    b64="$(base64 -w0 "$keyfile")"
    printf 'apiVersion: v1\nkind: Secret\nmetadata:\n  name: %s\n  namespace: apps\ndata:\n  key: %s\n' "$name" "$b64"
    ;;
  *" label "*)
    cat > "${KUBECTL_STDIN_LABEL:?}"
    cat "$KUBECTL_STDIN_LABEL"
    ;;
  *" apply "*)
    cat > "${KUBECTL_STDIN_APPLY:?}"
    ;;
esac
exit "${KUBECTL_EXIT:-0}"
STUB

chmod +x "$bin/tnctl" "$bin/buildctl" "$bin/kubectl"

# A paho that exists only to prove a path never imports it.
mkdir -p "$work/nopaho"
cat > "$work/nopaho/paho.py" <<'PY'
with open(__import__("os").environ["FAKE_PAHO_MARKER"], "a", encoding="utf-8") as h:
    h.write("imported\n")
raise ImportError("paho must not be imported here")
PY

# fake_paho <dir> <v1|v2>: the paho.mqtt.client module the relay and the processor import.
fake_paho() {
  local dir="$1" variant="$2"
  mkdir -p "$dir/paho/mqtt"
  : > "$dir/paho/__init__.py"
  : > "$dir/paho/mqtt/__init__.py"
  {
    printf 'import json, os\n\n'
    if [ "$variant" = v2 ]; then
      printf 'class CallbackAPIVersion:\n    VERSION1 = 1\n    VERSION2 = 2\n\n'
      printf 'CALL_EXTRA = (None,)  # the v2 callback takes the properties as a fifth argument\n\n'
    else
      printf 'CALL_EXTRA = ()\n\n'
    fi
    cat <<'PY'
def _log(**kw):
    path = os.environ.get("FAKE_PAHO_LOG")
    if path:
        with open(path, "a", encoding="utf-8") as h:
            h.write(json.dumps(kw) + "\n")

class Message:
    def __init__(self, topic, payload):
        self.topic = topic
        self.payload = payload

class Client:
    on_connect = None
    on_disconnect = None
    on_message = None

    def __init__(self, *args, **kwargs):
        self._connected = False
        self._subs = []
        _log(event="init", args=list(args), kwargs=kwargs)

    def connect(self, host, port):
        self._connected = True  # connect() is synchronous in real paho
        _log(event="connect", host=host, port=port)
        return 0

    def connect_async(self, host, port, keepalive=60):
        _log(event="connect_async", host=host, port=port, keepalive=keepalive)
        return 0

    def loop_start(self):
        self._connected = True
        _log(event="loop_start")
        if self.on_connect is not None:
            self.on_connect(self, None, {}, 0, *CALL_EXTRA)
        message = os.environ.get("FAKE_PAHO_MESSAGE")
        if message and self._subs:
            topic = os.environ.get("FAKE_PAHO_TOPIC", "in/devices/d1/state")
            self.on_message(self, None, Message(topic, message.encode("utf-8")))

    def loop_stop(self):
        _log(event="loop_stop")

    def disconnect(self):
        _log(event="disconnect")

    def reconnect_delay_set(self, *args, **kwargs):
        _log(event="reconnect_delay_set")

    def subscribe(self, topic, qos=0):
        if not self._connected:
            _log(event="subscribe_no_conn", topic=topic)
            return (4, None)
        self._subs.append(topic)
        _log(event="subscribe", topic=topic, qos=qos)
        return (0, 1)

    def publish(self, topic, payload, qos=0, retain=False):
        text = payload.decode("utf-8") if isinstance(payload, bytes) else payload
        _log(event="publish", topic=topic, payload=text, qos=qos, retain=retain)
PY
  } > "$dir/paho/mqtt/client.py"
}

fake_paho "$work/fake-v2" v2
fake_paho "$work/fake-v1" v1

# The stream route: a local SSE server with a mode.
cat > "$work/sse_server.py" <<'PY'
import sys
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

mode, portfile, countfile, queryfile, authfile = sys.argv[1:6]
PATH_OK = "/fleet/teknoir-demo/mqtt/stream"

MESSAGE = (
    'event: message\n'
    'data: {"topic":"/devices/d1/state","payload":"{\\"spec\\":{\\"metrics\\":[]}}",'
    '"retain":false,"ts":"2026-11-09T10:00:00.000Z"}\n\n'
)
BINARY = (
    'event: message\n'
    'data: {"topic":"/devices/d2/events/x","payloadBase64":"aGk=",'
    '"retain":false,"ts":"2026-11-09T10:00:01.000Z"}\n\n'
)
DROPPED = 'event: dropped\ndata: {"count":7,"reason":"rate"}\n\n'
ERROR = 'event: error\ndata: {"code":"access_lost","message":"the caller lost access"}\n\n'


def record(path, text):
    with open(path, "a", encoding="utf-8") as handle:
        handle.write(text + "\n")


class Handler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def log_message(self, *args):
        pass

    def do_GET(self):
        self.close_connection = True
        try:
            self.answer()
        except OSError:
            pass  # the client went away: the test does not need a traceback for that

    def answer(self):
        record(countfile, self.path)
        if self.path.split("?", 1)[0] != PATH_OK:
            body = b"not found"
            self.send_response(404)
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)
            return
        record(queryfile, self.path.split("?", 1)[1] if "?" in self.path else "")
        record(authfile, self.headers.get("Authorization", ""))
        if mode == "unauthorized":
            body = b'{"message":"no"}'
            self.send_response(401)
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)
            return
        if mode == "badrequest":
            body = b'{"message":"topic \\"#\\" is not a stream topic"}'
            self.send_response(400)
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)
            return
        if mode in ("retry0", "retryinf"):
            body = b'{"message":"busy"}'
            self.send_response(429)
            self.send_header("Retry-After", "0" if mode == "retry0" else "inf")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)
            return
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream; charset=utf-8")
        self.send_header("Cache-Control", "no-store")
        self.send_header("Connection", "close")
        self.end_headers()
        self.wfile.write(b": keepalive\n\n")
        if mode == "error":
            self.wfile.write(MESSAGE.encode())
            self.wfile.write(ERROR.encode())
            self.wfile.flush()
            return
        if mode == "cuterror":  # an error event that the end of the stream cuts off
            self.wfile.write(MESSAGE.encode())
            self.wfile.write(ERROR.rstrip("\n").encode() + b"\n")
            self.wfile.flush()
            return
        if mode == "bigline":  # one 1.5 MiB comment line: only the line cap catches it
            self.wfile.write(b": " + b"x" * (3 << 19) + b"\n\n")
            self.wfile.flush()
            time.sleep(30)
            return
        if mode == "bigutf8":  # 2.7 MB of data in 900 K characters
            line = ("data: " + "\u20ac" * 300000 + "\n").encode()
            self.wfile.write(b"event: message\n" + line * 3 + b"\n")
            self.wfile.flush()
            time.sleep(30)
            return
        self.wfile.write(MESSAGE.encode())
        self.wfile.write(BINARY.encode())
        self.wfile.write(DROPPED.encode())
        self.wfile.flush()
        time.sleep(30)


server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
server.daemon_threads = True
server.handle_error = lambda *args: None
with open(portfile, "w", encoding="utf-8") as handle:
    handle.write(str(server.server_address[1]))
server.serve_forever()
PY

start_server() {  # mode -> sets server_pid and api
  local mode="$1"
  rm -f "$work/port"
  : > "$work/count"
  "$PYTHON" "$work/sse_server.py" "$mode" "$work/port" "$work/count" \
    "$work/query" "$work/auth" &
  server_pid=$!
  wait_for_nonempty "$work/port" || true
  api="http://127.0.0.1:$(cat "$work/port" 2>/dev/null || echo 0)"
}

stop_server() {
  kill "$server_pid" 2>/dev/null || true
  wait "$server_pid" 2>/dev/null || true
}

printf 'source_fixture\n' > "$work/keyfile"

# ---------------------------------------------------------------- sample.sh

sample="$mqtt/scripts/sample.sh"
export TNCTL_LOG="$work/tnctl.log"
: > "$TNCTL_LOG"

out="$("$sample" '/devices/+/state/#' 2>&1)"
rc=$?
check_eq "sample.sh: default run exits 0" 0 "$rc"
check_eq "sample.sh: --seconds default" 10 "$(arg_after "$TNCTL_LOG" --seconds)"
check_eq "sample.sh: --count default" 50 "$(arg_after "$TNCTL_LOG" --count)"
check_eq "sample.sh: topic passed" '/devices/+/state/#' "$(arg_after "$TNCTL_LOG" 'stream')"
if has_arg "$TNCTL_LOG" --json; then ok "sample.sh: --json passed"; else fail "sample.sh: --json not passed"; fi

: > "$TNCTL_LOG"
"$sample" --seconds 5 --count 3 '/devices/+/state/#' >/dev/null 2>&1
check_eq "sample.sh: --seconds passed" 5 "$(arg_after "$TNCTL_LOG" --seconds)"
check_eq "sample.sh: --count passed" 3 "$(arg_after "$TNCTL_LOG" --count)"

: > "$TNCTL_LOG"
"$sample" --seconds 61 '/devices/+/state/#' >/dev/null 2>&1
check_eq "sample.sh: --seconds 61 refused" 2 "$?"
: > "$TNCTL_LOG"
"$sample" --count 501 '/devices/+/state/#' >/dev/null 2>&1
check_eq "sample.sh: --count 501 refused" 2 "$?"
if [ -s "$TNCTL_LOG" ]; then fail "sample.sh: tnctl ran after a refused limit"; else ok "sample.sh: tnctl not run after a refused limit"; fi

"$sample" >/dev/null 2>&1
check_eq "sample.sh: no topic is a usage error" 2 "$?"

cat > "$work/tnctl.jsonl" <<'JSON'
{"topic":"/devices/d1/state","payload":"{}","retain":false,"ts":"t"}
{"topic":"/devices/d1/state","payload":"{}","retain":false,"ts":"t"}
{"topic":"/devices/d2/events/x","payload":"{}","retain":false,"ts":"t"}
JSON
summary="$(TNCTL_OUTPUT="$work/tnctl.jsonl" "$sample" --summary '/devices/+/state/#' 2>/dev/null)"
check_eq "sample.sh: --summary counts per topic" \
  "$(printf '2\t/devices/d1/state\n1\t/devices/d2/events/x')" "$summary"

TNCTL_EXIT=7 "$sample" '/devices/+/state/#' >/dev/null 2>&1
check_eq "sample.sh: exit code passed through" 7 "$?"

TNCTL_EXIT=7 TNCTL_OUTPUT="$work/tnctl.jsonl" "$sample" --summary '/devices/+/state/#' >/dev/null 2>&1
check_eq "sample.sh: --summary passes the exit code through" 7 "$?"

printf '#!/usr/bin/env bash\ncat > /dev/null\nexit 5\n' > "$work/jq-fail"
printf '#!/usr/bin/env bash\nexit 5\n' > "$work/jq-early"
# shellcheck disable=SC2016
printf '#!/usr/bin/env bash\nsleep 0.5\nprintf "%%s\\n" "{\\"topic\\":\\"/devices/d1/state\\"}"\n' \
  > "$work/tnctl-late"
chmod +x "$work/jq-fail" "$work/jq-early" "$work/tnctl-late"
JQ="$work/jq-fail" TNCTL_OUTPUT="$work/tnctl.jsonl" "$sample" --summary '/devices/+/state/#' \
  > /dev/null 2> "$work/sample-jq.err"
check_eq "sample.sh: a jq failure exits 1" 1 "$?"
check_contains "sample.sh: a jq failure is named" "$work/sample-jq.err" "jq failed (exit 5)"
JQ="$work/jq-early" TNCTL="$work/tnctl-late" "$sample" --summary '/devices/+/state/#' \
  > /dev/null 2> "$work/sample-jq-early.err"
check_eq "sample.sh: a jq that ends before tnctl writes exits 1, not 141" 1 "$?"
check_contains "sample.sh: an early jq failure is named" "$work/sample-jq-early.err" "jq failed (exit 5)"
JQ="$work/no-such-jq" "$sample" --summary '/devices/+/state/#' > /dev/null 2> "$work/sample-nojq.err"
check_eq "sample.sh: --summary without jq exits 2" 2 "$?"
check_contains "sample.sh: --summary without jq says so" "$work/sample-nojq.err" "needs jq"

# ---------------------------------------------------------------- mqtt-relay.py: stdin

relay="$mqtt/scripts/mqtt-relay.py"
export FAKE_PAHO_MARKER="$work/nopaho.marker"
rm -f "$FAKE_PAHO_MARKER"
export FAKE_PAHO_MESSAGE=

cat > "$work/in.jsonl" <<'JSON'
{"topic":"/devices/d1/state","payload":"hello","retain":false,"ts":"t"}
{"topic":"/devices/tb-scout/events/x","payload":"mine","retain":false,"ts":"t"}
{"topic":"/devices/d2/events/x","payloadBase64":"aGk=","retain":false,"ts":"t"}
JSON

out="$(PYTHONPATH="$work/nopaho" TEKNOIRBOT_NAME=scout \
  "$PYTHON" "$relay" --dry-run < "$work/in.jsonl" 2>"$work/relay.err")"
printf '%s\n' "$out" > "$work/relay.out"
check_eq "relay: stdin --dry-run maps the topic and decodes base64" \
  "$(printf 'in/devices/d1/state hello\nin/devices/d2/events/x hi')" "$out"
check_absent "relay: the bot's own device is dropped" "$work/relay.out" "tb-scout"
if [ -e "$FAKE_PAHO_MARKER" ]; then fail "relay: --dry-run imported paho"; else ok "relay: --dry-run imports no paho"; fi

"$PYTHON" "$relay" --dry-run --prefix toe/ < "$work/in.jsonl" >/dev/null 2>&1
check_eq "relay: --prefix toe/ is refused" 3 "$?"

"$PYTHON" "$relay" --dry-run --prefix toe < "$work/in.jsonl" >/dev/null 2>&1
check_eq "relay: --prefix toe (no slash) is refused" 3 "$?"

: > "$work/empty.jsonl"
out="$("$PYTHON" "$relay" --dry-run < "$work/empty.jsonl" 2>&1)"
rc=$?
check_eq "relay: an empty stdin ends with 0 and prints nothing" "0|" "$rc|$out"

# ---------------------------------------------------------------- mqtt-relay.py: SSE

printf '  sse-fixture-key \n' > "$work/sse-key"

start_server ok
out="$work/sse.out"
err="$work/sse.err"
: > "$out"
: > "$err"
PYTHONPATH="$work/nopaho" TEKNOIRBOT_NAMESPACE=teknoir-demo \
  "$PYTHON" "$relay" --sse --dry-run --api "$api" --key-file "$work/sse-key" \
  --topic '/devices/+/state/#' --topic /devices/d1/events/alerts > "$out" 2> "$err" &
relay_pid=$!
wait_for "$out" "in/devices/d2/events/x"
wait_for "$err" "dropped"
kill "$relay_pid" 2>/dev/null || true
wait "$relay_pid" 2>/dev/null || true
stop_server

check_contains "relay --sse: prints a text message" "$out" "in/devices/d1/state "
check_contains "relay --sse: prints a base64 message decoded" "$out" "in/devices/d2/events/x hi"
check_contains "relay --sse: one stderr line for dropped" "$err" "dropped 7 message(s)"
dropped_lines="$(grep -c 'dropped' "$err" 2>/dev/null || true)"
check_eq "relay --sse: exactly one dropped line" 1 "$dropped_lines"
check_eq "relay --sse: the topic query values are the --topic values" \
  "$(printf '/devices/+/state/#\n/devices/d1/events/alerts')" \
  "$("$PYTHON" -c 'import sys, urllib.parse
print("\n".join(urllib.parse.parse_qs(open(sys.argv[1], encoding="utf-8").readline().strip())["topic"]))' "$work/query")"
check_contains "relay --sse: + and # are percent-encoded" "$work/query" "%2B"
if grep -qF "Bearer sse-fixture-key" "$work/auth"; then ok "relay --sse: sends the key as a Bearer token"; else fail "relay --sse: no Bearer token"; fi
if [ -e "$FAKE_PAHO_MARKER" ]; then fail "relay --sse --dry-run: imported paho"; else ok "relay --sse --dry-run: imports no paho"; fi

start_server error
: > "$work/count"
err="$work/sse-error.err"
PYTHONPATH="$work/nopaho" TEKNOIRBOT_NAMESPACE=teknoir-demo timeout 20 \
  "$PYTHON" "$relay" --sse --dry-run --api "$api" --key-file "$work/sse-key" \
  --topic '/devices/+/state/#' > /dev/null 2> "$err"
rc=$?
stop_server
check_eq "relay --sse: an error event exits 1" 1 "$rc"
check_contains "relay --sse: the error message is printed" "$err" "the caller lost access"
check_eq "relay --sse: exactly one connection after an error event" 1 "$(grep -c . "$work/count")"

start_server unauthorized
err="$work/sse-401.err"
PYTHONPATH="$work/nopaho" TEKNOIRBOT_NAMESPACE=teknoir-demo timeout 20 \
  "$PYTHON" "$relay" --sse --dry-run --api "$api" --key-file "$work/sse-key" \
  --topic '/devices/+/state/#' > /dev/null 2> "$err"
rc=$?
stop_server
check_eq "relay --sse: HTTP 401 exits 3" 3 "$rc"
check_contains "relay --sse: 401 names the key" "$err" "key"

start_server badrequest
err="$work/sse-400.err"
PYTHONPATH="$work/nopaho" TEKNOIRBOT_NAMESPACE=teknoir-demo timeout 20 \
  "$PYTHON" "$relay" --sse --dry-run --api "$api" --key-file "$work/sse-key" \
  --topic '/devices/+/state/#' > /dev/null 2> "$err"
rc=$?
stop_server
check_eq "relay --sse: HTTP 400 exits 2" 2 "$rc"
check_contains "relay --sse: 400 prints the server's message" "$err" "is not a stream topic"

PYTHONPATH="$work/nopaho" TEKNOIRBOT_NAMESPACE=teknoir-demo timeout 20 \
  "$PYTHON" "$relay" --sse --dry-run --api "http://192.0.2.10:8080" --key-file "$work/sse-key" \
  --topic '/devices/+/state/#' > /dev/null 2>&1
check_eq "relay --sse: http to a non-loopback host is refused" 2 "$?"

PYTHONPATH="$work/nopaho" TEKNOIRBOT_NAMESPACE=teknoir-demo timeout 20 \
  "$PYTHON" "$relay" --sse --dry-run --api "https://tb-api.example:8085" --key-file "$work/sse-key" \
  --topic '/devices/+/state' > /dev/null 2>&1
check_eq "relay --sse: a concrete topic above the subtree is refused" 2 "$?"

printf 'SECRETAAA\nSECRETBBB\n' > "$work/key-multi"
printf 'tbk1.ns.bot.SECRET\377DDD\n' > "$work/key-binary"
for kind in multi binary; do
  PYTHONPATH="$work/nopaho" TEKNOIRBOT_NAMESPACE=teknoir-demo timeout 20 \
    "$PYTHON" "$relay" --sse --dry-run --api "http://127.0.0.1:1" --key-file "$work/key-$kind" \
    --topic '/devices/+/state/#' > /dev/null 2> "$work/key-$kind.err"
  check_eq "relay --sse: a $kind key file exits 3" 3 "$?"
  check_contains "relay --sse: a $kind key file is named" "$work/key-$kind.err" "single-line key"
  check_absent "relay --sse: a $kind key file is not printed" "$work/key-$kind.err" "SECRET"
  check_absent "relay --sse: a $kind key file gives no traceback" "$work/key-$kind.err" "Traceback"
done

tls_result="$(PYTHONPATH="$work/nopaho" "$PYTHON" -B -c '
import http.client, importlib.util, ssl, sys
spec = importlib.util.spec_from_file_location("relay", sys.argv[1])
relay = importlib.util.module_from_spec(spec)
spec.loader.exec_module(relay)
def refuse(self, *args, **kwargs):
    raise ssl.SSLCertVerificationError("certificate verify failed")
http.client.HTTPConnection.request = refuse
try:
    relay.sse_open("http://127.0.0.1:1", "teknoir-demo", ["/devices/+/state/#"], sys.argv[2], 5)
except relay.Fatal as exc:
    print("fatal %d" % exc.code)
except OSError:
    print("retried")
' "$relay" "$work/sse-key" 2>&1)"
check_eq "relay --sse: a TLS failure is retried, not blamed on the key" retried "$tls_result"

start_server retry0
err="$work/sse-retry0.err"
PYTHONPATH="$work/nopaho" TEKNOIRBOT_NAMESPACE=teknoir-demo timeout 3 \
  "$PYTHON" "$relay" --sse --dry-run --api "$api" --key-file "$work/sse-key" \
  --topic '/devices/+/state/#' > /dev/null 2> "$err"
stop_server
connections="$(grep -c . "$work/count")"
if [ "$connections" -le 4 ]; then
  ok "relay --sse: Retry-After: 0 waits at least 1 s"
else
  fail "relay --sse: Retry-After: 0 made $connections connections in 3 s"
fi

start_server retryinf
err="$work/sse-retryinf.err"
PYTHONPATH="$work/nopaho" TEKNOIRBOT_NAMESPACE=teknoir-demo timeout 3 \
  "$PYTHON" "$relay" --sse --dry-run --api "$api" --key-file "$work/sse-key" \
  --topic '/devices/+/state/#' > /dev/null 2> "$err"
rc=$?
stop_server
check_eq "relay --sse: Retry-After: inf keeps the relay running" 124 "$rc"
check_absent "relay --sse: Retry-After: inf gives no traceback" "$err" "Traceback"
check_contains "relay --sse: Retry-After is capped at 300 s" "$err" "in 300"

start_server cuterror
err="$work/sse-cuterror.err"
PYTHONPATH="$work/nopaho" TEKNOIRBOT_NAMESPACE=teknoir-demo timeout 4 \
  "$PYTHON" "$relay" --sse --dry-run --api "$api" --key-file "$work/sse-key" \
  --topic '/devices/+/state/#' > /dev/null 2> "$err"
rc=$?
stop_server
check_eq "relay --sse: an error event cut off by the end of the stream is dropped" 124 "$rc"
if [ "$(grep -c . "$work/count")" -ge 2 ]; then
  ok "relay --sse: it connects again after a cut-off event"
else
  fail "relay --sse: no second connection after a cut-off event"
fi
check_contains "relay --sse: a cut-off stream counts as ended" "$err" "the stream ended"

for mode in bigline bigutf8; do
  start_server "$mode"
  err="$work/sse-$mode.err"
  PYTHONPATH="$work/nopaho" TEKNOIRBOT_NAMESPACE=teknoir-demo timeout 3 \
    "$PYTHON" "$relay" --sse --dry-run --api "$api" --key-file "$work/sse-key" \
    --topic '/devices/+/state/#' > /dev/null 2> "$err"
  stop_server
  check_contains "relay --sse: $mode is capped at 1 MiB" "$err" "larger than 1 MiB"
done

# ---------------------------------------------------------------- mqtt-relay.py: broker

for variant in v2 v1; do
  export FAKE_PAHO_LOG="$work/paho-$variant.log"
  : > "$FAKE_PAHO_LOG"
  PYTHONPATH="$work/fake-$variant" "$PYTHON" "$relay" < "$work/in.jsonl" > /dev/null 2>&1
  rc=$?
  check_eq "relay ($variant): stdin mode exits 0" 0 "$rc"
  published="$("$PYTHON" -c 'import json, sys
for line in open(sys.argv[1], encoding="utf-8"):
    d = json.loads(line)
    if d.get("event") == "publish" and d.get("topic") == "in/devices/d1/state":
        print("%s %s %s" % (d["topic"], d["qos"], d["retain"]))' "$FAKE_PAHO_LOG")"
  check_eq "relay ($variant): publishes in/devices/d1/state, qos 0, no retain" \
    "in/devices/d1/state 0 False" "$published"
  connect_to="$("$PYTHON" -c 'import json, sys
for line in open(sys.argv[1], encoding="utf-8"):
    d = json.loads(line)
    if d.get("event") == "connect":
        print("%s %s" % (d["host"], d["port"]))
        break' "$FAKE_PAHO_LOG")"
  check_eq "relay ($variant): connects to the local broker by default" \
    "mqtt.kube-system.svc.cluster.local 1883" "$connect_to"
  init_args="$("$PYTHON" -c 'import json, sys
for line in open(sys.argv[1], encoding="utf-8"):
    d = json.loads(line)
    if d.get("event") == "init":
        print(",".join(str(a) for a in d["args"]))
        break' "$FAKE_PAHO_LOG")"
  if [ "$variant" = v2 ]; then
    check_eq "relay (v2): the client takes CallbackAPIVersion.VERSION2" 2 "$init_args"
  else
    check_eq "relay (v1): the client takes no callback version" "" "$init_args"
  fi
done
export FAKE_PAHO_LOG=

# ---------------------------------------------------------------- teknoirbot-app-key

appkey="$mqtt/scripts/teknoirbot-app-key"
export KUBECTL_LOG="$work/kubectl.log"
export KUBECTL_STDIN_LABEL="$work/kubectl.label.stdin"
export KUBECTL_STDIN_APPLY="$work/kubectl.apply.stdin"
keyvalue="fixture-bot-key-2f8c1d"

env -u TEKNOIRBOT_API -u TNCTL_BOT_KEY_FILE "$appkey" demo-key > /dev/null 2>&1
check_eq "app-key: outside bot mode exits 2" 2 "$?"

printf '%s\n' "$keyvalue" > "$work/key"
TEKNOIRBOT_API="https://tb-api.example:8085/api/device/bot/v1" TNCTL_BOT_KEY_FILE="$work/key" \
  "$appkey" 'Bad_Name' > /dev/null 2>&1
check_eq "app-key: a bad name exits 2" 2 "$?"

: > "$KUBECTL_LOG"
out="$(TEKNOIRBOT_API="https://tb-api.example:8085/api/device/bot/v1" TNCTL_BOT_KEY_FILE="$work/key" \
  "$appkey" demo-key 2> "$work/appkey.err")"
rc=$?
check_eq "app-key: the copy exits 0" 0 "$rc"

calls="$(grep -c . "$KUBECTL_LOG")"
check_eq "app-key: three kubectl calls" 3 "$calls"
# The three calls run in one pipeline, so their order in the log is not fixed: pick each out by
# its subcommand (the lines are tab-separated, so a whole-field match is exact).
kubectl_call() {  # subcommand
  awk -F'\t' -v want="$1" '{ for (i = 1; i <= NF; i++) if ($i == want) { print; next } }' "$KUBECTL_LOG" | tr '\t' ' '
}
create_line="$(kubectl_call create)"
label_line="$(kubectl_call label)"
apply_line="$(kubectl_call apply)"
case "$create_line" in
  *"create secret generic demo-key"*"--from-file=key=$work/key"*"--dry-run=client"*"-o yaml"*)
    ok "app-key: create takes the key file with --dry-run=client -o yaml" ;;
  *) fail "app-key: create argv is [$create_line]" ;;
esac
case "$label_line" in
  *"label --local -f -"*"app.kubernetes.io/managed-by=teknoirbot-app-key"*"-o yaml"*)
    ok "app-key: label --local -f - sets the managed-by label" ;;
  *) fail "app-key: label argv is [$label_line]" ;;
esac
case "$apply_line" in
  *"-n apps apply --server-side --field-manager=teknoirbot-app-key -f -"*)
    ok "app-key: apply --server-side takes the Secret on stdin" ;;
  *) fail "app-key: apply argv is [$apply_line]" ;;
esac
check_contains "app-key: the Secret YAML reaches the apply on stdin" "$KUBECTL_STDIN_APPLY" "kind: Secret"
check_contains "app-key: the Secret carries the key data" "$KUBECTL_STDIN_APPLY" "key:"
check_absent "app-key: the key value is in no argv" "$KUBECTL_LOG" "$keyvalue"
keyb64="$(printf '%s\n' "$keyvalue" | base64 -w0)"
check_absent "app-key: the base64 key is in no argv" "$KUBECTL_LOG" "$keyb64"
printf '%s' "$out" > "$work/appkey.out"
check_absent "app-key: the key value is not on stdout" "$work/appkey.out" "$keyvalue"
check_absent "app-key: the base64 key is not on stdout" "$work/appkey.out" "$keyb64"
check_absent "app-key: the key value is not on stderr" "$work/appkey.err" "$keyvalue"
check_absent "app-key: the base64 key is not on stderr" "$work/appkey.err" "$keyb64"
check_contains "app-key: it prints how to mount the Secret" "$work/appkey.out" "mountPath: /run/teknoirbot-app"
check_contains "app-key: the mount snippet carries a --topic" "$work/appkey.out" "--topic"
check_contains "app-key: the mount snippet sets TEKNOIRBOT_API" "$work/appkey.out" "TEKNOIRBOT_API"
check_contains "app-key: the mount snippet carries the bot API URL" "$work/appkey.out" \
  "https://tb-api.example:8085/api/device/bot/v1"

# ---------------------------------------------------------------- teknoirbot-build

build="$k3s/scripts/teknoirbot-build"
mkdir -p "$work/ctx" "$work/home"
printf 'FROM scratch\n' > "$work/ctx/Dockerfile"
ctx_abs="$(cd "$work/ctx" && pwd)"
export BUILDCTL_LOG="$work/buildctl.log"
export BUILDKIT_HOST="tcp://buildkitd.teknoir-build:1234"
export HOME="$work/home"
: > "$BUILDCTL_LOG"

out="$("$build" "$work/ctx" hello --tag test-tag 2>"$work/build.err")"
rc=$?
check_eq "build: exits 0" 0 "$rc"
check_eq "build: the last line is the pull name" "image: localhost:5000/hello:test-tag" \
  "$(printf '%s\n' "$out" | tail -n 1)"
cat > "$work/buildctl.want" <<WANT
--addr
tcp://buildkitd.teknoir-build:1234
build
--frontend
dockerfile.v0
--progress
plain
--local
context=$ctx_abs
--local
dockerfile=$ctx_abs
--opt
filename=Dockerfile
--output
type=image,name=registry.teknoir-build.svc.cluster.local:5000/hello:test-tag,push=true,registry.insecure=true
WANT
if diff -u "$work/buildctl.want" "$BUILDCTL_LOG" > "$work/buildctl.diff" 2>&1; then
  ok "build: the buildctl argv is exactly as specified"
else
  fail "build: the buildctl argv differs (see below)"
  cat "$work/buildctl.diff"
fi

"$build" "$work/ctx" 'Bad_Name' > /dev/null 2>&1
check_eq "build: a bad app name exits 2" 2 "$?"

: > "$BUILDCTL_LOG"
"$build" "$work/ctx" hello --tag -leading > /dev/null 2>"$work/build-tag.err"
check_eq "build: a tag starting with '-' exits 2" 2 "$?"
check_contains "build: the '-' tag message" "$work/build-tag.err" \
  "must start with a letter, a digit or '_'"
if [ -s "$BUILDCTL_LOG" ]; then fail "build: buildctl ran with a '-' tag"; else ok "build: buildctl not run with a '-' tag"; fi

long_tag="$(printf 'a%.0s' {1..129})"
: > "$BUILDCTL_LOG"
"$build" "$work/ctx" hello --tag "$long_tag" > /dev/null 2>"$work/build-long.err"
check_eq "build: a 129-character tag exits 2" 2 "$?"
check_contains "build: the long tag message" "$work/build-long.err" \
  "is longer than 128 characters"
if [ -s "$BUILDCTL_LOG" ]; then fail "build: buildctl ran with a 129-character tag"; else ok "build: buildctl not run with a 129-character tag"; fi

: > "$BUILDCTL_LOG"
"$build" "$work/ctx" hello --tag .leading > /dev/null 2>"$work/build-dot.err"
check_eq "build: a tag starting with '.' exits 2" 2 "$?"
if [ -s "$BUILDCTL_LOG" ]; then fail "build: buildctl ran with a '.' tag"; else ok "build: buildctl not run with a '.' tag"; fi

tag128="$(printf 'a%.0s' {1..128})"
out="$("$build" "$work/ctx" hello --tag "$tag128" 2>/dev/null)"
check_eq "build: a 128-character tag is accepted" "image: localhost:5000/hello:$tag128" \
  "$(printf '%s\n' "$out" | tail -n 1)"

mkdir -p "$work/ro-home" "$work/build-tmp" "$work/caller-config"
chmod 555 "$work/ro-home"
cat > "$work/buildctl-dc" <<'STUB'
#!/usr/bin/env bash
printf '%s\n' "${DOCKER_CONFIG:-}" > "${BUILDCTL_LOG:?}"
STUB
chmod +x "$work/buildctl-dc"
HOME="$work/ro-home" TMPDIR="$work/build-tmp" BUILDCTL="$work/buildctl-dc" \
  "$build" "$work/ctx" hello > /dev/null 2>&1
check_eq "build: an unwritable HOME exits 0" 0 "$?"
used="$(cat "$BUILDCTL_LOG")"
if [ -n "$used" ] && [ ! -e "$used" ]; then ok "build: the temporary DOCKER_CONFIG is removed"; else fail "build: the temporary DOCKER_CONFIG [$used] is left"; fi
DOCKER_CONFIG="$work/caller-config" BUILDCTL="$work/buildctl-dc" \
  "$build" "$work/ctx" hello > /dev/null 2>&1
if [ -d "$work/caller-config" ]; then ok "build: a DOCKER_CONFIG the caller set is kept"; else fail "build: the caller's DOCKER_CONFIG was removed"; fi

: > "$BUILDCTL_LOG"
"$build" "$work/ctx" hello > /dev/null 2>&1
output_arg="$(awk '/^--output$/ { getline; print; exit }' "$BUILDCTL_LOG")"
if printf '%s' "$output_arg" | grep -Eq '^type=image,name=registry\.teknoir-build\.svc\.cluster\.local:5000/hello:[0-9]{8}-[0-9]{6},push=true,registry\.insecure=true$'; then
  ok "build: the default tag is a UTC timestamp"
else
  fail "build: the default tag is [$output_arg]"
fi

env -u BUILDKIT_HOST "$build" "$work/ctx" hello > /dev/null 2>&1
check_eq "build: no BUILDKIT_HOST exits 2" 2 "$?"

export HOME="$orig_home"
unset BUILDKIT_HOST

# ---------------------------------------------------------------- processor.py

processor="$mqtt/templates/processor.py"
cat > "$work/state.jsonl" <<'JSON'
{"topic":"/devices/d1/state","payload":"{\"spec\":{\"metrics\":[{\"metricId\":\"node_cpu_usage\",\"value\":100}]}}","retain":false,"ts":"t"}
{"topic":"/devices/d1/state","payload":"{\"spec\":{\"metrics\":[{\"metricId\":\"node_cpu_usage\",\"value\":300}]}}","retain":false,"ts":"t"}
{"topic":"/devices/d2/state","payload":"{\"spec\":{\"metrics\":[{\"metricId\":\"node_cpu_usage\",\"value\":50}]}}","retain":false,"ts":"t"}
this is not json
{"topic":"/devices/d2/state","payload":"not json","retain":false,"ts":"t"}
JSON

out="$(PYTHONPATH="$work/nopaho" timeout -k 5 20 "$PYTHON" "$processor" --stdin < "$work/state.jsonl" 2>"$work/proc.err")"
check_eq "processor: --stdin exits 0" 0 "$?"
printf '%s\n' "$out" > "$work/proc.out"
check_contains "processor: the summary goes to toe/events/cpu-summary" "$work/proc.out" "toe/events/cpu-summary"
summaries="$(printf '%s\n' "$out" | sed 's/^toe\/events\/cpu-summary //' | "$PYTHON" -c '
import json, sys
for line in sys.stdin:
    line = line.strip()
    if not line:
        continue
    d = json.loads(line)["data"]
    print("%s %d %g %g %g" % (d["device"], d["count"], d["min"], d["max"], d["mean"]))')"
check_eq "processor: one summary per device with count, min, max, mean" \
  "$(printf 'd1 2 100 300 200\nd2 1 50 50 50')" "$summaries"
check_contains "processor: it counts the messages it skipped" "$work/proc.err" "skipped 2 message(s)"
if [ -e "$FAKE_PAHO_MARKER" ]; then fail "processor: --stdin imported paho"; else ok "processor: --stdin imports no paho"; fi

multi='{"topic":"/devices/d1/state","payload":"{\"spec\":{\"metrics\":[{\"metricId\":\"node_cpu_usage\",\"labels\":{\"name\":\"node-1\"},\"value\":100},{\"metricId\":\"node_cpu_usage\",\"labels\":{\"name\":\"node-2\"},\"value\":50}]}}","retain":false,"ts":"t"}'
multi_out="$(printf '%s\n' "$multi" | PYTHONPATH="$work/nopaho" timeout 20 "$PYTHON" "$processor" --stdin 2>/dev/null \
  | sed 's/^toe\/events\/cpu-summary //' | "$PYTHON" -c 'import json, sys
d = json.loads(sys.stdin.readline())["data"]
print("%d %g %g" % (d["count"], d["min"], d["max"]))')"
check_eq "processor: a multi-node device is the sum of its nodes" "1 150 150" "$multi_out"

"$PYTHON" -c 'import json
big = "{\"spec\":{\"metrics\":[{\"metricId\":\"node_cpu_usage\",\"value\":%s}]}}" % ("9" * 400)
good = "{\"spec\":{\"metrics\":[{\"metricId\":\"node_cpu_usage\",\"value\":5}]}}"
print(json.dumps({"topic": "/devices/d1/state", "payload": big}))
print(json.dumps({"topic": "/devices/d2/state", "payload": good}))' > "$work/poison.jsonl"
PYTHONPATH="$work/nopaho" timeout 20 "$PYTHON" "$processor" --stdin < "$work/poison.jsonl" \
  > "$work/poison.out" 2> "$work/poison.err"
check_eq "processor: a value too large for a float exits 0" 0 "$?"
check_eq "processor: the message after a too-large value is summarized" 1 \
  "$(grep -c '"device":"d2"' "$work/poison.out")"
check_contains "processor: a too-large value is skipped" "$work/poison.err" "skipped 1 message(s)"

lock_result="$(PYTHONPATH="$work/nopaho" "$PYTHON" -B -c '
import importlib.util, sys
spec = importlib.util.spec_from_file_location("processor", sys.argv[1])
proc = importlib.util.module_from_spec(spec)
spec.loader.exec_module(proc)

class Guarded(dict):
    def _check(self):
        if not proc.LOCK.locked():
            raise AssertionError("WINDOWS used without LOCK")
    def get(self, *args):
        self._check()
        return super().get(*args)
    def __setitem__(self, key, value):
        self._check()
        super().__setitem__(key, value)
    def items(self):
        self._check()
        return super().items()
    def pop(self, *args):
        self._check()
        return super().pop(*args)
    def __iter__(self):
        self._check()
        return super().__iter__()

proc.WINDOWS = Guarded()
proc.add_sample("d1", 1.0, 0.0)
proc.add_sample("d2", 2.0, 0.0)
assert len(proc.flush_due(1e12)) == 2
proc.add_sample("d3", 3.0, 0.0)
assert len(proc.flush_all()) == 1
print("ok")
' "$processor" 2>&1 | tail -1)"
check_eq "processor: WINDOWS is used only under LOCK" ok "$lock_result"

# --stdin closes its windows on the same timer as the broker path: lines 0.5 s apart with
# WINDOW_SECONDS=1 print more than one summary while the input is still open.
one_line='{"topic":"/devices/d1/state","payload":"{\"spec\":{\"metrics\":[{\"metricId\":\"node_cpu_usage\",\"value\":100}]}}","retain":false,"ts":"t"}'
mkfifo "$work/slow.fifo"
(
  for _ in $(seq 1 10); do
    printf '%s\n' "$one_line"
    sleep 0.5
  done
  sleep 5  # keep stdin open: the windows must close on the timer, not at EOF
) > "$work/slow.fifo" &
writer_pid=$!
PYTHONPATH="$work/nopaho" WINDOW_SECONDS=1 timeout 25 "$PYTHON" "$processor" --stdin \
  < "$work/slow.fifo" > "$work/slow.out" 2> "$work/slow.err" &
proc_pid=$!
early=0
for _ in $(seq 1 60); do
  early="$(grep -c 'toe/events/cpu-summary' "$work/slow.out" 2>/dev/null)"
  [ "${early:-0}" -ge 2 ] && break
  sleep 0.1
done
if kill -0 "$writer_pid" 2>/dev/null; then
  ok "processor: --stdin still reads when the second window closes"
else
  fail "processor: --stdin: the input ended before the timer could be tested"
fi
if [ "${early:-0}" -ge 2 ]; then
  ok "processor: --stdin prints more than one summary before the input ends"
else
  fail "processor: --stdin printed [$early] summar(ies) before the input ended"
fi
wait "$proc_pid"
check_eq "processor: --stdin ends 0 when the input ends" 0 "$?"

# SIGTERM ends --stdin too: one window open and stdin held open, exactly one summary, exit 0.
mkfifo "$work/hold.fifo"
( printf '%s\n' "$one_line"; sleep 40 ) > "$work/hold.fifo" &  # outlasts timeout 20
writer_pid=$!
# The single-quoted script runs in the child bash, where "$@" is the real argv. The FIFO reaches
# the processor on fd 3: a non-interactive shell gives a background job /dev/null on fd 0.
# shellcheck disable=SC2016
PYTHONPATH="$work/nopaho" timeout 20 bash -c '
  "$@" <&3 &
  p=$!
  sleep 0.8
  kill -TERM "$p" 2>/dev/null || true
  wait "$p"
' _ "$PYTHON" "$processor" --stdin 3< "$work/hold.fifo" > "$work/hold.out" 2> "$work/hold.err"
rc=$?
kill "$writer_pid" 2>/dev/null || true
wait "$writer_pid" 2>/dev/null || true
check_eq "processor: --stdin exits 0 on SIGTERM" 0 "$rc"
held="$(grep -c 'toe/events/cpu-summary' "$work/hold.out" 2>/dev/null)"
check_eq "processor: --stdin flushes its one window on SIGTERM" 1 "${held:-0}"

out="$(OUTPUT_STREAM=alerts "$PYTHON" "$processor" --stdin < "$work/state.jsonl" > /dev/null 2>&1)"
check_eq "processor: OUTPUT_STREAM=alerts exits 2" 2 "$?"
OUTPUT_STREAM=Bad_Name "$PYTHON" "$processor" --stdin < "$work/state.jsonl" > /dev/null 2>&1
check_eq "processor: OUTPUT_STREAM=Bad_Name exits 2" 2 "$?"

export FAKE_PAHO_LOG="$work/paho-processor.log"
: > "$FAKE_PAHO_LOG"
export FAKE_PAHO_MESSAGE='{"spec":{"metrics":[{"metricId":"node_cpu_usage","value":250}]}}'
export FAKE_PAHO_TOPIC='in/devices/d9/state'
PYTHONPATH="$work/fake-v2" OUTPUT_STREAM=cpu-summary WINDOW_SECONDS=1 timeout -k 5 20 \
  "$PYTHON" "$processor" > /dev/null 2> "$work/proc-broker.err" &
proc_pid=$!
wait_for "$FAKE_PAHO_LOG" "toe/events/cpu-summary"
kill -TERM "$proc_pid" 2>/dev/null || true
wait "$proc_pid" 2>/dev/null || true
published="$("$PYTHON" -c 'import json, sys
for line in open(sys.argv[1], encoding="utf-8"):
    d = json.loads(line)
    if d.get("event") == "publish":
        print(d["topic"])' "$FAKE_PAHO_LOG")"
check_eq "processor: the broker path publishes toe/events/cpu-summary" "toe/events/cpu-summary" "$published"
check_contains "processor: it subscribes to the local broker" "$FAKE_PAHO_LOG" '"event": "subscribe"'
check_absent "processor: no subscribe before the connection" "$FAKE_PAHO_LOG" subscribe_no_conn

# A window that the timer has not closed yet goes out on SIGTERM (WINDOW_SECONDS is far away).
: > "$FAKE_PAHO_LOG"
PYTHONPATH="$work/fake-v2" OUTPUT_STREAM=cpu-summary WINDOW_SECONDS=3600 timeout -k 5 20 \
  "$PYTHON" "$processor" > /dev/null 2> "$work/proc-sigterm.err" &
proc_pid=$!
wait_for "$FAKE_PAHO_LOG" '"event": "subscribe"'
kill -TERM "$proc_pid" 2>/dev/null || true
wait "$proc_pid"
rc=$?
sigterm_published="$(grep -c '"event": "publish"' "$FAKE_PAHO_LOG" 2>/dev/null)"
check_eq "processor: SIGTERM exits 0" 0 "$rc"
check_eq "processor: SIGTERM flushes the open window once" 1 "$sigterm_published"
export FAKE_PAHO_MESSAGE=
export FAKE_PAHO_TOPIC=

printf '\n%d failure(s)\n' "$fails"
[ "$fails" -eq 0 ]
