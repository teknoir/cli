# `tnctl whoami`

Show who this `tnctl` works as: a person with a login, or the TeknoirBot it runs inside.

```bash
tnctl whoami [-o json]
```

## As a person

```bash
$ tnctl whoami
user a@b.c on teknoir.cloud, teamspace teknoir-demo
```

The account comes from the stored login (`~/.tnctl.yaml`): its `email` claim, or its preferred username or
subject when it has none. The teamspace is the one `tnctl` targets, with the usual precedence (`-n`, the
domain's stored namespace). The command does not ask the platform, so a login that has ended still prints;
`tnctl login --check` tests it. Without a login it is exit 6 (`not logged in: run tnctl login`).

`-o json`:

```json
{"mode":"user","email":"a@b.c","domain":"teknoir.cloud","namespace":"teknoir-demo"}
```

## Inside a TeknoirBot

```bash
$ tnctl whoami
bot teknoir-demo/scout for a@b.c, access read, unit Karlstad (se-karlstad)
```

It asks the bot API (`GET $TEKNOIRBOT_API/whoami`): the bot's teamspace and name, the person it works for, what
the bot may do in the teamspace (`none`, `read` or `creator`, which acts with the creator's permissions) and
the organisation unit the bot is placed in (`—` when it is not placed), with the unit's id in brackets when it
differs from the name: that id is what `tnctl robot add --unit` takes.

A bot set to `creator` whose creator may only view in the teamspace acts at `read`, and the line says so:
`access read (set to creator, but a@b.c may only view)`. A teamspace admin can give the creator write access.

`-o json` prints the platform's answer unchanged:

```json
{"mode":"bot","bot":{"namespace":"teknoir-demo","name":"scout","unit":"Karlstad","unitId":"se-karlstad","access":"read","configuredAccess":"creator"},"creator":{"email":"a@b.c"},"domain":"teknoir.cloud"}
```

`access` is the level the bot acts at, `configuredAccess` the level its creator set. `unit` and `unitId` are
`null` for a bot that is not placed. See [Bot mode](../bot-mode.md).

## Exit codes

| Exit | Meaning |
| --- | --- |
| 0 | ok |
| 1 | the bot API failed (for example a 503 while the platform cannot look the bot up; with `-o json` the error carries `retryAfter` when the platform sent one) |
| 2 | usage |
| 6 | not logged in (a person's `tnctl` without a stored login); in a bot, the key was not accepted or the bot may not be used |
| 9 | too many requests: wait the `Retry-After` time and run it again |

## See also

- [`login`](login.md) — `tnctl login --check` tests the stored login.
- [Bot mode](../bot-mode.md) — what bot mode is and which variables turn it on.
