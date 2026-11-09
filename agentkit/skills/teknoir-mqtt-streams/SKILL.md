---
name: teknoir-mqtt-streams
description: Read, process and publish the MQTT telemetry of Teknoir devices and robots with tnctl mqtt stream. Follow device state and events, sample topics, aggregate per device, alert with hysteresis, join two streams, and publish your own stream from a TeknoirBot. Use when asked to watch, sample, process or publish device, robot or fleet MQTT data, topics or telemetry, or to build a stream processor. For configuration writes use teknoir-config; to run the app itself on a bot computer use teknoir-k3s-apps; for device problems use teknoir-debug.
license: Apache-2.0
metadata:
  teknoir-kit-version: "0.1.0"
---

# Teknoir MQTT streams

Paths in this skill are relative to this skill's directory.

## The model

Devices do not talk to each other. Apps on a device publish to its **device-local** broker
(`mqtt.kube-system:1883`, no authentication). **TOE**, the on-device agent, republishes
`toe/events/<x>` as `/devices/<device>/events/<x>` and `toe/state/<x>` as
`/devices/<device>/state/<x>` on the **teamspace** broker. Platform services — fleet-metrics, the
historian, the Observatory, the platform UI — subscribe there. Your agent **reads** the teamspace
topics only through a scoped, read-only stream (`tnctl mqtt stream`, the `mqtt_sample` tool) and
**writes** only to the local broker's `toe/events/<stream>`, which TOE forwards as the bot's own
event. The agent holds no credential for the teamspace broker.

That gives three rules for everything below:

- Read with `tnctl mqtt stream`; it shows only devices you may already see.
- Publish only to the local broker, only under `toe/events/`, only with a stream name of your own.
- Never subscribe to your own output; it comes back to you as `/devices/tb-<bot>/events/<name>`.

## Topic map

All teamspace topics are under `/devices/<device>/`, where `<device>` is the Device name.

| Topic | Direction | Published by | Payload | Notes |
| --- | --- | --- | --- | --- |
| `/devices/<d>/state` | device to cloud | TOE, about every 60 s | `Metrics` document | the device heartbeat; read it |
| `/devices/<d>/state/<x>[/<y>]` | device to cloud | an app on the device | `Metrics` document | read it; see the label warning below |
| `/devices/<d>/state/events` | device to cloud | TOE, every Kubernetes Event | `core/v1` Event | read it; useful when logs are not available |
| `/devices/<d>/events` and `/events/<x>[/<y>]` | device to cloud | an app on the device | free JSON | read it; your output goes here |
| `/devices/<d>/config` | cloud to device | device-controller | JSON array of objects | **reserved, never publish** |
| `/devices/<d>/commands[/<x>]` | cloud to device | the platform | command JSON | **reserved, never publish** |
| `/devices/<d>/events/inference` | device to cloud | the video pipeline | large, with images | **reserved, never publish** |
| `/devices/<d>/events/peripheral-manager` | device to cloud | a peripheral manager | peripheral JSON | **reserved, never publish** |
| `toe/events/<x>` (local broker) | app to TOE | you | free JSON | TOE forwards it to `/devices/tb-<bot>/events/<x>` |
| `toe/state/<x>` (local broker) | app to TOE | you, rarely | `Metrics` document | forwards to state; only numeric metrics with safe label values |

An event family with a platform consumer is **reserved**: `inference`, `peripheral-manager`,
`alerts`, `movements`, `stale-alerts`, `file`, `file-legacy`, `line-crossing-cutouts`,
`face-cover-cutouts`, `employee-loitering-cutouts`, `person-cutouts`, `faces`, `telemetry`,
`faults`. Publishing there feeds a pipeline that is not yours (the alert history, the Observatory,
the historian, the video pipeline), plants catalog entities, or collides with a demo app.
`config` and `commands` are worse: a `config` item is applied as cluster-admin on the device, and
`commands` restarts the device agent. Never publish to either, anywhere.

Anything else is free: `rollavg`, `agg-1m`, `cpu-summary`, `joined-x`.

## Reading

```bash
tnctl mqtt stream /devices/+/state/# --seconds 10 --json
tnctl mqtt stream /devices/vision-01/events/alerts --count 5 --json
tnctl mqtt stream /devices/+/state/# /devices/+/events/alerts --seconds 60 --reconnect
```

- A topic is `/devices/<device|+>/state/#`, `/devices/<device|+>/events/#`, or a concrete topic
  below one of them with at least one further segment and no `+` or `#` inside it. One stream
  follows 1 to 8 topics of at most 256 characters. Anything else is refused before a request is
  sent (exit 2): `#`, `/devices/#`, `$SYS/...`, a shared subscription, `/devices/d1/config`,
  `/devices/+/commands/#`, a `+` after the device name.
- `--json` prints one JSON line per message:
  `{"topic":"/devices/d1/state","payload":"...","retain":false,"ts":"..."}`. `payload` is a
  **string**; a JSON payload needs a second parse (`jq -c '.payload|fromjson?'`). A payload that is
  not UTF-8 arrives as `payloadBase64`.
- Without `--json` each message is one line, `<topic> <payload>`, and a binary payload shows as
  `<base64:N bytes>`.
- `--count N` and `--seconds S` stop cleanly (exit 0). Dropped messages are reported on stderr,
  with why: `rate` (the platform forwards at most 200 messages per second per stream), `size`
  (a message over 256 KiB) or `slow` (the reader did not keep up). A message over 1 MiB never
  arrives: the broker drops it, and no dropped report counts it.
- `--reconnect` connects again when the stream ends, the network fails, or the gateway answers
  `502`, `503` or `504` while the portal is being replaced (1 s doubling to 30 s; after a
  connection that delivered messages the wait starts at 1 s again), and waits out a `503` that
  carries `Retry-After` (the platform cannot reach the broker, or is restarting) for that long, at
  most 60 s, instead of the backoff; a `429` that carries `Retry-After` because the bot made more
  than 120 requests in a minute is waited out the same way. It does **not** reconnect after a
  refused request (a bad topic, no access, a device that is not there: `400`, `403`, `404`), a
  `429` that says too many live streams are open, `live MQTT is not configured`, a missing key, or
  an error the platform reported on the stream (`access_lost`, `broker_closed`). If no stream
  opened before `--seconds` ends, it exits with the last answer's code. Run a long stream under a
  supervisor (Kubernetes, or a shell loop) so that an error ends and restarts the whole pipeline.
  The platform ends every stream cleanly after 60 minutes and when the portal restarts, so a
  long-running reader always uses `--reconnect` or the relay.
- Four streams at once per caller (per bot for a bot). `ts` is when the platform received the
  message; a payload's own timestamp is when the device produced it, and the two differ after a
  device comes back online (see above).
- A device heartbeat carries **every** pod and node metric, so it is tens of KB per message.
  Sample the narrowest topic you can, and filter before you aggregate.
- A 404 (exit 3) has two meanings. If the message is `device <name> not found`, that device is
  outside what you may see. Any other 404 from `tnctl mqtt stream` means this platform has no
  stream route yet: tell the person (a bot has no other read path).
- Exit 9 (429) on start: read tnctl's message. If it says to try again in N s, the bot made too
  many requests in a minute: wait that long and try once more. Otherwise you already hold 4
  streams: stop a pipeline you started for this task; before you stop or delete a relay
  Deployment, ask the person which one may go. Never retry in a loop. A relay Deployment refused
  this way keeps running and logs `the platform asked the stream to wait (HTTP 429)`.
- Exit 1 with `live MQTT is not configured`: the platform cannot stream this teamspace's MQTT.
  Tell the person; there is nothing to retry.

### A first look

`scripts/sample.sh` wraps the same command with cheap defaults and a `--summary`:

```bash
scripts/sample.sh '/devices/+/state/#'                  # 10 s, 50 messages, JSON lines
scripts/sample.sh --summary --seconds 20 '/devices/+/events/#'
```

Defaults are 10 seconds and 50 messages, at most 60 seconds and 500 messages.

### The `mqtt_sample` tool

When the agent has the Teknoir tool server registered (the `teknoir` server of `tnctl mcp`), the
`mqtt_sample` tool takes `topics` (1 to 8), `seconds` (default 5, at most 30) and `max` (default
50, at most 200) and returns `{"messages":[...],"dropped":N,"stoppedBy":...}`. It is the same read
as the command, with no shell, but a payload over 4096 bytes (a binary one over 3072) is cut
(`payloadTruncated`, `payloadBytes`) and a sample stops when it is full (`stoppedBy: "size"`).
Prefer the command when you need whole payloads or to process the output.

## Payload formats

**State heartbeat** (`/devices/<d>/state`) is a `Metrics` document. The field is `metricId`
(camel case), and `node_cpu_usage` is in **millicores** (250 means a quarter of one core), not a
fraction — never render it as a percentage without dividing by the node's capacity:

```json
{"apiVersion":"teknoir.org/v1","kind":"Metrics","metadata":{"name":"DeviceMetrics"},
 "spec":{"metrics":[
  {"metricId":"node_cpu_usage","description":"...","labels":{"name":"node-1"},"value":310},
  {"metricId":"pod_running","description":"...","labels":{"name":"app","namespace":"default","node":"node-1"},"value":1}]}}
```

`node_cpu_usage` is in millicores; the field is `metricId`. TOE also emits `node_*`, `pod_*`,
`app_up`, `app_restarts`, `toe_build_info`, `toe_cloud_queue_size` and `toe_edge_queue_size`.

**A Kubernetes Event** (`/devices/<d>/state/events`) is a full `core/v1` Event:

```json
{"kind":"Event","apiVersion":"v1","metadata":{"name":"app.17f","namespace":"default"},
 "reason":"BackOff","message":"Back-off restarting failed container","type":"Warning"}
```

**A custom event** (`/devices/<d>/events/<x>`) is free JSON. The platform's own families use an
envelope: `apiVersion` `teknoir.org/v1beta2`, `kind`, `metadata.{id,namespace,timestamp}` and
`data`. Keep to it: `metadata.id` is what deduplication keys on. Timestamps are UTC with
milliseconds and a `Z`.

```json
{"apiVersion":"teknoir.org/v1beta2","kind":"RobotTelemetry",
 "metadata":{"id":"7c1f...","timestamp":"2026-11-09T10:00:01.000Z"},
 "data":{"robot":"mobile-01","mode":"AUTONOMOUS","speed_mps":0.7,
         "battery":{"percent":71.4,"charging":false},"faults":["LIDAR_DROPOUT"]}}
```

An alert (`events/alerts`, reserved) carries `data.type` (for example `person_down`,
`line_crossing`, `motion`), `data.severity`, `data.from_device` and a `detection` with `label`,
`score` and normalized box coordinates. Movement events have the same envelope with
`kind: MovementEvent`. An inference message carries a base64 JPEG in `data.image`: reading
`events/alerts` or a concrete non-image topic is cheaper than `events/inference`, whose frames the
stream mostly drops as too large.

## Processing patterns

All of them read the `--json` lines, so they work in the desktop terminal and in a pod. `ts` is
the platform's receipt time; prefer a payload's own timestamp for event-time windows, and tolerate
late messages (a device that was offline delivers a burst with old timestamps).

**Filter** — one device, one field:

```bash
tnctl mqtt stream '/devices/+/events/#' --json | jq -c '
  select(.topic | test("^/devices/vision-01/"))
  | .payload | fromjson? | select(.data.type == "motion")
  | {device: "vision-01", type: .data.type, ts: .metadata.timestamp}'
```

**Per-device windowed aggregate** — the shape of `templates/processor.py`: keep
`{count, sum, min, max}` per device, close the window when its start is `WINDOW_SECONDS` old in
wall-clock time, publish one summary per device and reset. Close on a timer, not only on the next
message, or a quiet device never reports (the template flushes on its own timer, on the same
timer in broker and in `--stdin` mode).

**Rate and threshold alert with hysteresis** — never alert on a single sample:

```python
# per device; fire once when the value crosses high, clear once it drops below low
if not state["firing"] and value >= high:   state["firing"] = True;  emit("raised", value)
elif state["firing"] and value <= low:      state["firing"] = False; emit("cleared", value)
```

Use a sliding window (mean over the last N seconds, or N of M samples) for the value, keep `high`
above `low` (for example 900 and 600 millicores), and put the threshold, the window and the
device in the emitted event so the next reader knows what fired.

**Join of two streams by device and time** — subscribe both topics in one command (up to 8), keep
the last value of the slow stream per device, attach it to a fast event when it arrives, and mark
it stale when it is older than twice the producer's period (about 120 s for a heartbeat). Never
wait for the pair: emit the event with `"state": null` and a `"stale": true` flag.

**Dedupe** — events carry `metadata.id`; fall back to a hash of the payload. Keep a bounded LRU
(4096 entries is plenty for a bot) so the memory cannot grow. The stream is at-most-once: a
reconnect loses the messages of the gap and creates no duplicates, so duplicates come from a
producer that republishes the same event.

**Rules of thumb**

- Never feed your own output back in: an input set that includes `/devices/tb-<bot>/events/#`
  matches your own output once TOE forwards it. Filter the bot's own device out, always.
- Sample before you process, and keep the input set narrow.
- Do not block the reader. The stream drops and counts; a stalled stdin backs up into tnctl, and
  the platform then drops messages and reports them as `dropped` with the reason `slow`. Use
  bounded queues and count your own drops.
- Publish state as an event, not as `toe/state/...`: fleet-metrics renders state label values
  **unescaped**, so a `"`, a `\` or a newline in a label value breaks the whole teamspace's
  Prometheus scrape. If you must publish state, use numbers only and label values from
  `[A-Za-z0-9_.:/-]`, and repeat at least every 60 s (the series expire after 90 s).
- Keep each published message well under 256 KiB (the stream would drop it, and TOE drops over
  3 MB).
- When the input stream ends, end the processor too and let the supervisor decide whether to
  restart. The kit's relay and processor exit 0 at the end of stdin, so a shell loop must look at
  tnctl's own status: restart only after tnctl exits 1 (an error event such as `access_lost`, or a
  server error), never after another exit code, and stop when its message says `live MQTT is not
  configured`. The loop needs bash (`PIPESTATUS`): put it in a file and run it with `bash <file>`,
  never `sh`. tnctl's own messages are in `tnctl.err` while it runs.

  ```bash
  while :; do
    tnctl mqtt stream '/devices/+/state/#' --json --reconnect 2> tnctl.err | scripts/mqtt-relay.py
    rc=${PIPESTATUS[0]}; cat tnctl.err >&2
    [ "$rc" -eq 1 ] && ! grep -q 'live MQTT is not configured' tnctl.err || break
    sleep 30
  done
  ```

## Publishing results (bot)

Publish to the bot computer's **local** broker on `toe/events/<stream>`. TOE republishes it to
`/devices/tb-<bot>/events/<stream>`, where people and platform services see it.

```bash
mosquitto_pub -h mqtt.kube-system.svc.cluster.local -t toe/events/test -m '{"hello":"world"}'
mosquitto_sub -h mqtt.kube-system.svc.cluster.local -t 'toe/#' -v   # what TOE will forward
```

`<stream>` is one DNS label — lowercase letters, digits and single hyphens, starting and ending
with a letter or a digit, at most 63 characters — and not
one of the reserved event names above. Good names: `rollavg`, `agg-1m`, `cpu-summary`. Never
publish the reserved topics, and never mirror anything under `toe/` (TOE would forward the mirror
back to the teamspace as the bot's own event).

## Two ways to run a processor on the bot's computer

**(a) In the desktop terminal.** No Deployment; it stops when the desktop restarts:

```bash
tnctl mqtt stream '/devices/+/state/#' --json --reconnect | scripts/mqtt-relay.py
INPUT_TOPIC='in/devices/+/state' WINDOW_SECONDS=60 python3 templates/processor.py
```

`scripts/mqtt-relay.py` republishes the stream onto the local broker under `in/<topic>`, so
`processor.py` is a plain MQTT client of the local broker and runs as its own command. Both keep
running until you stop them. A person can leave the terminal open. An agent starts each in the
background; each tool call is a new shell, so keep the process ids in files:

```bash
nohup setsid sh -c 'echo $$ > relay.pid; tnctl mqtt stream "/devices/+/state/#" --json --reconnect | scripts/mqtt-relay.py' > relay.log 2>&1 &
INPUT_TOPIC='in/devices/+/state' nohup python3 templates/processor.py > processor.log 2>&1 & echo $! > processor.pid
```

Read the logs. When the trial is over, stop the whole relay pipeline with
`kill -- -"$(cat relay.pid)"` (killing only the `sh` leaves tnctl streaming and holding one of
your 4 streams), and the processor with `kill "$(cat processor.pid)"`.

**(b) As Deployments in `apps`.** For something that must survive a desktop restart, the same two
pieces are two Deployments built with the `teknoir-k3s-apps` skill:

1. The relay runs with `--sse`: it reads the stream route itself with the bot key, which needs
   the key in the `apps` namespace. `scripts/teknoirbot-app-key <name>` copies it into the Secret
   `apps/<name>` without printing it.
2. The processor runs as its own Deployment and subscribes to the local broker.

A relay Deployment holds one of the bot's 4 streams for as long as it runs.

```yaml
# relay: needs the key Secret, mounted read-only
args: ["--sse", "--key-file", "/run/teknoirbot-app/key", "--topic", "/devices/+/state/#"]
env:
  # copy the values from your environment; none of them is secret
  - {name: TEKNOIRBOT_API, value: "<the value of $TEKNOIRBOT_API>"}
  - {name: TEKNOIRBOT_NAMESPACE, value: "<teamspace>"}
  - {name: TEKNOIRBOT_NAME, value: "<bot>"}
volumeMounts:
  - {name: bot-key, mountPath: /run/teknoirbot-app, readOnly: true}
# at the relay pod's level (not the processor's)
volumes:
  - {name: bot-key, secret: {secretName: <name>}}

# processor: no key at all, only the local broker
env:
  - {name: MQTT_HOST, value: "mqtt.kube-system.svc.cluster.local"}
  - {name: INPUT_TOPIC, value: "in/devices/+/state"}
  - {name: OUTPUT_STREAM, value: "cpu-summary"}
```

The relay keeps reconnecting by itself, also through a portal restart (the gateway's `502`, `503`
or `504`), except after the platform ends the stream with an error (`access_lost`,
`broker_closed`); Kubernetes then restarts the pod, which is what you want — access may come back.
So an EMQX restart (`broker_closed`) restarts the relay pod once: while the broker is down, the
new pod keeps running and logs `cannot be reached right now` with `connecting again in 10.0s`
until the broker is back. The relay also stops at once when the portal has no live MQTT
(`live MQTT is not configured`); ask an admin, restarting does not help. A relay in
CrashLoopBackOff is never a broker or portal restart: read its log (a refused key or topics, a
404, `the stream route answered HTTP 500`, `live MQTT is not configured`, `access_lost` each time).

The copy in `apps` is as sensitive as the key itself and stays inside the bot's own computer.
Never `kubectl get` or `kubectl describe` that Secret with `-o yaml`, `-o json` or `-o jsonpath`;
nor with any other output format (`-o go-template`, `-o template`, `-o custom-columns`), nor by
listing the Secrets of `apps` with an output format, nor with `kubectl edit`: list Secrets only
with the default table or `-o name`. Never `kubectl exec` into a pod that mounts it; never print
its env or files. Mount it read-only only into the relay. Delete it
(`kubectl -n apps delete secret <name>`) when the relay is gone.

## People on a laptop

A person reads with the same command under their own login, and processes locally:

```bash
tnctl mqtt stream '/devices/+/state/#' --json | python3 templates/processor.py --stdin
```

`--stdin` reads the `--json` lines and prints the summaries instead of publishing them. Windows
close on the same timer as in broker mode, so a quiet stream still prints. Publishing to the
platform needs a device: the person's own device app, a Notebook or a Devstudio. There is no bot
computer and no local broker to publish to.

## Safety

- Never fetch, print, store or pass on a bot key, a device credential or a token. The only places
  a skill reads the key file are `scripts/teknoirbot-app-key`, which copies it into a Secret
  without printing it, and `scripts/mqtt-relay.py --sse`, which reads the file it is given and
  prints nothing of it.
- Never publish to `/devices/<d>/config`, `/devices/<d>/commands`, `.../events/peripheral-manager`
  or `.../events/inference`, and never publish a reserved event name.
- Read before you write, and read the `dropped` lines: they tell you the stream is rate- or
  size-limiting you, not that the devices are quiet.

## Bot mode

In bot mode (`tnctl whoami -o json` reports `"mode":"bot"`) everything above applies with these
differences:

- The stream is the **only** read path. The bot holds no teamspace broker credential, so there is
  no `mosquitto_sub` on `/devices/...#`, no MQTT client with a password, and no token to mint.
  A 404 whose message is not `device <name> not found` means this platform has no stream route
  yet: tell the person, and do not work around it.
- The stream needs access `read` or `creator`. On exit 6, run `tnctl whoami -o json` and branch on
  `.bot.access`: at `none`, ask the bot's creator to set it to Read only or Act as me on the Bots
  page. If whoami fails too, or access is not `none`, give the person tnctl's message unchanged
  (the bot may be paused, or its computer disabled). Only when tnctl says 'the bot key is missing
  or empty' or 'the bot API did not accept the bot's key' must a person re-create the bot. Never
  retry.
- The bot reads only devices in its creator's scope inside its own unit. A device outside it is a
  404, as if it did not exist.
- The bot's output is `toe/events/<stream>` on its own computer, nothing else. It never publishes
  on the teamspace broker.
- `kubectl`, `buildctl` and the local broker exist only on the bot's computer; the relay and the
  processor as Deployments need the `teknoir-k3s-apps` skill. The bot's own device is
  `tb-$TEKNOIRBOT_NAME` (`$TEKNOIRBOT_NAME` holds the bot's name without `tb-`); filter out topics
  under `/devices/tb-$TEKNOIRBOT_NAME/`.
- What a bot may run and where is in [bot-vm.md](../teknoir-platform/references/bot-vm.md).
