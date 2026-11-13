# Bot mode

A **TeknoirBot** is a computer with an AI council for a person. Inside it, `tnctl` runs in **bot mode**:
every fleet API call goes to the bot API with the bot's own key, instead of to the platform with the user's
Keycloak login. The bot works only in its own teamspace, cannot read device logins, and cannot open tunnels.

A person's `tnctl` and a bot's `tnctl` are the same binary; the environment decides which it is.

## When bot mode is on

Bot mode is on when **both** of these variables are set and non-empty:

| Variable | Meaning |
| --- | --- |
| `TEKNOIRBOT_API` | The bot API's base, for example `https://tb-api.teknoir.cloud:8085/api/device/bot/v1`. |
| `TNCTL_BOT_KEY_FILE` | The file holding the bot's key. |

Two more are set for a bot:

| Variable | Meaning |
| --- | --- |
| `TEKNOIRBOT_NAMESPACE` | The bot's teamspace. **Required** in bot mode: without it every fleet call is a usage error (exit 2). |
| `TEKNOIR_DOMAIN` | The platform's domain. Informational (it appears in `-o json`), never used to reach anything. |

With bot mode on, `tnctl` never reads `~/.tnctl.yaml` and never uses the user's login: a `$HOME/.tnctl.yaml`
that names another domain or teamspace is ignored. `-n/--namespace` (or `TNCTL_NAMESPACE`) that names another
teamspace than `TEKNOIRBOT_NAMESPACE` is refused: _a TeknoirBot works only in its own teamspace
(`teknoir-demo`)_.

## Where the key comes from

The key is **a file in the bot's computer**, at the path `$TNCTL_BOT_KEY_FILE` (on a bot's computer,
`/run/teknoirbot/key`). A bot gets its key when its computer is made; re-creating the bot gives it a new
computer and a new key. `tnctl` reads the file on every request. It never prints, logs or reports the key.
When the file is missing or empty, every fleet call refuses with:

```text
the bot key is missing or empty (/run/teknoirbot/key): this computer predates the agent; re-create the bot
```

## What a bot may do

Everything a person's `tnctl` does with the fleet API: `robots`, `robot` (show, `add`, `disable`, `enable`,
`remove`), `config`, `devices`, `device`, `whoami`, `api`, `inspect`, `mqtt stream`, `mqtt sample` and `mcp` (limited to the
bot's teamspace). The platform decides what the bot's creator may do in the teamspace (`whoami` reports
`access none`, `read` or `creator`).

It also runs the one command that is the bot's own: [`council serve`](commands/council.md), the process of its
council image, which takes the chat turns the platform queues for the bot and answers them with the bot's key.
A bot's `mcp` runs without the council tools: `--council` is for a person's own agent, and `tnctl mcp
--council` and `tnctl agent install --council` are both refused before anything starts (exit 2) with
`--council is for a person's own agent: a TeknoirBot cannot drive a council`.

A bot may **not** run the commands that open a session or hold a device login. They are refused before
anything runs, with exit code 6 and this message:

```text
"tnctl ssh" is not available to a TeknoirBot: it cannot open tunnels or hold device logins. For logs use "tnctl inspect logs".
```

| Refused | Why |
| --- | --- |
| `login` | A bot has no user login to make. |
| `ssh`, `exec`, `logs`, `port-forward`, `socks-proxy`, `wg` (every subcommand), `wg-proxy` | They reach the device through a tunnel, which needs the device login. |
| `ansible`, `ansible-playbook`, `ansible-inventory` | They are an inventory of tunneled devices. |
| `bootstrap` (and its subcommands) | It generates the scripts that install a device. |
| `add` (and its subcommands), `domain` | They change where `tnctl` points, and a bot's platform is `$TEKNOIRBOT_API`. |
| `ns` **with** a teamspace | A bot has exactly one teamspace. `tnctl ns` without one (or with `--list`) lists them. |
| `chat` | It is the person's chat with a bot's council; a bot does not use it: `"tnctl chat" is not available to a TeknoirBot: a bot does not use the person chat.` |

`--domain` and `--config` are refused with them (exit 2): a bot's platform comes from `$TEKNOIRBOT_API`.

A write that waits on the robot itself (`config … --wait`, and a bare `--wait`, which means `ready`) needs a
device shell, so in bot mode it works only with `--wait=published`: `applied` and `ready` are refused
before anything is written (exit 6).

Use [`tnctl inspect`](commands/inspect.md) for logs and pods, and the fleet API through
[`tnctl api`](commands/api.md) for anything no command covers.

Device logins are refused outright: `Credentials` and `CredentialsBulk` answer without a request, and
`tnctl api` refuses any path with a `credentials` segment, in both modes.

## Who am I

```bash
$ tnctl whoami
bot teknoir-demo/scout for a@b.c, access read, unit Karlstad (se-karlstad)
```

The unit is `—` when the bot is not placed in an organisation unit; the id in brackets is what
`tnctl robot add --unit` takes. A bot set to `creator` whose creator may only view acts at `read`, and the line
ends the access with `(set to creator, but a@b.c may only view)`. `-o json` prints the platform's answer:

```json
{"mode":"bot","bot":{"namespace":"teknoir-demo","name":"scout","unit":"Karlstad","unitId":"se-karlstad","access":"read","configuredAccess":"read"},"creator":{"email":"a@b.c"},"domain":"teknoir.cloud"}
```

`access` is the level the bot acts at, `configuredAccess` the level its creator set.

Outside bot mode the same command prints the person: `user a@b.c on teknoir.cloud, teamspace teknoir-demo`.

## The routes in bot mode

The fleet root is the bot API's: `$TEKNOIRBOT_API/fleet`, and a teamspace's base is
`$TEKNOIRBOT_API/fleet/<namespace>`. Every fleet route keeps its path below that base, so
`tnctl api GET teknoir-demo/robots` works the same in both modes.

Every request carries `Authorization: Bearer <key>`, with the key file's content trimmed. `$TEKNOIRBOT_API`
must be `https` (only a loopback address may be plain `http`, which the tests use): the key must not travel
in clear text, so anything else exits 6 with _TEKNOIRBOT_API must be https_.

`-d` is the global `--domain`; send a body with `--data`.

## See also

- [`whoami`](commands/whoami.md), [`api`](commands/api.md), [`inspect`](commands/inspect.md), [`mqtt`](commands/mqtt.md), [`mcp`](commands/mcp.md), [`chat`](commands/chat.md), [`council`](commands/council.md)
- [Configuration](configuration.md) — the user's `~/.tnctl.yaml`, which bot mode ignores.
