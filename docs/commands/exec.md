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
| `--root` | Run the command as root (`sh -c` through sudo, with the platform's password for the device login when sudo asks for one). See [Running as root](#running-as-root). |
| `--timeout` | Stop the command after this long (exit code 4). Default: no limit. |

## Running as root

The login user becomes root through sudo. On most devices sudo asks for the login user's password: the
bootstrap gives the login passwordless sudo only when the platform holds no password for it, and the portal,
a robot and a virtual device all create one. So `tnctl exec <device> -- sudo ...` fails there with `sudo: a
password is required` (there is no terminal to ask on); it only works on a device with passwordless sudo.

`--root` works on both. `tnctl` already has the login's password (it comes with the device login, from the
same audited request) and gives it to sudo itself, but only when sudo asks for it:

1. The command first runs with `sudo -n`, which never asks, and nothing else. When that works (passwordless
   sudo, or sudo still remembers the login) that is all: **the password is not sent to the device at all**,
   and such a device behaves as before.
2. Only when sudo answers `a password is required` does `tnctl` run the command again, this time with the
   password. **The password is sent as the first line of the command's stdin, over the encrypted ssh
   session**, and nowhere else: not in a command line, not in the environment, not in `--debug` output or a
   log. A small wrapper on the device reads exactly that line. From then on every root command of the same
   `tnctl` run carries the password; sudo is not asked without it a second time.
3. The wrapper first lets PAM's own helper (`unix_chkpwd`, what the login itself is checked with) look at the
   password. A password that is wrong is refused there, and sudo never sees it, so it is not counted as a
   failed login by a device that locks accounts (`pam_faillock`). Without the helper, or for a login that is
   not in the device's `/etc/passwd`, sudo decides.
4. Then the wrapper gives the password to `sudo -v`, once, and runs the command with `sudo -n`.

What you pipe into `tnctl exec --root` reaches the command whole: it is sent only after the command has
started as root, so it never meets sudo's password prompt, and a sudo that logs input (`log_input`) logs it
once, for the command.

`--root` runs the command with `sh -c`, as root, with the environment sudo gives it. sudo itself runs with
`LC_ALL=C` so that `tnctl` can read what it says; the command gets the login's own `LC_ALL` back. The same
path is used by [`logs`](logs.md) and by `--wait=applied|ready` of [`config`](config.md#waiting-for-a-change).

When root is not to be had, `tnctl` exits 1 and says which case it is. Whatever the case, nothing is tried a
second time by the same `tnctl` run:

| Message | Meaning |
| --- | --- |
| `sudo on <device> asks for a password, and the platform has none for the device login` | The login has no passwordless sudo and no password on the platform. Set the password in the portal to the device's, or re-bootstrap the device. |
| `... the platform's password for the device login has a line break or a NUL byte in it ...` | Such a password cannot be sent as one line; it is not sent at all. Change it. |
| `sudo on <device> did not accept the platform's password for the device login ...` | The password on the device is another one than the platform's (changed on the device, or the device was bootstrapped with an older one). |
| `sudo on <device> refused the device login although the platform's password for it is right ...` | The password is the device's, and sudo still said no: the login is locked after failed attempts (`pam_faillock`), or expired. Try again when the lock has ended. |
| `sudo on <device> accepted the password but asks for it again for the next command ...` | sudo keeps no ticket there (`timestamp_timeout=0`). `tnctl` cannot run root commands on that device; use [`ssh`](ssh.md). |
| `sudo on <device> only works on a terminal (requiretty in its sudoers) ...` | `tnctl exec` has no terminal. Use [`ssh`](ssh.md), or take `requiretty` out of the device's sudoers. The password was not sent. |
| `the device login user may not use sudo on <device> ...` | The login is not in the device's sudoers. |
| `the sudoers on <device> do not let the device login user run sh as root ...` | The login may run some commands through sudo, but not `sh`, and `--root` runs every command as `sh -c`. |
| `sudo on <device> accepted the device login and still asks for a password for the command ...` | The same, for a login whose rules name single commands without a password. Run the allowed command yourself: `tnctl exec <device> -- sudo -n <command>`. |
| `sudo on <device> did not run the command: <what sudo said>` | Another refusal of sudo's, in its own words. |
| `sudo is not installed on <device> ...` | No sudo on the login user's PATH. |

`tnctl` never changes the device for this: no sudoers file is written and no password is set.

What a password that the device does not accept costs there: with PAM's helper (every Debian, Ubuntu and
L4T has it) nothing that counts; the helper writes one line to the auth log. Without it the password goes to
sudo once: one line in sudo's log, and 2 failed logins for `pam_faillock` where a device has that set up (1
with sudo-rs), whose defaults lock the account at 3. One known limit: sudo 1.9.9 (Ubuntu 22.04) hands even
a `sudo -n` that needs a password to PAM, so with `pam_faillock` there the first root command of every
`tnctl` run counts as one failed login, which an accepted password clears again (and a wrong password on
such a device without the helper is then 3: the login is locked for `pam_faillock`'s unlock time). Measured with
`test/sudo-root` in the `cli` repository (Debian 13, Ubuntu 20.04 and 22.04, sudo-rs on Ubuntu 26.04).

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
