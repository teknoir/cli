# `tnctl mqtt`

Follow the MQTT messages of the teamspace's devices, as a person or inside a TeknoirBot.

```bash
tnctl mqtt stream <topic>... [--count N] [--seconds S] [--json] [--reconnect]
```

## Topics

A topic is

- `/devices/<device|+>/state/#` — the whole state subtree of one device, or with `+` of every device;
- `/devices/<device|+>/events/#` — the same for events;
- or a concrete topic below `/devices/<device|+>/state/` or `/devices/<device|+>/events/`: one or more
  further segments, none empty and none containing `+`, `#`, NUL or whitespace.

`<device>` is `+` or a DNS label (`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`, at most 63 characters), and the whole
topic is at most 256 characters. Anything else is refused on the client (exit 2, nothing is sent): `#`,
`/devices/#`, a shared subscription such as `$share/a//devices/+/state/#`, `/devices/d1/config`,
`/devices/+/commands/#`, `/devices/D1/state/#`, a `+` inside the concrete part. **1 to 8 topics** are followed
at once.

```bash
tnctl mqtt stream /devices/+/state/#
tnctl mqtt stream /devices/vision-01/events/# --count 5 --json
```

## Output

One line per message:

```text
/devices/vision-01/state/telemetry {"cpu":0.25,"memory":0.5}
```

A payload that is not text shows as `<base64:N bytes>`. A text payload keeps its newlines escaped (`\n`, `\r`),
so one message is always one line. With `--json`, each message's data is one JSON line, exactly as the platform
sent it (compacted, so a field this tnctl does not know survives):
`{"topic":"/devices/d1/state/x","payload":"hello","retain":false,"ts":"2026-11-08T10:00:00Z"}`.

Messages the platform had to drop are reported on stderr, with why (`rate`: too many messages too fast, `size`:
the messages were too large, `slow`: the reader did not keep up). An error the platform reports ends the stream: it
is printed and the exit code is non-zero.

| Flag | Description |
| --- | --- |
| `--count N` | Stop after N messages (exit 0). |
| `--seconds S` | Stop after S seconds (exit 0 once a stream opened). |
| `--json` | Print each message's data as one JSON line. |
| `--reconnect` | Connect again when the stream ends, the network fails or the portal restarts. |

Ctrl-C stops cleanly as well. `--reconnect` connects again when the stream ends, the network fails, or the
gateway answers `502`, `503` or `504` while the portal is being replaced: it waits 1 s, then doubles the pause
up to 30 s, and says so on stderr each time; after a connection that delivered messages the wait starts at 1 s
again. It also waits out a `503` that carries `Retry-After` — the platform cannot reach the broker, or is
restarting — for that long, at most 60 s, instead of the 1 s-to-30 s pause; a `429` that carries `Retry-After`
because a bot made more than 120 requests in a minute is waited out the same way. It does **not** reconnect after
a refused request (a bad topic, no access, a device that is not there: `400`, `403`, `404`), a `429` that says
too many live streams are open or that too many bot keys were refused, `live MQTT is not configured`, a missing
key, or an error the platform reported on the stream (`access_lost`, `broker_closed`). If no stream opened before
`--seconds` ends, it exits with the last answer's code, or 4 when no answer came.

Without `--reconnect` the command exits **0 only** for a clean end of the stream (the platform ended a `200`
answer), `--count` or `--seconds` after the stream opened; a network failure — a refused connection, a DNS
error, a reset — is an error: exit 1 with its message on stderr (exit 4 when the connection or the answer timed
out, or `--seconds` ended before any answer came: a platform that cannot reach its broker answers after 10 s).

## Exit codes

| Exit | Meaning |
| --- | --- |
| 0 | ok: stopped by `--count`, `--seconds` (after a stream opened), Ctrl-C, or a clean end of the stream |
| 1 | error: the platform reported an error on the stream; it answered `500`, `502` or `503` (with `--reconnect` only a `503` that says `live MQTT is not configured`, and a `500`, end it); or the network failed without `--reconnect` |
| 2 | usage: a topic that cannot be followed, or more than 8 |
| 3 | not found (`404`): a device in a topic is not in the teamspace, or the platform has no stream route |
| 4 | timeout: without `--reconnect` the gateway answered `504` or no answer came within 30 s; or `--seconds` ended before a stream opened and the last answer was a `504` or none came |
| 5 | rejected (`400`): the platform refused the topics |
| 6 | not logged in, not allowed to follow MQTT in this teamspace, or a missing bot key |
| 9 | rate limited (`429`): too many live streams open (close one), or too many refused bot keys (the bot's key is not accepted: it needs a new one); with `--reconnect` a bot's request budget (more than 120 requests in a minute) is waited out instead |

A run whose `--seconds` ended before any stream opened exits with its last answer's code from this table, with
`--reconnect` too: a `502` or `503` gives 1, a `504` 4, a bot's request budget `429` 9, a network failure 1
(4 when it timed out); 4 when no answer came.

## See also

- [Bot mode](../bot-mode.md) — a bot follows MQTT with its own key.
- [`inspect`](inspect.md) — a pod's log without a stream.
