# `tnctl wg`

Inspect the WireGuard tunnel to a device. `tnctl ssh` and `tnctl port-forward` use WireGuard
automatically when it is active; this command is for checking and troubleshooting. To only list a device's
services, `tnctl port-forward <device>` is enough.

## `tnctl wg status`

```bash
tnctl wg status [device]
```

Opens one short-lived WireGuard session to the device (service `ssh`), tries the direct path for
about 2.5 seconds, measures one TCP connect, and prints what it found:

```text
Device         my-team/my-device
Hub            reachable (https://wg.teknoir.cloud)
Device online  yes
Path           direct (LAN)
Candidates     3
RTT            1.4ms (TCP connect to ssh)
Services       ssh (external, tcp)
               grafana (internal, http, web) - Dashboards
```

| Line | Meaning |
| --- | --- |
| `Hub` | Whether the WireGuard hub answered. A denial counts as reachable. |
| `Device online` | `yes`, `no` (the hub says the agent is not connected) or `unknown`. |
| `Problem` | The hub's reason and hint when no session could be made. |
| `Path` | `direct (LAN)`, `direct` or `via hub`; see [`port-forward`](port-forward.md#over-wireguard). |
| `Candidates` | Addresses the device offered for the direct path. |
| `RTT` | Time to open a TCP connection to the device's ssh service over that path. |
| `Services` | The services published for the device, read from the catalog annotation `ztna.teknoir.org/services`. |

The exit status is 0 when a session was made and the ssh service answered, 1 otherwise.

| Flag | Description |
| --- | --- |
| `--no-p2p` | Skip the direct-path attempt (reports `via hub`). |
| `--wg-hub-url` | Hub URL (default `https://wg.<domain>`, env `TNCTL_WG_HUB`). |
| `--turn-server` | TURN `host:port` override (env `TNCTL_WG_TURN_SERVER`). |

Plus all [global flags](index.md#global-flags).

## wg-proxy

`tnctl wg-proxy` is the SSH `ProxyCommand` behind `tnctl ssh` over WireGuard. It is visible in `tnctl --help` because
ssh configs and the [Ansible inventory](ansible.md) call it directly. It is normally not run by hand; see
`tnctl wg-proxy --help` for its flags, and [`ssh`](ssh.md#copying-files-scp-and-rsync) for using it with `scp` and `rsync`.
