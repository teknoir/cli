# `tnctl api`

Call the fleet API directly, with the same login (or bot key) the other commands use.

```bash
tnctl api <METHOD> <path> [--data JSON|@file|-]
```

`<path>` is relative to the **fleet root** and names the teamspace itself, for example
`teknoir-demo/robots` or `teknoir-demo/robots/vision-station-01`. `<METHOD>` is `GET`, `POST`, `PUT`, `PATCH`
or `DELETE`. The answer is printed as indented JSON.

```bash
tnctl api GET teknoir-demo/robots
tnctl api GET teknoir-demo/robots/vision-station-01
tnctl api POST teknoir-demo/robots --data '{"name":"amr-02","type":"amr"}'
tnctl api PATCH 'teknoir-demo/config/vs/overrides/resource?id=HelmChart%2Fdefault%2Fnotebook' --data @patch.json
echo '{"name":"amr-02","type":"amr"}' | tnctl api POST teknoir-demo/robots --data -
```

| Flag | Description |
| --- | --- |
| `--data VALUE` | The JSON body: JSON text, `@file` to read it from a file, or `-` for stdin. It must be JSON; anything else is exit 2 and nothing is sent. |

There is no `-d` shorthand: `-d` is the global `--domain`. The shorthand would collide with it and cobra
refuses to build the command tree.

`-d` is the global `--domain`; send a body with `--data`. A body written with `-d` would set the domain and
be sent as no body at all, so `tnctl api` refuses a `--domain` that starts with `{`, `[` or `@`, or is `-`
(exit 2).

In bot mode the fleet root is the bot API's (`$TEKNOIRBOT_API/fleet`), so the same command lines work; see
[Bot mode](../bot-mode.md).

## The path

The path is relative to the fleet root: no leading `/`, no URL, no `.` or `..` segment, no empty segment
(`a//b`), no `\`, no `;` in a segment, and no `://`. Anything else is refused before a request is sent
(exit 2):

```text
the path must be relative to the fleet API, like teknoir-demo/robots
```

A query is sent as written: `tnctl api GET 'teknoir-demo/robots?unit=a%2Fb'`. The path part is unescaped to
check it and escaped again per segment for the request; the query is not touched.

## A device's login is refused

A path with a `credentials` segment (in any case, not only the last one) is refused before any request is
sent, in both modes (exit 2):

```text
tnctl api does not call a device's login route: a device login is a secret tnctl never prints. Read a robot or device with tnctl robot, tnctl device or tnctl config
```

## Errors

API errors go through the same classification as every other command: `404` is exit 3, `409` exit 8, `422`
exit 5, `403` and a rejected login exit 6, `429` exit 9, with the platform's own message.

## See also

- [`robots`](robots.md), [`config`](config.md), [`inspect`](inspect.md) — the routes this command reaches, with their arguments checked.
