# `tnctl ssh`

Open an interactive SSH session on an edge device through its tunnel.

## Synopsis

```bash
tnctl ssh [device-name]
```

The device may be given as an argument, via the `--device` global flag, or selected interactively if
omitted.

## Which tunnel is used

`tnctl` picks the tunnel automatically, per device:

1. **WireGuard** when the device has an active WireGuard tunnel. This is the default.
2. Otherwise the **legacy SSH tunnel** (deadend) when the device's legacy tunnel is active. `tnctl`
   prints one line on stderr:

   ```text
   tnctl: <device> has no active WireGuard tunnel; using the legacy SSH tunnel
   ```

3. Otherwise it stops with one message, the same for `ssh`, `port-forward` and `socks-proxy`:

   ```text
   Tunneling is not active for <device>. It turns on by itself once the device is bootstrapped and online; if it stays off, a teamspace admin turned it off or the device is flagged. See: tnctl wg status <device>
   ```

!!! note "The legacy SSH tunnel is deprecated"
    The fallback exists because some environments (production) have devices without WireGuard. It
    goes away after production has moved to WireGuard. The `tnctl devices` list shows which tunnel each
    device has (`wireguard`, `legacy` or `off`).

## Requirements

- A domain and namespace must be set, and you are logged in (`tnctl login`).
- Local `ssh` is installed.
- The developer (or admin) role in the device's teamspace **and** permission to open terminals. `tnctl ssh`
  reads the device login from the platform, which gives it only to those; a viewer is told so
  (`The login keys of devices need the developer role in teamspace ...`). Logging in again does not change that.
- Over WireGuard: the device online and connected, and
  a network that allows outbound HTTPS to the hub and outbound UDP to the relay (see
  [troubleshooting](#troubleshooting)). `ncat` is **not** needed.
- Over the legacy tunnel: local `ncat` as well.

## Over WireGuard

The SSH connection is carried by a short-lived, per-session WireGuard tunnel. See
**[Remote Access](../concepts/remote-access.md#wireguard-tunnel)** for how it works.

| Flag | Default | Description |
| --- | --- | --- |
| `--show-password` | `false` | Print the device's SSH/sudo password in the banner. |
| `--no-p2p` | `false` | Do not try a direct path to the device; always go through the hub. |
| `--wg-hub-url` | `https://wg.<domain>` | Where to create the WireGuard session. Also `TNCTL_WG_HUB`. Must be `https`. |
| `--turn-server` | the relay the hub hands out | `host:port` that replaces the TURN relay address (for lab networks). Also `TNCTL_WG_TURN_SERVER`. |

A flag wins over its environment variable, which wins over the default. These flags only apply to
a WireGuard tunnel; with the legacy tunnel they are an error.

The device login is the device's username and RSA key, read from the platform when the session starts. The
device password (also the sudo password) is **not** printed by default, so it does not end up in your
scrollback or logs; `tnctl ssh --show-password` prints it in the banner. The device's SSH host key is verified strictly against the key the device itself reported to
the hub.

### Access is checked by the hub, continuously

If the device is flagged compromised or fails its health check in Teknoir, the hub refuses new
connections and ends open ones. You see one line from `tnctl`, then `ssh` exits:

```text
tnctl: wireguard: device_revoked: device is flagged compromised/health-check-failed in Teknoir
```

Other refusals use the same form, for example `forbidden` (you need edit/admin access to the
teamspace), `device_offline` (the device agent is not connected), `device_disabled` (Tunneling is not
enabled for the device) or `unauthenticated` (run `tnctl login`).

## Over the legacy SSH tunnel (deprecated)

`tnctl` writes the decoded RSA key to a temporary `0600` file and launches `ssh` through the namespace
deadend proxy (`deadend-<namespace>.<domain>`) wrapped in TLS via `ncat --ssl`. The temporary key is
removed on exit.

## Examples

```bash
# Connect to a named device
tnctl ssh edge-01

# Use the --device flag
tnctl --device edge-01 ssh

# Pick interactively
tnctl ssh
```

!!! tip
    Add `--debug` to print the exact `ssh` command being executed.

## Copying files: scp and rsync

Over WireGuard, `scp` and `rsync` use `tnctl wg-proxy` as the ssh `ProxyCommand`. The proxy needs a
known_hosts file that it fills with the device's host key, and ssh must use the matching alias
`<device>.<namespace>.ztna.teknoir`. The device's login key is written by `tnctl ansible` to
`<user cache dir>/tnctl/ansible/<domain>/<namespace>/<device>.pem` (run `tnctl ansible <device> -m ping`
once to create it; on Linux the cache dir is `~/.cache`). Replace `DOMAIN`, `NS`, `DEV` and `USER`
(the device login name, `ansible_user` in `tnctl ansible-inventory --host DEV`):

```bash
KH=~/.cache/tnctl/ansible/DOMAIN/NS/DEV.wg_known_hosts
PROXY="tnctl wg-proxy --domain DOMAIN --namespace NS --device DEV --port 22 --known-hosts-file $KH"
SSHOPTS=(-i ~/.cache/tnctl/ansible/DOMAIN/NS/DEV.pem -o IdentitiesOnly=yes
         -o "ProxyCommand=$PROXY" -o UserKnownHostsFile=$KH
         -o StrictHostKeyChecking=yes -o HostKeyAlias=DEV.NS.ztna.teknoir)

scp "${SSHOPTS[@]}" ./file.txt USER@DEV:/tmp/
rsync -av -e "ssh ${SSHOPTS[*]@Q}" ./dir/ USER@DEV:/tmp/dir/
```

`tnctl wg-proxy` requires `--namespace`, `--device` and `--known-hosts-file` (and `--port`, default 22).
The domain and hub come from your login and configuration, so a bare
`-o ProxyCommand='tnctl wg-proxy --device DEV --port 22'` is not enough.

## Troubleshooting

- `Tunneling is not active for <device>`: turn Tunneling on for the device in the Teknoir console; for a
  new device, wait until it has connected.
- `Your tnctl login has expired. Run: tnctl login`: the stored login was revoked or idle too long. Run
  `tnctl login`; a running session ends at its next renewal with the same message. The exit status is 1, as for
  every WireGuard failure (exit 6 is reported only by the commands that call the platform API, such as
  `tnctl login --check`, `devices` and `config`), so a script should run `tnctl login --check` first.
- `device_offline`: the device is not connected to the hub. Check its power and network.
- Timeouts creating the session: the network must allow outbound HTTPS to `wg.<domain>` and outbound UDP
  (the relay, default port 3478).

## See also

- [`port-forward`](port-forward.md), [`socks-proxy`](socks-proxy.md), [`wg status`](wg.md)
- [Remote Access](../concepts/remote-access.md)
