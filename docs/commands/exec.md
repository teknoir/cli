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
   password. A password that is wrong is refused there, and sudo is never given it. Without the helper, or
   for a login that is not in the device's `/etc/passwd`, sudo decides.
4. Then the wrapper gives the password to `sudo -v`, once, and runs the command with `sudo -n`.

Step 1 is not free on every device: see [Failed logins](#failed-logins-and-pam_faillock) before you run
`tnctl` in a loop against a device that locks accounts.

What you pipe into `tnctl exec --root` reaches the command whole: it is sent only after the command has
started as root, so it never meets sudo's password prompt, and a sudo that logs input (`log_input`) logs it
once, for the command.

`--root` runs the command with `sh -c`, as root, with the environment sudo gives it. sudo itself runs with
`LC_ALL=C` so that `tnctl` can read what it says; the command gets the login's own `LC_ALL` back. The same
path is used by [`logs`](logs.md) and by `--wait=applied|ready` of [`config`](config.md#waiting-for-a-change).

`tnctl` reads sudo's answer only from what sudo itself wrote, and only when ssh reached the device. When ssh
ends with 255 before the command has started, the device was not reached: `tnctl` exits 1 with `cannot reach
<device> over WireGuard: <what ssh or its proxy said>`, for example `Your tnctl login has expired. Run: tnctl
login`. That is never taken for sudo's refusal, and nothing about it is remembered.

When root is not to be had, `tnctl` exits 1 and says which case it is. Whatever the case, nothing is tried a
second time by the same `tnctl` run:

| Message | Meaning |
| --- | --- |
| `sudo on <device> asks for a password, and the platform has none for the device login` | The login has no passwordless sudo and no password on the platform. Set the password in the portal to the device's, or re-bootstrap the device. |
| `... the platform's password for the device login has a line break or a NUL byte in it ...` | Such a password cannot be sent as one line; it is not sent at all. Change it. |
| `sudo on <device> did not accept the platform's password for the device login (the password on the device is another one) ...` | PAM's helper said so: the password on the device is another one than the platform's (changed on the device, or the device was bootstrapped with an older one). sudo was not given it. |
| `sudo on <device> did not accept the platform's password for the device login: the password on the device is another one, or the login is locked after failed attempts ...` | sudo refused the password, and PAM's helper had not said whether it is right (the device has none, the login is not in its `/etc/passwd`, or the helper could not tell). sudo refuses a locked login in the same words as a wrong password, so it is one of the two. If the password is right, wait for the lock to end (below). |
| `sudo on <device> refused the device login although the platform's password for it is right ...` | PAM's helper found the password right, and sudo still said no: the login is locked after failed attempts (`pam_faillock`), or expired. Try again when the lock has ended (below). |
| `sudo on <device> asks for the password of <user>, not for the device login's ...` | The device's sudoers have `targetpw`, `rootpw` or `runaspw`: sudo wants another user's password (root's), which the platform does not have. `tnctl` cannot run root commands on that device; use [`ssh`](ssh.md). The login's password was offered to sudo once before sudo's prompt said so. |
| `sudo on <device> accepted the password but asks for it again for the next command ...` | sudo keeps no ticket there (`timestamp_timeout=0`). `tnctl` cannot run root commands on that device; use [`ssh`](ssh.md). |
| `sudo on <device> only works on a terminal (requiretty in its sudoers) ...` | `tnctl exec` has no terminal. Use [`ssh`](ssh.md), or take `requiretty` out of the device's sudoers. The password was not sent. |
| `the device login user may not use sudo on <device> ...` | The login is not in the device's sudoers. |
| `the sudoers on <device> do not let the device login user run sh as root ...` | The login may run some commands through sudo, but not `sh`, and `--root` runs every command as `sh -c`. |
| `sudo on <device> accepted the device login and still asks for a password for the command ...` | The same, for a login whose rules name single commands without a password. Run the allowed command yourself: `tnctl exec <device> -- sudo -n <command>`. |
| `sudo on <device> did not run the command: <what sudo said>` | Another refusal of sudo's, in its own words (the last line it wrote). |
| `sudo is not installed on <device> ...` | No sudo on the login user's PATH. |

`tnctl` never changes the device for this: no sudoers file is written and no password is set.

### Failed logins and `pam_faillock`

A device that locks accounts after failed logins (`pam_faillock`; Debian, Ubuntu and the bootstrap do not
set it up, a hardened device may have it) counts what `tnctl` does there. Its defaults: 3 failed logins
within 15 minutes lock the login for 10 minutes. This is what one `tnctl` run (one `exec --root`, one `logs`,
one `--wait=applied|ready`, however many root commands it sends) adds to that count, measured with
`test/sudo-root` in the `cli` repository:

| sudo on the device | The password is right | Wrong, PAM's helper says so | Wrong, no helper | No password on the platform |
| --- | --- | --- | --- | --- |
| 1.8.31 (Ubuntu 20.04), 1.9.16 (Debian 13) | 0 | 0 | 2 | 0 |
| sudo-rs 0.2.13 (Ubuntu 26.04) | 0 | 0 | 1 | 0 |
| **1.9.9 (Ubuntu 22.04)** | 1, cleared when sudo accepts the password: 0 after the run | **1** | **3: locked by one run** | **1** |

sudo 1.9.9 is the exception: it takes even a `sudo -n` that needs a password to PAM, which counts it as a
failed login, and step 1 above is such a `sudo -n`. No question to that sudo is free (`sudo -n -v`, `sudo -n
-l` and `sudo -n true` all count), so `tnctl` cannot find out whether it asks without it. A run asks that way
once, and only a password that sudo accepts clears the count. On a device with **sudo 1.9.9 and
`pam_faillock`** that means:

- **Every `tnctl` run that sudo does not accept a password from is one failed login, and the third such run
  locks the login.** That is so with a wrong password on the platform (also when PAM's helper keeps it from
  sudo), with no password on the platform, and with a login that is not in the sudoers. Three `tnctl logs`
  in a row with a stale password are enough.
- **Three runs with the right password that start at the same moment lock it too:** each asks sudo before any
  of them has given the password. One after the other they are no problem (each clears its own count).
- A wrong password without the helper locks the login in one run (1 + 2).

With the other sudos only a wrong password that reaches sudo counts (no helper): 2 per run (1 with sudo-rs),
so the second such run (the third with sudo-rs) reaches the 3 that lock. With `targetpw` or `rootpw` the
login's password is offered to sudo once as root's: 2 failed logins held against root (3 with sudo 1.9.9, 1
with sudo-rs), none against the login. `pam_faillock` does not lock root for them unless the device sets
`even_deny_root`.

What a lock stops is the login's **password**, for `pam_faillock`'s unlock time after the last failed login:
`tnctl` root commands (the messages above), `sudo` typed by a person in [`ssh`](ssh.md), and anything else
that gives sudo that password. A refused attempt during the lock adds nothing to it. It ends by itself (with
`unlock_time=0` it does not, per `pam_faillock`'s manual: root then has to clear it with `faillock --user
<login> --reset`). It does not stop the login's ssh key: `tnctl ssh` and `tnctl exec` without `--root` still
work. And it does not touch the device's connection to the platform: MQTT, the TOE and WireGuard do not use
the login or its password.

A device whose `pam_faillock` also has the `account required pam_faillock.so` line of its manual clears the
count at every login that gets in, the ssh key login of each `tnctl` run included. Runs one after the other
then never add up, and a lock (three runs that started together) is gone at the next run.

PAM's helper is part of PAM on Debian and Ubuntu; a wrong password that it refuses is not a failed login.
None of this was tested on a Jetson: JetPack 5 is built on Ubuntu 20.04 (sudo 1.8.31) and JetPack 6 on
Ubuntu 22.04 (sudo 1.9.9), so a JetPack 6 device with `pam_faillock` is expected to behave like the 1.9.9
row.

## Exit codes

The exit status of the remote command is the exit status of `tnctl exec`, whatever it is, and its output goes to
stdout and stderr as it comes. `<device>` is a device name or a robot name; the platform says which device
runs the command. `tnctl` itself exits 1 when the device cannot be reached (no active WireGuard tunnel, the
hub denies access, ssh cannot connect, which ssh reports as 255; a remote command that exits 255 looks the
same, except with `--root`, where `tnctl` knows whether the command started) or when `--root` cannot become
root, 2 for a usage mistake (no `--`, no command), 3 for an unknown robot or device, 4 on `--timeout`, 5 when
the device is disabled (`-o json` code `device_disabled`), 6 when you are not logged in. An exit code of the remote command can therefore look like one of these (3 to 9 are
`tnctl`'s own on the other commands); use `-o json` of the other commands, not `exec`, for machine decisions
about the platform.

Only the WireGuard tunnel is supported; a device that only has the legacy SSH tunnel is refused with the
reason. See [`wg status`](wg.md) to check a device's tunnel.

## See also

- [`ssh`](ssh.md) - an interactive shell.
- [`logs`](logs.md) - logs of a workload on a robot.
- [`config wait`](config.md#waiting-for-a-change) - uses the same channel to check that a change is applied.
