---
name: teknoir-robots
description: Read, create and change Teknoir robots, their types and fleets, and hand a person the exact steps to put a real computer online. Use it when a task mentions a robot, a robot type, a fleet, a device that backs a robot, node roles, a robot that is waiting or offline, adding a robot, adopting a device, or bootstrapping a computer. Not for changing what runs on a robot (teknoir-config) or for a robot that is broken (teknoir-debug).
license: Apache-2.0
metadata:
  teknoir-kit-version: "0.1.0"
---

# Robots, types and fleets

A robot is the unit a person manages; a fleet and a type are configuration. This skill covers reading
them, creating and changing them, and the hand-off to a person for the steps only a person can do.

Paths in this skill are relative to this skill's directory.

## The model

| Thing | What it is |
|---|---|
| **Robot** | One managed machine: a k3s cluster of one server and its agent nodes. It has a type, node roles, labels, a placement and its own configuration layer. |
| **Device** | The cloud identity behind the robot: keys, MQTT login, tunnel. A robot always has exactly one; a device without a robot exists too (a plain "Add Device"). |
| **Robot type** | The base for every robot of its kind: the node roles (exactly one is the k3s server) and the applications every such robot starts with. |
| **Fleet** | A `DeviceApplicationSet`: a label selector, a priority and applications. It is a configuration layer, not a group of hardware. |
| **Node role** | A named set of k3s node labels and taints on the type; each computer of the robot runs one role. |

Configuration layers, lowest first: platform, type, fleet, robot, local, override. Changing those is
the `teknoir-config` skill.

A robot's labels are copied onto its device, so a fleet selects robots by robot label.

## Naming rules

A robot, device, type and fleet name is a **DNS label**: 1–63 characters of lower-case letters,
digits and `-`, starting and ending with a letter or digit.

| Refused | What happens |
|---|---|
| Anything else | Exit 2 from `tnctl` before anything is sent: `robot name "X" is not a DNS label: …`. Through the API: 400 `The robot name "X" is not valid. …`. |
| A name starting `tb-` | 400: those names are kept for the computers of TeknoirBots. Pick another name, or another device. |
| A virtual device name over 58 characters | Refused by admission: a virtual device's name has less room than a label. |
| `add`, `disable`, `enable`, `remove` as a robot name | The subcommand wins. Read it with `tnctl robot -- <name>`. |

Robot label keys under `teknoir.org/`, `kubernetes.io/` and `k8s.io/` are reserved: `labels: the label
key "K" is reserved for the platform. Use another key.`

## Listing and reading

```bash
tnctl robots                                  # NAME TYPE PHASE NODES OVERRIDES FLEETS
tnctl robots site=karlstad -o json            # a label filter; AND over several labels
tnctl robot <robot>                           # one robot: type, phase, computers, layers
tnctl robot <robot> -o json                   # the platform's answer, including the bootstrap plan
```

`tnctl robots` shows `Disabled`, `Disabling`, `Enabling`, `Not fully disabled` or `Enabling failed` in
the phase column when the robot is in the disable cycle. An empty teamspace prints `[]` and exits 0.

There is no tnctl subcommand for types or fleets: use `tnctl api GET <teamspace>/types` and
`…/fleets`.

```bash
tnctl api GET <teamspace>/types               # name, display name, roles, applications, robot count
tnctl api GET <teamspace>/fleets              # selector, priority, applications, matchedRobots
```

Read [references/robot-api.md](references/robot-api.md) when you need the exact body of a create, a
type or a fleet, or the fields of a robot detail.

## Creating a robot

```bash
tnctl robot add <robot> --type <type> [--device <device>] [--unit <unitId>] [--label key=value]...
```

| Flag | Meaning |
|---|---|
| `--type` | Required. The type the robot is created from. List them with `tnctl api GET <teamspace>/types`. |
| `--device` | Adopt an existing device instead of creating one. It must exist, be visible to you, back no other robot, and not be a bot's computer. |
| `--unit` | The org unit's **id** (not its name): `.bot.unitId` from `tnctl whoami -o json` for a bot's own unit, otherwise `units[].id` from `tnctl api GET <teamspace>/org`. Without it, an adopted device that is already placed keeps its placement; otherwise a placed bot or a person limited to one unit gets that unit, a person who is not limited gets an unplaced robot, and a person limited to several units is refused (exit 5). |
| `--label` | A label of the robot, `key=value`. Repeat for several. |
| `-o json` | Print the robot the platform answered instead of one line. |

A robot placed in an org unit is confined to it, and a person limited to units can create robots only
there.

**What the platform does next**, within about a minute:

1. Without `--device`, creates a Device named like the robot, with a board from the type's server role
   when that role names a known one, and otherwise `generic`; with `--device`, adopts that existing
   device.
2. Adds the robot's labels and applications to the device; an unlabelled device is adopted and keeps
   its applications by handing them to the robot.
3. Creates the robot's k3s join-token Secret and fills the device's key pair.
4. Reports the phase. For a new device the create answer is `WaitingForDevice` with `Device N does not
   exist yet. The platform creates it within a minute.`; an adopted device answers with its own phase
   (often `Registered` or `Online`).

A robot created through the API is never a virtual device. To put a robot on a VM, create the virtual
device first in the portal and adopt it with `--device`.

**A robot cannot take a TeknoirBot's computer as its device.** `tnctl robot add` is refused (exit 8,
or exit 5 for a `tb-` name); a robot whose device became a bot's computer later shows
`WaitingForDevice` with a conflict message.

`tnctl robot add` has no flags for node names or the server address. To set them, PATCH after the
create (see below).

## Changing a robot

```bash
tnctl api PATCH <teamspace>/robots/<robot> --data '{"serverAddress":"192.168.1.20"}'
```

For labels, nodes or applications, never send a partial body: use the read-merge-send example below.

The body takes `labels`, `nodes`, `serverAddress` and `applications`, and at least one of them
(`Nothing to change. Send at least one of labels, nodes, serverAddress or applications.`).

- `serverAddress` is the server computer's LAN address, which agent computers join. Send `""` to clear
  it.
- `nodes` is the list of `{role, nodeName}` bindings; every node name must be unique.
- `applications` is the robot's own configuration layer.

**`labels`, `nodes` and `applications` replace the whole set.** Sending only the new label deletes
every other label the robot has, and the same holds for node bindings and applications. Read first
(`.labels`, `.spec.nodes`, `.spec.applications` of `tnctl robot <robot> -o json`), merge, then send the
full map or list:

```bash
# Add the label site=karlstad and keep every existing label.
tnctl robot amr-02 -o json | jq -c '{labels: ((.labels // {}) + {"site": "karlstad"})}' > patch.json
tnctl api PATCH <teamspace>/robots/amr-02 --data @patch.json
tnctl robot amr-02                            # check that every label is still there
```

Reserved label keys cannot be set, and a conflict (exit 8) means someone changed the robot first:
re-read and apply your change again.

**The type cannot be changed.** A person removes the robot and creates it again with the new type. Tell
them the two commands; do not try to rewrite the type.

Applications may only name a repository the teamspace is allowed to use. When it is not: 403 `The
repository R is not available to this teamspace. Ask a platform administrator to allow it.`

## Types and fleets

Create with `tnctl api POST` and change with `tnctl api PUT`. Both apply to the whole teamspace, so a
person limited to org units, and a TeknoirBot that is placed in one, are refused.

A **PUT replaces the whole object** (a field you leave out is cleared; a fleet's priority goes back to 0). Start from what the platform has: a type with `tnctl api GET <teamspace>/types/<type> | jq '{name,displayName,description,nodes,capabilities,applications:[.applications[]|{repo_url,revision,path,node_role}]}' > type.json`; a fleet has no single read, so `tnctl api GET <teamspace>/fleets | jq '.[] | select(.name=="<fleet>") | {name,displayName,description,selector,priority,applications:[.applications[]|{repo_url,revision,path,node_role}]}' > fleet.json`. Edit the file and send it with `tnctl api PUT <teamspace>/types/<type> --data @type.json` (or `…/fleets/<fleet> --data @fleet.json`). The `name` in the body, if present, must equal the name in the path; a type and a fleet cannot be renamed.

What the platform checks:

| Object | Rules |
|---|---|
| Type | At least one node role and at most 20; role names unique; exactly one `k3s: server`; a blocking taint on the server role is refused; `applications` at most 50, each `node_role` a role of this type; `capabilities` at most 20 words. |
| Fleet | A selector with at least one `matchLabels` or `matchExpressions` (operators `In`, `NotIn`, `Exists`, `DoesNotExist`) — an empty selector would match every device; `priority` a whole number, higher wins; `applications` at most 50. |

Deleting a type that robots use is refused: `N robot(s) use robot type "T" (a, b). Move or delete them
first.`

## Disable, enable, remove

These are a teamspace admin's commands, and a TeknoirBot may never run them.

```bash
tnctl robot disable <robot> --reason "…" --wait      # withdraw MQTT key, close sessions, Tunneling off
tnctl robot enable <robot> --wait                     # register the key again
tnctl robot remove <robot> --yes                      # remove the robot
tnctl device disable|enable <device>                  # the same for a device without a robot
tnctl device remove <device> --yes                    # only a device that backs no robot
```

- A disabled robot keeps running its apps but cannot reach the platform.
- `--reason` is optional: one line of at most 200 characters (exit 2 otherwise); `remove` without
  `--yes` prints what would go and exits 2, removing nothing.
- `tnctl device remove` works only for a device that backs no robot; remove the robot instead.
- `tnctl robot remove` deletes the robot **and its device**, with the device's overrides, local items,
  keys, MQTT access and tunnels. It cannot be undone. Only a device that belongs to another robot
  survives, and then the command says so before it runs.

When a person asks for one of these, print the exact command and let them run it.

## Physical onboarding and bootstrap

Putting a real computer online is human work. Your job is to produce the exact, secret-free steps and
hand them over.

What a person does, on a trusted workstation with their own login:

```bash
tnctl ns <teamspace>
tnctl bootstrap <device> --skip-upload [--robot <robot> --role <role> --agent-role <role> …]
```

That writes `bootstrap_server_<device>.sh`, `bootstrap_agent_<device>.sh` and
`bootstrap_docker_server_<device>.sh`, mode 0600, in the current directory. They embed the device
login, the k3s join token and, if asked, a registry credential: **they are secrets**. The person copies
each to its computer and runs it with `sudo`, the server script first, and waits until the node is
Ready before each agent script.

It fails until the device has its key pair; wait a few minutes after the create and try again.

**Do not run `tnctl bootstrap`**, and do not print the scripts. A bot is refused it outright; a person's
agent must not handle device credentials.

What you can do is read the plan the platform has already written for the robot — it contains no
secret:

```bash
tnctl robot <robot> -o json | jq -r '.bootstrap.prepare[], (.bootstrap.steps[] | .nodeName, .commands[], .note), (.bootstrap.blockers[])'
```

Give the person `prepare` (the commands to run on their workstation), `steps` (what to run on each
computer, in order) and `blockers` (what must be fixed first). Then poll:

```bash
tnctl robot <robot> -o json | jq -r '.phase, .phaseMessage'
```

`WaitingForDevice` → `Registered` (the device exists, nothing has connected) → `Online`. A `blockers`
entry about the server address means the robot needs `serverAddress` set (see Changing a robot).

## Virtual devices

A virtual device is a KubeVirt VM with its own k3s, created in the portal with **Devices → Add Device
→ Virtual** and a preset (Small, Medium) or custom resources. It is not creatable through the fleet
API, and a robot created with `tnctl robot add` is never virtual.

To put a robot on a VM: create the virtual device first, then `tnctl robot add <robot> --type <type>
--device <vm>`. Without `--unit`, a placed device keeps its placement.

A teamspace has an allowance (a number of devices, vCPU, memory and disk) and each device a maximum
size. When the VM is not admitted, the portal shows the reason on the device's page; a robot on it
typically stays `WaitingForDevice` or `Registered`, because the admission state is not part of the
robot answer. Hand that to a person: only a platform administrator changes the allowance.

## Lifecycle

### Phases

| Phase | Meaning | What you do |
|---|---|---|
| `WaitingForDevice` | The device does not exist yet, its keys are not filled, or it conflicts with another robot or a bot's computer. | Wait a minute after a create. If it stays, read `phaseMessage`. A robot's device cannot be changed: give a teamspace admin `tnctl robot remove <robot> --yes` and `tnctl robot add <robot> --type <type> --device <other>`. If the device is a TeknoirBot's computer, the remove is refused (exit 5); report that to the person instead. |
| `Registered` | The device has keys but has never reported. | A person runs the bootstrap. Hand over the plan. |
| `Online` | The device reported within the last 5 minutes, with no firing alert. | Nothing. |
| `Degraded` | Online, with firing alerts. | `teknoir-debug`. |
| `Offline` | Reported before, silent for 5 minutes or more. | Power and network on site: a person's job, not the platform's. |

The controller reconciles every 60 seconds, so a phase can lag a minute behind reality, and a robot
whose metrics cannot be read keeps its previous phase.

A disabled robot keeps one of these in `.phase`; check `.disabled` (`state`: `disabling`, `disabled`
or `enabling`, and `error`). Its `phaseMessage` says who disabled it, when and why,
`overview.robots.disabled` counts it outside the phases, and `tnctl robots` shows `Disabled`,
`Disabling`, `Enabling`, `Not fully disabled` or `Enabling failed` in PHASE.

### Conditions

| Condition | When |
|---|---|
| `TypeFound` | False when the robot's type does not exist: `Type X does not exist in this teamspace. Create it, or change the robot's type.` The robot still gets its device. |
| `DeviceReady` | True when the device has keys; False while they are being made, or on a conflict. |
| `DeviceConflict` | `OtherRobot` when the device belongs to another robot; `TeknoirBot` when it is a bot's computer. |

Conditions are not in the robot answer the API returns: read the phase and `phaseMessage` instead, and
`bootstrap.blockers` for a missing type.

### Refusals you will meet

| Answer | Exit | What it means |
|---|---|---|
| `A robot named "N" already exists. Pick another name.` | 8 | The name is taken. Do not retry; pick another. |
| `Device "D" already backs robot "R". Pick another device or robot name.` | 8 | Pick another device. |
| `A device named "N" already exists. Pick another robot name, or choose that device to adopt it.` | 8 | Use another name, or adopt it with `--device N`. |
| `Device "D" belongs to robot "R". Pick another device.` | 8 | Pick another device. |
| `Device "D" does not exist. Leave the device empty to create a new one.` | 5 | The device is not there or not visible to you. |
| `The robot type "T" does not exist in this teamspace. Create it first or pick another robot type.` | 5 | Create the type, or use another. |
| `X is the computer of TeknoirBot B and cannot back a robot.` | 8 | Pick another device. Do not retry. |
| `Robot names starting with tb- are kept for the computers of TeknoirBots…` / `Device tb-x has a name kept for the computers of TeknoirBots…` | 5 | Pick another name or device. |
| `This TeknoirBot may only read (Read only)…` | 6 | A bot at Read only cannot write. Ask the creator. |
| `Choose the org unit for this robot: A, B.` | 5 | Pass `--unit <id>`. |
| `Your account is limited to org units but has none assigned…` | 5 | Ask a teamspace admin to assign a unit. |
| `You can only place robots in your own units (…) or below them.` | 6 | The unit is outside your scope. |

### `NotAdmitted`

`NotAdmitted` is not a robot phase. It is the state of a **virtual device** whose request the platform
refused, and of a TeknoirBot whose computer could not be admitted. A robot on such a device shows its
usual phase, so the reason is not in the robot answer at all.

When a robot on a VM never comes online, do not guess: tell the person to open the device (or the bot)
in the portal, where the admission reason is shown — no allowance, above the per-device maximum, the
allowance exceeded, virtual devices not enabled in this environment, or the VM runtime missing. Only a
platform administrator can change an allowance.

## Worked examples

**Create a robot of an existing type and hand over the bootstrap.**

```bash
tnctl whoami -o json                      # mode, teamspace, and for a bot its access
tnctl api GET <teamspace>/types           # the type's name and roles
tnctl robot add amr-02 --type amr --label site=karlstad
tnctl robot amr-02 -o json | jq -r '.phase, .phaseMessage'
```

`robot amr-02 created (device amr-02)`. Wait for `Registered`, then give the person the `bootstrap`
plan and poll until `Online`.

**Adopt an existing device.**

```bash
tnctl api GET <teamspace>/devices | jq -r '.[] | select((.robot // "") == "" and (.bot // "") == "") | .name'   # devices that back no robot and are no bot's computer
tnctl robot add vs-02 --type vision --device spare-01
```

The robot takes over the device's applications. A device that is already placed keeps its placement
unless you pass `--unit`, which moves it; an unplaced device is placed as described under `--unit`.

**Place a robot in an org unit.**

```bash
tnctl api GET <teamspace>/org             # units[]: pick the id, not the name
tnctl robot add amr-03 --type amr --unit <unitId>
```

A wrong id: `The org unit "x" does not exist in teamspace <teamspace>. Pick a unit from the org tree.`

## Bot mode

In bot mode the reads above work unchanged: `tnctl robots`, `tnctl robot <name>`, and
`tnctl api GET <teamspace>/types|fleets|org`. Branch on `.bot.access` from `tnctl whoami -o json`.

| Action | `read` | `creator` |
|---|---|---|
| List and read robots, types, fleets, org | Yes | Yes |
| `tnctl robot add`, `PATCH <teamspace>/robots/<robot>` | No (exit 6) | Yes, inside the bot's unit |
| Create or change a type or a fleet | No | Only if the bot is placed in no unit and its creator is not limited to units; a placed bot is always refused |
| `robot disable`, `enable`, `remove`, device lifecycle | Never | Never |

A bot never removes, disables or enables a robot or a device, and never resets anything. When the task
needs it, give the person the command (see Disable, enable, remove).

`inspect` needs Act as me; at Read only it is refused. A write that waits on the robot must use
`--wait=published`.

The bot's own computer, its registry and its builder are in
[../teknoir-platform/references/bot-vm.md](../teknoir-platform/references/bot-vm.md).
