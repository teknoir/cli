#!/usr/bin/env python3
"""A windowed aggregate over device telemetry, for a TeknoirBot.

Reads the device state metrics that mqtt-relay.py mirrored into the local broker
(`in/devices/<device>/state` by default), keeps one window per device, and publishes one summary
per window: count, min, max and mean of one metric, with the device and when the window ended.
The value of a metric is the device's total over its nodes (a multi-node Robot reports one
entry per node). The summary goes out as `toe/events/<OUTPUT_STREAM>` on the local broker; TOE
republishes it to `/devices/tb-<bot>/events/<OUTPUT_STREAM>`, where people and platform services
see it.

Two input modes:

  default   subscribe to the local broker at MQTT_HOST:MQTT_PORT (a Deployment, or the desktop)
  --stdin   read `tnctl mqtt stream --json` lines from stdin and print the summaries instead
            (the laptop path: nothing is published)

Settings come from the environment: MQTT_HOST, MQTT_PORT, INPUT_TOPIC (default
in/devices/+/state), METRIC (default node_cpu_usage, in millicores), WINDOW_SECONDS (default 60)
and OUTPUT_STREAM (default cpu-summary).

Exit codes: 0 at the end of stdin or after SIGTERM, 1 reading stdin failed, 2 a usage error (a bad
OUTPUT_STREAM).
"""

import base64
import json
import math
import os
import re
import signal
import sys
import threading
import time
import uuid

MQTT_HOST = os.environ.get("MQTT_HOST", "mqtt.kube-system.svc.cluster.local")
MQTT_PORT = int(os.environ.get("MQTT_PORT", "1883"))
INPUT_TOPIC = os.environ.get("INPUT_TOPIC", "in/devices/+/state")
METRIC = os.environ.get("METRIC", "node_cpu_usage")
OUTPUT_STREAM = os.environ.get("OUTPUT_STREAM", "cpu-summary").strip()

try:
    WINDOW_SECONDS = float(os.environ.get("WINDOW_SECONDS", "60"))
except ValueError:
    WINDOW_SECONDS = 0.0

STREAM_LABEL = re.compile(r"^[a-z0-9]([-a-z0-9]*[a-z0-9])?$")

# Stream names a platform service already consumes; publishing there feeds a pipeline that is not
# yours (alerts history, the Observatory, the historian) or collides with a demo app.
RESERVED_STREAMS = (
    "inference", "peripheral-manager", "alerts", "movements", "stale-alerts", "file", "file-legacy",
    "line-crossing-cutouts", "face-cover-cutouts", "employee-loitering-cutouts", "person-cutouts",
    "faces", "telemetry", "faults",
)

STOP = threading.Event()
LOCK = threading.Lock()
WINDOWS = {}
SKIPPED = 0


def now_iso():
    stamp = time.strftime("%Y-%m-%dT%H:%M:%S", time.gmtime())
    return f"{stamp}.{int(time.time() % 1 * 1000):03d}Z"


def check_stream(name):
    """'<stream>' is one DNS label, and not one of the reserved event names."""
    if not name or len(name) > 63 or not STREAM_LABEL.match(name):
        return (f"{name!r} is not a stream name: use one DNS label (lowercase letters, digits "
                "and '-', at most 63 characters), for example agg-1m")
    if name in RESERVED_STREAMS:
        return (f"{name!r} is a reserved event name (a platform service consumes it): pick "
                "another, for example agg-1m or rollavg")
    return None


def device_of(topic):
    """The device segment of a device topic, with or without the relay's prefix."""
    parts = topic.split("/")
    for index, part in enumerate(parts):
        if part == "devices" and index + 1 < len(parts):
            candidate = parts[index + 1]
            if candidate and candidate != "+":
                return candidate
    return None


def metric_value(doc, metric_id):
    """The device's total for this metric id (one entry per node), or None."""
    if not isinstance(doc, dict):
        return None
    spec = doc.get("spec")
    if not isinstance(spec, dict):
        return None
    metrics = spec.get("metrics")
    if not isinstance(metrics, list):
        return None
    total, found = 0.0, False
    for item in metrics:
        if not isinstance(item, dict) or item.get("metricId") != metric_id:
            continue
        value = item.get("value")
        if isinstance(value, bool) or not isinstance(value, (int, float)):
            continue
        try:
            number = float(value)
        except OverflowError:
            continue
        if not math.isfinite(number):
            continue
        total += number
        found = True
    return total if found else None


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


def ingest(topic, payload):
    """Add one broker or stream message to its device's window."""
    global SKIPPED
    device = device_of(topic)
    if device is None:
        SKIPPED += 1
        return
    try:
        doc = json.loads(payload.decode("utf-8"))
    except (UnicodeDecodeError, ValueError, RecursionError):
        SKIPPED += 1
        return
    value = metric_value(doc, METRIC)
    if value is None:
        SKIPPED += 1
        return
    add_sample(device, value, time.monotonic())


def add_sample(device, value, now):
    with LOCK:
        window = WINDOWS.get(device)
        if window is None:
            window = {"start": now, "count": 0, "sum": 0.0, "min": value, "max": value}
            WINDOWS[device] = window
        window["count"] += 1
        window["sum"] += value
        window["min"] = min(window["min"], value)
        window["max"] = max(window["max"], value)


def summary(device, window):
    return {
        "apiVersion": "teknoir.org/v1beta2",
        "kind": "TelemetrySummary",
        "metadata": {"id": str(uuid.uuid4()), "timestamp": now_iso()},
        "data": {
            "device": device,
            "metric": METRIC,
            "window_seconds": WINDOW_SECONDS,
            "window_end": now_iso(),
            "count": window["count"],
            "min": window["min"],
            "max": window["max"],
            "mean": round(window["sum"] / window["count"], 3),
        },
    }


def flush_due(now):
    with LOCK:
        due = [d for d, w in WINDOWS.items() if now - w["start"] >= WINDOW_SECONDS]
        return [summary(d, WINDOWS.pop(d)) for d in sorted(due)]


def flush_all():
    with LOCK:
        return [summary(d, WINDOWS.pop(d)) for d in sorted(WINDOWS)]


class Output:
    """Publishes to `toe/events/<OUTPUT_STREAM>`, or prints with --stdin."""

    def __init__(self, use_broker):
        self.topic = "toe/events/" + OUTPUT_STREAM
        self.client = None
        if not use_broker:
            return
        import paho.mqtt.client as mqtt  # only the broker path needs paho

        if hasattr(mqtt, "CallbackAPIVersion"):
            self.client = mqtt.Client(mqtt.CallbackAPIVersion.VERSION2)
        else:
            self.client = mqtt.Client()
        self.client.on_connect = on_connect
        self.client.on_disconnect = on_disconnect
        self.client.on_message = on_message
        if hasattr(self.client, "reconnect_delay_set"):
            self.client.reconnect_delay_set(1, 30)
        self.client.connect_async(MQTT_HOST, MQTT_PORT, keepalive=30)
        self.client.loop_start()

    def send(self, doc):
        payload = json.dumps(doc, separators=(",", ":"))
        if self.client is None:
            print(f"{self.topic} {payload}", flush=True)
            return
        self.client.publish(self.topic, payload, qos=0, retain=False)

    def close(self):
        if self.client is None:
            return
        if hasattr(self.client, "loop_stop"):
            self.client.loop_stop()
        if hasattr(self.client, "disconnect"):
            self.client.disconnect()


def reason_failed(code):
    if code is None:
        return False
    if isinstance(code, int):
        return code != 0
    return bool(getattr(code, "is_failure", False))


def reason_code_of(args):
    for value in reversed(args):
        if isinstance(value, bool):
            continue
        if isinstance(value, int):
            return value
        if hasattr(value, "is_failure") or hasattr(value, "value"):
            return value
    return None


def on_connect(client, *args):
    code = reason_code_of(args)
    if reason_failed(code):
        print(f"processor: the broker refused the connection (reason "
              f"{getattr(code, 'value', code)})", file=sys.stderr, flush=True)
        return
    client.subscribe(INPUT_TOPIC, 0)  # on every connect: a clean session forgets subscriptions


def on_disconnect(client, *args):
    code = reason_code_of(args)
    if reason_failed(code):
        print(f"processor: the broker connection ended (reason "
              f"{getattr(code, 'value', code)}); paho reconnects", file=sys.stderr, flush=True)


def on_message(client, userdata, message):
    try:
        ingest(message.topic, message.payload)
    except Exception as exc:  # noqa: BLE001 - an exception here would kill paho's network thread
        print(f"processor: a message on {message.topic} failed: {exc!r}", file=sys.stderr,
              flush=True)


def on_signal(signum, frame):
    STOP.set()


def run_stdin(out):
    done = threading.Event()
    failed = threading.Event()

    def reader():
        global SKIPPED
        try:
            for line in sys.stdin:
                line = line.strip()
                if not line:
                    continue
                try:
                    doc = json.loads(line)
                except (ValueError, RecursionError):
                    SKIPPED += 1
                    continue
                if not isinstance(doc, dict):
                    SKIPPED += 1
                    continue
                topic = doc.get("topic")
                payload = payload_of(doc)
                if not isinstance(topic, str) or payload is None:
                    SKIPPED += 1
                    continue
                ingest(topic, payload)
        except Exception as exc:
            print(f"processor: reading stdin failed: {exc!r}", file=sys.stderr, flush=True)
            failed.set()
            raise
        finally:
            done.set()  # a reader that dies must not leave the main loop waiting forever

    threading.Thread(target=reader, daemon=True).start()
    # The same timer as the broker path: a quiet stream still closes its windows.
    while not done.wait(0.5) and not STOP.is_set():
        for doc in flush_due(time.monotonic()):
            out.send(doc)
    # The end of the input, or a SIGTERM, flushes every window that still has messages in it.
    for doc in flush_all():
        out.send(doc)
    return 1 if failed.is_set() else 0


def run_broker(out):
    while not STOP.wait(0.5):
        for doc in flush_due(time.monotonic()):
            out.send(doc)
    for doc in flush_all():
        out.send(doc)
    return 0


def main(argv=None):
    args = sys.argv[1:] if argv is None else argv
    stdin_mode = False
    for arg in args:
        if arg == "--stdin":
            stdin_mode = True
        elif arg in ("-h", "--help"):
            sys.stdout.write(__doc__ or "")
            return 0
        else:
            sys.stderr.write(f"processor.py: unknown argument {arg!r} (see --help)\n")
            return 2

    problem = check_stream(OUTPUT_STREAM)
    if problem:
        sys.stderr.write(f"processor: OUTPUT_STREAM {problem}\n")
        return 2
    if WINDOW_SECONDS <= 0:
        sys.stderr.write("processor: WINDOW_SECONDS must be a positive number of seconds\n")
        return 2

    for signum in (signal.SIGTERM, signal.SIGINT):
        signal.signal(signum, on_signal)

    out = Output(use_broker=not stdin_mode)
    try:
        if stdin_mode:
            return run_stdin(out)
        return run_broker(out)
    finally:
        out.close()
        if SKIPPED:
            print(f"processor: skipped {SKIPPED} message(s) without a {METRIC} value for a "
                  "device", file=sys.stderr, flush=True)


if __name__ == "__main__":
    sys.exit(main())
