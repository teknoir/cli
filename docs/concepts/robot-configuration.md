# Robot configuration

How a robot's configuration is built, what an override is, and how to drive it from a terminal or an agent. The commands are in [`config`](../commands/config.md), [`robots`](../commands/robots.md), [`exec`](../commands/exec.md) and [`logs`](../commands/logs.md).

## Layers

A robot is one edge computer or a k3s cluster of computers, with one Device behind it. Its configuration is a stack of layers. Lowest first, a higher layer wins:

| Layer | Comes from |
| --- | --- |
| platform | The platform itself (WireGuard agent, reverse tunnel, keys). Not overridable. |
| type | The robot type |
| fleet | A fleet (an application set) that selects the robot by label |
| robot | The robot's own applications |
| local | Items written straight to the device, for example by Devstudio |
| override | You, with `tnctl config` or the portal |

Items with the same kind, namespace and name are one item. HelmCharts are merged: maps merge key by key, a list replaces the whole list, `null` removes the key and the chart's own default. Other kinds are replaced whole by the higher layer.

## Overrides

An override is stored on the robot's Device, wins over every git layer and needs no commit. `reset` puts the robot back to what git says.

| You change | With |
| --- | --- |
| Chart values | `config set k.path=value`, `-f values.yaml`, `config delete` |
| Chart version | `config set --version` |
| Container image | `config set --image` |
| Revision of an application | `config try` / `config untry` |

Values and version apply to HelmCharts. A workload (Deployment and so on) takes an image override. Turning an application off on one robot is not possible yet.

Use overrides for testing. When a change works, `config promote` prints the YAML to commit. After it is merged, `config reset` the override.

## Published, applied, ready

A write has three stages. `--wait` follows them.

| Level | Meaning |
| --- | --- |
| `published` | The platform evaluated the write and sent the configuration to the robot. It does not mean it runs. |
| `applied` | The robot has it: values and version on the device and the helm Job succeeded, or the workload has the image |
| `ready` | Applied, pods Ready on the new image, no restart for 20 s |

Without `--wait` the command returns when the platform has stored the change. A bare `--wait` means `ready`. Write `--wait=applied` with an equals sign. `applied` and `ready` check the robot over WireGuard SSH; if it cannot be reached nothing is written and tnctl suggests `--wait=published`. The default `--timeout` is 5 minutes. On timeout the change stays written (exit 4).

A reset reaches the robot: the platform stamps an item whose content changed with `teknoir.org/config-changed-at`, so the robot applies a return to earlier content.

## Exit codes

| Exit | Meaning | What an agent does |
| --- | --- | --- |
| 0 | ok | continue |
| 1 | error | read the message |
| 2 | usage: bad arguments, ambiguous resource name | fix the command |
| 3 | not found: robot, device or resource | check the names |
| 4 | timeout: `--wait` ran out of time | `config wait` again, or read the logs |
| 5 | rejected: invalid values, unknown revision, no such target, not overridable | fix the input; retrying does not help |
| 6 | not logged in, token rejected, or not allowed | `tnctl login --no-browser` |
| 7 | applied but unhealthy: helm Job failed, pods crash or cannot pull | `tnctl logs`, then change or reset |
| 8 | conflict: someone changed the same overrides | run the command again |
| 9 | rate limited | wait `error.retryAfter` seconds (`-o json`), run again |

## Tips for agents

- Log in once with `tnctl login --no-browser` and approve the printed URL and code on any machine.
- Use `-o json`. Data goes to stdout, progress and warnings to stderr, so `tnctl ... -o json | jq` is safe. With `-o json` errors are `{"error":{"code","message"}}` on stderr.
- Branch on the exit code, not on message text.
- Retry on exit 8 immediately, and on exit 9 after `retryAfter` seconds. Do not retry on 5.
- Use `--dry-run` to see the effective change before writing. Use `--note` to say why.
- A moving tag such as `:latest` does not roll out. Use a new tag or a digest.
- `exec` returns the remote command's exit code, so 3 to 9 can be the command's own.

## A test loop

```bash
tnctl config get vision-station-01                                   # what is configured
tnctl config set vision-station-01 notebook --image :pr-123 --wait   # try, wait until ready
tnctl logs vision-station-01 notebook --tail 50                      # does it work
tnctl config diff vision-station-01                                  # what differs from git
tnctl config promote vision-station-01 notebook > notebook.yaml      # commit this
tnctl config reset vision-station-01 --all --wait=applied            # back to git
```
