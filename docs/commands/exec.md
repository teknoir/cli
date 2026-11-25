# `tnctl exec`

Run one command on a device over its WireGuard tunnel and pass its exit code through.

```bash
tnctl exec [--root] <device> -- <command...>
```

Not interactive: no terminal, `ssh` runs in BatchMode and never waits for a password. Use
[`tnctl ssh`](ssh.md) for a shell. The command goes after `--`.

- Several words are separate arguments; `tnctl` quotes them for the remote shell, so `it's` and `$HOME`
  arrive as typed.
- One word is a shell command line, so pipes and `&&` work: `tnctl exec vision-station-01 -- 'df -h / | tail -1'`.

`exec` needs the developer role in the teamspace and permission to open terminals: it reads the device login from
the platform, which gives it only to those (a viewer gets `not allowed for this account or teamspace`, exit 6).

The device is the robot's server computer, and the command runs as its login user. The robot's k3s needs
root: run it with `--root`, as `tnctl exec --root <device> -- k3s kubectl ...`.

```bash
tnctl exec vision-station-01 -- uname -a
tnctl exec --root vision-station-01 -- k3s kubectl get pods -A
tnctl exec --root vision-station-01 -- k3s kubectl -n kube-system get helmcharts
tnctl exec --root vision-station-01 -- 'journalctl -u k3s -n 20 --no-pager'
tnctl exec --root vision-station-01 -- k3s kubectl apply -f - < manifest.yaml
tnctl --device vision-station-01 exec -- hostname
```

| Flag | Description |
| --- | --- |
| `--root` | Run the command as root (`sh -c` through sudo). See [Running as root](#running-as-root). |
| `--timeout` | Stop the command after this long (exit code 4). Default: no limit. |

## Running as root

The login user becomes root through sudo. On most devices sudo asks for the login user's password: the
bootstrap gives the login passwordless sudo only when the platform holds no password for it, and the portal,
a robot and a virtual device all create one. So `tnctl exec <device> -- sudo ...` fails there with `sudo: a
password is required` (there is no terminal to ask on); it only works on a device with passwordless sudo.

`--root` works on both. `tnctl` already has the login's password (it comes with the device login, from the
same audited request) and hands it to sudo itself:

- A small wrapper runs as the login user and first asks `sudo -n true`. When that works (passwordless sudo,
  or sudo still remembers the password) no password is used at all; such a device behaves as before.
- Otherwise the wrapper gives the password to `sudo -v`, once, and then runs the command with `sudo -n`.
- The password travels on stdin of the ssh session, as its first line, and nowhere else: not in a command
  line, not in the environment, not in `--debug` output or a log. The wrapper always takes exactly that line,
  so what you pipe into `tnctl exec --root` reaches the command whole, and never reaches sudo's password
  prompt.

`--root` runs the command with `sh -c`, as root, with the environment sudo gives it. The same path is used by
[`logs`](logs.md) and by `--wait=applied|ready` of [`config`](config.md#waiting-for-a-change).

When root is not to be had, `tnctl` exits 1 and says which case it is; nothing is tried a second time:

| Message | Meaning |
| --- | --- |
| `sudo on <device> asks for a password, and the platform has none for the device login` | The login has no passwordless sudo and no password on the platform. Set the password in the portal to the device's, or re-bootstrap the device. |
| `... the platform's password for the device login has a line break or a NUL byte in it ...` | Such a password cannot be sent as one line; it is not sent at all. Change it. |
| `sudo on <device> did not accept the platform's password for the device login ...` | The password on the device is another one than the platform's (changed on the device, or the device was bootstrapped with an older one), or the login may not use sudo. It was sent once: every failed attempt counts against the login on a device that locks accounts. |
| `sudo on <device> accepted the password but asks for it again for the next command ...` | sudo keeps no ticket there (`timestamp_timeout=0`). `tnctl` cannot run root commands on that device; use [`ssh`](ssh.md). |
| `sudo is not installed on <device> ...` | No sudo on the login user's PATH. |

`tnctl` never changes the device for this: no sudoers file is written and no password is set.

## Exit codes

The exit status of the remote command is the exit status of `tnctl exec`, whatever it is, and its output goes to
stdout and stderr as it comes. `<device>` is a device name or a robot name; the platform says which device
runs the command. `tnctl` itself exits 1 when the device cannot be reached (no active WireGuard tunnel, the
hub denies access, ssh cannot connect, which ssh reports as 255; a remote command that exits 255 looks the
same) or when `--root` cannot become root, 2 for a usage mistake (no `--`, no command), 3 for an unknown robot or device, 4 on `--timeout`, 5 when
the device is disabled (`-o json` code `device_disabled`), 6 when you are not logged in. An exit code of the remote command can therefore look like one of these (3 to 9 are
`tnctl`'s own on the other commands); use `-o json` of the other commands, not `exec`, for machine decisions
about the platform.

Only the WireGuard tunnel is supported; a device that only has the legacy SSH tunnel is refused with the
reason. See [`wg status`](wg.md) to check a device's tunnel.

## See also

- [`ssh`](ssh.md) - an interactive shell.
- [`logs`](logs.md) - logs of a workload on a robot.
- [`config wait`](config.md#waiting-for-a-change) - uses the same channel to check that a change is applied.
