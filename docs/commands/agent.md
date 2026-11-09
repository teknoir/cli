# `tnctl agent`

Install the **Teknoir agent kit** for the coding agents on this computer: the Agent Skills that teach an
agent to operate Teknoir devices and robots, the guard hook and the registration of the read-only
`teknoir` MCP server. The kit ships inside `tnctl`, so it installs offline and always matches the `tnctl`
that installs it.

```bash
tnctl agent install [--agent claude|codex|opencode|all] [--user|--project|--managed DIR]
                    [--mcp] [--hooks] [--agents-md] [--dry-run] [--force]
tnctl agent list [--check] [--user|--project|--managed DIR]
tnctl agent uninstall [--agent ...] [--user|--project|--managed DIR] [--mcp] [--hooks] [--agents-md]
```

The kit works with your own `tnctl` rights: an agent sees what you see, in the teamspace your `tnctl`
targets, and no more.

## Where it goes

| Agent | `--user` (default) | `--project` |
|---|---|---|
| Claude Code | `~/.claude/skills/` | `.claude/skills/` |
| Codex, OpenCode | `~/.agents/skills/` | `.agents/skills/` |

User scope follows the variables the agents themselves read, so the kit lands where the agent looks:
`CLAUDE_CONFIG_DIR` replaces `~/.claude` (and puts `.claude.json` in it), `CODEX_HOME` replaces
`~/.codex` (only `config.toml`, not the skills: those stay in `~/.agents/skills`), and `XDG_CONFIG_HOME`
moves the OpenCode directory to `$XDG_CONFIG_HOME/opencode` instead of `~/.config/opencode`. A variable
set to the empty string counts as unset.

Codex and OpenCode read the same root, so `--agent codex,opencode` writes one copy. Each root gets a
`.tnctl-agent-kit.json` manifest: the kit version, the `tnctl` that installed it, and the hash of every
file it wrote. The manifest is how an upgrade, `agent list --check` and `agent uninstall` tell a file the
kit wrote from one you changed.

`--managed DIR` always installs the skills to `DIR/skills` and the guard to `DIR/hooks/teknoir-guard`, and
nothing else: no MCP entry and no settings file. `--hooks` only adds the settings entry to add by hand
(see below). This is how the TeknoirBot image installs the kit into `/etc/claude-code/.claude`.

## Install

```bash
tnctl agent install --agent claude --mcp          # Claude Code: skills plus the MCP server
tnctl agent install --agent codex --mcp           # Codex
tnctl agent install --agent opencode --mcp        # OpenCode
tnctl agent install --project --agent claude --mcp --agents-md   # in this repository
```

With no `--agent` every agent is installed for, in the user scope.

- `--mcp` registers the read-only `teknoir` MCP server ([`tnctl mcp`](mcp.md)) in the agent's config
  file, merging into what is there: every other server and key survives. A `teknoir` entry that differs is kept and
  warned about unless `--force` replaces it. Codex starts an MCP server with a cleared environment, so the
  kit's Codex entry names the variables `tnctl` reads in `env_vars` (bot mode's `TEKNOIRBOT_API`,
  `TNCTL_BOT_KEY_FILE`, `TEKNOIRBOT_NAMESPACE` and `TEKNOIR_DOMAIN`, and `TNCTL_NAMESPACE` and
  `TNCTL_DOMAIN`); an install from an older `tnctl` has no `env_vars` line, so `tnctl agent install --mcp
  --force` updates it.
- `--hooks` installs the guard hook for Claude Code and adds a `PreToolUse` entry for `Bash` and the
  `teknoir` MCP tools (`mcp__teknoir__*`, or `mcp__plugin_teknoir_teknoir__*` when the server comes from
  the kit's plugin) to `~/.claude/settings.json` (project: `.claude/settings.json`), keeping the other
  hooks and permissions. The command is the guard's absolute path, shell-quoted, so a home or a project
  directory with a space in it works. In project scope the command is
  `"$CLAUDE_PROJECT_DIR"/.claude/hooks/teknoir-guard` (in the JSON file:
  `"command": "\"$CLAUDE_PROJECT_DIR\"/.claude/hooks/teknoir-guard"`), so the committed file works on
  every machine. A re-run adds no duplicate, and `uninstall --hooks` takes out that handler only, never a
  hook of yours beside it. For Codex and OpenCode it prints that hooks are installed for Claude Code
  only. With `--managed` it prints the entry to add to Claude Code's managed settings
  (`/etc/claude-code/managed-settings.json`, or a file in `/etc/claude-code/managed-settings.d/`) instead
  of writing any.
- `--agents-md` (project scope) adds a short section to `./AGENTS.md`, between
  `<!-- teknoir-agent-kit:begin -->` and `<!-- teknoir-agent-kit:end -->`; a re-run replaces only that
  section.
- `--dry-run` prints the plan and writes none of the kit's files or agent configs (like every `tnctl`
  command, the first run creates `~/.tnctl.yaml`).
- `--force` overwrites a file you changed, and replaces a differing `teknoir` MCP entry.

### What it will not overwrite

A file that exists, differs from the kit's and is not the one the kit wrote (you changed it), or was never
recorded, is **refused**: it is listed, the rest of the install still runs, and the exit code is 8. The
same goes for an OpenCode `.jsonc` with comments, which `tnctl` does not edit: it prints the exact snippet
to add and the file's path. A kit file that is not a regular file (a symlink, a directory) is refused too,
and `--force` does not replace it. `AGENTS.md` with damaged `teknoir-agent-kit` markers is refused: fix the
markers by hand. So is a manifest whose entries are not paths inside its root.

Files the kit wrote and you did not change are upgraded in place; a file the kit no longer has is deleted
if it is unchanged, and kept (and reported) if you changed it.

## The MCP files

| Agent | `--user` | `--project` | Entry |
|---|---|---|---|
| Claude Code | `~/.claude.json`, key `mcpServers` | `.mcp.json`, key `mcpServers` | `{"type":"stdio","command":"tnctl","args":["mcp"]}` |
| Codex | `~/.codex/config.toml` | `.codex/config.toml` | `[mcp_servers.teknoir]`, `command = "tnctl"`, `args = ["mcp"]`, `env_vars = [...]` |
| OpenCode | `~/.config/opencode/opencode.jsonc` if it exists, else `opencode.json` | `opencode.jsonc` if it exists, else `opencode.json` | under `mcp.servers`: `{"type":"local","command":["tnctl","mcp"]}` |

JSON files keep every foreign key and value as JSON; `tnctl` writes them with 2-space indent, so an
existing file's **key order changes** (the data does not). Values are written as they are (`&`, `<` and
`>` are not escaped) and the file ends in one newline, so installing and then uninstalling gives a file
`tnctl` wrote back byte for byte. While it edits `~/.claude.json` (`$CLAUDE_CONFIG_DIR/.claude.json` when
that is set), `tnctl` takes the lock Claude Code itself takes while it saves that file (the same path plus
`.lock`). If Claude Code is saving at that moment, `tnctl` waits; after 5 seconds it fails with
`Claude Code holds …; run again`. A lock older than 10 seconds counts as left behind and is taken over.
Codex's TOML is edited line by line, so its comments stay; the result is parsed and compared with the
original **before** anything is written, and an edit that would break the file, or touch anything beside
`[mcp_servers.teknoir]`, is refused with the lines to edit by hand. A config file that is a symlink
(dotfiles, stow, home-manager) keeps its link: the change goes to the file it points at. In project scope
such a file is refused (exit 8) with the lines to edit by hand, and so is a project whose `.claude`,
`.agents` or `.codex` is a symlink. `AGENTS.md` is the exception: a link to a Markdown file inside the
project, outside `.git` (AGENTS.md -> CLAUDE.md), is followed, and the section goes into that file.

## List and check

```bash
tnctl agent list                  # both scopes, every root with a manifest
tnctl agent list --check          # exit non-zero when anything differs from this tnctl's kit
tnctl agent list --user -o json   # the roots and the MCP registrations, as JSON
```

`list` prints each root, the kit version installed and the one this `tnctl` carries, the files missing,
changed or outdated, and which agent config files register the `teknoir` MCP server. `--check` exits 5 when
anything differs: a missing file, a changed file or another kit version, or nothing installed at all. A
managed install is checked as a whole, so a `DIR` whose skills or guard are not there fails too. CI and the
TeknoirBot image test use it.

## Uninstall

```bash
tnctl agent uninstall --agent claude --mcp --hooks
tnctl agent uninstall --project --mcp --agents-md
```

Deletes exactly the files the manifests record, while they still match (a file you changed is kept and
reported), removes the skill directories that become empty and the manifests. With `--mcp` it removes the
`teknoir` MCP entry from the config files, with `--hooks` the guard and its `PreToolUse` entry, with
`--agents-md` the section from `./AGENTS.md`. A managed install always holds the guard, so it goes with or
without `--hooks`.

Codex and OpenCode read the same `~/.agents/skills` (project: `.agents/skills`), so an uninstall of one of
them keeps that root: it goes only when both are named (`--agent codex,opencode`, or the default, all).

## The plugin instead

Claude Code can also take the kit as a plugin, from the repository:

```text
/plugin marketplace add teknoir/cli
/plugin install teknoir@teknoir-agent-kit
```

The plugin has no hook: a hook that sees every command is installed only with `tnctl agent install
--hooks`.

## Output and exit codes

One line per action `(write|keep|upgrade|refuse|delete|register|already registered|skip)`, then a summary;
`--dry-run` prefixes each with `would`. `-o json` prints the actions as
`[{"action","path","agent","note"}]`; `agent list -o json` prints `{"roots":[...],"mcp":[...]}`. With
`-o json` or `-o yaml` the warnings go to stderr as `warning: …`, so stdout stays parseable; the table
output prints them after the actions.

| Exit | Meaning |
|---|---|
| 0 | ok |
| 1 | error |
| 2 | usage: an unknown agent, two scopes, `--agents-md` without `--project` |
| 5 | `list --check`: an install differs from this `tnctl`'s kit, or nothing is installed |
| 8 | a file you changed, a commented `.jsonc`, a symlink `tnctl` does not follow, a damaged manifest or damaged AGENTS.md markers, or a Codex edit `tnctl` cannot make safely: needs `--force` or a hand edit |

## See also

- [`whoami`](whoami.md) - which account or TeknoirBot this `tnctl` is.
- [Bot mode](../bot-mode.md) - the same kit inside a TeknoirBot.
