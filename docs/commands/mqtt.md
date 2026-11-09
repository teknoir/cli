# `tnctl mqtt`

Follow the MQTT messages of the teamspace's devices, as a person or inside a TeknoirBot.

```bash
tnctl mqtt stream <topic>... [--count N] [--seconds S] [--json] [--reconnect]
```

## Topics

A topic is

- `/devices/<device|+>/state/#` — the whole state subtree of one device, or with `+` of every device;
- `/devices/<device|+>/events/#` — the same for events;
- or a concrete topic below `/devices/<device>/state/` or `/devices/<device>/events/`.

Anything else is refused on the client (exit 2, nothing is sent): `#`, `/devices/#`, a shared subscription such
as `$share/a//devices/+/state/#`, `/devices/d1/config`, `/devices/+/commands/#`, a `+` inside the concrete part.
**1 to 8 topics** are followed at once.

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
the messages were too large). An error the platform reports ends the stream: it is printed and the exit code is
non-zero.

| Flag | Description |
| --- | --- |
| `--count N` | Stop after N messages (exit 0). |
| `--seconds S` | Stop after S seconds (exit 0). |
| `--json` | Print each message's data as one JSON line. |
| `--reconnect` | Connect again when the stream ends or the network fails. |

Ctrl-C stops cleanly as well. `--reconnect` waits 1 s, then doubles the pause up to 30 s, and says so on stderr
each time; after a connection that delivered messages the wait starts at 1 s again. It does **not** reconnect
after a refused request (a non-2xx answer: a bad topic, no access), a missing key, or an error the platform
reported: those are not network failures.

Without `--reconnect` the command exits **0 only** for a clean end of the stream (the platform ended a `200`
answer), `--count` or `--seconds`; a network failure — a refused connection, a DNS error, a reset — is an error:
exit 1 with its message on stderr.

## Exit codes

| Exit | Meaning |
| --- | --- |
| 0 | ok: stopped by `--count`, `--seconds`, Ctrl-C, or a clean end of the stream |
| 1 | error: the platform reported an error on the stream, or the network failed without `--reconnect` |
| 2 | usage: a topic that cannot be followed, or more than 8 |
| 6 | not logged in, not allowed to follow MQTT in this teamspace, or a missing bot key |

## See also

- [Bot mode](../bot-mode.md) — a bot follows MQTT with its own key.
- [`inspect`](inspect.md) — a pod's log without a stream.
