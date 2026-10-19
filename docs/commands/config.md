# `tnctl config`

Read and override the configuration of a robot or device: chart values, chart version, container images
and the revision of an application. Built for fast testing on one robot, by people and by agents:
change a value, try an image, see whether it runs, put it back.

```bash
tnctl config get vision-station-01
tnctl config set vision-station-01 notebook --image :pr-123 --wait
tnctl config set vision-station-01 notebook replicas=2 log.level=debug --wait=applied
tnctl config diff vision-station-01
tnctl config reset vision-station-01 --all
```

## Layers and overrides

A robot's configuration is layered, lowest first: **platform < type < fleet < robot < local < override**.
HelmCharts with the same identity are merged (values deep-merged, higher layer wins per key, lists
replaced); other kinds are replaced by the higher layer. The **override** layer is the one you write with
`tnctl config`: it is stored on the robot's Device, always wins, and needs no git commit. `reset` puts the
robot back to what git says.

An override can change:

| What | How |
| --- | --- |
| Chart values (HelmChart) | `k.path=value`, `-f values.yaml`, `delete` |
| Chart version (HelmChart) | `--version` |
| Container images | `--image` (a workload's container, or the image keys of a chart) |
| Which revision of an application runs | `try` / `untry` |

Not overridable: the platform's own items (the WireGuard agent, reverse tunnel, keys). Turning an
application off on one robot is not possible yet.

`<target>` is a robot name or a device name; a device that backs a robot is the same as the robot.
`<resource>` is `Kind/namespace/name` (`HelmChart/default/notebook`), `Kind/name` or a unique name
(`notebook`). A name that matches several resources is refused (exit 2) with the candidates listed.

## Commands

| Command | Does |
| --- | --- |
| `config get <target> [<resource>]` | Effective configuration; one resource with provenance per value |
| `config set <target> <resource> [k.path=value...]` | Write an override |
| `config unset <target> <resource> [k.path...]` | Remove override keys; the git value returns |
| `config delete <target> <resource> k.path...` | Remove a value from the robot (writes `null`) |
| `config reset <target> [<resource> \| --all]` | Back to git |
| `config try <target> <app-path> <revision>` | Run another branch, tag or commit of an application |
| `config untry <target> <app-path>` | Back to the application's own revision |
| `config diff <target> [<resource>]` | Git result against effective, as a unified diff |
| `config wait <target> [<resource>]` | Wait until published, applied or ready |
| `config links <target> <resource>` | GitHub links of the file and the chart |
| `config promote <target> <resource>` | The YAML to commit so the override becomes permanent |

All accept the [global flags](index.md#global-flags), including `-o json|yaml`.

### get

```bash
tnctl config get <target> [<resource>] [--layers] [--provenance] [--rendered] [--overrides] [--defaults]
```

Without a resource: one row per effective resource with the layer that wins, what it contains
(`chart@version` or images) and the state of its override. `--layers` lists every layer's copy, `--overrides`
only the overrides with who set them, why, and the state the platform reports.

With a resource: its merged values (HelmChart) or images, chart, git source and override.
`--provenance` shows one row per value with the layer it comes from, `--rendered` the item exactly as
published to the device, `--defaults` adds the chart's own defaults at the pinned version.

### set

```bash
tnctl config set <target> <resource> [k.path=value...] [--image [path|ctr=]X] [--version V]
                 [-f values.yaml] [--string] [--note TEXT] [--dry-run] [--wait[=LEVEL]] [--timeout 5m]
```

```bash
tnctl config set vision-station-01 HelmChart/default/notebook image.tag=pr-123 --wait
tnctl config set vision-station-01 notebook --image :pr-123 --note "try pr-123" --wait=applied
tnctl config set vision-station-01 notebook replicas=2 resources.limits.memory=2Gi --dry-run
tnctl config set vision-station-01 notebook 'tolerations=[{key: gpu, operator: Exists}]'
tnctl config set vision-station-01 notebook -f my-values.yaml
tnctl config set vision-station-01 Deployment/default/whoami --image traefik/whoami:v1.11.0
```

- `--image X`: `X` is `repo:tag`, `:tag` (only the tag), `repo` (keeps the tag) or `repo@sha256:...`.
  The platform finds the chart's image keys (and writes only what changed, the tag as a string) or sets
  the container's image. Prefix `path=` (a chart with several images, such as `defaults.image`) or
  `container=` (a workload with several containers) to pick one. Several `--image container=repo:tag` for
  one workload are allowed with full images; a HelmChart takes one `--image` per command.
- `-f values.yaml` replaces the value overrides of the resource with the file (`-` reads stdin). `k=v`
  pairs are applied on top of the file. The override's version and note are kept unless you set them.
  Cannot be combined with `--image`.
- `--note` says why; everyone who looks at the robot sees it.
- `--dry-run` shows the before/after diff of the effective resource and writes nothing.
- Values and `--version` apply to HelmCharts; a workload takes `--image`.

The override stays until you `unset` or `reset` it. Setting a value equal to git is accepted; the platform
reports the override as `redundant`.

### Value paths and values

A path is dot separated keys. A dot inside a key is escaped with a backslash, as in Helm:
`nodeSelector.teknoir\.org/node-role=vision`. A backslash is `\\`, an equals sign inside a key `\=`. The
first unescaped `=` ends the path. There are no list indexes (`items[0]=x` is refused): a list is set whole.

Values are read like YAML 1.1 scalars, which is how Helm reads values:

| Written | Becomes |
| --- | --- |
| `true`, `false` | boolean |
| `5`, `-5`, `1.5` | number |
| `on`, `yes`, `y` / `off`, `no`, `n` | boolean, **with a warning** |
| `010`, `0x10`, `1_000`, `1e3`, `1.10` | number (8, 16, 1000, 1000, 1.1), **with a warning** |
| `null`, `~` | null: deletes the key from the git layers and the chart default |
| `k=` | the empty string |
| `"1.10"`, `'on'` | the text between the quotes |
| `[a, b]`, `{x: 1}` | a list or a map (YAML flow); a list replaces the whole list |
| anything else | text |
| `.nan`, `.inf` | refused |

The warning goes to stderr, for example
`debug=on: read as boolean true (YAML 1.1, like Helm); use --string to keep the text`. Two things keep text
as text without a warning: keys whose name ends in `tag`, `repository`, `registry`, `digest`, `version`,
`name`, `image`, `host`, `url` or `namespace` (a tag `20261019` or `1.10` stays a string), and a key whose
current value is a string. `--string` keeps every `k=v` of the command as text.

### unset, delete, reset

`unset` takes back your override of a key, so the git value returns: `tnctl config unset vision-station-01
notebook image.tag`. `--version` removes a version override, `--image container` the image override of a
workload's container. With no keys and no flags the whole override of the resource is removed.

`delete` writes `null`: the value disappears from the robot, including the chart's own default. To take
that back, `unset` the key.

`reset <target> <resource>` removes the override of one resource, `reset <target> --all` every override of
the robot, values and tried revisions included. It never guesses: you need one of the two.

### try and untry

```bash
tnctl config try vision-station-01 apps/mock-vision feature-x
tnctl config try vision-station-01 apps/mock-vision v1.2.0 --note "release candidate" --wait=applied
tnctl config try vision-station-01 . main --repo https://github.com/teknoir/teknoir-demo-gitops
tnctl config untry vision-station-01 apps/mock-vision
```

`<app-path>` is the application's path in its repository, whichever layer it comes from; the repository is
found from the robot's applications or given with `--repo`. `.` with `--repo` means every application of
that repository. The application is replaced in place, so values of the old revision do not leak into the
new one. The platform checks that the revision exists (branch, tag, or a commit of 7 to 40 hex characters);
if not, exit 5 and nothing changes.

### diff, links, promote

`diff` prints what git yields against what the robot has, as a unified diff, for one resource or for every
resource with an override. Its exit code is 0 whether or not there is a difference.

`links` prints the GitHub links of the file a resource comes from (view, permalink, history, edit when the
revision is a branch) and of its chart (source, `values.yaml` at the pinned version, release).

`promote <target> <resource> [--to robot|fleet:<name>|type]` prints the YAML to commit to git so the
override becomes permanent, with the suggested path and a GitHub link. The YAML is on stdout (redirect it
to a file), the rest on stderr. Nothing is written to git. When it is merged, `reset` the override.

## Waiting for a change

`--wait[=LEVEL]` on every write (and `config wait`) follows the change; without it the command returns as soon
as the platform has stored it. A bare `--wait` means `ready`. Write `--wait=applied`; `--wait applied` makes
`applied` an argument.

| Level | Reached when |
| --- | --- |
| `published` | The platform evaluated the write (its `overridesRevision` equals the written revision) and the override's state is `active` or `redundant`. `invalid`, `no-target` or `not-applicable` ends with exit 5 and the platform's reason. |
| `applied` | The robot has it. HelmChart: the on-device HelmChart has the published values and version and the helm Job is a new one that succeeded. Workload: the containers have the published images and the controller has seen the latest generation. |
| `ready` | Applied, and the rollout is complete, the pods are Ready on the new image and have not restarted for 20 s. |

`applied` and `ready` look at the robot over WireGuard SSH (the robot's server computer, as
[`exec`](exec.md)): `tnctl` first records what the robot has, then writes, then polls every 2 s over one
connection. If the robot cannot be reached, the command stops before it writes anything and suggests
`--wait=published`. `--timeout` (default 5m) bounds the wait; the change itself is written even if the
wait times out.

A failing Job (`failed > 0`, with the last 30 log lines), a pod in `CrashLoopBackOff`, `ImagePullBackOff`,
`ErrImagePull`, `InvalidImageName` or `CreateContainerConfigError` ends with exit 7. "Published" means the
platform sent the configuration to the robot, not that it runs; `applied` and `ready` are the checks that it
does. A moving tag (`:latest`) does not change the spec, so nothing rolls out: use a new tag or digest.

`tnctl config wait <target> [<resource>] [--for published|applied|ready] [--timeout 5m]` does the same
without writing; without a resource it follows every resource with an override. Default `--for` is `ready`.

## Output

`-o json` / `-o yaml` print the platform's answer unchanged. A write prints the platform's write result
(`revision`, `before`/`after` for `--dry-run`, `overrides`, `warnings`; tnctl's own warnings are added to
`warnings`) and, when you waited, a `wait` object:
`{"level":"applied","reached":true,"seconds":42}`. Data is on stdout; progress, warnings and `Commit as:` lines
are on stderr. Errors with `-o json` are `{"error":{"code":"timeout","message":"..."}}` on stderr.

## Exit codes

| Exit | Meaning |
| --- | --- |
| 0 | ok |
| 1 | error |
| 2 | usage: bad arguments or flags, ambiguous resource name |
| 3 | not found: no such robot, device, resource or revision |
| 4 | timeout: `--wait` ran out of time (the change itself is written) |
| 5 | rejected: the platform refused it (invalid values, no such target, not overridable) |
| 6 | not logged in, token rejected, or not allowed: run `tnctl login` |
| 7 | applied but unhealthy: the helm Job failed or pods crash or cannot pull the image |
| 8 | conflict: someone else changed the same robot's overrides at the same time; run the command again |

## An agent's loop

```bash
tnctl login --no-browser                                   # once; the token is refreshed afterwards
tnctl config set vision-station-01 notebook --image :pr-123 --note "agent: try pr-123" --wait=ready -o json
tnctl logs vision-station-01 HelmChart/default/notebook --tail 100
tnctl config set vision-station-01 notebook log.level=debug --wait=applied
tnctl config promote vision-station-01 notebook > notebook.yaml   # when it works: commit it
tnctl config reset vision-station-01 --all
```

Writes need write access to the teamspace, like the web UI; every write is audited with your name.

## See also

- [`robots`](robots.md), [`exec`](exec.md), [`logs`](logs.md).
