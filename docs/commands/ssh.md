# `tnctl ssh`

Open an interactive SSH session on an edge device through Teknoir's secure proxy.

## Synopsis

```bash
tnctl ssh [device-name] [--wireguard [--turn-server host:port] [--wg-hub-url url]]
```

The device may be given as an argument, via the `--device` global flag, or selected interactively if
omitted.

## Requirements

- A domain and namespace must be set.
- **Remote Access** must be enabled for the device in the Teknoir Console, otherwise:

  ```text
  remote access is not enabled for device <name>. Please enable it in the Teknoir Console
  ```

- Local `ssh` and `ncat` must be installed.

## How it works

`tnctl` fetches the device's credentials, writes the decoded RSA key to a temporary `0600` file, and
launches `ssh` through the namespace deadend proxy (`deadend-<namespace>.<domain>`) wrapped in TLS via
`ncat --ssl`. The temporary key is removed on exit. If the device exposes a password, it is printed
once for convenience. See **[Remote Access](../concepts/remote-access.md)** for the full transport.

## WireGuard (ZTNA) path: `--wireguard`

```bash
tnctl ssh --wireguard edge-01
```

With `--wireguard`, the SSH connection to the device is carried by a short-lived, per-session WireGuard
tunnel instead of the SSH tunnel through deadend. It runs side by side with the default path: without
the flag nothing changes. See
**[Remote Access: WireGuard (ZTNA) path](../concepts/remote-access.md#wireguard-ztna-path)** for how
it works.

| Flag | Default | Description |
| --- | --- | --- |
| `--wireguard` | `false` | Use the WireGuard tunnel for this connection. |
| `--wg-hub-url` | `https://wg.<domain>` | Where to create the WireGuard session. Also `TNCTL_WG_HUB`. Must be `https`. |
| `--turn-server` | the relay the hub hands out | `host:port` that replaces the TURN relay address (for lab networks). Also `TNCTL_WG_TURN_SERVER`. |

A flag wins over its environment variable, which wins over the default. `--wg-hub-url` and
`--turn-server` are rejected without `--wireguard`.

Requirements for `--wireguard`:

- You are logged in (`tnctl login`) and have edit or admin access to the device's teamspace.
- WireGuard is enabled for the device in the Teknoir Console and the device is online and connected.
- Local `ssh` is installed. `ncat` is **not** needed.
- Your network allows outbound HTTPS to the hub and outbound UDP to the TURN relay.
- The Remote Access switch is **not** required: it applies to the SSH tunnel only.

The device login is unchanged: `tnctl` still uses the device's username and RSA key, and prints the
device password the same way. The device's SSH host key is verified strictly against the key the
device itself reported to the hub.

### Access is checked by the hub, continuously

If the device is flagged compromised or fails its health check in Teknoir, the hub refuses new
connections and ends open ones. You see one line from `tnctl`, then `ssh` exits:

```text
tnctl: wireguard: device_revoked: device is flagged compromised/health-check-failed in Teknoir
```

Other refusals use the same form, for example `forbidden` (you need edit/admin access to the
teamspace), `device_offline` (the device agent is not connected), `device_disabled` (WireGuard is not
enabled for the device) or `unauthenticated` (run `tnctl login`).

## Examples

```bash
# Connect to a named device
tnctl ssh edge-01

# Use the --device flag
tnctl --device edge-01 ssh

# Pick interactively
tnctl ssh

# Through the WireGuard tunnel
tnctl ssh --wireguard edge-01
```

!!! tip
    Add `--debug` to print the exact `ssh` command being executed.

## See also

- [`port-forward`](port-forward.md), [`socks-proxy`](socks-proxy.md)
- [Remote Access](../concepts/remote-access.md), including the [WireGuard (ZTNA) path](../concepts/remote-access.md#wireguard-ztna-path)
