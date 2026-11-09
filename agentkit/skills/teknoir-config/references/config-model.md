# The configuration model in detail

Read this when you need the exact merge rules, the states the platform reports for an override, or
the messages that go with them. The commands are in [../SKILL.md](../SKILL.md).

## Layers

Lowest first; a higher layer wins:

| Layer | Comes from | Stored in | Writable by |
|---|---|---|---|
| platform | the agent, the tunnels, the keys | Device `spec.manifest.apps`, managed by the platform | nobody; not overridable and not offable |
| type | the robot type's `applications` | the RobotType resource | git, or `tnctl api PUT <teamspace>/types/<name>` |
| fleet | every `DeviceApplicationSet` whose selector matches the device's labels | the DeviceApplicationSet | git, or `tnctl api PUT <teamspace>/fleets/<name>` |
| robot | the robot's own `applications` | the Robot resource, copied to the Device | git, or `tnctl api PATCH <teamspace>/robots/<robot>` |
| local | items written straight to the Device (Devstudio, `config add`) | Device `spec.manifest.apps`, inline | `config add`, `config edit`, `config remove` |
| override | `Device.spec.overrides` and in-place overrides of local items | the Device | `config set`, `delete`, `on`, `off`, `try`, `reset` |

Fleets are applied in ascending `priority`, then by name; the one that sorts last wins.

A **TeknoirBot's own computer** gets no git layer at all: no type, no fleet, no robot applications. A
write to it through the fleet API is refused (exit 8, `<name> is the computer of TeknoirBot <bot>;
its configuration is managed by the platform.`). An override stored there anyway reports `no-target`
(an application) or `not-applicable` (a platform or local item).

An **application** is `{repo_url, revision?, path, node_role?}`. The same application listed in
several layers is kept once, in the highest layer. `node_role` adds a `nodeSelector` and tolerations
for that role.

## Identity

Two items are the same item when kind, namespace and name match. The platform prints that as
`<Kind>-<name>-<namespace>` (namespace `default` when empty) in `identity`; the commands take it as
`Kind/namespace/name`. They are not interchangeable.

## Merge rules

- **HelmChart** (`helm.cattle.io/*`): copies merge from the top down, until the first copy that is
  not a HelmChart or that carries the annotation `teknoir.org/merge: replace` - that copy replaces
  everything below it.
- `spec.valuesContent` is parsed like Helm parses it (YAML 1.1: `yes` and `on` are booleans, `0o17`
  is octal, dates stay strings) and deep-merged from low to high:
  - maps merge key by key;
  - **lists are replaced whole**, never merged;
  - a scalar replaces the one below it;
  - an explicit **`null` stays a null leaf and removes the key** from the lower layers; Helm then
    also drops the chart's own default.
- `spec.set` merges per key (a null deletes the key). Every other `spec` field comes from the highest
  copy that sets it. `metadata.labels` and `metadata.annotations` merge.
- **Every other kind is replaced whole** by the highest copy.
- A local or platform item is never merged with a git copy: it replaces everything below it. The one
  exception is a local item with an in-place override, which is merged with its override.
- An error in `valuesContent` reports
  `invalid spec.valuesContent from <layer>:<source> of HelmChart <identity>: <parser message>`; a copy
  without `spec.chart` in any layer reports `<identity> has no spec.chart or spec.chartContent in any
  layer`.
- `config get --provenance` gives, per leaf of the merged values, the path, value, layer, source and
  the overrides above it. A value the platform treats as a secret shows as `<hidden>`.

## Overrides

An override of a resource becomes one more HelmChart copy on top of the git copies (layer `override`)
for values and version; container images are set on the winning workload after the merge, because
workloads are not merged. An application override (`try`) rewrites the revision before the layers are
resolved.

- All parts of an override are checked first: an override with one part wrong applies **none** of its
  parts (state `invalid`).
- `off: true` leaves the resource out of the configuration. Its values, version and images stay
  dormant until `on`.
- Overriding a local item in place keeps the original in the annotation `teknoir.org/override-base`.
  An entry in `spec.overrides` for a local item is not applied and warns:
  `The override of <key> is not applied: it is a local item of the device. Edit it in place.`

### States the controller reports (`overrideStatus[]`)

| State | Meaning |
|---|---|
| `active` | applied and in the published configuration |
| `redundant` | git already gives this result; the override can be removed |
| `no-target` | no layer provides the key; the override can be removed |
| `invalid` | the override cannot be applied (bad YAML, wrong kind for the field, unknown revision); the message says why |
| `not-applicable` | the key is a local item or belongs to the platform: overrides apply to items rendered from git |
| `off` | the resource is off on this robot |
| `pending` | the fleet API's state while the controller has not evaluated the latest overrides revision |

The target is `Kind/namespace/name`, or `app:<repo_url>#<path>` for an application override.

### Warnings on the effective view

- `The override <key> is ignored: the key must be Kind/namespace/name.`
- `The override of <key> is not applied: it belongs to the platform.`
- `The override of <key> is not applied: no layer provides it.`
- `The override of <key> is not applied: there is no container <names>; it has: <names>.`

### Message from the controller itself

`values and version apply to HelmCharts; use images for workloads`,
`images apply to workloads; use values (for example image.tag) for a HelmChart`,
`the resource is not a helm.cattle.io HelmChart`, `the key must be Kind/namespace/name`, `use <canonical>`.

## Rows and statuses

`config get` lists one row per effective resource. `status` is one of:

| Status | Meaning |
|---|---|
| `winner` | this copy is the one applied |
| `merged` | a lower HelmChart copy whose values are part of the merged result |
| `overridden` | a higher layer replaces this resource entirely |
| `pending` | not in the Device's rendered configuration: the controller has not reconciled yet, or the merge failed. If it stays, read `configuration.warnings` |
| `off` | turned off on this robot: the row stays, the resource is not in the configuration |

`deviations[]` lists the resources where the robot's configuration differs from its type, with a
reason of `off`, `overridden` or `missing`.

## What gets published, and what the robot keeps

device-controller validates every item (a schema, the robot agent's shape, the name) and publishes
the whole `spec.manifest.apps` as one retained MQTT message to `/devices/<device>/config`. An item
that is invalid is held back at its last published version (`ConfigItemHeldBack`, "invalid, the
previous version stays") or left out (`ConfigItemDropped`). The config hash of the published payload
is what `config get` shows as `Published:`; the fleet API exposes it as
`published: {hash, lastChange, message}`.

The robot's agent applies the items and keeps only `apiVersion`, `kind`, `metadata`, `data`,
`binaryData`, `type`, `spec` and `status` of each. `config add` refuses an item with any other
top-level field (exit 5, `'<field>' is dropped by the robot's agent ...`); a Secret's `stringData` is
converted to `data`.
