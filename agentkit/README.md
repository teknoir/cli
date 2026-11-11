# Teknoir agent kit

The Teknoir agent kit turns a coding agent into an expert operator of Teknoir devices and robots. It is
a set of Agent Skills (folders with `SKILL.md`), the `teknoir` MCP server registration and a guard hook.
It ships inside the `tnctl` binary, so it installs offline and always matches the `tnctl` that installs it.

The kit works with your own `tnctl login` rights: an agent sees what you see, in the teamspace your
`tnctl` targets, and nothing more.

## Install

Claude Code:

```bash
tnctl agent install --agent claude --mcp
```

or as a plugin (Claude Code adds the kit from the repository):

```text
/plugin marketplace add teknoir/cli
/plugin install teknoir@teknoir-agent-kit
```

Codex:

```bash
tnctl agent install --agent codex --mcp
```

OpenCode:

```bash
tnctl agent install --agent opencode --mcp
```

With no `--agent` the kit is installed for all three. Skills go to `~/.claude/skills` (Claude Code) and
`~/.agents/skills` (Codex, OpenCode) by default.

### The council server

`--council` (with `--mcp`) registers `tnctl mcp --council` instead of the read-only `tnctl mcp`: the read
tools plus `council_info`, `council_ask`, `council_run` and `council_result`, which hand work to a
TeknoirBot's council (skill `teknoir-council`). An install switches between the two forms without
`--force`.

```bash
tnctl agent install --agent claude --mcp --council
```

### Project scope

`--project` installs into the current directory instead of your home: `.claude/skills`, plus `.mcp.json` with
`--mcp` and `.claude/settings.json` with `--hooks`; `.agents/skills`, plus `.codex/config.toml` /
`opencode.json(c)` with `--mcp`. Commit them to share the kit with everyone who works in the repository.
`--agents-md` adds a short section to `./AGENTS.md` as well.

### The guard hook

`--hooks` installs `hooks/teknoir-guard` and registers it in Claude Code's `settings.json` as a `PreToolUse`
hook for `Bash` and `mcp__teknoir__*` calls (`mcp__plugin_teknoir_teknoir__*` when the server comes from
the kit's plugin), so it can refuse a command before it runs. Hooks work with Claude Code only; the other
agents get the skills and the MCP server.

### Managed directory (images)

`--managed DIR` installs the skills to `DIR/skills`, and for Claude Code the guard to
`DIR/hooks/teknoir-guard`, and touches nothing else — no MCP entry and no settings file. `--hooks` only
adds the settings entry to copy into Claude Code's managed settings. A managed install without `claude` in
`--agent` writes no guard: `tnctl agent install --agent opencode --managed /etc/teknoirbot/kit` is how the
TeknoirBot's council image installs the kit.

## Where it goes

| Agent | User scope | Project scope |
| --- | --- | --- |
| Claude Code | `~/.claude/skills/` | `.claude/skills/` |
| Codex, OpenCode | `~/.agents/skills/` | `.agents/skills/` |

Every root gets a `.tnctl-agent-kit.json` manifest: the kit version, the `tnctl` that installed it and the
hash of each file, so a later `tnctl agent install` can tell an untouched file from one you changed. Such a
file is never overwritten without `--force`.

## Commands

```bash
tnctl agent install [--agent claude|codex|opencode|all] [--user|--project|--managed DIR]
                    [--mcp] [--council] [--hooks] [--agents-md] [--dry-run] [--force]
tnctl agent list [--agent ...] [--check] [--user|--project|--managed DIR]
tnctl agent uninstall [--agent ...] [--user|--project|--managed DIR] [--mcp] [--hooks] [--agents-md]
```

See [`docs/commands/agent.md`](../docs/commands/agent.md).

## What is in the kit

| Skill | Use it for |
| --- | --- |
| `teknoir-platform` | The entry skill: what Teknoir is, person or TeknoirBot, which command answers which question, exit codes, safety rules. |
| `teknoir-robots` | Robots, robot types and fleets: read, create and change them, and the steps a person takes to put a real computer online. |
| `teknoir-config` | A robot's configuration: layers, overrides, local items, diff, dry-run, write and wait. |
| `teknoir-debug` | Why a robot or workload is unhealthy: problems, `tnctl inspect`, logs, PromQL, `scripts/triage.sh`. |
| `teknoir-mqtt-streams` | Reading, processing and publishing devices' MQTT telemetry with `tnctl mqtt stream`: sampling, the relay, a processor template. |
| `teknoir-k3s-apps` | Building an app in a TeknoirBot's computer and running it on its k3s, or delivering it to robots. |
| `teknoir-council` | Leading a TeknoirBot's council from your own agent with the `council_*` tools: when to ask one member, when to ask the bot, how to judge and wait for the reports. |

Also in the kit:

- `hooks/teknoir-guard` — the guard hook. In this version it allows every call; the refusal rules come
  with `tnctl agent guard`.
- `.mcp.json` and `.claude-plugin/plugin.json` — the `teknoir` MCP server, which is
  [`tnctl mcp`](../docs/commands/mcp.md), and the Claude Code plugin.
- `AGENTS.snippet.md` — the section `--agents-md` puts between markers in `./AGENTS.md`.

`agentkit/test/` holds the tests of the skills' scripts (`bash agentkit/test/scripts-b.sh`, `scripts-c.sh`);
it is not embedded.
