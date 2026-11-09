# The fleet API behind `tnctl api`

Every route below is one entry of the platform's TeknoirBot route table, grouped as the source groups
them. A route a bot may call is `read` (access Read only or Act as me), `creator` (Act as me only) or
`never` (no bot, whatever its access). A person's rights are their own groups in the teamspace.

Read this file when a `tnctl api` call fails with exit 6, or when you need to know whether a TeknoirBot
may call a route at all.

## Before the table

- **Paths are relative to the fleet root and start with the teamspace**: `<teamspace>/robots`,
  `<teamspace>/config/<robot>`. `tnctl api` prepends the root and the identity: a person's login, or the
  bot's own API address and key.
- **The `credentials` routes are refused.** `tnctl api` refuses any path with a `credentials` segment,
  in both modes, and the platform refuses a bot in the handler as well. A device's login is a secret
  neither tnctl nor an agent ever handles.
- **Inspect routes need `?enable=false`** and are better called through `tnctl inspect`. A raw `GET`
  without it turns the robot's Shell on for a person. `tnctl inspect …` always sends it, and the
  platform forces it for a bot.
- A `never` route answers 403 `A TeknoirBot may not use this endpoint.` (exit 6). A `creator` route at
  access Read only answers 403 `This TeknoirBot may only read (Read only)…`. A `read` route at access
  Off answers 403 `This TeknoirBot's agent has no platform access (Off)…`. In every case: report it and
  stop; the bot cannot widen its own access.
- Query parameters are sent as written; percent-encode values that contain `/`, `?` or `&`.

## The bot's own teamspace

| Method | Path | What it does | Bot |
|---|---|---|---|
| GET | `teamspaces` (no teamspace segment) | The teamspaces the caller may use, with `access: read\|write`. A bot sees only its own. | `read` |

## Person pages

| Method | Path | What it does | Bot |
|---|---|---|---|
| GET | `<teamspace>/workspace` | The Devstudio workspaces of the teamspace. | `never` |
| GET | `<teamspace>/settings` | The teamspace settings page. | `never` |
| GET | `<teamspace>/observatory` | The Observatory dashboard data. | `never` |
| GET | `<teamspace>/mqtt/dashboard` | The MQTT dashboard. | `never` |
| GET | `<teamspace>/git/paths` | The folders of an allowed configuration repository. | `never` |
| GET | `<teamspace>/git/refs` | The branches and tags of an allowed configuration repository. | `never` |

## Reading the teamspace and its org tree

| Method | Path | What it does | Bot |
|---|---|---|---|
| GET | `<teamspace>/permissions` | What the caller may do there: `{teamspaceAdmin}`. | `read` |
| GET | `<teamspace>/overview` | Counts of robots, types, fleets and devices, and the items that need a person. | `read` |
| GET | `<teamspace>/problems` | Robots and devices with problems, most severe first; `?robot=` and `?device=` narrow it. | `read` |
| GET | `<teamspace>/org` | The org tree: `units[]` with their ids, what is placed where, and what is unplaced. | `read` |
| GET | `<teamspace>/labels` | The label keys and values in use, for selecting robots and devices. | `read` |

## Robots

| Method | Path | What it does | Bot |
|---|---|---|---|
| GET | `<teamspace>/robots` | Every robot in scope; `?unit=<unit id>` narrows it to one org unit. There is no label filter on this route: use `tnctl robots <key>=<value>`, or filter `.labels` yourself. | `read` |
| GET | `<teamspace>/robots/<robot>` | One robot: phase, nodes, layers, and its secret-free bootstrap plan. | `read` |
| POST | `<teamspace>/robots` | Create a robot and the device that backs it (201). | `creator` |
| PATCH | `<teamspace>/robots/<robot>` | Change `labels`, `nodes`, `serverAddress` or `applications`. | `creator` |
| DELETE | `<teamspace>/robots/<robot>` | Remove a robot; `?deleteDevice=true` removes its own device too. Teamspace admin only. | `never` |

## Robot types and fleets

| Method | Path | What it does | Bot |
|---|---|---|---|
| GET | `<teamspace>/types` | Every robot type: roles, baseline applications, robot counts. | `read` |
| GET | `<teamspace>/types/<type>` | One robot type, with the robots that use it. | `read` |
| POST | `<teamspace>/types` | Create a robot type. Applies to the whole teamspace, so a unit-limited caller is refused. | `creator` |
| PUT | `<teamspace>/types/<type>` | Replace a robot type; the name in the body must equal the one in the path. | `creator` |
| DELETE | `<teamspace>/types/<type>` | Remove a robot type that no robot uses. | `never` |
| GET | `<teamspace>/fleets` | Every fleet: selector, priority, applications, and what it matches. | `read` |
| POST | `<teamspace>/fleets` | Create a fleet. Applies to the whole teamspace, so a unit-limited caller is refused. | `creator` |
| PUT | `<teamspace>/fleets/<fleet>` | Replace a fleet; the name in the body must equal the one in the path. | `creator` |
| DELETE | `<teamspace>/fleets/<fleet>` | Remove a fleet. | `never` |

## Devices

| Method | Path | What it does | Bot |
|---|---|---|---|
| GET | `<teamspace>/devices` | Every device in scope. `?label=<key>=<value>` filters (repeat it, at most 20; all must match), and `?unit=<unit id>` narrows it to one org unit. Never carries keys. | `read` |
| GET | `<teamspace>/devices/<device>` | One device: state, tunnel, configuration hash, placement. | `read` |
| DELETE | `<teamspace>/devices/<device>` | Remove a device that backs no robot. Teamspace admin only. | `never` |
| POST | `<teamspace>/devices/<device>/disable` | Disable a device: withdraw its MQTT key, close sessions, turn Tunneling off. | `never` |
| POST | `<teamspace>/devices/<device>/enable` | Enable it again. | `never` |
| PUT | `<teamspace>/devices/<device>/desktop` | Set the device's remote desktop. | `never` |
| POST | `<teamspace>/devices/<device>/credentials` | The device's login: user, SSH key, password. | `never` |
| POST | `<teamspace>/devices/credentials` | The logins of several devices, by name. | `never` |

## Bots

| Method | Path | What it does | Bot |
|---|---|---|---|
| GET | `<teamspace>/bots` | The TeknoirBots of the teamspace. | `never` |
| GET | `<teamspace>/bots/<bot>` | One bot: phase, resources, agent access. | `never` |
| POST | `<teamspace>/bots` | Create a bot. | `never` |
| PATCH | `<teamspace>/bots/<bot>` | Change a bot, including its agent access level. | `never` |
| DELETE | `<teamspace>/bots/<bot>` | Remove a bot and its computer. | `never` |

## Prometheus

| Method | Path | What it does | Bot |
|---|---|---|---|
| GET | `<teamspace>/prometheus/query` | One PromQL query: `?query=…&time=…`. Narrowed to the devices in scope. | `read` |
| GET | `<teamspace>/prometheus/query_range` | A range query: `?query=…&start=…&end=…&step=…`. | `read` |

Metrics from a device report in the device's unit: `node_cpu_usage` is in millicores, and a metrics
document's field name on the wire is `metricId`.

## Configuration and overrides

| Method | Path | What it does | Bot |
|---|---|---|---|
| GET | `<teamspace>/config/<target>` | The configuration of a robot or device, by layer. | `read` |
| GET | `<teamspace>/config/<target>/resource` | One rendered resource: `?id=<Kind>/<namespace>/<name>`. | `read` |
| GET | `<teamspace>/config/<target>/promote` | The override values as they were typed, secrets included, ready to promote into git. | `creator` |
| PATCH | `<teamspace>/config/<target>/overrides/resource` | Change values of one resource. `?id=<Kind>/<namespace>/<name>` (required). | `creator` |
| PUT | `<teamspace>/config/<target>/overrides/resource` | Write the override of one resource. `?id=<Kind>/<namespace>/<name>` (required). | `creator` |
| DELETE | `<teamspace>/config/<target>/overrides/resource` | Remove the override of one resource. `?id=<Kind>/<namespace>/<name>` (required). | `creator` |
| PUT | `<teamspace>/config/<target>/overrides/application` | Pin one application to another revision. | `creator` |
| DELETE | `<teamspace>/config/<target>/overrides/application` | Remove that pin: `?repo_url=<repo>` (required) `&path=<path>`. | `creator` |
| PATCH | `<teamspace>/config/<target>/overrides/application/off` | Turn every resource of one application off or on, in one write: `?repo_url=<repo>` (required) `&path=<path>`, body `{"off":true}` or `{"off":false}`. Off deletes what the items own. When data would be deleted, the platform answers 409 and asks for `confirm=<text>`, which only a person gives. Use `tnctl config off <robot> --app <path>` instead; for one resource, `tnctl config off <robot> <resource>`. | `creator` |
| DELETE | `<teamspace>/config/<target>/overrides` | Reset every override at once. | `never` |
| GET | `<teamspace>/local-kinds` | The kinds a local item may have. | `read` |
| GET | `<teamspace>/local-kinds/schema` | The JSON schema the platform checks one kind with: `?apiVersion=<apiVersion>&kind=<Kind>`. | `read` |
| GET | `<teamspace>/config/<target>/local` | One local item as YAML (`.manifest`), secret values replaced by `<hidden>`: `?id=<Kind>/<namespace>/<name>` (required). `tnctl config get <robot>` lists them all. | `read` |
| PUT | `<teamspace>/config/<target>/local` | Add or replace a local item. A bot is held to a kind list and a pod-security baseline. | `creator` |
| DELETE | `<teamspace>/config/<target>/local` | Remove a local item: `?id=<Kind>/<namespace>/<name>` (required). | `creator` |

`GET …/promote` returns secrets as typed. Treat its answer as sensitive and never copy it elsewhere.

## Connect access

| Method | Path | What it does | Bot |
|---|---|---|---|
| GET | `<teamspace>/access` | The Connect access summary of the teamspace. | `read` |
| GET | `<teamspace>/robots/<robot>/access` | One robot's access entries: the Shell and apps, and who may open them. | `read` |
| POST | `<teamspace>/robots/<robot>/access/<id>/enable` | Turn one access entry on, the Shell included. | `never` |
| DELETE | `<teamspace>/robots/<robot>/access/<id>` | Turn one access entry off. | `never` |

## Inspection

These read a robot through its Shell. Send `?enable=false`; better, use `tnctl inspect`, which always
does. The `logs` route takes `?container=`, `?previous=`, `?tail=` (1–2000) and `?since=` (for example
`10m`, at most `168h`).

| Method | Path | What it does | Bot |
|---|---|---|---|
| GET | `<teamspace>/robots/<robot>/inspect/pods/<podNamespace>/<pod>` | One pod: phase, containers, recent events. | `creator` |
| GET | `<teamspace>/robots/<robot>/inspect/pods/<podNamespace>/<pod>/logs` | The log of one pod. | `creator` |
| GET | `<teamspace>/robots/<robot>/inspect/releases/<releaseNamespace>/<release>` | An installed Helm release and its objects. | `creator` |

## Streaming device messages

| Method | Path | What it does | Bot |
|---|---|---|---|
| GET | `<teamspace>/mqtt/stream` | A server-sent stream of the teamspace's device topics: `?topic=…`, 1–8 topics of `/devices/<device\|+>/state/#` or `/events/#`, or a concrete topic below them. | `read` |

Use `tnctl mqtt stream` rather than this route: it parses the stream, applies the topic rules before
sending anything, and prints one message per line.
