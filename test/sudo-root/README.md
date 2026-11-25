# Real-sudo tests of the root wrapper

`tnctl exec --root`, `tnctl logs` and `--wait=applied|ready` run commands as root on a device through one
small wrapper script (`rootWrapper` in `cmd/devexec_root.go`). The wrapper reads the first stdin line (the
login user's password, or an empty line), decides what sudo needs, and only then lets the command read the
rest of stdin.

What it promises depends on how sudo, the shells and sshd really behave, so it is tested against the real
ones: a throwaway Debian trixie container (the base of Teknoir virtual devices: sudo 1.9.16, dash as `sh`,
OpenSSH 10) with sshd and one login user per sudo state and login shell. No device is involved.

```
test/sudo-root/run.sh            # build the image, start the container, run the tests, remove it
test/sudo-root/run.sh -v         # go test flags are passed on
test/sudo-root/run.sh -run 'TestSudoRig(WrongPassword|NoTicket)'
```

Needs `docker`, `go`, `ssh` and `ssh-keygen`; about 15 s once the image is built (the first build pulls
`debian:trixie-slim` and installs sudo, zsh and openssh-server). The tests are the `TestSudoRig*` tests in
`cmd/devexec_root_rig_test.go`. They are part of the normal package and are **skipped** unless `run.sh` has
set the `TN_SUDO_RIG_*` variables, so `scripts/test` and CI need no Docker. The same cases run in the unit
suite against stand-ins for ssh and sudo (`cmd/devexec_root_test.go`); this rig is the proof that the
stand-ins behave like the real thing.

`run.sh` makes a new ssh key and a random password for every run (with quotes, `$`, `\n`, `%s` and spaces in
it), publishes sshd on a random port of 127.0.0.1 only, pins the container's host key, and removes the
container and the key when it ends.

## Users

`setup.sh` creates them in the container. `<shell>` is `bash` or `zsh`, the user's login shell.

| User | sudo | Stands for |
| --- | --- | --- |
| `pw-<shell>` | in group `sudo`: `%sudo ALL=(ALL:ALL) ALL`, asks for the password | the normal device: bootstrapped with a password (portal, robot, virtual device) |
| `np-<shell>` | group `sudo` plus the drop-in `#<uid> ALL=(ALL:ALL) NOPASSWD: ALL` | a login without a password on the platform (`90-teknoir-platform-user`), an os-teknoir image |
| `nt-<shell>` | as `pw`, with `timestamp_timeout=0` | a hardened sudo that keeps no ticket |
| `gl-<shell>` | as `pw`, with `timestamp_type=global` | a ticket that is still valid when the next call comes |

sudo logs to `/var/log/sudo.log` in the container; the tests read it to count failed password attempts.

## What is tested

Every case runs for both login shells, over the real `ssh` client with the arguments of a device session
(no terminal, BatchMode, ControlMaster) minus the WireGuard ProxyCommand.

| Test | Proves |
| --- | --- |
| `PasswordSudo` | Password sudo works: the script runs as root, its exit status and output are its own. Several calls over one multiplexed connection (an ssh master is checked to be running). A manifest on stdin after the password arrives whole and unchanged; a script that reads no stdin and a call without stdin work. No failed attempt is logged. The master does not outlive the session. |
| `PasswordlessSudo` | A NOPASSWD login works with the right password, a wrong one and none: the password line is sent, is not given to sudo (no `validate` entry in sudo's log) and does not reach the command. |
| `WrongPassword` | With a payload of several lines behind a wrong password (the payload even starts with the right one): exactly one failed attempt in sudo's log, the script does not run, no payload line is used as a guess. More calls on the session do not reach sudo again. |
| `NoPassword` | sudo asks and the platform has no password, or one with a line break: nothing is sent to sudo, not even an empty line. |
| `NoTicket` | `timestamp_timeout=0`: the wrapper reports it with its own exit status (93) and message. |
| `ValidTicket` | sudo is still open from an earlier call: a second session that holds a wrong password runs anyway, no attempt is made, the payload is whole. |
| `TwoCallsFromOneParent` | Two wrapper calls from one parent process on the device, each with its own stdin, for password, global-ticket and NOPASSWD users: both payloads whole, the password in neither. |

In every test the password must not appear in the command's stdout or stderr, in `--debug` output, in the
error text or in sudo's log.

## Why the wrapper looks the way it does

Found with this rig (sudo 1.9.16p2):

- **The payload must not share a stdin with `sudo -S`.** `printf 'pw\npayload' | sudo -S cmd` hands the
  password line to `cmd` whenever sudo does not need it (NOPASSWD, a valid ticket), and with a wrong
  password sudo reads the payload's lines as two more guesses. The wrapper reads the line itself and gives
  it to `sudo -S -v` on a pipe of its own.
- **The last sudo must not be exec'ed, and must not be the last command.** Without a terminal sudo keeps the
  ticket per parent process. `sudo -v` and the final `sudo -n` must therefore be children of the same shell.
  `exec sudo -n ...` replaces that shell, and bash and zsh exec the last command of a `-c` script by
  themselves; either way the final sudo answers `a password is required`. Hence `sudo -n sh -c "$1"` followed
  by `exit $?`.
- **`sudo -n true`, not `sudo -n -v`, asks "is sudo open?".** A login with a NOPASSWD rule that is also in
  group `sudo` needs a password for `-v`.
- **An empty line is a failed attempt.** `printf '\n' | sudo -S -v` logs `1 incorrect password attempt`, so
  nothing is sent when there is no password.
