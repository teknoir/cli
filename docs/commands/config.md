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
| `config add <target> -f <file\|->` | Add a local resource (any supported Kubernetes kind) to a robot |
| `config edit <target> <resource>` | Edit a local item in `$EDITOR`, checked by the platform before it is saved |
| `config kinds` | The kinds `add` supports, the refused ones with reasons, and starter manifests |
| `config remove <target> <resource>` | Remove a local item (written straight to the robot, for example by Devstudio) |
| `config try <target> <app-path> <revision>` | Run another branch, tag or commit of an application |
| `config untry <target> <app-path>` | Back to the application's own revision |
| `config diff <target> [<resource>]` | Git result against effective, as a unified diff |
| `config wait <target> [<resource>]` | Wait until published, applied or ready |
| `config links <target> <resource>` | GitHub links of the file and the chart |
| `config promote <target> <resource>` | The YAML to commit so the override becomes permanent |

All accept the [global flags](index.md#global-flags), including `-o json|yaml`.

### get

```bash
tnctl config get <target> [<resource>] [--layers] [--provenance] [--rendered] [--overrides] [--defaults] [--manifest]
```

Without a resource: one row per effective resource with the layer that wins, what it contains
(`chart@version` or images) and the state of its override. `--layers` lists every layer's copy, `--overrides`
only the overrides with who set them, why, and the state the platform reports.

With a resource: its merged values (HelmChart) or images, chart, git source and override.
`--provenance` shows one row per value with the layer it comes from, `--rendered` the item exactly as
published to the device, `--defaults` adds the chart's own defaults at the pinned version.
`--manifest` prints the stored YAML of a **local** item, ready to edit (`> item.yaml`, then `config add --replace`,
or use `config edit`). For an item that is not local (it comes from git or is managed by the platform) the
platform answers 409: tnctl prints its message and a hint how to make a local copy
(`config get ... --rendered > item.yaml`, then `config add`), and exits 8. Secret values show as `<hidden>`.

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
  pairs are applied on top of the file, which is edited in place: its comments, key order and the spelling
  of the scalars you did not touch (a plain `yes` stays `yes`) are sent as they are. The override's version
  and note are kept unless you set them. Cannot be combined with `--image`.
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
| `[a, b]`, `{x: 1}` | a list or a map (YAML flow, plain `yes`/`on` inside read as booleans too); a list replaces the whole list |
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

### add

```bash
tnctl config add <target> -f <file|-> [--replace] [--dry-run] [--wait[=published|applied|ready]] [--timeout 5m]
```

```bash
tnctl config kinds --template Deployment > whoami.yaml     # a starter manifest
tnctl config add vision-station-01 -f whoami.yaml --dry-run
tnctl config add vision-station-01 -f whoami.yaml --wait=applied
```

Adds a Kubernetes resource to one robot as a **local item**: the platform checks it against the Kubernetes
schemas, stores it in the device's manifest and publishes it to the robot, whose agent applies it. A local item
wins over a git copy with the same `Kind/namespace/name` (the platform says which copy it hides, as a warning)
and stays until `config remove`. Which kinds are accepted: `tnctl config kinds`.

`-f` is a YAML or JSON file, `-` reads stdin. Several documents separated by `---` are added one by one, in
order, with one request each; the first failure stops the run, the documents before it stay written, and the
exit code is that failure's. Empty documents and comments are skipped.

| Result line | Meaning |
| --- | --- |
| `added <id> to <target>` | a new local item |
| `replaced <id> on <target>` | `--replace` overwrote the existing local item |
| `unchanged <id> on <target> (already as given)` | the stored item says exactly this; nothing was written |
| `would add / would replace / would leave ... unchanged` | the same with `--dry-run` |

Without `--replace` an existing local item with the same identity gives exit 8 (`already exists; use --replace
to overwrite it`). Exit 8 also means the identity belongs to a git item that cannot be shadowed or to the
platform itself (the message says which). `--dry-run` runs every check and writes nothing; it cannot be
combined with `--wait`.

**Problems in the manifest** are printed to stderr as `FILE:LINE:COL: error: message`, one per finding, and the
command exits 5 (nothing of that document is written). The positions are the lines of *your file*: the platform
reports them inside each document and tnctl adds the line where that document starts. Warnings use
`warning:` and do not fail. A dry run that finds errors exits 5 too, so `--dry-run` works as a check in a script.

```
stack.yaml:12:13: error: Invalid type. Expected: integer, given: string (spec.replicas)
```

**`--wait`** (default `--timeout` 5m; a bare `--wait` is `ready`, write `--wait=applied`). With several documents
the wait starts after the last one is written and follows all of them.

| Level | Reached when |
| --- | --- |
| `published` | device-controller published a new config (the hash that `config get` shows as `Published:` changed; not needed when everything was `unchanged`), every item wins in the robot's `local` layer, and the published item is the version just written |
| `applied` | the robot has it, checked over WireGuard SSH: the object exists and carries the new version (`teknoir.org/config-changed-at`); Deployments, StatefulSets, DaemonSets and ReplicaSets also need `metadata.generation == status.observedGeneration`; for a HelmChart the on-device chart has the values and version, its `helm-install-<name>` Job succeeded (a new one when the chart changed) and a release secret exists |
| `ready` | applied, and Deployments, StatefulSets and DaemonSets finished rolling out with Ready pods that did not restart for 20 s; a Job completed (a failed Job is exit 7); a Pod is Ready; for a HelmChart its release's workloads are ready. Other kinds are as `applied` |

The robot has to be reachable before anything is written (`applied`, `ready`), as for the other writes.

`-o json|yaml` prints the platform's answer for each document (`id`, `action`, `validation`, the stored `item`
with secret values hidden, `replaces`, `warnings`): one object for one document, a list for several, and with
`--wait` a `wait` object in each. A refused manifest prints its answer (with the findings in
`validation.issues`) before the command exits 5.

### edit

```bash
tnctl config edit <target> <resource> [--wait[=published|applied|ready]] [--timeout 5m]
```

Fetches the stored manifest of a local item, writes it to a private temporary file (mode 0600 in a 0700
directory, removed afterwards) and opens it in `$VISUAL`, else `$EDITOR`, else `vi`. The command line is split
into words with quotes but never run through a shell, so `VISUAL='code --wait'` and paths with spaces in quotes
work. When the editor exits:

- an empty file, or a file without changes (also one that differs only in comments or spelling that the platform
  stores identically): nothing is written, exit 0 (`No changes: ...`);
- otherwise the platform checks the manifest with a dry run. With errors the editor opens **again** with the
  errors as a comment block at the top of the file (the line numbers are those of the file in the editor; the block
  is removed when you save). Close the editor without changing anything to give up: exit 5, nothing written;
- a valid manifest is written with `replace` and followed with `--wait`, as for `add`.

The kind, namespace and name cannot change in an edit (that would be a second item): the editor reopens with a
message. Only one document is allowed. An item that is not local gives exit 8 with the hint of `get --manifest`.
If the item carries an override on this robot, saving a new version drops it (tnctl says so before it opens the
editor), and a tool such as Devstudio that wrote the item may write it again.

### kinds

```bash
tnctl config kinds [--template KIND] [-o json]
```

A table of the kinds that `add` and `edit` accept (`APIVERSION`, `KIND`, `SCOPE`, `NOTE`: for example a Pod
should be a Deployment, and removing a Namespace deletes everything in it), then the refused kinds with the
reason, and the schema version used for the check. `--template KIND` prints a starter manifest of one kind
(case does not matter). `-o json` is the platform's answer, templates included.

Supported: ConfigMap, Secret, Service, Deployment, StatefulSet, DaemonSet, Job, CronJob, Pod, ReplicaSet,
Ingress, PersistentVolumeClaim, NetworkPolicy, HorizontalPodAutoscaler, PodDisruptionBudget, LimitRange,
ResourceQuota, ServiceAccount, HelmChart, HelmChartConfig and, cluster-scoped, Namespace. Refused: Role,
RoleBinding, ClusterRole and ClusterRoleBinding (the robot's agent drops their rules, subjects and roleRef, and it
runs as cluster-admin), webhook configurations, StorageClass, PriorityClass, RuntimeClass, PersistentVolume, Node,
CustomResourceDefinition and every kind without a bundled schema (a custom resource whose CRD is missing on the
robot stops the robot's whole configuration from applying). The table is authoritative; the platform may change it.

### remove

```bash
tnctl config remove vision-station-01 HelmChart/default/local-llm --dry-run
tnctl config remove vision-station-01 local-llm --wait=applied
```

Removes a **local** item: an entry that a tool such as Devstudio wrote straight to the robot's device, shown in
the `local` layer of `config get`. The robot deletes the object; for a HelmChart its release is uninstalled.
The tool that wrote the item may write it again, so stop it first if it keeps coming back.
`--dry-run` shows the item that would go (secret values hidden) and removes nothing. Without `--dry-run` the
result is `Removed <id> from <target>.`; `-o json` prints the platform's answer (`removed`, `owner`,
`hadOverride`, `warnings`).

Items that come from git are not removed with `remove`. Asking for one gives exit 8 and the platform says where
to remove it: git, the robot's apps, a fleet or the robot type. Overrides are not removed here either: use
`unset` or `reset`.

If git has a copy of the item with the same identity (a fleet, the robot type, ...), that copy takes over: the
answer says `The fleet gpu-nvidia version (git) applies again.` and nothing is uninstalled. An override of the
item on this robot is either removed with it (`Its override on this robot was removed too.`) or stays and
applies to the git version (`This robot's override stays and applies to the git version.`). A removal that
finds nothing to remove (someone else was first) prints `... was already gone` and waits for nothing; a 404
says the item may already have been removed.

`--wait=published` waits until device-controller has published a new config without the item: the config hash
that `config get` shows as `Published:` differs from the one before the removal, and the item is no longer in
the local layer. If that does not happen the command exits 4 with `device-controller has not published a new
config for <device> yet`. `--wait=applied` (a bare `--wait` is the same for `remove`) also looks at the robot
over WireGuard SSH, as for the other writes, and waits until `k3s kubectl get <kind> <name>` finds nothing;
for a HelmChart also until no helm release secret (`owner=helm,name=<name>`, in the chart's target namespace)
is left. The robot has to be reachable before anything is removed. If the item stays after `--timeout` the
command exits 4 and prints how to delete it by hand: `tnctl exec <target> -- sudo k3s kubectl -n <ns> delete
<kind> <name>`.

When a git copy takes over, `--wait=applied` never probes the robot for the object to be gone (the object stays,
managed by git) and never prints the manual delete command; `published` already means that the git copy wins in
the robot's configuration, and `applied` is reported at the same point, once the published hash changed.

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
resource with an override (also in-place changes of local items). An override whose resource git no
longer yields is listed as `stale` with the command that removes it (`reset`), and does not fail the rest.
Its exit code is 0 whether or not there is a difference.

`links` prints the GitHub links of the file a resource comes from (view, permalink, history, edit when the
revision is a branch) and of its chart (source, `values.yaml` at the pinned version, release).

`promote <target> <resource> [--to robot|fleet:<name>|type]` prints the YAML to commit to git so the
override becomes permanent, with the suggested path and a GitHub link. The YAML is on stdout (redirect it
to a file), the rest on stderr. Nothing is written to git. When it is merged, `reset` the override.

## Waiting for a change

`--wait[=LEVEL]` on every write (and `config wait`) follows the change; without it the command returns as soon
as the platform has stored it. A bare `--wait` means `ready`. Write `--wait=applied`; `--wait applied` makes
`applied` an argument (`unset`, `delete`, `reset` and `try` refuse it with a hint instead of taking it as a key).

| Level | Reached when |
| --- | --- |
| `published` | The platform evaluated the write (its `overridesRevision` equals the written revision, or a later one when somebody else wrote after you) and the override's state is `active` or `redundant`. `invalid`, `no-target` or `not-applicable` ends with exit 5 and the platform's reason. |
| `applied` | The robot has it (for `add` and `edit`: see [add](#add)). HelmChart: the on-device HelmChart has the published values and version and the helm Job is a new one that succeeded. Values the platform hides (keys that look like credentials show as `<hidden>`) are only checked to be present. Workload: the containers have the published images and the controller has seen the latest generation. |
| `ready` | Applied, and the rollout is complete, the pods are Ready on the new image and have not restarted for 20 s. With `--image` only the workloads that run the image are held to it; the other workloads of the release only have to be rolled out and Ready. |

`applied` and `ready` look at the robot over WireGuard SSH (the robot's server computer, as
[`exec`](exec.md)): `tnctl` first records what the robot has, then writes, then polls every 2 s over one
connection. If the robot cannot be reached, the command stops before it writes anything and suggests
`--wait=published`. `--timeout` (default 5m) bounds the wait; the change itself is written even if the
wait times out. A `tnctl` that is interrupted (Ctrl-C, SIGTERM) while waiting closes its connection and
removes the temporary device key before it exits with 1; the change itself stays written.

For `try` and `untry` the resources to check are those of the new revision, read after it is published;
a resource that is no longer part of the configuration is skipped.

A failing helm Job (its Failed condition, or 3 failed attempts; helm-controller retries a Job that fails
once, so a single failure is only reported as pending, with the last 30 log lines when it ends as failed), a pod in `CrashLoopBackOff`, `ImagePullBackOff`,
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
| 3 | not found: no such robot, device or resource |
| 4 | timeout: `--wait` ran out of time (the change itself is written) |
| 5 | rejected: the platform refused it (invalid values or manifest, unknown revision, no such target, not overridable) |
| 6 | not logged in, token rejected, or not allowed: run `tnctl login` |
| 7 | applied but unhealthy: the helm Job failed or pods crash or cannot pull the image |
| 8 | conflict: someone else changed the same robot's overrides at the same time; run the command again. For `add`: the local item exists already (use `--replace`) or the identity is not yours to replace. For `remove`, `edit` and `get --manifest`: the item is not local, the message says where to remove it |
| 9 | rate limited: the platform allows 60 writes a minute per user; wait the `Retry-After` time (`-o json`: `error.retryAfter`, seconds) and run it again |

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
