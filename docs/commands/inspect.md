# `tnctl inspect`

Read what a robot runs, without opening a session: the pod of a failing app, its log, and the objects of an
installed Helm release. It works as a person and inside a TeknoirBot, where it is the only way to read logs.

```bash
tnctl inspect pod     <robot> <podNamespace>/<pod>
tnctl inspect logs    <robot> <podNamespace>/<pod> [-c container] [--previous] [--tail N] [--since 10m]
tnctl inspect release <robot> <releaseNamespace>/<release>
```

The platform answers from the robot's web-terminal pod, read-only. **`tnctl` always asks with `enable=false`**:
the platform then never turns the robot's Shell on. When the Shell is off it answers `409` with

```text
The Shell of vision-station-01 is off; ask a person to turn it on.
```

and `tnctl` prints exactly that (exit 5). Turn it on in the platform's UI (or with `tnctl exec`) and ask again.

As a person the command needs the same access as opening a terminal in the robot; a viewer or someone without
`pods/exec` gets the platform's `403` (exit 6). Inside a TeknoirBot it needs no access of its own.

## `inspect pod`

One row per fact: pod, phase (with its reason), node, QoS class, start time, owner, restarts, the conditions
that are not healthy, and the containers with ready state, restarts and state (for example
`waiting: CrashLoopBackOff` or `terminated: Error (exit 1)`). Recent events follow, warnings first.

```bash
tnctl inspect pod vision-station-01 default/whoami-7d9f-x2k4p
tnctl inspect pod vision-station-01 default/whoami-7d9f-x2k4p -o json
```

## `inspect logs`

The pod's log goes to stdout, line for line, so `| grep` and `> file` work.

| Flag | Description |
| --- | --- |
| `-c, --container` | Container name, for a pod with more than one. |
| `--previous` | The previous instance of the container (after a crash). |
| `--tail N` | Lines from the end (the platform's default when not given). |
| `--since D` | Only lines newer than a duration such as `30s`, `10m`, `2h`. |

When the platform cut the log off, or redacted lines that held credentials, a note goes to stderr; the log on
stdout stays what the app printed.

```bash
tnctl inspect logs vision-station-01 default/whoami-7d9f-x2k4p --tail 50 --since 10m
tnctl inspect logs vision-station-01 default/notebook-0 -c notebook --previous
```

## `inspect release`

The installed Helm release: revision, status, chart and version, deploy time, and the objects it installed as a
table (kind, name, namespace, API version). An object whose values are hidden (always a Secret) says so.

```bash
tnctl inspect release vision-station-01 default/notebook
```

`-o json` prints the platform's answer unchanged, including the installed manifest with Secret values hidden.

## Exit codes

| Exit | Meaning |
| --- | --- |
| 0 | ok |
| 2 | usage: `<namespace>/<name>` expected, a bad `--since` or container name, a bad output format |
| 3 | not found: no such robot, pod or release |
| 5 | rejected: the robot's Shell is off, or the platform refused the request |
| 6 | not logged in or not allowed: opening a terminal in the robot is not this account's |
| 8 | conflict: another inspection of the same robot is running, or the robot is busy |
| 9 | too many requests: wait and ask again |

## See also

- [`logs`](logs.md) — the live log over the tunnel; refused in bot mode.
- [`config`](config.md) — what the robot is configured to run.
- [Bot mode](../bot-mode.md)
