# `tnctl port-forward`

Forward a local port to a device, or to a service reachable from the device's network.

## Synopsis

```bash
tnctl port-forward [device] [local-port]:[target-host]:[target-port]
```

You can specify the mapping positionally or with flags.

## Flags

| Flag | Shorthand | Description |
| --- | --- | --- |
| `--port` | `-p` | Local port to listen on. |
| `--to` | `-t` | Target address on the device network (e.g. `127.0.0.1:80`). |

Plus all [global flags](index.md#global-flags).

## Mapping formats

| Form | Meaning |
| --- | --- |
| `local:targetPort` | Forwards to `127.0.0.1:targetPort` on the device. |
| `local:targetHost:targetPort` | Forwards to `targetHost:targetPort` on the device's network. |

If no local port is given (via mapping or `--port`), a random port between 8000 and 65000 is chosen.

## Web-service detection

When the target port is a common web port (`80`, `443`, `3000`, `5000`, `8080`, `8443`, `9000`,
`9090`), `tnctl` prints a ready-to-open URL such as `http://localhost:<port>` (or `https://` for
`443`/`8443`).

## Examples

```bash
# Forward local 8080 to the device's localhost:80
tnctl port-forward my-device 8080:localhost:80

# Forward to another host on the device network
tnctl port-forward my-device 8080:192.168.1.10:80

# Shorthand: local:targetPort (defaults target host to localhost)
tnctl port-forward my-device 31883:31883

# Using flags
tnctl port-forward --port 8080 --to localhost:80
```

Press <kbd>Ctrl</kbd>+<kbd>C</kbd> to stop forwarding.

## Requirements

Same as [`ssh`](ssh.md): a set domain/namespace, remote access enabled for the device, and local
`ssh`/`ncat`. See **[Remote Access](../concepts/remote-access.md)**.

## See also

- [`socks-proxy`](socks-proxy.md) — proxy the whole network instead of a single port.
