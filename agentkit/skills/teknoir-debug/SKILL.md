---
name: teknoir-debug
description: Find out why a Teknoir robot, device or app is unhealthy - offline, Degraded, waiting for its device, an app that crash-loops, restarts, cannot pull its image or fails its readiness check, or a robot whose Shell or Tunneling is off. Use it when a task says a robot is broken, down, offline, degraded, erroring, OOM-killed or not running, or asks for its logs, pods, events, metrics or a root cause. Triage in order with tnctl whoami, the fleet API overview and problems, robot access, config state, tnctl inspect and Prometheus. Not for changing what a robot runs (teknoir-config) and not for creating robots or onboarding a computer (teknoir-robots).
license: Apache-2.0
metadata:
  teknoir-kit-version: "0.1.0"
---

# Why a robot is unhealthy

Work through the steps in order. Each one narrows the cause, and most answers arrive in the first
three. Every command is read-only.

Paths in this skill are relative to this skill's directory.

## Triage order

### 1. Identity

```bash
tnctl whoami -o json
```

`{"mode":"user",...}` or `{"mode":"bot","bot":{"namespace","access",...},...}`. The teamspace is
`.namespace` for a person and `.bot.namespace` for a bot; the block below writes it as `<teamspace>`
and you pass it as `-n <teamspace>` after the subcommand of every later command. The bot's access
levels are in [Bot mode](#bot-mode).

Stop here if `whoami` answers "unknown command": tnctl is older than this kit. Tell the person to
upgrade it and stop.

### 2. Overview and problems

```bash
tnctl api GET <teamspace>/overview -o json
tnctl api GET <teamspace>/problems -o json
tnctl api GET "<teamspace>/problems?robot=<robot>" -o json      # one robot
```

- `overview.robots` counts `total, online, waiting, degraded, offline, disabled`; `overview.problems`
  counts `total, critical, warning, byKind`; `overview.needsYou` lists what wants a person, worst first.
- `problems.problems[]` has one entry per failing app or node, critical first. Read `kind`,
  `severity`, `title`, `detail`, `hint`, and for an app `pods[]` (the pod names `inspect` takes),
  `namespace`, `image`, `reason`, `restarts`, `since`.
- **`source: "unavailable"` with an empty list is not healthy.** Prometheus could not be asked: the
  problems are unknown, not absent. Say so, and keep going with the robot's phase.
- There is no `tnctl overview` or `tnctl problems` command: the fleet API route is the way.

### 3. The robot itself

```bash
tnctl robot <robot> -n <teamspace> -o json
tnctl api GET <teamspace>/robots/<robot>/access -o json
```

| Read | Where | What it tells you |
|---|---|---|
| `phase`, `phaseMessage` | `robot` | the platform's verdict: `WaitingForDevice`, `Registered`, `Online`, `Degraded`, `Offline`; the message is a sentence |
| `nodes[]` | `robot` | each computer: `role`, `joined`, `ready` (a node that is not ready explains `node-not-ready`) |
| `device` | `robot` | the device name that `device_id`, the MQTT topics and `devices/<device>` take; it is not always the robot's name |
| `disabled` | `robot` | set while the device is disabled or on its way in or out: `state`, `by`, `reason` |
| `phaseMessage` for a Degraded robot | `robot` | the platform replaces it with a sentence naming the problems |
| `tunnel.state`, `tunnel.connected` | `access` | `active`, `pending`, `revoked`, `disabled`, `off`; `connected` is whether the robot's agent is up now |
| `terminal.state` | `access` | the Shell's state (`ready`, `off`, `starting`, `offline`, `paused`, ...): what `inspect` can do |
| last seen | step 7 | the API has no last-seen field: read `device_last_seen` from Prometheus |

Conditions are **not** in the API answer: read `phase` and `phaseMessage`. A virtual device's
`Admitted` condition is not returned either; see [references/failures.md](references/failures.md).

### 4. Configuration state

```bash
tnctl config get <robot> -n <teamspace> -o json
```

An app that is missing or runs an old version is a configuration problem, not a crash. What to look
at:

- `configuration.deviations[]`: resources that differ from the robot type, with the reason `off`,
  `overridden` or `missing`.
- `configuration.warnings[]`: overrides the platform could not apply.
- `overrideStatus[]`: entries with `state` `invalid` or `no-target` will never apply.
- rows with `status: "pending"`: the controller has not rendered them yet.
- `published`: absent means the controller has published nothing. When present, `hash` and
  `lastChange` say when. A `published.message` explains a hold-back: an item that is invalid keeps its
  previous published version (`ConfigItemHeldBack`) or is left out (`ConfigItemDropped`).
- There is no "applied" in the config answer: the platform knows what it published, and only a read
  on the robot itself (`config wait --for applied`, over WireGuard) knows what the robot did with it.
  If the published configuration is right, the problem is on the robot: step 5.

Changing any of it is the [../teknoir-config/SKILL.md](../teknoir-config/SKILL.md) skill.

### 5. Pods, logs and releases

```bash
tnctl inspect pod <robot> <namespace>/<pod> -n <teamspace> -o json
tnctl inspect logs <robot> <namespace>/<pod> -n <teamspace> --tail 100
tnctl inspect logs <robot> <namespace>/<pod> -n <teamspace> --previous --tail 100
tnctl inspect release <robot> <namespace>/<release> -n <teamspace> -o json
```

- Pod names come from step 2 (`problems[].pods[]` and `problems[].namespace`), from the device's
  `state.apps` (each with its `namespace`), or from Prometheus: `pod_running{device_id="<device>"}`
  lists every pod with `name` and `namespace`. Inspect refuses a pod in `kube-system` or
  `teknoir-desktop` (the remote desktop, which `state.apps` lists too) unless a problem names it.
- `inspect pod` also carries the recent events (`-o json`: `events[]`), so you usually do not need a
  separate event source. `eventsTruncated` says the list was cut.
- `--previous` is the log of the container instance before a restart: that is where a crash-loop's
  error is. `--tail` is 1 to 2000 (default 500); `--since` takes `30s`, `10m`, `2h`, at most `168h`.
- `inspect release` shows the Helm revision, status and the objects it installed - the way to check
  that a chart actually installed.
- **It needs the robot's Shell.** A bot needs access `creator`. When the Shell is off, the answer is
  exit 5 with `The Shell of <robot> is off. Ask a person to turn it on in the portal.` Hand that to a
  person (see [The Shell](#the-shell)).
- A container that never started (an image pull failure) has no log: read the pod's events instead.

### 6. Events from the robot

```bash
tnctl mqtt stream /devices/<device>/state/events -n <teamspace> --seconds 60 --count 50 --json
```

Every Kubernetes Event of the robot is published there. This is the only event source for a bot at
access `read`, where `inspect` is refused. **Only events from now on arrive**: the messages are not
retained, so run it while the problem happens, or ask the person to reproduce it. `--seconds` bounds
the run; `--json` prints each event's data as one JSON line.

A 404 whose message is `device <name> not found` means that device is outside what you may see.
When tnctl says `this platform has no live MQTT stream yet`, the platform is older than this tnctl:
say so instead of retrying.

### 7. Prometheus

```bash
q='time() - max(device_last_seen{device_id="<device>"} > 0)'
tnctl api GET "<teamspace>/prometheus/query?query=$(jq -rn --arg q "$q" '$q|@uri')" -o json
```

- The query is sent exactly as written: percent-encode it yourself, as above.
- Pin `device_id` in every selector. For an account limited to org units, and for a bot placed in a
  unit, the platform adds the devices in scope to every selector. A device outside that scope, or a
  series without `device_id` such as `up`, gives an empty `result`, not an error. For such an
  account, a query the platform cannot parse or check is refused (exit 6). For everyone, a query over
  4096 characters, or one Prometheus rejects, is exit 5.
- The answer is the Prometheus API's JSON: `{"status":"success","data":{"result":[...]}}`. An empty
  `result` is a valid answer: no series.
- The `pod_*` and `node_*` series of a device disappear about 90 seconds after it stops reporting;
  only `device_last_seen` stays. An empty `pod_running` next to an old `device_last_seen` means the
  robot is offline, not that it has no pods.
- Metric names, units and recipes: [references/promql.md](references/promql.md).

## Failure tables

### Problem kinds

`kind` of a problem, worst first within its severity:

| Kind | Severity | Meaning | First step |
|---|---|---|---|
| `image-pull` | critical | the robot cannot download the image (`ImagePullBackOff`, `ErrImagePull`, `ErrImageNeverPull`, `InvalidImageName`) | the pod's events (`inspect pod`): a wrong tag, a 401/403, a DNS or timeout error; then the image name and the registry |
| `crash-loop` | critical | the container starts and stops (`CrashLoopBackOff`, `Error`, `OOMKilled`, a restart storm) | `inspect logs --previous`, then `inspect pod` for the exit code and `oomKilled` |
| `not-running` | critical | the pod is not running (`Pending`, `ContainerCreating`, `CreateContainerConfigError`, ...) | the pod's Warning events: `FailedScheduling` is resources or taints, `FailedMount` a volume, `CreateContainerConfigError` a missing ConfigMap or Secret |
| `not-ready` | warning | it runs, but not every container is ready | the logs and the readiness probe's events (`Unhealthy`) |
| `high-memory` | warning | a container has been above 90 % of its memory limit for 15 minutes | raise the limit or cut the load (the config skill) |
| `high-cpu` | warning | above 90 % of its CPU limit for 15 minutes | it is throttled at its limit |
| `disk` | warning (critical when the alert says so) | a node is low on disk | free space on the robot: a person's job |
| `node-not-ready` | critical | a computer's k3s agent does not report Ready | power, network, or the k3s service: a person's job |
| `node-pressure` | warning | a node is above 90 % of its CPU or memory | check the workloads; apps may be evicted |
| `device-lost` | critical | a device that backs no robot has not reported for minutes | power and network |
| `other` | the rule's severity, else warning | any other firing alert (`gpu`, `cpu_temp`, ...) | the alert name in `hint` and `rules[]` |

Problems lag: the alert rules wait 5 minutes for pods and nodes, 15 for resources, 1 for disk. A
failure that just happened is not in `/problems` yet - check `pod_running == 0` directly
([references/promql.md](references/promql.md)).

### What the three critical workload kinds usually mean

| What you see | Likely cause | Where the answer is |
|---|---|---|
| `image-pull`: `manifest unknown`, `not found` | the tag is wrong or the image was never pushed | the pod's events, then the image in `config get` |
| `image-pull`: `unauthorized`, `pull access denied`, `403` | a private image the robot has no credential for | the pod's events; the robot's registry access |
| `image-pull`: `no such host`, `i/o timeout` | the robot cannot reach the registry (DNS, egress, an airgapped robot) | the pod's events; a person checks the network |
| `crash-loop`: last state `OOMKilled`, exit 137 | the container passed its memory limit | `inspect logs --previous`; raise the limit (teknoir-config) |
| `crash-loop`: exit 1 or 2 | the app's own error, in its previous log | `inspect logs --previous` |
| `crash-loop`: exit 126 or 127 | a wrong or missing command or entrypoint | the image, or `command`/`args` |
| `not-running`: `CreateContainerConfigError` | a ConfigMap or Secret it needs does not exist | the pod's events |
| `not-running`: `FailedScheduling` | no node has the CPU, memory or taint tolerance | the pod's events; `node_cpu_requests` against `node_cpu_allocatable`, and `node_memory_requests` against `node_memory_allocatable` |
| `not-ready`: the readiness probe fails | the app is up but not answering | the logs and the `Unhealthy` event |

More, including the `NotAdmitted` reasons of a virtual device and the exit codes of a crash loop:
[references/failures.md](references/failures.md).

### Phases

| Phase | Meaning | What you do |
|---|---|---|
| `WaitingForDevice` | the device does not exist yet, has no keys, or conflicts with another robot or a TeknoirBot's computer | read `phaseMessage`; a minute after a create is normal |
| `Registered` | the device has keys but has never reported | the computer was never bootstrapped: a person's job (teknoir-robots) |
| `Online` | it reported within 5 minutes and no alert is firing | if the app still fails, it is the app: steps 4 to 5 |
| `Degraded` | it is online and an alert is firing | the problems of step 2 name it |
| `Offline` | it reported before and has been silent for 5 minutes or more | power and network on site; check the tunnel state in step 3 |

A **disabled** robot is in none of these: `disabled` is set, its apps keep running, and it cannot
reach the platform. Only a teamspace admin enables it.

The controller reconciles every 60 seconds, so a phase lags reality by up to a minute, and it keeps
its previous phase while Prometheus is unreachable - a stale phase is possible.

### Degraded with no problem

`Degraded` with an empty problem list means the device reports a firing alert in
`state.alerts[]` that the problems view does not fold into an entry. Name those alerts
(`alerts[].name` from `tnctl api GET <teamspace>/devices/<device>`) and give them to a person if they
are not recognisable.

### Tunneling and access states

| `tunnel.state` | Meaning | What you do |
|---|---|---|
| `active` | Tunneling works | check `connected`: false means the robot's agent is not up |
| `pending` | enrolled but not active yet (or a state the platform does not recognise) | wait a minute after a reboot; a stuck `pending` is an admin's job |
| `revoked` | flagged compromised or health-check-failed, or the identity is void | an admin's job: `tnctl wg status <device>` names the reason |
| `disabled` | Tunneling is off for this device | an admin's job |
| `off` | not enabled | an admin's job |

The terminal's `AccessState` also has `no-exec` (your account may not open a terminal on it): access,
not a fault. `unavailable` belongs to the remote desktop only.

### inspect error codes

tnctl does not print the platform's code. With `-o json`, `.error.code` is tnctl's own name for the
exit code (`rejected`, `conflict`, `not_found`, `auth` or `session_expired`, `rate_limited`,
`timeout`, `usage`, `error`), and `.error.message` holds the platform's sentence: alone for
`terminal_off`, otherwise followed by `(HTTP n)`, and for a 403 after `not allowed for this account
or teamspace: `. Branch on the exit code first, then use the message to tell apart rows that share an
exit code. The Code column gives the platform's name for reference.

| Code | HTTP | tnctl | Meaning | What you do |
|---|---|---|---|---|
| `terminal_off` | 409 | **5** | the robot's Shell is off (or paused, `disabled`, `tunneling-off`, `outdated`) | read `.terminal.state` from `tnctl api GET <teamspace>/robots/<robot>/access -o json`. If it is `off`, a person turns the Shell on in the portal, then you ask again. Any other state is an admin's job (see Tunneling), or a person runs `tnctl logs`. |
| `not_ready` | 409 | 8 | the message says the Shell is starting | ask again after 5 s, at most a few times |
| `not_started` | 409 | 8 | "The container has not started yet.": an image pull or volume wait, so there is no log | do not retry; read the events with `inspect pod` |
| `invalid` | 400 | 5 | the platform refused a parameter | fix the command; do not retry |
| `forbidden` | 403 | 6 | the robot refused the read | report it; do not retry |
| `not_found` | 404 | 3 | not a pod or release of this robot, or the pod is in `kube-system` or `teknoir-desktop` (the remote desktop) and in no problem | use the names from step 2 |
| `no_previous` | 404 | 3 | the container never restarted, so there is no previous log | drop `--previous` |
| `no_write` | 403 | 6 | the person needs the developer role in the teamspace | a teamspace admin |
| `no_exec` | 403 | 6 | the person may not open a terminal on devices here (`pods/exec`) | a teamspace admin |
| `offline` | 503 | 1 | the robot is offline | step 3 |
| `unavailable` | 503 | 1 | inspection is off on this platform, or this robot cannot answer it (an older robot agent or web terminal) | a person runs `tnctl logs` from their computer; an admin updates the robot |
| `rate_limited`, `session_limit` | 429 | 9 | too many reads, or 5 sessions already open | wait, close a Shell tab, or ask again |
| `busy` | 429 | 9 | the robot or the portal is busy with other reads | wait `error.retryAfter` s and retry once |
| `too_large` | 413 | 1 | the answer is bigger than the platform allows | narrow it with `--tail` or `--since` |
| `device_error` | 502 | 1 | the read failed on the robot | retry once; then a person |
| `timeout` | 504 | 4 | the read took too long | retry once with a smaller tail |

`ImagePullBackOff`, `CrashLoopBackOff` and the virtual device's `NotAdmitted` reasons are in
[references/failures.md](references/failures.md).

## The Shell

The Shell is the robot's web terminal: a small pod on the robot, reached over Tunneling, that the
platform runs a few fixed, read-only `kubectl` commands in for `tnctl inspect`. So `inspect` needs
Tunneling to be active, the terminal to be on and ready, and the caller to be allowed to open a
terminal.

- **Never turn a Shell on.** `tnctl inspect` always sends `enable=false`, so it never does. Never call
  an inspect route with a raw `tnctl api`: a bare GET without `enable=false` turns the Shell on for a
  person - a state change through a read.
- **A person turns it on** on the robot's page in the portal (the Shell button), or by opening a
  terminal there once. A bot never can, and a bot can never read the robot's logs without it.
- When it is off, `inspect` exits 5 and says so. Give the person the robot and the pod
  (`<namespace>/<pod>`) and ask them to turn the Shell on in the portal; then retry.

## Hand-over to a person

Hand over when the answer needs something only a person has: a shell on the robot, the portal, an
admin right, or a physical check.

| When | Why | What to give them |
|---|---|---|
| the Shell is off (`inspect` exit 5 and `.terminal.state` is `off`) | only a person turns it on | the robot, the pod, "turn the Shell on in the portal", then you retry |
| the tunnel is `revoked`, `disabled`, `pending` or `off`, or the terminal is `paused`, `tunneling-off`, `disabled` | an admin act | `tnctl wg status <device>` and the state you saw |
| the robot is `Offline` for more than 5 minutes | power or network on site | the last-seen age and the tunnel state |
| the robot is `Registered` | it was never bootstrapped | `tnctl bootstrap <device> ...` (teknoir-robots); it needs the device login |
| `WaitingForDevice` for more than a minute | no device, no keys, or a conflict with another robot or a TeknoirBot | the `phaseMessage`; a teamspace admin |
| `disk`, `node-pressure`, or anything needing `df`, `journalctl` or `kube-system` | needs a shell on the device | `tnctl exec <robot> -- df -h /`, `tnctl logs <robot> <resource>` |
| exit 6 and the message names the developer role or pods/exec, or a bot at Read only | rights, not a fault | ask a teamspace admin, or switch the bot to Act as me |
| the fix is a configuration change and the bot is at `read` | writes need `creator` | the exact `tnctl config` command, from teknoir-config |
| a virtual device's `NotAdmitted` or `Failed` | the fleet API does not return it | the device or bot page in the portal; an admin changes the allowance |

Person-only tools, with the exact command. They all run over WireGuard and are refused for a bot
(exit 6):

```bash
tnctl ssh <device>                                   # a shell on the robot
tnctl exec <robot> -- sudo k3s kubectl get pods -A   # one command
tnctl logs <robot> <resource|pod> --tail 100         # live logs over the tunnel
tnctl wg status <device>                             # is the tunnel up, and where it goes
```

A bot has `tnctl inspect logs` instead of `tnctl logs`, and nothing instead of `ssh` and `exec`.

## The triage script

```bash
scripts/triage.sh <robot> [-n <teamspace>] [--json]
```

It runs steps 1 to 5 in one go (step 2 only as this robot's problems, without the overview),
read-only, and prints one section per step: whoami, the robot, the robot's problems, the
configuration state and - when it may - up to three failing pods. It never writes, never uses
`tnctl api` with a method other than GET, and never calls an inspect route outside `tnctl inspect`. A
step that fails prints `failed: tnctl <command> exited <n> (<code>): <message>`, read from tnctl's
JSON error, plus ` - retry after <s>s` when tnctl gives `retryAfter`, and the script goes on. With
`--json`, each step has `ok`, `exitCode`, `error`, `code`, `retryAfter`, `summary` and `data`.

Run it first when a robot is reported broken: it finds the shape of the problem faster than the
commands by hand, and `--json` gives you the same data to filter. Exit codes: 0 it ran, 1 jq is
missing, 2 usage, 6 `whoami` itself was refused. Then continue from the step that showed something.

It needs `tnctl` on the PATH (override with `TNCTL=<path>`). It runs on bash 3.2 or newer (macOS's
own bash is fine) and needs jq 1.6 or newer.

## Bot mode

`tnctl whoami -o json` -> `"mode":"bot"` and `.bot.access`.

| Action | `none` | `read` | `creator` |
|---|---|---|---|
| `whoami` | yes | yes | yes |
| the fleet API `overview`, `problems`, `devices`, `robots/<robot>/access`, Prometheus | every read: exit 6 | yes | yes |
| `tnctl robot <robot>`, `tnctl devices` | no: exit 6 | yes | yes |
| `tnctl config get` and `config wait --for published` | no | yes | yes |
| `tnctl inspect pod`, `logs`, `release` | no | **no**: exit 6 | yes |
| `tnctl mqtt stream .../state/events` | no | yes | yes |
| `tnctl ssh`, `exec`, `logs`, `wg`, `port-forward` | refused, whatever the access | refused | refused |

- Branch on `.bot.access`, never on the text of an error message (the one exception: the inspect
  table above, where the message tells apart codes that share an exit code).
- At access `none` every route is refused (exit 6): the bot's creator has turned its access Off.
- A 401 means the bot's key was not accepted: a person must re-create the bot. Never run `tnctl login`
  from a bot.
- At `read`, `inspect` is refused because it needs `creator` ("Act as me"). Use the fleet API's
  problems, Prometheus and the MQTT event stream instead, and tell the person what to check in the
  portal.
- A bot has no device shell and no WireGuard: it never turns a Shell on, never runs `exec`, and never
  reads the robot's files. When a fix needs one, hand it over.
- An exit 6 in bot mode is a permission: do not retry it and do not look for a workaround.
- The bot's own computer, its cluster and its limits are in
  [../teknoir-platform/references/bot-vm.md](../teknoir-platform/references/bot-vm.md).

## Safety

- Everything here is a read. Never run `tnctl exec`, `tnctl logs`, `tnctl ssh` or turn a Shell on; give
  the person the command.
- Never print a device login, a bot key, a token or a password. Logs and events are redacted by the
  platform, but the redaction is not a guarantee: quote only the lines you need, never a whole log.
- Never call a device's `credentials` route, and never publish to `/devices/<device>/config`,
  `/commands`, `peripheral-manager` or `inference`.
