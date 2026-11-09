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
bot teknoir-demo/scout for a@b.c, access read, unit Karlstad
```

It asks the bot API (`GET $TEKNOIRBOT_API/whoami`): the bot's teamspace and name, the person it works for, what
that person may do in the teamspace (`none`, `read` or `creator`) and the organisation unit the bot is placed
in (`—` when it is not placed). `-o json` prints the platform's answer unchanged. See [Bot mode](../bot-mode.md).

## Exit codes

| Exit | Meaning |
| --- | --- |
| 0 | ok |
| 2 | usage |
| 6 | not logged in (a person's `tnctl` without a stored login) |

## See also

- [`login`](login.md) — `tnctl login --check` tests the stored login.
- [Bot mode](../bot-mode.md) — what bot mode is and which variables turn it on.
