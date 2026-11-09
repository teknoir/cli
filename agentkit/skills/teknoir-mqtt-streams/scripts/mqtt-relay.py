#!/usr/bin/env python3
"""Republish a read-only Teknoir MQTT stream into the device's local broker.

The platform delivers device telemetry over a scoped stream that only reads. Apps on a device
stay plain MQTT clients: this relay takes the stream and republishes each message to the local
broker, so a processor can subscribe there with no credential at all.

Input modes:

  default   read `tnctl mqtt stream --json` lines from stdin (the desktop terminal)
  --sse     read the stream route directly with the TeknoirBot's key file (a pod in `apps`)

Output: each message goes to `<prefix><topic without its leading slash>` on the local broker
(`mqtt.kube-system.svc.cluster.local:1883`, no auth, QoS 0, no retain). The prefix defaults to
`in/` and may not be under `toe/`: TOE would forward that back to the teamspace as the bot's own
event. Messages of the bot's own device (`/devices/tb-$TEKNOIRBOT_NAME/`) are dropped, so the
relay never feeds its own output back in.

Exit codes: 0 at the end of stdin, 2 a usage error, 3 refused (the key, or a `toe/` prefix),
1 anything else (the platform ended the stream, or a network failure in one-shot use).

The key is read from the key file on every connect and is never printed.
"""

import argparse
import base64
import http.client
import json
import math
import os
import re
import ssl
import sys
import time
import urllib.parse

MAX_TOPICS = 8
MAX_TOPIC_LEN = 256
MAX_EVENT = 1 << 20
IDLE_TIMEOUT = 90.0
BACKOFF_START = 1.0
BACKOFF_MAX = 30.0
DEFAULT_HOST = "mqtt.kube-system.svc.cluster.local"
DEFAULT_PORT = 1883
DEFAULT_PREFIX = "in/"
LOOPBACK_HOSTS = ("127.0.0.1", "::1", "localhost")

DEVICE_LABEL = re.compile(r"^[a-z0-9]([-a-z0-9]*[a-z0-9])?$")

# The platform's dropped reasons, as `tnctl mqtt stream` prints them.
DROPPED_REASONS = {
    "rate": "too many messages too fast",
    "size": "the messages were too large",
    "slow": "the reader was too slow",
}


class Fatal(Exception):
    """Ends the relay: the condition will not go away by connecting again."""

    def __init__(self, code, message):
        Exception.__init__(self, message)
        self.code = code


class Retry(Exception):
    """The platform asked us to wait, then connect again."""

    def __init__(self, seconds, message):
        Exception.__init__(self, message)
        self.seconds = seconds


def validate_topic(topic):
    """The topic rule of the stream route, as `tnctl mqtt stream` applies it."""
    if len(topic) > MAX_TOPIC_LEN:
        return False
    parts = topic.split("/")
    if len(parts) < 5 or parts[0] != "" or parts[1] != "devices":
        return False
    device = parts[2]
    if device != "+" and (len(device) > 63 or not DEVICE_LABEL.match(device)):
        return False
    if parts[3] not in ("state", "events"):
        return False
    if len(parts) == 5 and parts[4] == "#":
        return True
    for segment in parts[4:]:
        if segment == "" or any(c in "+#\x00" for c in segment):
            return False
        if any(c.isspace() for c in segment):
            return False
    return True


def destination(prefix, topic):
    return prefix + topic.lstrip("/")


def one_line(payload):
    """A payload on one printed line, like tnctl does."""
    text = payload.decode("utf-8", "replace")
    return text.replace("\r", "\\r").replace("\n", "\\n")


def reason_code_of(args):
    """The reason code of a paho callback argument list, v1 or v2."""
    for value in reversed(args):
        if isinstance(value, bool):
            continue
        if isinstance(value, int):
            return value
        if hasattr(value, "is_failure") or hasattr(value, "value"):
            return value
    return None


def reason_failed(code):
    if code is None:
        return False
    if isinstance(code, int):
        return code != 0
    return bool(getattr(code, "is_failure", False))


def on_connect(client, *args):
    code = reason_code_of(args)
    if reason_failed(code):
        print(
            f"relay: the broker refused the connection (reason {getattr(code, 'value', code)})",
            file=sys.stderr, flush=True)


def on_disconnect(client, *args):
    code = reason_code_of(args)
    if reason_failed(code):
        print(
            f"relay: the broker connection ended (reason {getattr(code, 'value', code)})",
            file=sys.stderr, flush=True)


class Sink:
    """Sends each message to the local broker, or prints it with --dry-run."""

    def __init__(self, dry_run, host, port, prefix):
        self.dry_run = dry_run
        self.prefix = prefix
        self.client = None
        self.send_count = 0
        if dry_run:
            return
        import paho.mqtt.client as mqtt  # only the broker path needs paho

        if hasattr(mqtt, "CallbackAPIVersion"):
            self.client = mqtt.Client(mqtt.CallbackAPIVersion.VERSION2)
        else:
            self.client = mqtt.Client()
        self.client.on_connect = on_connect
        self.client.on_disconnect = on_disconnect
        if hasattr(self.client, "reconnect_delay_set"):
            self.client.reconnect_delay_set(int(BACKOFF_START), int(BACKOFF_MAX))
        self.client.connect(host, port)
        if hasattr(self.client, "loop_start"):
            self.client.loop_start()

    def send(self, topic, payload):
        target = destination(self.prefix, topic)
        self.send_count += 1
        if self.dry_run:
            print(f"{target} {one_line(payload)}", flush=True)
            return
        self.client.publish(target, payload, qos=0, retain=False)

    def close(self):
        if self.client is None:
            return
        if hasattr(self.client, "loop_stop"):
            self.client.loop_stop()
        if hasattr(self.client, "disconnect"):
            self.client.disconnect()


def read_key(path):
    if not path:
        raise Fatal(3, "no key file: pass --key-file or set TNCTL_BOT_KEY_FILE")
    try:
        with open(path, encoding="utf-8") as handle:
            key = handle.read().strip()
    except OSError as exc:
        raise Fatal(3, f"cannot read the bot key file {path}: {exc.strerror or exc}") from None
    except UnicodeDecodeError:
        raise Fatal(3, f"the bot key file {path} does not hold a single-line key") from None
    if not key:
        raise Fatal(3, f"the bot key file {path} is empty")
    if any(not (0x21 <= ord(c) <= 0x7e) for c in key):
        raise Fatal(3, f"the bot key file {path} does not hold a single-line key")
    return key


def stream_url(api, namespace, topics):
    query = urllib.parse.urlencode([("topic", topic) for topic in topics])
    quoted = urllib.parse.quote(namespace, safe="")
    return f"{api.rstrip('/')}/fleet/{quoted}/mqtt/stream?{query}"


def check_api(api):
    parts = urllib.parse.urlsplit(api)
    if not api or parts.scheme not in ("https", "http") or not parts.hostname:
        raise Fatal(
            2,
            "--api must be the bot API URL, for example "
            f"https://tb-api.<domain>:8085/api/device/bot/v1 (got {api!r})")
    if parts.scheme == "http" and parts.hostname not in LOOPBACK_HOSTS:
        raise Fatal(2, f"--api must use https; only a loopback host may use http (got {api!r})")


def retry_after_seconds(value):
    if not value:
        return None
    try:
        seconds = float(value)
    except ValueError:
        return None
    if math.isnan(seconds):
        return None
    return min(max(seconds, BACKOFF_START), 300.0)


def response_message(response):
    """The server's own message from an error answer, if it has one."""
    try:
        body = response.read(65536).decode("utf-8", "replace").strip()
    except (OSError, http.client.HTTPException):
        return "(no message)"
    text = body
    try:
        doc = json.loads(body)
    except ValueError:
        doc = None
    if isinstance(doc, dict):
        for key in ("message", "error"):
            value = doc.get(key)
            if isinstance(value, dict):
                value = value.get("message")
            if isinstance(value, str) and value:
                text = value
                break
    # One log line per answer, however the body was shaped: the relay's log is the operator's only clue.
    return " ".join(text.split())[:300] or "(no message)"


def sse_open(api, namespace, topics, key_file, idle_timeout):
    """Open the stream: (connection, response) on 2xx; Fatal or Retry otherwise."""
    key = read_key(key_file)  # re-read on every connect: the platform may replace the key
    url = stream_url(api, namespace, topics)
    parts = urllib.parse.urlsplit(url)
    if parts.scheme == "https":
        connection = http.client.HTTPSConnection(
            parts.hostname, parts.port or 443, timeout=idle_timeout,
            context=ssl.create_default_context())
    else:
        connection = http.client.HTTPConnection(
            parts.hostname, parts.port or 80, timeout=idle_timeout)
    path = parts.path
    if parts.query:
        path = path + "?" + parts.query
    headers = {
        "Accept": "text/event-stream",
        "Authorization": "Bearer " + key,
        "User-Agent": "teknoir-mqtt-relay",
    }
    try:
        connection.request("GET", path, headers=headers)
    except OSError:
        connection.close()
        raise  # network and TLS failures (ssl.SSLCertVerificationError is also a ValueError): retried
    except (ValueError, UnicodeError):
        connection.close()
        raise Fatal(3, "the bot key cannot be sent as an HTTP header") from None
    response = connection.getresponse()
    status = response.status
    if status in (401, 403):
        connection.close()
        raise Fatal(
            3,
            f"the platform refused the key (HTTP {status}): the bot key is not accepted, "
            "or this bot may not read MQTT in this teamspace")
    if status == 400:
        message = response_message(response)
        connection.close()
        raise Fatal(2, f"the platform refused the topics (HTTP 400): {message}")
    if status == 404:
        message = response_message(response)
        connection.close()
        if not re.fullmatch(r"device \S+ not found", message):
            message += " (this platform has no stream route yet, or --api is not its bot API)"
        raise Fatal(1, f"HTTP 404 from the stream route: {message}")
    if status in (502, 504):
        message = response_message(response)
        connection.close()
        raise Retry(None, f"the gateway answered HTTP {status} (the portal may be restarting): {message}")
    if status in (429, 503):
        seconds = retry_after_seconds(response.getheader("Retry-After"))
        message = response_message(response)
        connection.close()
        if status == 503 and "live MQTT is not configured" in message:
            raise Fatal(1, f"the platform has no live MQTT: {message}")
        raise Retry(seconds, f"the platform asked the stream to wait (HTTP {status}): {message}")
    if status < 200 or status >= 300:
        connection.close()
        raise Fatal(1, f"the stream route answered HTTP {status}")
    return connection, response


def sse_events(response):
    """Yield (event, data) as tnctl parses Server-Sent Events."""
    event = ""
    data = []
    size = 0
    while True:
        raw = response.readline(MAX_EVENT + 2)  # raises OSError on a read timeout
        if not raw:
            return  # an event cut off by the end of the stream is dropped, as tnctl does
        if len(raw) > MAX_EVENT and not raw.endswith(b"\n"):
            raise OSError("a stream line is larger than 1 MiB")
        line = raw.decode("utf-8", "replace").removesuffix("\n").removesuffix("\r")
        if line == "":
            if data:
                yield event or "message", "\n".join(data)
            event, data, size = "", [], 0
            continue
        if line.startswith(":"):
            continue  # keepalive
        field, _, value = line.partition(":")
        value = value.removeprefix(" ")
        if field == "event":
            event = value
        elif field == "data":
            data.append(value)
            size += len(value.encode("utf-8")) + 1
            if size > MAX_EVENT:
                raise OSError("a stream event is larger than 1 MiB")


def payload_of(doc):
    """The message's bytes: text payload, or the decoded payloadBase64."""
    payload = doc.get("payload")
    if isinstance(payload, str):
        return payload.encode("utf-8")
    encoded = doc.get("payloadBase64")
    if isinstance(encoded, str) and encoded:
        try:
            return base64.b64decode(encoded)
        except (ValueError, TypeError):
            return None
    return None


def own_device_prefix():
    name = os.environ.get("TEKNOIRBOT_NAME", "").strip()
    return "/devices/tb-" + name if name else ""


def deliver(topic, payload, sink):
    own = own_device_prefix()
    if own and (topic == own or topic.startswith(own + "/")):
        return  # never feed our own output back in
    sink.send(topic, payload)


def handle_event(name, data, sink):
    try:
        doc = json.loads(data)
    except ValueError:
        return
    if not isinstance(doc, dict):
        return
    if name == "dropped":
        count = doc.get("count", 0)
        reason = doc.get("reason") or "unknown reason"
        reason = DROPPED_REASONS.get(reason, reason) if isinstance(reason, str) else reason
        print(f"relay: the platform dropped {count} message(s): {reason}",
              file=sys.stderr, flush=True)
        return
    if name == "error":
        message = doc.get("message") or doc.get("code") or "the platform ended the stream"
        raise Fatal(1, f"the platform ended the stream: {message}")
    if name != "message":
        return
    topic = doc.get("topic")
    if not isinstance(topic, str) or not topic:
        return
    payload = payload_of(doc)
    if payload is None:
        return
    deliver(topic, payload, sink)


def run_stdin(sink):
    for line in sys.stdin:
        line = line.strip()
        if not line:
            continue
        try:
            doc = json.loads(line)
        except ValueError:
            continue
        if not isinstance(doc, dict):
            continue
        topic = doc.get("topic")
        if not isinstance(topic, str) or not topic:
            continue
        payload = payload_of(doc)
        if payload is None:
            continue
        deliver(topic, payload, sink)
    return 0


def backoff(delay):
    return min(delay * 2, BACKOFF_MAX)


def run_sse(api, namespace, topics, key_file, sink):
    delay = BACKOFF_START
    while True:
        try:
            connection, response = sse_open(api, namespace, topics, key_file, IDLE_TIMEOUT)
        except Retry as exc:
            wait = exc.seconds if exc.seconds is not None else delay
            print(f"relay: {exc}; connecting again in {wait}s", file=sys.stderr, flush=True)
            time.sleep(wait)
            delay = backoff(delay)
            continue
        except OSError as exc:
            print(f"relay: the stream failed: {exc}; connecting again in {delay}s",
                  file=sys.stderr, flush=True)
            time.sleep(delay)
            delay = backoff(delay)
            continue
        delay = BACKOFF_START  # a connect that worked starts the backoff over
        try:
            for name, data in sse_events(response):
                handle_event(name, data, sink)
        except (OSError, http.client.HTTPException) as exc:
            print(f"relay: the stream failed: {exc}; connecting again in {delay}s",
                  file=sys.stderr, flush=True)
            time.sleep(delay)
            delay = backoff(delay)
        else:
            print(f"relay: the stream ended; connecting again in {delay}s",
                  file=sys.stderr, flush=True)
            time.sleep(delay)
            delay = backoff(delay)
        finally:
            try:
                response.close()
            except (OSError, http.client.HTTPException):
                pass
            try:
                connection.close()
            except (OSError, http.client.HTTPException):
                pass


def parse_args(argv):
    parser = argparse.ArgumentParser(
        prog="mqtt-relay.py",
        description="Republish a Teknoir device MQTT stream into the device's local broker.")
    parser.add_argument("--sse", action="store_true",
                        help="read the stream route directly instead of stdin")
    parser.add_argument("--topic", action="append", default=[], metavar="TOPIC",
                        help="a topic to follow with --sse (repeatable, 1 to 8)")
    parser.add_argument("--api", default=os.environ.get("TEKNOIRBOT_API", ""), metavar="URL",
                        help="the bot API URL with --sse (default: $TEKNOIRBOT_API)")
    parser.add_argument("--key-file", default=os.environ.get("TNCTL_BOT_KEY_FILE", ""),
                        metavar="PATH", help="the bot key file (default: $TNCTL_BOT_KEY_FILE)")
    parser.add_argument("--host", default=DEFAULT_HOST,
                        help=f"the local broker host (default: {DEFAULT_HOST})")
    parser.add_argument("--port", type=int, default=DEFAULT_PORT,
                        help=f"the local broker port (default: {DEFAULT_PORT})")
    parser.add_argument("--prefix", default=DEFAULT_PREFIX,
                        help=f"the topic prefix on the local broker (default: {DEFAULT_PREFIX})")
    parser.add_argument("--dry-run", action="store_true",
                        help="print the messages instead of publishing them")
    return parser.parse_args(argv)


def main(argv=None):
    args = parse_args(argv)

    prefix = args.prefix
    if prefix and not prefix.endswith("/"):
        prefix = prefix + "/"
    if prefix.split("/", 1)[0] == "toe":
        print(
            f"relay: the prefix {args.prefix!r} is under toe/: TOE would forward that back to "
            "the teamspace as the bot's own event; pick another prefix such as in/",
            file=sys.stderr)
        return 3

    if args.sse:
        if not 1 <= len(args.topic) <= MAX_TOPICS:
            print(f"relay: --sse needs 1 to {MAX_TOPICS} topics (--topic)", file=sys.stderr)
            return 2
        for topic in args.topic:
            if not validate_topic(topic):
                print(
                    f"relay: {topic!r} is not a topic a stream can follow: use "
                    "/devices/<device|+>/state/#, /devices/<device|+>/events/#, or a concrete "
                    "topic below them",
                    file=sys.stderr)
                return 2
        try:
            check_api(args.api)
        except Fatal as exc:
            print(f"relay: {exc}", file=sys.stderr)
            return exc.code
        namespace = os.environ.get("TEKNOIRBOT_NAMESPACE", "").strip()
        if not namespace:
            print("relay: --sse needs TEKNOIRBOT_NAMESPACE (the teamspace of the bot)",
                  file=sys.stderr)
            return 2

    try:
        sink = Sink(args.dry_run, args.host, args.port, prefix)
    except OSError as exc:
        print(f"relay: cannot reach the broker {args.host}:{args.port}: {exc}", file=sys.stderr)
        return 1

    try:
        if args.sse:
            return run_sse(args.api, namespace, args.topic, args.key_file, sink)
        return run_stdin(sink)
    except Fatal as exc:
        print(f"relay: {exc}", file=sys.stderr)
        return exc.code
    except KeyboardInterrupt:
        return 0
    finally:
        sink.close()


if __name__ == "__main__":
    sys.exit(main())
