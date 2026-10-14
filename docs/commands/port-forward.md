# `tnctl port-forward`

Forward a local port to a device, or to a service reachable from the device's network.

## Synopsis

```bash
tnctl port-forward [device] [local-port]:[target-host]:[target-port]
tnctl port-forward --wireguard [device] [local-port:]service-id
```

You can specify the mapping positionally or with flags.

## Flags

| Flag | Shorthand | Description |
| --- | --- | --- |
| `--port` | `-p` | Local port to listen on. |
| `--to` | `-t` | Target address on the device network (e.g. `127.0.0.1:80`). |
| `--wireguard` | | Forward a device **service** through the WireGuard (ZTNA) tunnel. See [below](#through-wireguard-wireguard). |
| `--no-p2p` | | With `--wireguard`: skip the direct path, always go through the hub. |
| `--wg-hub-url` | `https://wg.<domain>` | With `--wireguard`: where to create the session (env `TNCTL_WG_HUB`). Must be `https`. |
| `--turn-server` | hub's | With `--wireguard`: TURN `host:port` override (env `TNCTL_WG_TURN_SERVER`). |

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

## Through WireGuard (`--wireguard`)

```bash
tnctl port-forward --wireguard my-device 8080:grafana
tnctl port-forward --wireguard my-device grafana        # random free local port
```

With `--wireguard` the target is a **service ID** that the device publishes (a web UI, an SSH
port, a Kubernetes Service inside the device's k3s), not `host:port`. The forward is bound to
`127.0.0.1` only, and nothing else on the device or its network becomes reachable. List a device's
services with [`tnctl wg status`](wg.md).

Without a device argument the device comes from `--device`, or `tnctl` offers a picker. A single bare
word is taken as the device; give `--device` to make it the service.

`tnctl` first tries a **direct** WireGuard path to the device (peer to peer, through NAT when
possible), for up to about 2.5 seconds, and otherwise relays through the hub. Every accepted
connection is dialled separately, so the path can change while the forward runs:

```text
Forwarding 127.0.0.1:8080 -> my-device/grafana (path: direct (LAN))
Open http://localhost:8080
Press Ctrl+C to stop.
```

| Path | Meaning |
| --- | --- |
| `direct (LAN)` | Same network as the device; traffic never leaves it. |
| `direct` | Peer to peer over the internet; the hub carries no traffic. |
| `via hub` | Relayed by the hub (a direct path was not possible, or `--no-p2p`). |

The `Open http://…` line appears for services published as web UIs. If the path changes later,
`tnctl` prints `path is now: …` on stderr. The session is renewed with a fresh login token for as long
as the command runs, and it ends at once if the device is revoked or your access is removed.

Errors name a reason and what to do, for example `service_unknown` (no such service; check
`tnctl wg status`), `service_denied` (the device refused the target), `p2p_unavailable` (retry with
`--no-p2p`), and the reasons listed under [`ssh --wireguard`](ssh.md#wireguard-ztna-path-wireguard).

## Requirements

Same as [`ssh`](ssh.md): a set domain/namespace, remote access enabled for the device, and local
`ssh`/`ncat`. With `--wireguard` neither the remote-access switch nor `ssh`/`ncat` is needed; WireGuard
must be enabled for the device in the Teknoir Console and the device must be online. See **[Remote Access](../concepts/remote-access.md)**.

## See also

- [`wg status`](wg.md): check the path and list a device's services.
- [`socks-proxy`](socks-proxy.md) — proxy the whole network instead of a single port.
