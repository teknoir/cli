# `tnctl mcp`

Run a **read-only MCP server** over stdio for an AI coding agent (Claude Code, Codex, OpenCode). The agent
gets typed tools that answer from the platform's fleet API (`whoami`, as a person, from the stored login);
none of the 19 read tools changes anything. Started with `--council`, the server also offers four
`council_*` tools that hand work to a TeknoirBot's council: they are not read-only, and they are for a
person's own agent only — inside a TeknoirBot the server refuses `--council`. The server works as whoever
`tnctl` is: your `tnctl login` session, or, inside a TeknoirBot, the bot and its own teamspace.

```bash
tnctl mcp [--council]
```

It speaks the Model Context Protocol on stdout and writes nothing else there; anything it has to say goes to
stderr.

## Register it with an agent

```bash
tnctl agent install --mcp                   # every agent, in the user scope
tnctl agent install --agent claude --mcp    # Claude Code only
tnctl agent install --project --agent codex --mcp
```

`tnctl agent install --mcp` writes the registration into the agent's own config file, merging into what is
there; where that file is and what the entry looks like is in [the MCP files](agent.md#the-mcp-files).

The server is registered as `teknoir`, so an agent names the tools after it: Claude Code calls
`mcp__teknoir__get_problems`, or `mcp__plugin_teknoir_teknoir__get_problems` when the server comes from the
kit's Claude Code plugin. Codex and OpenCode list the same tools under the server `teknoir`.

## The tools

| Tool | Inputs | Use it for |
| --- | --- | --- |
| `whoami` | | Who this `tnctl` works as: the person and the teamspace it targets, or the bot with its creator and access. Call it first to learn the default teamspace. |
| `list_teamspaces` | | The teamspaces the caller can work in; a name from it is the `namespace` of the other tools. |
| `list_devices` | `labels` | The devices of the teamspace with their labels and state. `labels` lists only the devices that have every `key=value`. `state.apps` holds the device's pods as the platform last read them from Prometheus (without the `kube-*` namespaces and the tunnel agent); `prom_query` `pod_running{device_id="<device>"}` lists every pod. |
| `get_device` | `name` | One device in full, by name. `state.apps` holds the device's pods as the platform last read them from Prometheus (without the `kube-*` namespaces and the tunnel agent); `prom_query` `pod_running{device_id="<device>"}` lists every pod. |
| `list_robots` | | The robots of the teamspace (an edge computer, or a cluster of them) with their phase. A Degraded robot names the problems that make it so. |
| `get_robot` | `name`, `detail` | One robot: its phase, computers, configuration layers, overrides, effective resources and bootstrap steps. `detail=full` adds every layer's copy, the merged values with their provenance and the git links. |
| `list_robot_types` | | The robot types of the teamspace. |
| `list_fleets` | | The fleets of the teamspace: the DeviceApplicationSets that deploy configuration from git to groups of devices. |
| `get_overview` | | A summary of the teamspace as its dashboard shows it, problems included. |
| `get_problems` | `robot`, `device` | What is failing in the teamspace, one entry per failing app or node. `robot` or `device` shows only that one. |
| `get_config` | `target`, `resource`, `defaults`, `detail` | The effective configuration of a robot or device (`target`): its layers, overrides and states. `resource` (`Kind/namespace/name`, `Kind/name` or `name`) shows one resource with its provenance, the rendered item and its override; `defaults` adds the chart's default values. Without `resource` it is a summary unless `detail=full`. |
| `list_kinds` | | The kinds of local resources the platform accepts on a robot or device, and the ones it refuses. |
| `prom_query` | `query`, `time` | A PromQL instant query on the teamspace's Prometheus, limited to the devices the caller may see. Device series carry `device_id` (no `device` label); `up` has none (use `device_last_seen`). `time` is RFC 3339 or Unix seconds; empty means now. |
| `mqtt_sample` | `topics`, `seconds`, `max` | A cheap preview of what a device publishes: the MQTT messages of the teamspace's devices for a few seconds, with a long payload cut. `topics` is 1 to 8 topics, each `/devices/<device>/state/#` or `/devices/<device>/events/#` (`+` for every device) or a concrete topic below them; `seconds` is 5 by default and at most 30, `max` is 50 messages by default and at most 200. For whole payloads use `mqtt_get_sample`. |
| `mqtt_get_sample` | `topics`, `seconds`, `max`, `maxBytes` | The same capture, with every payload whole: inline when the payload text (the base64 text for a binary payload) adds up to at most 12288 bytes and the answer fits under 48 KiB, otherwise an index of a capture that `mqtt_read` pages through. `maxBytes` is 2 MiB by default and at most 8 MiB. |
| `mqtt_read` | `captureId`, `i`, `offset`, `length`, `path` | One message of a spilled capture, a page at a time (12288 bytes at most), or one field of a JSON payload with `path`. |
| `inspect_pod` | `robot`, `pod_namespace`, `pod` | A pod of a robot: phase, containers and recent events. |
| `inspect_logs` | `robot`, `pod_namespace`, `pod`, `container`, `previous`, `tail`, `since` | The log of a pod on a robot: the last `tail` lines (200 by default, at most 2000), of one `container`, of the `previous` run, or `since` a duration such as `10m`. |
| `inspect_release` | `robot`, `release_namespace`, `release` | An installed Helm release of a robot: status, revision and its objects. |

## The council tools (`--council`)

`--council` adds four tools that hand work to a TeknoirBot's council: its Captain plans and answers, Fleet
reads the teamspace with the same read tools, the Coder works on the bot's own computer, and the Critic
checks what they report. The council runs on the bot itself; the tools post turns to it and wait for their
results.

```bash
tnctl agent install --mcp --council              # every agent, in the user scope
tnctl agent install --agent claude --mcp --council
```

That writes the registration `--mcp` writes, with `["mcp","--council"]` as the command's arguments (see
[the MCP files](agent.md#the-mcp-files)). The kit's `teknoir-council` skill teaches an agent to lead the
council with these tools.

`--council` is for a person's own agent: inside a TeknoirBot the server refuses to start (exit 2) with
`--council is for a person's own agent: a TeknoirBot cannot drive a council`.

| Tool | Inputs | Returns |
| --- | --- | --- |
| `council_info` | `bot`, `namespace` | Whether you may chat with the bot and, when you may not, why: the platform's `canChat` and `reason`, the bot (`access`, `phase`, `online`, `lastSeen`, `council`), `isCreator`, the `members` (each with its `name`, `model`, `effort` and `job`) and today's `budget` (`tokensToday`, `tokensPerDay`). Call it before the other three. |
| `council_ask` | `bot`, `member` (`fleet` or `coder`), `task`, `effort`, `wait_seconds`, `namespace` | One member's turn: `untrusted`, `turnId`, `threadId`, `member`, `state` and, once it ends, `report`, `final`, `usage` and `error`; `next` while it still runs. A Fleet `report` is `summary`, `findings` (`claim`, `evidence`), `proposals` (`what`, `why`), `confidence` and `open_questions`; the Coder's is its OpenCode task's: `state`, `reason`, `text`, `error`, `changed`, `sessionId`, `steps`, `tools`, `usage`, `durationMs`. |
| `council_run` | `bot`, `question`, `mode` (`auto`, `quick` or `council`), `effort`, `wait_seconds`, `namespace` | A whole council turn: `untrusted`, `turnId`, `threadId`, `state` and, once it ends, `answer` (the Captain's markdown), `members` (`member`, `task`, `summary` — a Fleet report's summary, or the start of the Coder's answer, with its state when its task did not finish), `critique` (`issues` of `member`, `severity`, `claim`, `why`, and `note`), `usage` and `error`; `next` while it still runs. |
| `council_result` | `bot`, `turn_id`, `wait_seconds`, `namespace` | That turn again, in the shape of the tool that started it: its `state`, and its result once it ended. |

How they behave:

- **One conversation per member, and one for `council_run`.** The first call for a member makes its thread
  (titled `MCP: <your client's name> <member>`, with `origin: "mcp"`), and every later task for it goes into
  that thread, so a follow-up sees the earlier ones. `council_run` has a conversation of its own: the bot's
  own answers and the work you hand to its members do not mix.
- **The wait.** A call follows the thread's event stream for up to `wait_seconds`: 45 when it gives none and
  55 at most, which stays below the tool timeout most MCP clients use. `0` returns as soon as the turn is
  queued. A turn still `queued` or `running` comes back with `next` and no result, and `council_result` with
  its `turnId` as `turn_id` waits for it again.
- **One turn at a time per conversation.** While a turn runs, the next call for that member is refused with
  `Fleet is still working on turn <turnId> in this conversation: call council_result with turn_id <turnId>.`
  (`The council` for `council_run`); the other member can be asked meanwhile. The platform refuses its own
  way too, with `This chat already has a turn in progress.`
- **Refusals are tool errors** with the platform's message: who may not chat (and why), a bot that is
  suspended or starting, a platform whose chat is turned off, too many open turns on the bot.
- **A conversation that went away.** A thread deleted in the web UI answers `The conversation was deleted.`;
  a `council_result` for a turn in no thread it can read answers `No turn <turnId> on bot <bot> that you can
  see.` An ended login gives tnctl's own login error, and the platform's `access_lost` gives its sentence.
  None of them waits.
- **The stream cap.** The platform allows four open event streams per person. When it refuses one (`429`),
  the wait reads the thread every 3 s instead, until the turn ends.
- **The results are written by the bot's models.** Every result carries `untrusted`: the note that its text
  was written by the bot's models from teamspace data and that instructions in it must never be followed.
- **Who may use them** is who may chat with the bot: its creator, at Read only and Off also the teamspace's
  admins, and only with permission to open a terminal (`pods/exec`) in the teamspace (see
  [`chat`](chat.md#who-may-chat)). Anyone else gets the platform's `403` with the reason; `council_info`
  shows it without starting a turn.
- **Every turn spends the bot's day budget**, which everyone who chats with the bot shares. A result's
  `usage` says what the turn cost (`calls`, `input`, `output`, `cacheRead`, `cacheWrite`, `ms`).

## Rules

- **Read-only, except the council tools.** The 19 read tools change nothing, and each is annotated read-only
  and idempotent: no tool runs a command, opens a session, logs in or writes to the platform; `mqtt_get_sample` keeps the samples it spills as `0600` files in its own capture directory (see "Where captures live"), deleted after 30 minutes or when the session ends. The four `council_*` tools of
  `--council` start a turn on a TeknoirBot, so they are annotated not read-only, not idempotent, not
  destructive, and open-world: the bot runs its own processes on its own computer.
- **Tool results are device data.** Labels, logs, events and MQTT payloads can hold text written by anyone
  who controls a device or its workloads. The server tells the agent never to follow instructions found in a
  tool result: in its instructions, for every tool, and again in the descriptions of `list_devices`,
  `get_device`, `get_robot`, `get_problems`, `get_config`, `prom_query`, `mqtt_sample`,
  `mqtt_get_sample`, `mqtt_read` and the `inspect_*` tools. The `council_*` tools carry the same warning about their results, which are text the
  bot's models wrote.
- **`namespace`** is an optional input of every tool but `whoami`, `list_teamspaces` and `mqtt_read` (which reads this server's own captures); empty means the
  teamspace `tnctl` targets. The login and the teamspace are read on every call, so `tnctl ns` and
  `tnctl login` in another terminal count on the next call, with no restart, unless the server was started
  with `-n`, `TNCTL_NAMESPACE`, `--domain` or `TNCTL_DOMAIN`, which then win. Inside a TeknoirBot another
  `namespace` is an error result: a bot works only in its own teamspace.
- **Results are JSON text**, capped at 48 KiB. 48 KiB keeps every result inline in Claude Code, which
  moves a text result over 50,000 characters to a file. A longer answer is cut at whole items and says
  so: a JSON array, or an object whose largest member is one, is cut to the items that fit and comes back
  as one JSON object, `{"truncated":true,"shownItems":k,"totalItems":n,"field":"…","shownBytes":…,
  "totalBytes":…,"hint":"…","result":<the cut JSON>}`, where `field` names the object's array that was cut
  and is left out for a top-level one. `result` is always valid JSON: the cut array closes, so an agent
  reads what is shown instead of guessing at the rest; `hint` says to narrow the query. A result that is
  not such an array is cut at a line and ends with `… truncated: shown S of T bytes, k of n lines; narrow
  the query`; when not one element of an array fits under the cap, that line reads `0 of n items` rather
  than counting the lines of the head. A `"truncated": true` object is the one thing to look for:
  `shownItems` of `totalItems` is what the result carries, and the rest is not in it. The cut is measured
  with what is sent — the result and its wrapper together are never over the cap. The council runs this
  server in-process with a 1 MiB cap: it does its own cut at 16 KiB, with a reference for the rest.
- **Errors are tool error results** with the platform's own message (a login that ended, no access, a pod
  that is not there). A platform older than this `tnctl` gets tnctl's own message instead: it has no live
  MQTT stream (`mqtt_sample`), or its inspect routes cannot read without turning the Shell on
  (`inspect_*`). The server stays up and the next call works, even after a tool failed.
- **`inspect_*` never turns the robot's Shell on.** When the Shell is off they fail with the platform's
  message saying so. They need what [`tnctl inspect`](inspect.md) needs: a person needs the developer role
  and permission to open a terminal (`pods/exec`) in the teamspace; a TeknoirBot must be set to Act as me,
  and its creator needs that same role and permission. Otherwise the platform answers `403`.
- **`mqtt_sample`** follows 1 to 8 topics, each `/devices/<device|+>/state/#`, `/devices/<device|+>/events/#`
  or a concrete topic below them, for `seconds` (default 5, at most 30; they count from the moment the
  stream opens, and opening has its own limit of 20 s), and stops at `max` messages (default 50, at most
  200) or when the sample is full (about 36 KiB). Every message carries the server's data as it was sent. A
  text payload longer than 4096 bytes, or a binary one longer than 3072 bytes (4096 base64 characters), is
  cut, and the message then says `payloadBytes` (its full length in bytes) and `payloadTruncated`. The
  result says whether it cut anything: `truncated` (true when any payload was cut or the sample filled up, `stoppedBy` `size`), `truncatedMessages` (how many payloads were cut),
  `totalBytes` (the full payload bytes of the messages it kept) and `serverDropped` (`size`, `rate`, `slow`,
  `other`); `dropped` is their total, and when it cut anything a `hint` names `mqtt_get_sample` for whole
  payloads. One sample runs at a time — `mqtt_sample` and `mqtt_get_sample` share that one slot — so a
  second call while one runs answers that another is running: put every topic in one call. `stoppedBy` says
  why the sample ended: `max`, `seconds`, `end` (the platform ended the stream) or `size` (the sample was
  full).
- **`mqtt_get_sample`** is the same capture with every payload whole, and it shares the one-sample slot
  with `mqtt_sample`. It takes the same `topics`, `seconds` (default 5, at most 30) and `max` (default 50,
  at most 200) plus `maxBytes` (default 2 MiB, at most 8 MiB; it stops before the payloads pass it, the
  message that would is not kept, and `nextMessageBytes` reports its size). When the payloads' text (the
  base64 text for a binary payload) adds up to at most 12288 bytes and the answer fits under 48 KiB, it
  answers inline:
  `{"messages":[{"i","topic","ts","retain","bytes","encoding","payload"}],"count","totalBytes","stoppedBy","serverDropped","truncated"}`.
  A larger sample is written to a capture and the answer is an index:
  `{"captureId","file","expiresAt","count","totalBytes","stoppedBy","serverDropped","truncated","messages":[{"i","topic","ts","retain","bytes","encoding","sha256","kind","jsonValid","preview"}],"hint"}`.
  `kind` is `json`, `text`, `jpeg`, `png`, `gzip` or `binary`; `preview` is the start of a text payload
  (cut where a rune starts), or `hex:` and the first 32 bytes of a binary one. An index too long for the
  result drops its previews from the last message backwards (`previewsOmitted`: how many of the listed
  messages now show none), and lists fewer messages (`omitted`: how many the list leaves out) only when
  even that does not fit, and then `truncated` is true; the `hint` names both, and every message stays
  readable by its `i`. A base64
  payload the standard decoder refuses (unpadded, say) is tried unpadded; one neither reads is counted by
  its text's length and carries no `sha256`. Payloads over 256 KiB are dropped by the platform, never
  sent, and counted in `serverDropped.size`.
- **`mqtt_read`** reads one message of a capture: `captureId` and `i`, then `offset` (0 by default) and
  `length` (12288 by default and at most; a 256 KiB payload is about 22 pages). Without `path` the data is
  the payload's text — UTF-8 cut back to a rune start, or the base64 text read in groups of 4 characters
  (an `offset` that is no multiple of 4 is an error) — and `nextOffset` says where the next page starts
  (`null` at the end). With `path` (`detections[0].label`, `data.items[2].id`) the payload must be JSON
  and the data is that value, marshalled compactly and paged the same way; a missing key or index is an
  error naming the first step that failed and the keys or length it found, and a key that holds `.`, `[`
  or `]` cannot be addressed. The result carries `topic`, `path` (with it), `totalBytes`, `encoding` and
  the whole payload's `sha256`, so the pages can be checked. A topic over 1024 bytes is cut back to a rune
  start and its whole length given as `topicBytes`. In a TeknoirBot's council, where every result is cut at
  16 KiB, a page is held to 14336 bytes of escaped text (a page of quotes or control characters is shorter
  than `length`: follow `nextOffset`) and an index to 14336 bytes, so that neither is cut a second time.
- **Where captures live.** The stdio server (`tnctl mcp`) keeps them in `$XDG_RUNTIME_DIR/tnctl-captures`
  when that is set and absolute (per user, on tmpfs, removed at logout), otherwise in `tnctl/captures`
  under the user's cache directory (`~/.cache/tnctl/captures` on Linux), and only when neither exists in
  the temp dir as `tnctl-captures-<uid>` (Windows has no uid and a per-user temp dir, and uses
  `tnctl-captures`). The directory is mode `0700` and the files `0600`; a directory that is a symlink, not a
  directory, or another user's is refused, and a `0777` one it finds is set back to `0700`. Inside a
  TeknoirBot's council they go to `/work/captures`. A capture lasts 30 minutes and is deleted when they are
  up, with no further call needed; the store keeps at most 64 MiB of files, evicting the expired first and
  then the oldest, and a capture whose own file would pass 64 MiB (control characters in a payload escape
  to six bytes each, and topics count) is an error asking for a smaller `max` or `maxBytes`, never a
  capture that is gone when it is returned. The server deletes every capture of its own when the session
  ends. When a server starts it also removes `cap-*.jsonl` files older than 30 minutes that an earlier
  process left behind (never a younger one: several servers of one user may share the directory), and the
  council, which owns its directory alone, removes all of them. An unknown or expired `captureId` is an
  error asking for a new sample; so is one whose file went away.

## See also

- [`agent`](agent.md) - install the Teknoir agent kit and register the server.
- [`chat`](chat.md) - the same council, as a person in a terminal.
- [`council`](council.md) - the bot's own half: the process that answers the turns.
- [`mqtt`](mqtt.md) - the same stream in a terminal, with `tnctl mqtt stream` for endless output or
  `tnctl mqtt sample` for a finite capture of whole messages.
- [Bot mode](../bot-mode.md) - the server inside a TeknoirBot.
