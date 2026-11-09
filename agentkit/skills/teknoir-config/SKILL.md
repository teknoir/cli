---
name: teknoir-config
description: Read and change what a Teknoir robot or device runs - chart values, chart version, container images, local Kubernetes items, which apps are off on one robot, and the git revision an app runs. Use it for tnctl config (get, diff, links, kinds, set, unset, delete, on, off, add, remove, reset, promote, try, wait), for an effective configuration, an override, a local item, or a robot that runs the wrong version or misses an app. Not for a broken or unhealthy robot, for its logs or for missing metrics (teknoir-debug), and not for robots, robot types and fleets themselves (teknoir-robots).
license: Apache-2.0
metadata:
  teknoir-kit-version: "0.1.0"
---

# The configuration of a robot

A robot's configuration is a stack of layers that the platform merges into one result and publishes
to the robot. `tnctl config` reads that result and writes the top layer, the override, for one robot.

Paths in this skill are relative to this skill's directory.

## The model

The layers, lowest first. A higher layer wins:

| Layer | What puts it there | Changed with |
|---|---|---|
| type | The robot type's applications (git) | git, or `tnctl api PUT <teamspace>/types/<name>` (teknoir-robots) |
| fleet | Every `DeviceApplicationSet` whose label selector matches the robot, in priority order (git) | git, or `tnctl api PUT <teamspace>/fleets/<name>` |
| robot | The robot's own applications (git) | git, or `tnctl api PATCH <teamspace>/robots/<robot>` |
| local | Items written straight to the robot's device, by Devstudio or by `config add` | `config add`, `config edit`, `config remove` |
| override | Your changes on this one robot | `config set`, `config delete`, `config on`, `config off`, `config try`, `config reset` |

Below type there is also the **platform** layer: the robot's agent, its tunnels and its keys. Nobody
overrides or turns those off; `config get` shows them so you can tell them apart from yours.

**Effective** means what the platform computed after applying all the layers: the values a HelmChart
finally runs with, the image of each container, and which resource is off. `config get` and
`config diff` show the effective configuration; `--provenance` shows which layer every single value
came from.

**Apps versus values.** An *application* is a path in a git repository (a directory of YAML files, or
one YAML file) listed in a type, a fleet or the robot's own applications. A *value* is one key inside
a HelmChart's `spec.valuesContent`. `config try`, `config untry` and `config off --app` act on an
application; `set`, `unset`, `delete`, `off` and `on` act on one resource, named `Kind/namespace/name`.

**What a device actually runs.** device-controller renders the layers, the local items and the
overrides into `spec.manifest.apps` of the robot's Device, validates every item, and publishes the
whole configuration as one retained MQTT message. The robot's agent applies it and keeps only some
fields of each item (a Role's rules, for example, would be dropped). So a write is not running when
the platform stored it: it is running only after the robot reports it.

**GitOps sources versus the fleet API.** tnctl never writes git. Git is the permanent place: it
affects every robot the layer selects, is reviewed, and rolls back with a revert. An override is
temporary by design, affects one robot, and is stored on that robot's Device.

| | Git (a type, a fleet, the robot's applications) | Override (this robot only) |
|---|---|---|
| Written by | a person committing, or `tnctl api PUT/PATCH` of the applications | `tnctl config ...`, or the portal |
| Lifetime | until changed in git | until `unset`, `reset` or `remove` |
| To make it permanent | - | `config promote` prints the YAML; a person commits it; then `reset` |

Read [references/config-model.md](references/config-model.md) when you need the merge rules, the
override states and the messages the platform reports for them.

## Reading

```bash
tnctl whoami -o json
tnctl config get <robot>                             # one row per resource: layer, chart/images, override state
tnctl config get <robot> --overrides                 # only the overrides: who set them, why, the state
tnctl config get <robot> --layers                    # every layer's copy of each resource
tnctl config get <robot> <resource> --provenance     # one row per value, with the layer it comes from
tnctl config get <robot> <resource> --rendered       # the item exactly as published to the device
tnctl config diff <robot>                            # git result against effective, as a unified diff
tnctl config links <robot> <resource>                # the GitHub file, its history, the chart source
tnctl config kinds                                   # the kinds `config add` accepts, and the refused ones
```

- Each of them takes `-o json`; data goes to stdout, progress and warnings to stderr, so
  `| jq` is safe.
- `<resource>` is `Kind/namespace/name`, `Kind/name` or a unique name. A name that matches several
  resources is refused (exit 2) with the candidates listed.
- `config diff` exits 0 whether or not there is a difference; read its output, not its exit code.
- There is **no** `config logs`. Logs are `tnctl inspect logs <robot> <namespace>/<pod>`; see
  [../teknoir-debug/SKILL.md](../teknoir-debug/SKILL.md).

**The id that `set`, `unset`, `on` and `off` take.** The `identity` field of a row (`HelmChart-notebook-default`)
is not that id. Build it from `kind`, `namespace` and `name`:

```bash
tnctl config get <robot> -o json | jq -r '
  .configuration.effective[]
  | if (.namespace // "") == "" then "\(.kind)/\(.name)"
    else "\(.kind)/\(.namespace)/\(.name)" end'
```

For a cluster-scoped kind (`Namespace`) there is no namespace: give it as `Kind/name`, which the
commands accept. Add `| select(.status != "winner")` after `.configuration.effective[]` to see only
what is not simply winning (pending, off, overridden), or list only what has an override:

```bash
tnctl config get <robot> -o json | jq -r '(.overrides.resources // {}) | keys[]'
```

## Changing: dry run, write, wait

Always in this order:

1. **Dry run.** `--dry-run` writes nothing and prints the before/after of the effective resource, or
   for a manifest, runs every check and prints the findings. Most writes have it; `unset` of a whole
   resource, `reset` and `untry` do not - read `config diff` instead.
2. **Write.** Add `--note "why"`: everyone who looks at the robot sees it.
3. **Wait.** `--wait=published` (works in bot mode), `--wait=applied` or `--wait=ready` (a person
   only). Or `tnctl config wait <robot> [<resource>] --for published`.

### `set` and `unset`

```bash
tnctl config set <robot> <resource> replicas=2 resources.limits.memory=2Gi --dry-run
tnctl config set <robot> <resource> replicas=2 --note "load test" --wait=applied
tnctl config set <robot> <resource> --image :pr-123          # a tag only: keeps the repository
tnctl config set <robot> <resource> --image repo/image:v1.2  # a workload's container
tnctl config set <robot> <resource> --version 0.4.2          # HelmChart only
tnctl config set <robot> <resource> -f my-values.yaml        # replaces the value overrides
tnctl config set <robot> <resource> 'tolerations=[{key: gpu, operator: Exists}]'
tnctl config unset <robot> <resource> image.tag              # the git value returns
tnctl config unset <robot> <resource>                        # the whole override (no --dry-run)
```

- A path is dot separated keys; escape a dot inside a key: `nodeSelector.teknoir\.org/node-role=vision`.
  A list is set whole, not by index. `--string` keeps every `k=v` as text.
- Values are read like Helm reads them: `on` and `yes` become `true`, `010` becomes `8`, with a
  warning on stderr. Quote the value (`"1.10"`) to keep text.
- `--image` accepts `repo:tag`, `:tag`, `repo` (keeps the tag) or `repo@sha256:...`. A chart with
  several images takes `path=`, a workload with several containers `container=`.
- `-f` replaces all value overrides of the resource; cannot be combined with `--image`.
- `--version` is checked against the chart repository's index.
- Setting a value that git already has is accepted; the platform reports the override as `redundant`.
- Values and `--version` apply to HelmCharts; a workload takes `--image`. A moving tag (`:latest`)
  does not change the spec, so nothing rolls out.

### `delete`

```bash
tnctl config delete <robot> <resource> nodeSelector.teknoir\.org/node-role --dry-run
tnctl config delete <robot> <resource> nodeSelector.teknoir\.org/node-role --wait=applied
```

`delete` writes `null`: the key disappears from every git layer **and** from the chart's own default.
It is not "remove my override" - that is `unset`, which brings the git value back. Undo a `delete`
with `unset` of the same key.

### `off` and `on`

```bash
tnctl config off <robot> <resource> --dry-run                 # says what goes on the robot
tnctl config off <robot> <resource> --note "not at this site" --wait=applied
tnctl config off <robot> --app apps/mock-vision --note "unused here"
tnctl config on  <robot> <resource> --wait=applied            # a fresh install
```

- `off` turns a resource that a type, a fleet or the robot's apps provide off **on this robot only**.
  The robot deletes it and what it owns: a HelmChart's release and what the release created (its own
  PVCs included), a PVC item, and for a Namespace everything in it. What Kubernetes keeps stays
  (StatefulSet `volumeClaimTemplates` volumes).
- When data goes, the first call writes nothing: it prints the impact and
  `Run it again with --confirm <text>`. Then repeat it with exactly that `--confirm` text. A noop and
  `--dry-run` never ask.
- `on` is a **fresh install**: override values and version apply again, but the data the item owned
  when it went is not restored.
- `off` is refused (exit 8) for: a platform item, a local item (use `remove`), a CRD, an item git
  marks `teknoir.org/required`, a Namespace that still holds items that stay on, a robot whose agent
  is older than `toe_v4.2`, and an app whose off would need more than 64 resource overrides. A local
  item is removed with `config remove`, not turned off.
- `off --app <path>` writes every item of that application in one all-or-nothing change; `--repo`
  picks the repository when the path exists in several. A resource the application adds later is not
  off.

### `add`, `edit` and `remove` (local items)

```bash
tnctl config kinds --template Deployment > whoami.yaml
tnctl config add <robot> -f whoami.yaml --dry-run
tnctl config add <robot> -f whoami.yaml --wait=applied
tnctl config add <robot> -f whoami.yaml --replace --wait=applied   # change it later
tnctl config get <robot> Deployment/default/whoami --manifest > whoami.yaml   # only a local item
tnctl config remove <robot> Deployment/default/whoami --dry-run
tnctl config remove <robot> <resource> --wait=applied
```

- A local item wins over a git copy with the same `Kind/namespace/name` and stays until removed.
- Never use `config edit`: it needs a terminal editor. The loop is `get --manifest > file`,
  edit, `add --replace`.
- Several `---` documents are added one by one; the first failure stops the run and the earlier ones
  stay written. Namespace documents are sent first.
- Findings come as `file:line:col: error: message` and exit 5. Fix them; do not retry.
- A local item with an override in place: saving a new version drops the override.
- `remove` only works on a local item, and deleting a Namespace needs `--yes`. A git item is removed
  in git, the fleet, the robot type or the robot's apps - the message says where.
- Which kinds, the size limits and what a bot may not add:
  [references/local-items.md](references/local-items.md).

### `reset`

```bash
tnctl config diff <robot> <resource>          # first: no dry run exists
tnctl config reset <robot> <resource>
tnctl config reset <robot> --all --wait=applied
```

`reset` removes the override of one resource, or with `--all` every override of the robot (values,
versions, images, tried revisions and offs). It needs one of the two - it will not guess.

### `promote`

```bash
tnctl config promote <robot> <resource> --to robot > notebook.yaml      # or fleet:<name>, or type
```

**Sensitive.** `promote` prints the override values as they were typed - a password a person put in a
value comes back in clear - and it needs write access. It writes nothing to git or to the platform:
the YAML goes to stdout, the path, repository and branch (and a GitHub link) to stderr. Ask the
person before you run it, then give them the file and the commit location; after the merge, `reset`
the override.

### `try` and `untry`

```bash
tnctl config try <robot> apps/mock-vision feature-x --note "release candidate" --wait=applied
tnctl config untry <robot> apps/mock-vision
```

The revision must be a branch, a tag or a commit (7 to 40 hex characters) of the repository. `.` with
`--repo` means every application of that repository. The application is replaced in place, so no
value leaks from the old revision. `try --dry-run` checks the revision and writes nothing; `untry`
has no dry run, so read `config diff` first.

### Wait levels

| Level | Means | Bot |
|---|---|---|
| `published` | device-controller evaluated the write and published the configuration; the override is `active` or `redundant` | yes |
| `applied` | the robot has it: a HelmChart with the published values and version and its helm Job succeeded, a workload with the published image | needs a device shell: **refused** |
| `ready` | applied, and the rollout is complete: pods Ready, no restart for 20 s | needs a device shell: **refused** |

- Write `--wait=applied`, with an equals sign. `--wait applied` makes `applied` an argument and is
  refused with a hint.
- A bare `--wait` means `ready` (for `off`: `applied`).
- `--timeout` is 5 minutes by default. Exit 4 means the wait ran out **and the change is written**:
  `config wait` again or inspect; do not write it again.
- `applied` and `ready` look at the robot over WireGuard before anything is written. An unreachable
  robot means nothing is written and exit 1; use `--wait=published`.
- `published` is not "it runs". Follow it with `tnctl inspect pod` / `inspect logs` / `inspect
  release` when it matters.

## Failure modes

Branch on the exit code, never on the message text.

| Exit | Meaning for a config write | What to do |
|---|---|---|
| 0 | ok, including a noop (already off, already identical) | read `warnings` in `-o json` |
| 1 | other: the robot could not be reached before an `applied`/`ready` write, or the wait was interrupted | the change is or is not written: read the message |
| 2 | usage: bad argument, ambiguous resource, `--dry-run` with `--wait`, `off` without its `--confirm`, removing a Namespace without `--yes` | fix the command |
| 3 | not found: no such robot, device, resource, application or tried revision | check names with `config get` |
| 4 | the wait ran out; the change **is** written | `config wait` again, inspect |
| 5 | the platform refused: invalid values or manifest, unknown revision, the override is `invalid`, `no-target` or `not-applicable`, the device is disabled | fix the input; retrying changes nothing |
| 6 | not logged in, token rejected, or not allowed (401/403): no write access, a bot at Read only, a bot command refused | a person logs in, or the bot's creator raises the access. Do not work around it |
| 7 | applied but unhealthy: a helm Job failed, a pod crashes or cannot pull its image | inspect the logs, then change or `reset` |
| 8 | conflict: someone changed the same overrides (retry once, immediately); the local item exists (use `--replace`); the item is not local; `off`/`on` refused; the confirmation text changed | retry only a concurrent change |
| 9 | rate limited | wait `error.retryAfter` seconds from `-o json`, then retry once |

Messages you will meet, with what they mean:

| Message (excerpt) | Exit | What to do |
|---|---|---|
| `The overrides of <robot> were changed by someone else just now. Try again.` | 8 | retry once, immediately |
| `<id> is a <Kind>: values and version apply to HelmCharts. Use --image ...` | 2 or 5 | use `--image`, or `image.tag` |
| `Version <v> is not in the chart repository's index ...` | 5 | pick a version the index lists |
| `<id> is not part of the configuration of <robot>. Check the kind, namespace and name.` | 3 | use the id from `config get`, not the identity |
| `<id> belongs to the platform and cannot be overridden.` | 5 | use `off`, or leave it |
| `At most 64 resources can be overridden on one device. Reset some first.` | 5 | `reset` one first |
| `This would make the <robot>'s Device N KiB. At most 1228 KiB (1.2 MiB) fits ...` | 5 | remove something first |
| `Turning off <id> deletes data on the robot (<reasons>). Run it again with --confirm <text>` | 2 | repeat with that `--confirm` text |
| `Busy checking other manifests, try again` (HTTP 503) | 1 | if `-o json` gives `error.retryAfter`, wait that long and retry once; without it, report and stop |
| `Too many changes. Try again in N seconds.` | 9 | wait `retryAfter`, then once |
| `The portal may not change this device. Ask a platform administrator.` | 6 | a person with more rights, or an admin |
| `<robot> is the computer of TeknoirBot <bot>; its configuration is managed by the platform.` | 8 | not configurable this way; a person redeploys the bot |

## Bot mode

In bot mode everything here works the same, with these differences. Read `.bot.access` from
`tnctl whoami -o json` and branch on that field, never on the text of an error.

| Action | `read` | `creator` |
|---|---|---|
| `get`, `diff`, `links`, `kinds`, `get --manifest`, `config wait --for published` | yes | yes |
| `set`, `unset`, `delete`, `on`, `off`, `reset <resource>`, `add`, `edit`, `remove`, `try`, `untry` | no: exit 6 | yes |
| `reset --all` | never | never: `A TeknoirBot may not use this endpoint.` Reset one resource at a time |
| `promote` | no | yes, and it returns secrets: ask the person first |
| `--wait=applied`, `--wait=ready`, a bare `--wait` | refused | refused |

- At access `none` every call is refused (exit 6): the bot's creator has turned its access Off.
- Only `--wait=published` works: `applied` and `ready` read the robot over a device shell, which a bot
  never has. A bare `--wait` means `ready`, so it is refused too, with exit 6 **before anything is
  written**: `a TeknoirBot cannot run commands on devices: --wait=ready needs a device shell; use
  --wait=published`. `config wait` defaults to `--for ready`: always pass `--for published`.
- A 401 means the bot's key was not accepted: a person must re-create the bot. Never run `tnctl login`
  from a bot and never type a Teknoir password into its computer.
- After a write, check it with `tnctl config get`, `tnctl inspect pod`, `tnctl inspect logs` and
  `tnctl inspect release` (inspect needs `creator`).
- Local items: a bot may add only a short list of kinds and only if the item passes the platform's
  item filter; the rest are refused with exit 6 and the reason. The list and every refusal text are
  in [references/local-items.md](references/local-items.md).
- A bot cannot create namespaces, HelmCharts or ServiceAccounts for a robot, cannot disable a robot or
  a device, and cannot change its own computer's configuration. When a task needs one of those, give
  the person the exact command.
- The bot's own computer, its cluster and its permissions are in
  [../teknoir-platform/references/bot-vm.md](../teknoir-platform/references/bot-vm.md).

## Safety

- Never print or pass on a device login, a bot key, a token or a password. Never read
  `$TNCTL_BOT_KEY_FILE`. `promote` returns the override values as they were typed, a secret included:
  treat its output as sensitive, and ask before you run it.
- Read before you write: dry run, then the write with `--note`, then a wait.
- A `delete`, an `off` and a `remove` of a Namespace destroy data on the robot. Say so before you run
  one, and use `--dry-run` first.
- Never turn a robot's Shell on, and never call an inspect route with a bare `tnctl api`: use
  `tnctl inspect`, which sends `enable=false`.
