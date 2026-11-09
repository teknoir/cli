# Robot API shapes

The exact bodies and answers for `tnctl api`, the robot detail and the type and fleet objects. Read
this when you build a request or parse an answer; the commands in the skill cover the common cases.

## Create a robot

`tnctl robot add` fills `name`, `type`, `labels`, `device` and `orgUnit`. The other two fields exist on
the route but have no flag: send them with `tnctl api POST <teamspace>/robots --data '…'`, or PATCH
them after the create.

| Field | Type | Rule |
|---|---|---|
| `name` | string, required | DNS label, 1–63. A robot of that name must not exist, and the name may not start `tb-`. |
| `type` | string, required | A robot type in the same teamspace. |
| `labels` | map, optional | Kubernetes label syntax; keys under `teknoir.org/`, `kubernetes.io/`, `k8s.io/` are refused. |
| `device` | string, optional | An existing device to adopt. Default: a new device named like the robot. |
| `nodes` | list, optional | `[{role, nodeName}]`. Default: every non-optional role of the type, each named `<robot>-<role>`. |
| `serverAddress` | string, optional | Host name or IPv4, no scheme and no port. |
| `orgUnit` | string, optional | The unit's **id**: `^[A-Za-z0-9][A-Za-z0-9_-]{0,62}$`. |

```json
{
  "name": "amr-02",
  "type": "amr",
  "labels": {"site": "karlstad"},
  "device": "amr-01",
  "nodes": [{"role": "main", "nodeName": "amr-02-main"}],
  "serverAddress": "192.168.1.20",
  "orgUnit": "AbC123"
}
```

The answer is the robot detail, HTTP 201. Without `device`, the phase is `WaitingForDevice` with
`Device N does not exist yet. The platform creates it within a minute.`; with an adopted `device`, it
is that device's phase.

## The robot answer

`tnctl robot <robot> -o json` prints the platform's detail unchanged. `tnctl robots -o json` prints a
list of summaries: the same fields without `spec`, `configuration` and `bootstrap`.

| Field | Meaning |
|---|---|
| `name`, `namespace` | The robot and its teamspace. |
| `type`, `typeDisplayName` | Its type. |
| `labels` | Its own labels, copied onto the device. |
| `device` | The device that backs it (default: the robot's name). |
| `phase`, `phaseMessage` | `WaitingForDevice`, `Registered`, `Online`, `Degraded` or `Offline`, and a sentence for a person. |
| `nodes[]` | `{role, nodeName, joined, ready, k3s, hardware, optional}` per computer. |
| `layers[]` | `{layer, source, applications}`: which layer contributes which application. |
| `deviations`, `overrides`, `oldestOverrideAt` | Where the robot's configuration differs from git, and since when. |
| `fleets[]` | The fleets that match it. |
| `org` | `{unit, path[{id,name}]}` when it is placed in a unit. |
| `disabled` | `{state: disabling\|disabled\|enabling, at, by, reason, sessionsClosed, message, error}` when it is in the disable cycle. |
| `deviceOwner` | Another robot, when this robot runs on that robot's device. |
| `spec` | `{type, device, nodes, serverAddress, applications}`: the robot's settings. A PATCH takes `nodes`, `serverAddress` and `applications` as top-level fields (not under `spec`); `type` and `device` cannot be changed, and a PATCH ignores them without an error. |
| `configuration` | The configuration view the `teknoir-config` skill reads. |
| `bootstrap` | `{device, prepare[], steps[], blockers[]}` — the secret-free onboarding plan. |

```json
{
  "name": "amr-02",
  "namespace": "<teamspace>",
  "type": "amr",
  "device": "amr-02",
  "phase": "Registered",
  "phaseMessage": "Device amr-02 has not connected yet. Bootstrap the first computer to bring it online.",
  "labels": {"site": "karlstad"},
  "nodes": [{"role": "main", "nodeName": "amr-02-main", "joined": false, "ready": false, "k3s": "server"}],
  "bootstrap": {
    "device": "amr-02",
    "prepare": ["tnctl ns <teamspace>", "tnctl bootstrap amr-02 --skip-upload --robot amr-02 --role main"],
    "steps": [{"role": "main", "nodeName": "amr-02-main", "k3s": "server", "commands": ["sudo bash bootstrap_server_amr-02.sh"], "note": "Run this first and wait until the node is Ready."}],
    "blockers": []
  }
}
```

`blockers` entries a person must fix first: a missing type, a device that does not exist yet (reload in
a minute), two roles using one node name, or a missing `serverAddress` when the type has agent roles.

## Change a robot

`PATCH <teamspace>/robots/<robot>` takes any of `labels`, `nodes`, `serverAddress` and `applications`,
and at least one of them.

| Field | Effect |
|---|---|
| `labels` | **Replaces** the robot's non-reserved label set: a key you leave out is removed. |
| `nodes` | Replaces the role-to-computer bindings. |
| `serverAddress` | Sets or (`""`) clears the server's LAN address. |
| `applications` | **Replaces** the robot's own configuration layer. |

```json
{"serverAddress": "192.168.1.20"}
```

For `labels`, `nodes` or `applications`, send the full set: read `.labels`, `.spec.nodes` and
`.spec.applications` from `tnctl robot <robot> -o json`, merge your change in, then send it.

The answer is the updated robot detail. A 409 means someone changed the robot first: re-read and apply
your change again.

## A robot type

`POST <teamspace>/types` to create, `PUT <teamspace>/types/<type>` to replace (the body's `name` must
equal the path).

| Field | Rule |
|---|---|
| `name` | DNS label. Cannot be changed by a PUT. |
| `displayName` | Up to 100 characters. |
| `description` | Up to 500 characters. |
| `nodes[]` | At least 1, at most 20. Each `{role, k3s, hardware?, labels?, taints?, description?, optional?}`. |
| `nodes[].role` | DNS label, unique in the type. |
| `nodes[].k3s` | `server` or `agent`. Exactly one role is the server. |
| `nodes[].hardware` | A board label such as `raspberry-pi` or `generic-x86_64`; decides the device's board for robots created from this type. |
| `nodes[].labels` | At most 32; `kubernetes.io/`, `k8s.io/` and the platform's own keys are refused. |
| `nodes[].taints[]` | `{key, value?, effect}` with effect `NoSchedule`, `PreferNoSchedule` or `NoExecute`. A blocking taint on the server role is refused. |
| `nodes[].optional` | A role that a robot of this type need not have a computer for. |
| `applications[]` | At most 50. Each `{repo_url, revision?, path, node_role?}`; `node_role` must be a role of this type. |
| `capabilities[]` | At most 20 words of lower-case letters, digits and dashes. |

```json
{
  "name": "amr",
  "displayName": "AMR",
  "description": "An autonomous mobile robot",
  "nodes": [
    {"role": "main", "k3s": "server", "hardware": "generic-x86_64"},
    {"role": "vision", "k3s": "agent", "optional": true}
  ],
  "applications": [
    {"repo_url": "https://github.com/teknoir/teknoir-demo-gitops", "revision": "main", "path": "common/notebook.yaml"}
  ],
  "capabilities": ["camera"]
}
```

The answer adds the counts: `robots`, `robotsWithOverrides`, and the robots that use the type.

## A fleet

`POST <teamspace>/fleets` to create, `PUT <teamspace>/fleets/<fleet>` to replace.

| Field | Rule |
|---|---|
| `name` | DNS label. Cannot be changed by a PUT. |
| `displayName`, `description` | Up to 100 and 500 characters. |
| `selector` | At least one `matchLabels` or `matchExpressions` entry. An empty selector is refused: it would match every device. |
| `selector.matchExpressions[]` | `{key, operator, values}`; operators `In`, `NotIn`, `Exists`, `DoesNotExist`. |
| `priority` | A whole number; higher wins over lower, ties are decided by name. |
| `applications[]` | As for a type, at most 50. |

```json
{
  "name": "karlstad",
  "displayName": "Karlstad store",
  "selector": {"matchLabels": {"site": "karlstad"}},
  "priority": 10,
  "applications": [
    {"repo_url": "https://github.com/teknoir/teknoir-demo-gitops", "revision": "main", "path": "common/devstudio.yaml"}
  ]
}
```

The answer adds `matchedRobots[]` and `matchedDevices[]`: the devices the selector matches that back no
robot.

## Errors

With `-o json`, an error is one object on stderr: `{"error":{"code":"<name>","message":"…"}}`. The code
names and their exit codes are in the entry skill: `not_found` 3, `rejected` 5, `auth` 6, `conflict` 8,
`rate_limited` 9.
