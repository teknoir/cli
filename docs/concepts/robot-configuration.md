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

### Remove an override

`config unset <target> <resource> [k.path...]` takes back part of an override or, with no keys, all of it for that resource. `config reset <target> <resource>` does the same, and `config reset <target> --all` removes every override of the robot. The git value returns.

## Remove a local item

A local item is written straight to the robot by a tool such as Devstudio, not by git or an override. It shows in the `local` layer of `config get`. `config unset` and `reset` do not apply to it: there is no git value to go back to. Remove it with:

```bash
tnctl config remove vision-station-01 HelmChart/default/local-llm --dry-run   # what would go
tnctl config remove vision-station-01 local-llm --wait=applied                # remove, wait until it is gone
```

The robot deletes the object; a HelmChart's release is uninstalled. If git has a copy with the same identity, that copy applies again and nothing is uninstalled (the answer says so, and what happens to an override on this robot). The tool that wrote the item may write it again. Items from git are removed in git, in the robot's apps, a fleet or the robot type; `remove` on such an item exits 8 and names the place. `--wait=published` waits for device-controller to publish a new config (its hash changes). `--wait=applied` also checks over WireGuard SSH that the object (and a HelmChart's release) is gone, and prints the manual `kubectl delete` if it is not; when a git copy took over it checks nothing on the robot and prints no delete command.

## Add or edit a local item

A local item is a Kubernetes resource that belongs to one robot only: it is stored in the robot's device, wins over a git copy with the same `Kind/namespace/name` and stays until it is removed. Use it for a ConfigMap, a Service, a small workload or a HelmChart that only this robot needs. For something that belongs to many robots, put it in git.

```bash
tnctl config kinds                                       # what can be added, and what is refused (and why)
tnctl config kinds --template Deployment > whoami.yaml   # a starter manifest
tnctl config add vision-station-01 -f whoami.yaml --dry-run
tnctl config add vision-station-01 -f whoami.yaml --wait=applied
```

The platform checks the manifest against the Kubernetes schemas before it writes anything. Problems come back as `whoami.yaml:12:13: error: ...` with the line of your file (also for the second and later `---` documents) and exit 5. A manifest with several documents is added in order and stops at the first failure. `--replace` overwrites an existing local item; without it an existing one gives exit 8. Identical content is `unchanged` and writes nothing.

Not every kind is accepted: the robot's agent keeps only the top-level fields `apiVersion`, `kind`, `metadata`, `data`, `binaryData`, `type`, `spec` and `status`, so kinds that carry their content elsewhere (a Role's `rules`, for one) would arrive empty, and it runs as cluster-admin. `tnctl config kinds` lists the supported kinds with a note for each and the refused ones with the reason. A custom resource is refused too: if its CRD is missing on the robot, the robot's whole configuration stops applying.

### The edit loop

```bash
tnctl config edit vision-station-01 Deployment/default/whoami --wait=applied   # opens $VISUAL or $EDITOR
tnctl config get vision-station-01 Deployment/default/whoami --manifest > whoami.yaml   # or: get it, edit it yourself
tnctl config add vision-station-01 -f whoami.yaml --replace --wait=applied
```

`edit` opens the stored manifest, and when you save, the platform checks it. With errors the editor opens again with the errors at the top of the file; close it without changes to give up (nothing is written). An unchanged file writes nothing and exits 0. The kind, namespace and name cannot change. Only local items can be edited: for an item from git, `edit` and `get --manifest` exit 8 and show how to make a local copy (`config get ... --rendered`, then `config add`). `--wait` as in the table below; for a local item `applied` means that the robot's copy carries the new version (and, for a workload, that its controller has seen it; for a HelmChart that its helm Job succeeded and a release exists), and `ready` that a rollout is complete, a Job has completed, and so on.

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
| 5 | rejected: invalid values or manifest, unknown revision, no such target, not overridable | fix the input (for `add`, the `file:line:col` lines say where); retrying does not help |
| 6 | not logged in, login expired (`-o json` code `session_expired`), token rejected, or not allowed | `tnctl login --no-browser` |
| 7 | applied but unhealthy: helm Job failed, pods crash or cannot pull | `tnctl logs`, then change or reset |
| 8 | conflict: someone changed the same overrides; `add` of an item that exists; `remove`, `edit` or `get --manifest` of an item that is not local | run the command again; for `add`, use `--replace`; for the others, act where the message says |
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
