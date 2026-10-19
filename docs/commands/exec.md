# `tnctl exec`

Run one command on a device over its WireGuard tunnel and pass its exit code through.

```bash
tnctl exec <device> -- <command...>
```

Not interactive: no terminal, `ssh` runs in BatchMode and never waits for a password. Use
[`tnctl ssh`](ssh.md) for a shell. The command goes after `--`.

- Several words are separate arguments; `tnctl` quotes them for the remote shell, so `it's` and `$HOME`
  arrive as typed.
- One word is a shell command line, so pipes and `&&` work: `tnctl exec vision-station-01 -- 'df -h / | tail -1'`.

The device is the robot's server computer. The login user has passwordless sudo, and the robot's k3s is
`sudo k3s kubectl`.

```bash
tnctl exec vision-station-01 -- uname -a
tnctl exec vision-station-01 -- sudo k3s kubectl get pods -A
tnctl exec vision-station-01 -- sudo k3s kubectl -n kube-system get helmcharts
tnctl exec vision-station-01 -- 'journalctl -u k3s -n 20 --no-pager'
tnctl --device vision-station-01 exec -- hostname
```

| Flag | Description |
| --- | --- |
| `--timeout` | Stop the command after this long (exit code 4). Default: no limit. |

## Exit codes

The exit status of the remote command is the exit status of `tnctl exec`, whatever it is, and its output goes to
stdout and stderr as it comes. `<device>` is a device name or a robot name; the platform says which device
runs the command. `tnctl` itself exits 1 when the device cannot be reached (no active WireGuard tunnel, the
hub denies access, ssh cannot connect, which ssh reports as 255; a remote command that exits 255 looks the
same), 2 for a usage mistake (no `--`, no command), 3 for an unknown robot or device, 4 on `--timeout`, 6 when
you are not logged in. An exit code of the remote command can therefore look like one of these (3 to 9 are
`tnctl`'s own on the other commands); use `-o json` of the other commands, not `exec`, for machine decisions
about the platform.

Only the WireGuard tunnel is supported; a device that only has the legacy SSH tunnel is refused with the
reason. See [`wg status`](wg.md) to check a device's tunnel.

## See also

- [`ssh`](ssh.md) - an interactive shell.
- [`logs`](logs.md) - logs of a workload on a robot.
- [`config wait`](config.md#waiting-for-a-change) - uses the same channel to check that a change is applied.
