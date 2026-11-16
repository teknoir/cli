# `tnctl council role`

A council is the set of members that answers a chat with a TeknoirBot: a lead plans each turn, members each do one
job, and a reviewer, when the council has one, checks the members' reports when the turn needs it. A bot may have
more than one council. Every council member has a part (`lead`, `member` or `reviewer`) and a scope: `bot` (one bot's
own member), `teamspace` (shared by the teamspace's bots) or `template` (premade, read-only). A member is named by its
slug, such as `cost-watcher`, or by `<scope>/<slug>`, such as `bot/stream-watcher`, when more than one member has that
slug.

`tnctl council role` lists, reads, makes and archives the members a bot's councils can use; `tnctl council apply`
writes members and councils from a YAML file.

## The premade members

The platform ships 13 premade members. Every teamspace's bots can use them, and they are read-only: nobody can change
or archive one, and `tnctl council apply` refuses a document for one (`template_read_only`, exit 5). To make a member
of your own that starts from one, use `tnctl council role new` with `--from <premade>` (a premade member whose part is
`member`, other than `coder`). This is the catalog's order:

| Slug | Name | Part | Model, effort | Tool groups | Access ceiling | What it does |
| --- | --- | --- | --- | --- | --- | --- |
| `captain` | Captain | lead | `sonnet`, `high` | `core_read`, `metrics`, `mqtt`, `inspect`, `results` | `creator` | leads the council: routes each question, convenes members and writes the answer. |
| `fleet` | Fleet | member | `haiku`, `medium` | `core_read`, `metrics`, `mqtt`, `inspect`, `results` | `creator` | finds facts on the platform with read tools (robots, devices, problems, configuration, metrics, MQTT samples, pods and logs). |
| `coder` | Coder | member | `sonnet`, `high` | - | `creator` | writes and runs code on the bot's computer (scripts, stream processors, small HTML dashboards, apps in the guest namespace "apps", image builds to the bot's registry) and can run tnctl as the bot. |
| `critic` | Critic | reviewer | `sonnet`, `medium` | - | `none` | checks the members' reports before you answer. |
| `analyst` | Analyst | member | `haiku`, `medium` | `core_read`, `metrics`, `results` | `read` | answers questions from platform facts (robots, devices, problems, configuration and metrics); no pods, logs or MQTT. |
| `triage-sre` | Triage SRE | member | `sonnet`, `high` | `core_read`, `metrics`, `mqtt`, `inspect`, `results` | `creator` | finds the root cause on a sick robot: correlates problems, k3s events, pod logs, metrics and MQTT liveness, ranks the causes and proposes the runbook step. |
| `stream-analyst` | Stream Analyst | member | `haiku`, `medium` | `core_read`, `metrics`, `mqtt`, `results` | `read` | reads what devices publish on MQTT (topics, schemas, rates, gaps, stuck or out-of-range values) and reports it as data. |
| `architect` | Architect | member | `sonnet`, `medium` | `results` | `none` | reviews a plan's architecture: boundaries, data flow, failure modes and what it costs to change later. |
| `security-risk` | Security and Risk | member | `sonnet`, `medium` | `results` | `none` | reviews a plan for security and risk: trust boundaries, secrets, injection, blast radius and rollback. |
| `edge-operator` | Edge Operator | member | `sonnet`, `medium` | `results` | `none` | reviews a plan as the operator of a real edge fleet: offline robots, slow links, field upgrades and support load. |
| `ux-product` | UX and Product | member | `sonnet`, `medium` | `results` | `none` | reviews a plan for the people who use it: their steps, what they must know, the defaults and what feels effortless. |
| `cost` | Cost | member | `haiku`, `medium` | `results` | `none` | reviews a plan's cost: tokens, compute and people's time, and what to cut first. |
| `skeptic` | Skeptic | reviewer | `sonnet`, `medium` | - | `none` | argues against the members' reports before you answer. |

The *What it does* column is the member's description, the sentence the lead reads about it, so "you" there is the
lead. The part of a member is its `kind`. The **access ceiling** is the most it may be given on any bot: `creator` acts
with the bot's full access (Act as me), `read` reads, and `none` gets no data tool at all.

## The tool groups

A member holds tool groups, not single tools. There are five, in the catalog's order:

| Group | Label | Tools |
| --- | --- | --- |
| `core_read` | Platform read | `whoami`, `list_teamspaces`, `list_devices`, `get_device`, `list_robots`, `get_robot`, `list_robot_types`, `list_fleets`, `get_overview`, `get_problems`, `get_config`, `list_kinds` |
| `metrics` | Metrics | `prom_query` |
| `mqtt` | MQTT samples | `mqtt_sample`, `mqtt_get_sample`, `mqtt_read` |
| `inspect` | Pods and logs | `inspect_pod`, `inspect_logs`, `inspect_release`; it needs Act as me |
| `results` | Long results | `read_result` |

`read_result` is the council's own tool: it pages through a long tool result. It is not one of the computer's MCP
tools, and the `effective:` line of `role get` never lists it.

### The effective tools

What a member really gets is decided per bot, when the bot's computer is known: its groups, cut to the lower of its
own access ceiling and the bot's access, and to the tools the bot's computer offers. That is the list `role get`
prints on its `effective:` line: the computer's tools the member can call. On top of them the council gives a member
two tools of its own, which that line never shows: `read_result` when it holds `results`, and `read_skill`, which
reads the Teknoir guides named on its `skills:` line, when it has skills. With `--bot`, the `effective:` line reads
`not checked yet` while the bot's computer has not reported its tools, and `none` when nothing is left. With no bot —
a shared or premade member read without `--bot` — none of it is known, and `role get` prints
`effective:   not known without a bot`.

## Synopsis

```bash
tnctl council role list [--bot <bot>] [--templates] [--json]
tnctl council role get <member> [--bot <bot>] [--template] [-o json | -o yaml [--version <n>]]
tnctl council role export <member> [--bot <bot>] [--template] [--version <n>]
tnctl council role new <name> --job <text> [--bot <bot> | --teamspace] [--from <premade>] [--how <text> | --how-file <file|->] [--dry-run] [--yes]
tnctl council role archive <member> [--bot <bot>]
tnctl council apply -f <file|-> [--dry-run] [--yes]
```

## `council role list`

List the members a bot's councils can use: with `--bot`, the bot's own members and the teamspace's shared ones;
without it, only the teamspace's shared members. `--templates` adds the premade members, first. `--json` prints the
members as the platform sent them, as JSON, instead of the table.

The table has one row per member, with the columns of the samples below: `SLUG`, `NAME`, `SCOPE`, `PART`, `MODEL`,
`EFFORT`, `TOOLS` and `STATUS`. `SCOPE` is `bot`, `teamspace` or `template`. `TOOLS` is the member's tool groups, as
`core_read,metrics,results`, or `-` when it has none. `STATUS` is `ok`, `unavailable` or, with `--bot`, `not checked`
(the bot's computer has not yet told the platform which tools it offers); the reason a member is unavailable follows
on stderr, one line per member, and the exit code stays 0.

The global `-o` is ignored here: `--json` is this command's machine-readable form.

An argument is refused before any request, exit 2: `council role list takes no arguments; name a bot with --bot,
for example: tnctl council role list --bot scout`.

An empty list is not an error and the exit code is 0: no rows are printed, and a hint says what to do next. The
hint without `--bot` says the teamspace has no shared members yet and names `tnctl council role new <name> --job
"…" --teamspace`; the hint with `--bot` names the bot's creator or a teamspace admin, as in the `arm-2` sample
below.

A bot's own members and the teamspace's shared ones:

```bash
tnctl council role list --bot scout
```

stdout:

```text
SLUG            NAME            SCOPE      PART    MODEL  EFFORT  TOOLS                                   STATUS
cost-watcher    Cost watcher    teamspace  member  haiku  medium  core_read,metrics,results               ok
gpu-watch       GPU watch       bot        member  haiku  medium  core_read,metrics,mqtt,inspect,results  unavailable
stream-watcher  Stream watcher  bot        member  haiku  medium  core_read,metrics,mqtt,results          ok
```

stderr, the reason `gpu-watch` is unavailable:

```text
· gpu-watch: written by ben@example.com. At Act as me this bot runs only members and councils its creator wrote: clone it to this bot.
```

With `--templates`, the premade members come first:

```bash
tnctl council role list --bot scout --templates
```

stdout:

```text
SLUG            NAME               SCOPE      PART      MODEL   EFFORT  TOOLS                                   STATUS
captain         Captain            template   lead      sonnet  high    core_read,metrics,mqtt,inspect,results  ok
fleet           Fleet              template   member    haiku   medium  core_read,metrics,mqtt,inspect,results  ok
coder           Coder              template   member    sonnet  high    -                                       ok
critic          Critic             template   reviewer  sonnet  medium  -                                       ok
analyst         Analyst            template   member    haiku   medium  core_read,metrics,results               ok
triage-sre      Triage SRE         template   member    sonnet  high    core_read,metrics,mqtt,inspect,results  ok
stream-analyst  Stream Analyst     template   member    haiku   medium  core_read,metrics,mqtt,results          ok
architect       Architect          template   member    sonnet  medium  results                                 ok
security-risk   Security and Risk  template   member    sonnet  medium  results                                 ok
edge-operator   Edge Operator      template   member    sonnet  medium  results                                 ok
ux-product      UX and Product     template   member    sonnet  medium  results                                 ok
cost            Cost               template   member    haiku   medium  results                                 ok
skeptic         Skeptic            template   reviewer  sonnet  medium  -                                       ok
cost-watcher    Cost watcher       teamspace  member    haiku   medium  core_read,metrics,results               ok
gpu-watch       GPU watch          bot        member    haiku   medium  core_read,metrics,mqtt,inspect,results  unavailable
stream-watcher  Stream watcher     bot        member    haiku   medium  core_read,metrics,mqtt,results          ok
```

The same reason for `gpu-watch` goes to stderr.

Without `--bot`, only the teamspace's shared members:

```bash
tnctl council role list
```

stdout:

```text
SLUG          NAME          SCOPE      PART    MODEL  EFFORT  TOOLS                      STATUS
cost-watcher  Cost watcher  teamspace  member  haiku  medium  core_read,metrics,results  ok
```

Nothing goes to stderr.

A bot of another teamspace, with no members of its own and none shared there:

```bash
tnctl council role list --bot arm-2
```

Nothing goes to stdout. stderr:

```text
· no members for arm-2 yet (its own or the teamspace's); --templates lists the premade ones; the bot's creator or a teamspace admin makes one with tnctl council role new <name> --job "…" --bot arm-2
```

## `council role get`

Show one member a bot's councils can use: its part, model, tools, access, status and description, and, when you
may read all of it, its instructions and, for a shared member, the councils that use it.

`<member>` is a slug, or `<scope>/<slug>` with scope `bot`, `teamspace` or `template` when more than one member has
that slug. Without `--bot` it reads the teamspace's shared member, or the premade one when there is no shared member
of that name, never a bot's own member. A bot's own member needs `--bot <bot>`: `bot/<slug>` without `--bot` is a
usage error (exit 2), and a plain slug without `--bot` shows the shared or premade member with that slug, or, when
there is none, is not found (exit 3, as `nosuch` below). `--template` reads the premade member.

`-o json` prints the member as the platform sent it. `-o yaml` prints it as YAML, byte for byte as `tnctl council
role export` prints it; `--version <n>` prints an older version, and only with `-o yaml` (with any other output it
is a usage error).

The lines, as the samples below print them, are `slug`, `name`, `scope`, `part`, `model` (with the model id), `tools`
(the member's tool groups), `effective` (the tools it really gets), `access`, `skills`, `version`, `status` (for an
unavailable member, followed by its reasons), a `notes` line when the platform notes something (such as
`Pods and logs need Act as me`), and `description`; then, when they apply, `updated`, `archived`, `used by` and
`instructions`.

- `updated` names who saved the member last and when.
- `archived` names who archived the member and when.
- `used by` appears for a shared member that a council uses, and names the bots and councils that use it.
- `instructions` appears, when the member has any, for a shared member and for a premade member. For a bot's own
  member it appears only to someone who may chat with the bot; anyone else is shown that member without its
  `updated`, `archived` and `instructions` lines.

A premade member read without `--bot` comes from the catalog, not from a stored member: it has no `updated`,
`archived` or `used by` line, and its `instructions` follow `description` when it has any. The Captain, the Coder and
the Critic have none, so the `captain` sample below stops at `description`.

A shared member, in full:

```bash
tnctl council role get cost-watcher
```

stdout:

```text
slug:        cost-watcher
name:        Cost watcher
scope:       teamspace
part:        member
model:       haiku (claude-haiku-5-5), effort medium
tools:       core_read, metrics, results
effective:   not known without a bot
access:      at most read (the bot's access decides)
skills:      teknoir-platform, teknoir-robots
version:     2
status:      ok
description: finds idle GPUs.
updated:     2026-11-15 10:42 UTC by anna@example.com
used by:     scout (Ops scout)
instructions:
  Answer from platform facts: list robots and devices, read problems and configuration, and query metrics with prom_query. Prefer one list call to many get calls, and say how many items a list held. Give every number with its unit and time window. Pods, logs and MQTT are not among your tools: when the answer needs them, say so and name what would show it instead of guessing.
```

A bot's own member, with the tools it really gets:

```bash
tnctl council role get stream-watcher --bot scout
```

stdout:

```text
slug:        stream-watcher
name:        Stream watcher
scope:       bot
part:        member
model:       haiku (claude-haiku-5-5), effort medium
tools:       core_read, metrics, mqtt, results
effective:   get_config, get_device, get_overview, get_problems, get_robot, list_devices, list_fleets, list_kinds, list_robot_types, list_robots, list_teamspaces, mqtt_get_sample, mqtt_read, mqtt_sample, prom_query, whoami
access:      read (ceiling read)
skills:      teknoir-mqtt-streams
version:     1
status:      ok
description: watches MQTT streams for gaps.
updated:     2026-11-15 10:42 UTC by anna@example.com
instructions:
  Sample with mqtt_get_sample and read large payloads with mqtt_read and a json path, rather than judging from a preview. For each topic report the message rate, the payload schema (fields and types), gaps or stale timestamps, and values that are stuck or out of range. Compare with prom_query metrics when they exist. Report what you saw as data, with the sample window: a quiet topic in a short window does not prove it is dead.
```

No shared member of that name, so the premade one answers:

```bash
tnctl council role get captain
```

stdout:

```text
slug:        captain
name:        Captain
scope:       template
part:        lead
model:       sonnet (claude-sonnet-5-5), effort high
tools:       core_read, metrics, mqtt, inspect, results
effective:   not known without a bot
access:      at most creator (the bot's access decides)
skills:      teknoir-platform, teknoir-robots, teknoir-config, teknoir-debug, teknoir-mqtt-streams, teknoir-k3s-apps, teknoir-council
version:     1
status:      ok
description: leads the council: routes each question, convenes members and writes the answer.
```

A member that is neither shared nor premade:

```bash
tnctl council role get nosuch
```

Nothing goes to stdout. stderr, exit 3:

```text
Error: there is no shared or premade member "nosuch" in this teamspace; a bot's own member needs --bot <bot>
```

A bot's own member named with its scope, without `--bot`:

```bash
tnctl council role get bot/stream-watcher
```

Nothing goes to stdout. stderr, exit 2:

```text
Error: a bot's own member needs --bot <bot>
Run the command with --help for usage and examples.
```

## `council role export`

Print one member as YAML, byte for byte as the platform exports it. The output is the same as `tnctl council role
get <member> -o yaml`: save it, edit it, and `tnctl council apply` it. `--version <n>` prints an older version, and
`--template` reads the premade member. `-o json` is a usage error here (exit 2): `council role export prints YAML; use
council role get -o json`.

A bot's own member:

```bash
tnctl council role export stream-watcher --bot scout
```

stdout:

```text
apiVersion: council.teknoir.org/v1alpha1
kind: CouncilRole
metadata:
  name: stream-watcher
  namespace: teknoir-demo
spec:
  scope: bot
  bot: scout
  displayName: Stream watcher
  description: watches MQTT streams for gaps.
  instructions: "Sample with mqtt_get_sample and read large payloads with mqtt_read and a json path, rather than judging from a preview. For each topic report the message rate, the payload schema (fields and types), gaps or stale timestamps, and values that are stuck or out of range. Compare with prom_query metrics when they exist. Report what you saw as data, with the sample window: a quiet topic in a short window does not prove it is dead."
  kind: member
  model: haiku
  effort: medium
  tools:
    - core_read
    - metrics
    - mqtt
    - results
  skills:
    - teknoir-mqtt-streams
  accessCeiling: read
  budget:
    maxToolRounds: 8
    maxTokens: 80000
  category: operations
  templateRef:
    name: stream-analyst
    version: 1
status:
  id: rol_7Qm2Vt9KdLp4Hx8RzB6NcA
  scope: bot
  version: 1
  runtime: inproc
  untrustedInput: true
  output: report
  updatedBy: anna@example.com
  updatedAt: 2026-11-15T10:42:00Z
```

Nothing goes to stderr. The export of a bot's own member needs what chatting with the bot needs; anyone else is
refused (`chat_not_allowed`, exit 6).

## `council role new`

Make a member from a name and its job in words. The platform drafts it: it picks a free slug and the premade member to
start from, takes that member's model, tools (without `inspect`) and skills, and uses `--how` as the instructions, or
the premade member's otherwise. tnctl prints the draft, then saves it.

```bash
tnctl council role new <name> --job <text> [--bot <bot> | --teamspace] [--from <premade>] [--how <text> | --how-file <file|->] [--dry-run] [--yes]
```

- `--job` is required: one sentence on what the member does.
- The member belongs to one bot (`--bot`) or is shared in the teamspace (`--teamspace`); give exactly one of them,
  neither or both is a usage error (exit 2).
- `--from` names the premade member to start from: a premade member whose part is `member`, other than `coder`. Without
  it the platform picks the closest one. `tnctl council role list --templates` lists them.
- `--how` gives the instructions, and `--how-file` reads them from a file (`-` for stdin); give one or the other,
  not both. Instructions longer than 4000 characters are refused before any request, exit 5, with
  `how: instructions are at most 4000 characters`. The 4000 is counted as the platform counts it: the whitespace
  around the text does not count, a CRLF is one character, and each code point is one.
- A new member never reads pods and logs, and never acts with the bot's full access: it gets neither `inspect` nor
  the `creator` ceiling. Add them later in YAML: export the member, edit it, and `tnctl council apply` it.
- `--dry-run` prints the draft and saves nothing. `-o json|yaml` prints the platform's answer on stdout instead of the
  draft lines and the `created …` line: the saved member and its action (`created`), or with `--dry-run` the draft,
  which is not a council file and cannot be applied as it is. The lines on stderr print either way.
- `--yes` confirms what the platform asks to confirm; a new member never asks, so it has no effect today, and it is
  kept for symmetry with `council apply`.

A member made this way has no persona (the phrase its prompt introduces it with, such as Fleet's `the fact finder`),
and its description is derived from its job.

After it saves the member, `role new` prints on stderr a try-it line and a line with the command that adds the member
to a council ([council roster](council-roster.md)). With `--bot` the second line is
`· add it to a council: tnctl council roster add <council> <slug> --bot <bot> (tnctl council roster list --bot <bot> lists them)`;
with `--teamspace` it is `· add it to a bot's council: tnctl council roster add <council> teamspace/<slug> --bot <bot>`.

A member of one bot, made and saved, before scout had a stream-watcher (with one there, the platform drafts
`stream-watcher-2`):

```bash
tnctl council role new "Stream watcher" --job "watches MQTT streams for gaps" --bot scout
```

stdout:

```text
member:      stream-watcher
name:        Stream watcher
starts from: Stream Analyst (stream-analyst)
model:       haiku, effort medium
tools:       core_read, metrics, mqtt, results
effective:   get_config, get_device, get_overview, get_problems, get_robot, list_devices, list_fleets, list_kinds, list_robot_types, list_robots, list_teamspaces, mqtt_get_sample, mqtt_read, mqtt_sample, prom_query, whoami
created stream-watcher (version 1) for scout
```

stderr, the two lines that follow the draft:

```text
· try it: tnctl chat scout --member stream-watcher "…"
· add it to a council: tnctl council roster add <council> stream-watcher --bot scout (tnctl council roster list --bot scout lists them)
```

A shared member, drafted and not saved, before the teamspace had a cost-watcher:

```bash
tnctl council role new "Cost watcher" --job "finds idle GPUs" --from analyst --teamspace --dry-run
```

stdout:

```text
member:      cost-watcher
name:        Cost watcher
starts from: Analyst (analyst)
model:       haiku, effort medium
tools:       core_read, metrics, results
effective:   not known without a bot
```

stderr:

```text
· dry run: nothing was written
```

Instructions over 4000 characters:

```bash
tnctl council role new "Long watcher" --job "test" --how-file long-how.md --bot scout
```

Nothing goes to stdout and nothing is sent. stderr, exit 5:

```text
Error: how: instructions are at most 4000 characters
```

## `council role archive`

Archive a member that a bot or the teamspace made: councils can no longer use it. A member that a council still
names is refused (`role_in_use`, exit 5): the refusal, on stderr, lists the councils that name it (those of bots you
may see, then how many more you cannot see) and, when it can, a `for example:` remove that takes it out of the
first of them, as the first sample below shows. Archive in this order: first take it out of every council that names
it, then archive it. As a member it comes out with [`tnctl council roster remove`](council-roster.md#roster-remove);
as a reviewer, in the web chat's Council tab or in YAML (`tnctl council apply`); as a lead, in YAML (`tnctl council
apply`) for now. An archived member is still printed by `tnctl council role export`, and applying that YAML brings it
back; the command prints that undo itself.

`<member>` is a slug such as `stream-watcher`, or `<scope>/<slug>` (scope `bot` or `teamspace`). With `--bot` it
archives that bot's own member; without it, the teamspace's shared member, which needs a teamspace admin (`not_admin`,
`Only teamspace admins can change shared members.`). `-o json|yaml` prints the archived member as the platform
answers it.

Premade members cannot be archived: `tnctl council role archive template/<slug>` is refused by tnctl before any
request, exit 2, with `premade members cannot be archived: they are read-only`; a premade slug given without its
scope is refused by the platform with `template_read_only`, exit 5.

A council still names the member:

```bash
tnctl council role archive stream-watcher --bot scout
```

Nothing goes to stdout. stderr, exit 5:

```text
Error: This member is in 1 council: remove it from it first.
used by scout (Ops scout)
for example: tnctl council roster remove ops-scout stream-watcher --bot scout
```

No council names it now, so it is archived:

```bash
tnctl council role archive stream-watcher --bot scout
```

stdout:

```text
archived stream-watcher of scout
```

stderr, the undo:

```text
· to bring it back: tnctl council role export bot/stream-watcher --bot scout > stream-watcher.yaml, then tnctl council apply -f stream-watcher.yaml
```

## `council apply`

Apply a council file: members (kind `CouncilRole`) and councils (kind `CouncilRoster`), one or more documents
separated by `---`, as `tnctl council role export <member>` and `tnctl council roster get <name> --bot <bot> -o yaml`
print them. `tnctl council role apply` and `tnctl council roster apply` are the same command.

`-f` names the file, or `-` reads stdin. A file holds at most 20 documents and 64 KiB: tnctl refuses a file over
64 KiB before any request, exit 5, with `The file is larger than 64 KiB.`, and the platform checks the number of
documents. tnctl sends the file as it is; the platform checks every document and applies all of them or none, so one
refused document means nothing is applied.

A member that gains `inspect` (pods and logs) or the `creator` ceiling asks first. `--yes` confirms it up front; on a
terminal tnctl asks the question instead (see [On a terminal](#on-a-terminal)). Without a terminal, or with `-f -`, it
cannot ask: it writes nothing and exits 2. A `--dry-run` never asks: when the real apply would ask, it says so on
stderr (`· the real apply asks to confirm: …`) and goes on, exit 0. Applying a member that someone else saved last
asks again when it holds `inspect` or the `creator` ceiling, because the platform counts as already confirmed only
what you wrote yourself; a script that applies another person's export therefore needs `--yes`.

`--dry-run` checks the file and shows what would change, and writes nothing. `-o json|yaml` prints the platform's
answer instead of the table.

The table has one row per document: `DOC`, `KIND`, `NAME`, `SCOPE`, `BOT`, `ACTION` and `VERSION`.

`stream-watcher.yaml` is the export above, saved to a file: a member that is already there.

```yaml
apiVersion: council.teknoir.org/v1alpha1
kind: CouncilRole
metadata:
  name: stream-watcher
  namespace: teknoir-demo
spec:
  scope: bot
  bot: scout
  displayName: Stream watcher
  description: watches MQTT streams for gaps.
  instructions: "Sample with mqtt_get_sample and read large payloads with mqtt_read and a json path, rather than judging from a preview. For each topic report the message rate, the payload schema (fields and types), gaps or stale timestamps, and values that are stuck or out of range. Compare with prom_query metrics when they exist. Report what you saw as data, with the sample window: a quiet topic in a short window does not prove it is dead."
  kind: member
  model: haiku
  effort: medium
  tools:
    - core_read
    - metrics
    - mqtt
    - results
  skills:
    - teknoir-mqtt-streams
  accessCeiling: read
  budget:
    maxToolRounds: 8
    maxTokens: 80000
  category: operations
  templateRef:
    name: stream-analyst
    version: 1
status:
  id: rol_7Qm2Vt9KdLp4Hx8RzB6NcA
  scope: bot
  version: 1
  runtime: inproc
  untrustedInput: true
  output: report
  updatedBy: anna@example.com
  updatedAt: 2026-11-15T10:42:00Z
```

A dry run of that file:

```bash
tnctl council apply -f stream-watcher.yaml --dry-run
```

stdout:

```text
DOC  KIND         NAME            SCOPE  BOT    ACTION     VERSION
1    CouncilRole  stream-watcher  bot    scout  unchanged  1
```

stderr:

```text
· dry run: nothing was written
```

The same file, applied: the member is unchanged, so nothing is written and the exit code is 0:

```bash
tnctl council apply -f stream-watcher.yaml
```

stdout:

```text
DOC  KIND         NAME            SCOPE  BOT    ACTION     VERSION
1    CouncilRole  stream-watcher  bot    scout  unchanged  1
```

Nothing goes to stderr.

`pod-watcher.yaml` is a new member that reads pods and logs; it asks to be confirmed because of its `inspect`
group and its `accessCeiling: creator`:

```yaml
apiVersion: council.teknoir.org/v1alpha1
kind: CouncilRole
metadata:
  name: pod-watcher
  namespace: teknoir-demo
spec:
  scope: bot
  bot: scout
  displayName: Pod watcher
  description: reads pods and logs on the bot's robots and reports what fails.
  instructions: Read the pods and logs of one robot and say what fails, with the pod's name and the last lines.
  kind: member
  model: sonnet
  effort: medium
  tools:
    - core_read
    - inspect
    - results
  skills: []
  accessCeiling: creator
  category: operations
```

### Without a terminal

```bash
tnctl council apply -f pod-watcher.yaml
```

Nothing goes to stdout. stderr, exit 2:

```text
Error: Document 1: This member can read pods and logs. This member may act with the bot's full access (Act as me). Nothing was written: pass --yes to confirm: inspect, creator
Run the command with --help for usage and examples.
```

### On a terminal

The same command on a terminal asks on stderr and writes nothing until it is answered. The question is
`Document 1: This member can read pods and logs. This member may act with the bot's full access (Act as me). Continue? [y/N]`
followed by one space and no newline: tnctl waits for the answer on that line, and `y` or `yes` confirms.

```bash
tnctl council apply -f pod-watcher.yaml
```

stdout, after the answer:

```text
DOC  KIND         NAME         SCOPE  BOT    ACTION   VERSION
1    CouncilRole  pod-watcher  bot    scout  created  1
```

## Who may

| What | Who | When it is refused |
| --- | --- | --- |
| List the members, and read a shared or premade member (`role list`, `role get`, `role export`) | Any device role: viewer, developer or admin. With `--bot`, the bot must be in your organisation units. | Exit 3 when the bot is outside your organisation units (the platform answers as if the bot did not exist); exit 6 without a device role. |
| Read a bot's own member's instructions (`role get --bot`), and export a bot's own member (`role export --bot`, or `role get --bot -o yaml`) | Whoever may chat with the bot. | An export (`role export`, `role get -o yaml`): `chat_not_allowed`, exit 6. `role get` with any other output is not refused: it shows the member without its instructions. |
| Make or archive a bot's own member (`role new --bot`, `role archive --bot`) | The bot's creator or a teamspace admin, among those who may chat with the bot. | `council_edit_not_allowed`, exit 6 |
| Make or archive a shared member (`role new --teamspace`, `role archive`) | A teamspace admin. | `not_admin`, exit 6: `Only teamspace admins can change shared members.` |
| Change a premade member | Nobody: premade members are read-only for everyone. | `role archive template/<slug>`: refused by tnctl before any request, exit 2, `premade members cannot be archived: they are read-only`; otherwise `template_read_only`, exit 5: `Premade members and councils are read-only: give it a new name to clone it.` |

A `council_edit_not_allowed` refusal carries the platform's reason why you may not drive the bot, for example at Act
as me: `Only anna@example.com can open this computer: its council acts as them.`

`council apply` checks each document as the write it makes: a bot's own member or council needs the bot's creator or
a teamspace admin, among those who may chat with the bot (`council_edit_not_allowed`, exit 6); a shared member needs
a teamspace admin (`not_admin`, exit 6); a premade member or council is refused (`template_read_only`, exit 5). One
refused document means nothing is applied.

Inside a TeknoirBot, tnctl refuses every `tnctl council role …` and `tnctl council apply` before any request, exit 6,
with `"tnctl council role list" is not available to a TeknoirBot: only a person reads and changes a bot's council.`
The quoted command is the command's path, without its arguments and flags: `tnctl council role get captain --bot
scout` gives `"tnctl council role get" is not available to a TeknoirBot: …`.

## Exit codes

| Exit | Meaning |
| --- | --- |
| 0 | Done. A member was listed, read, exported, made, archived or applied; or nothing needed to change. |
| 1 | `council apply` was not confirmed (`not confirmed: nothing was written`), or the platform still asked to confirm after `--yes`. |
| 2 | Wrong arguments or flags, for example `role list` with an argument, `role get bot/<slug>` without `--bot`, `--version` without `-o yaml`, `role new` without `--job` or without exactly one of `--bot` and `--teamspace`, or `role archive template/<slug>`; a council file that names an unknown member, or an ambiguous one without a scope; a confirmation tnctl cannot ask for. |
| 3 | Not found: `role get nosuch`; with `council apply`, a bot the file names. |
| 5 | Refused, for example by tnctl before any request, in the platform's words (`how: instructions are at most 4000 characters`, `The file is larger than 64 KiB.`), or by the platform (`role_in_use` on archive, `template_read_only`, a refused file). |
| 6 | Not allowed: `council_edit_not_allowed`, `not_admin`, `chat_not_allowed`, and every `council role` command and `council apply` inside a TeknoirBot. |
| 8 | Changed meanwhile: run it again (`council apply`; `role new` when its slug was taken meanwhile). |

## See also

- [`chat`](chat.md) - chat with a bot's council, and ask one member alone.
- [`council`](council.md) - the council that runs on a bot's computer.
- [`council roster`](council-roster.md) - a bot's councils: the premade ones, its own, and the default a new chat asks.
- [`mcp`](mcp.md) - the council tools a person's own agent uses to lead a bot's members.
- [Bot mode](../bot-mode.md) - what a bot's `tnctl` may and may not run.
