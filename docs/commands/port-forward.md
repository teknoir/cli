# `tnctl port-forward`

Forward a local port to a service on a device.

## Synopsis

```bash
tnctl port-forward [device] [local-port:]service-id     # forward a service
tnctl port-forward [device]                              # list the device's services
```

One grammar: forward one of the device's **services** by its ID. The device may also come from
`--device`, or a picker is shown.

## Which tunnel is used

`tnctl` picks the tunnel automatically (see [`ssh`](ssh.md#which-tunnel-is-used)): WireGuard when the
device has an active WireGuard tunnel, otherwise the legacy SSH tunnel (deprecated, goes away after production
has moved to WireGuard), otherwise the message `Tunneling is not active for <device>. It turns on by itself once the device is bootstrapped and online; if it stays off, a teamspace admin turned it off or the device is flagged. See: tnctl wg status <device>`

## Over WireGuard

```bash
tnctl port-forward my-device                  # list the device's services (exit 0)
tnctl port-forward my-device grafana          # random free local port
tnctl port-forward my-device 8080:grafana     # local port 8080
```

The target is a **service ID** that the device publishes (a web UI, an SSH port, a Kubernetes Service
inside the device's k3s), not `host:port`. The forward is bound to `127.0.0.1` only, and nothing else on
the device or its network becomes reachable. `--to` is not available over WireGuard:

```text
--to is not available over WireGuard; forward one of the device's services: tnctl port-forward <device> <service-id> (list them with tnctl port-forward <device>)
```

A single bare word is taken as the device; give `--device` to make it the service.

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
`tnctl port-forward <device>`), `service_denied` (the device refused the target), `p2p_unavailable` (retry
with `--no-p2p`), and the reasons listed under [`ssh`](ssh.md#access-is-checked-by-the-hub-continuously).

Arbitrary `host:port` targets, and `ssh -L`/`-D` style forwarding, are deliberately not offered over
WireGuard: a device's services are the only things you can forward.

## Flags

| Flag | Shorthand | Description |
| --- | --- | --- |
| `--port` | `-p` | Local port to listen on. |
| `--to` | `-t` | Legacy SSH tunnel only: target address on the device network (e.g. `127.0.0.1:80`). |
| `--no-p2p` | | WireGuard: skip the direct path, always go through the hub. |
| `--wg-hub-url` | `https://wg.<domain>` | WireGuard: where to create the session (env `TNCTL_WG_HUB`). Must be `https`. |
| `--turn-server` | hub's | WireGuard: TURN `host:port` override (env `TNCTL_WG_TURN_SERVER`). |

Plus all [global flags](index.md#global-flags).

## Over the legacy SSH tunnel (deprecated)

```bash
tnctl port-forward [device] [local-port]:[target-host]:[target-port]
```

Only used for devices that have no active WireGuard tunnel. Requires local `ssh` and `ncat`.

| Form | Meaning |
| --- | --- |
| `local:targetPort` | Forwards to `127.0.0.1:targetPort` on the device. |
| `local:targetHost:targetPort` | Forwards to `targetHost:targetPort` on the device's network. |

If no local port is given (via mapping or `--port`), a random port between 8000 and 65000 is chosen.

## Web-service detection

When the target port is a common web port (`80`, `443`, `3000`, `5000`, `8080`, `8443`, `9000`,
`9090`), `tnctl` prints a ready-to-open URL such as `http://localhost:<port>` (or `https://` for
`443`/`8443`).

## Legacy examples

```bash
# Forward local 8080 to the device's localhost:80
tnctl port-forward my-device 8080:localhost:80

# Forward to another host on the device network
tnctl port-forward my-device 8080:192.168.1.10:80

# Shorthand: local:targetPort (defaults target host to localhost)
tnctl port-forward my-device 31883:31883

# Using flags
tnctl port-forward my-device --port 8080 --to localhost:80
```

Press <kbd>Ctrl</kbd>+<kbd>C</kbd> to stop forwarding.

## See also

- [`wg status`](wg.md): check the path to a device.
- [`socks-proxy`](socks-proxy.md): legacy only; proxies the whole network instead of a single port.
