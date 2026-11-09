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

`--managed DIR` always installs the skills to `DIR/skills` and the guard to `DIR/hooks/teknoir-guard`, and
touches nothing else — no MCP entry and no settings file. `--hooks` only adds the settings entry to copy
into Claude Code's managed settings. This is how the TeknoirBot image installs the kit.

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
                    [--mcp] [--hooks] [--agents-md] [--dry-run] [--force]
tnctl agent list [--check] [--user|--project|--managed DIR]
tnctl agent uninstall [--agent ...] [--user|--project|--managed DIR] [--mcp] [--hooks] [--agents-md]
```

See [`docs/commands/agent.md`](../docs/commands/agent.md).

## What is in the kit

- `skills/teknoir-platform/SKILL.md` — the entry skill: what Teknoir is and how to start.
- `hooks/teknoir-guard` — the guard hook (placeholder until kit 0.2).
- `.mcp.json` and `.claude-plugin/plugin.json` — the `teknoir` MCP server and the Claude Code plugin.

The skills' full content and the guard rules arrive in kit 0.2.
