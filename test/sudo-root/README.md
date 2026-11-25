# Real-sudo tests of the root wrapper

`tnctl exec --root`, `tnctl logs` and `--wait=applied|ready` run commands as root on a device through one
small wrapper script (`rootWrapper` in `cmd/devexec_root.go`), in two steps:

1. `sudo -n` alone (mode `try`). A device whose sudo needs no password for the command answers here, and the
   password never leaves `tnctl`.
2. Only when sudo says that a password is required: the same wrapper in mode `password`. `tnctl` sends the
   password as the first stdin line; the wrapper reads that line, asks PAM's helper (`unix_chkpwd`) whether it
   is right, gives it to `sudo -S -v` once, checks that sudo kept the ticket (`sudo -n -v`) and runs the
   command with `sudo -n`. The session remembers that sudo asks, so later commands start at this step.

The root shell prints a marker line on stderr before it runs the script. That line, and nothing in the
script's output or exit status, tells `tnctl` whether sudo let the command through; the payload on stdin is
sent only after it. The wrapper prints a marker line before each sudo whose words matter, too: sudo's reason
is read from the lines after those markers and from nowhere else, so what ssh, its proxy or the login shell
print is never taken for sudo's answer. And an ssh that ends with 255 before the script started is "the
device was not reached", whatever was printed.

What this promises depends on how sudo, PAM, the shells and sshd really behave, so it is tested against the
real ones: a throwaway container with sshd and one login user per sudo state. No device is involved.

```
test/sudo-root/run.sh            # build the image, start the container, run the tests, remove it
test/sudo-root/run.sh -v         # go test flags are passed on
test/sudo-root/run.sh -run 'TestSudoRig(WrongPassword|NoTicket)'
test/sudo-root/run.sh --matrix   # the four device systems below, one after the other
test/sudo-root/run.sh --clean    # remove the images run.sh built (it keeps them, to start faster)
TN_SUDO_RIG_BASE=ubuntu:22.04 test/sudo-root/run.sh
TN_SUDO_RIG_BASE=ubuntu:26.04 TN_SUDO_RIG_SUDO=sudo-rs test/sudo-root/run.sh
```

| `TN_SUDO_RIG_BASE` | `TN_SUDO_RIG_SUDO` | Why |
| --- | --- | --- |
| `debian:trixie-slim` (default) | `sudo` | The base of Teknoir virtual devices: sudo 1.9.16, dash as `sh`, OpenSSH 10. |
| `ubuntu:20.04` | `sudo` | sudo 1.8.31, the oldest sudo here. JetPack 5 (L4T r35) is built on this Ubuntu. |
| `ubuntu:22.04` | `sudo` | sudo 1.9.9, which takes a `sudo -n` that needs a password to PAM: `pam_faillock` counts it as a failed login. JetPack 6 (L4T r36) is built on this Ubuntu. |
| `ubuntu:26.04` | `sudo-rs` | The sudo of Ubuntu 25.10 and later: other messages, no `logfile`, `log_input`, `requiretty` or `timestamp_type` (those tests are skipped). |

No Jetson was tested: the L4T rows above are the Ubuntu release each JetPack is built on, and the sudo
version is that of the Ubuntu image (read from `sudo --version` in the container), not of an L4T image.

Needs `docker`, `go`, `ssh` and `ssh-keygen`; about 1 minute per system and 5 for `ubuntu:22.04` once its image is built (the first
build pulls the base image and installs sudo, zsh and openssh-server). The tests are the `TestSudoRig*` tests
in `cmd/devexec_root_rig_test.go`. They are part of the normal package and are **skipped** unless `run.sh`
has set the `TN_SUDO_RIG_*` variables, so `scripts/test` and CI need no Docker. The same cases run in the
unit suite against stand-ins for ssh, sudo and PAM's helper (`cmd/devexec_root_test.go`); this rig is the
proof that the stand-ins behave like the real thing.

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
| `gl-<shell>` | as `pw`, with `timestamp_type=global` (sudo only) | a ticket that is still valid when the next call comes |
| `sh-np` | as `pw`, plus `NOPASSWD: /usr/bin/sh` | a rule under which `sudo -n true` asks for a password and the real command does not |
| `k3-np` | as `pw`, plus `NOPASSWD: /usr/local/bin/k3s` | a device set up for `sudo -n k3s` only: `sh` needs the password |
| `id-pw` | only `/usr/bin/id`, with the password | sudoers that do not allow `sh`: sudo takes the password and then refuses |
| `id-np` | only `NOPASSWD: /usr/bin/id` | the same without a password: `sudo -v` has nothing to check |
| `ns` | not in the sudoers | a login that may not use sudo |
| `rt` | as `pw`, with `requiretty` (sudo only) | a sudo that wants a terminal |
| `li-pw`, `li-np` | password and passwordless sudo with `log_input` (sudo only) | a sudo that reads its stdin for the input log |
| `bs` | as `pw`; `sh` is bash for this login (its own `sh` first in `PATH`) | a device whose `sh` is bash, which execs the last command of a `-c` script |
| `fl-a`, `fl-b`, `fl-c` | as `pw` | the logins the `pam_faillock` tests count and lock |
| `tp`, `rp` | as `pw`, with `targetpw` and with `rootpw` | sudoers under which sudo asks for root's password, not the login's |

sudo logs to `/var/log/sudo.log` in the container (sudo-rs has no such log); the tests read it to see what
sudo was asked and to count failed password attempts. `pam_faillock` is set up with its defaults (3 failed
logins within 15 minutes lock the account for 10) and without the `account required pam_faillock.so` line,
so nothing but a password that sudo accepts clears the count; the tests read it with `faillock --user`. No
base image sets `pam_faillock` up by itself: the rig does, to measure what a hardened device would count.

## What is tested

Over the real `ssh` client with the arguments of a device session (no terminal, BatchMode, ControlMaster)
minus the WireGuard ProxyCommand.

| Test | Proves |
| --- | --- |
| `PasswordSudo` | Password sudo works: the script runs as root, its exit status and output are its own. Several calls over one multiplexed connection. A manifest on stdin arrives whole and unchanged; a script that reads no stdin and a call without stdin work. Seven root commands ask sudo without a password once (the first), and `pam_faillock` holds nothing afterwards. The master does not outlive the session. |
| `BashAsSh` | The same on a device whose `sh` is bash: the last sudo still finds the ticket of `sudo -v` (it would not without the `exit` after it). |
| `PasswordlessSudo` | A NOPASSWD login works with the right password, a wrong one and none, and never gets the password: sudo ran the scripts and nothing else. |
| `WrongPassword` | Five `tnctl` runs with a wrong password, a payload behind it that starts with the right one: PAM's helper refuses it, sudo is never given a password, the script does not run, more calls on the session do not reach the device. `pam_faillock`'s count after each run is asserted, not reset: 0, 0, 0, 0, 0 with every sudo but 1.9.9, and **1, 2, 3, 3, 3 with sudo 1.9.9** (the one `sudo -n` of each run): the third run locks the login, and the right password is then refused too (exit 95) until the lock is cleared. |
| `WithoutThePAMHelper` | Without the helper the wrong password goes to sudo once: 1 attempt in sudo's log, 2 failed logins for `pam_faillock` (1 with sudo-rs), no payload line used as a guess; the message says "another password, or locked". With sudo 1.9.9 that is 3 with the `sudo -n` before it: this one run locks the login, the right password is refused, and without the helper that is reported as "another password, or locked" as well. Once unlocked the right password works through sudo alone. |
| `LockedLogin` | A login that `pam_faillock` has locked: the right password is refused by sudo, which is reported as a locked login (exit 95), not as a rejected password, and adds nothing to the count; without the helper the same lock is reported as "another password, or locked" (exit 92). The lock stops sudo with the password, also typed by hand; the ssh key login still works. It works again when the lock is cleared. |
| `RefusedTryAndFaillock` | What a `sudo -n` that needs a password costs: 0 failed logins, 1 with sudo 1.9.9. A session asks that way once however many commands it runs, and an accepted password clears the count. |
| `Sudo199EveryRunIsAFailedLoginAndThreeLock` | The lock-out arithmetic, asserted on every system (it is 0 and "not locked" everywhere but sudo 1.9.9). Runs without a password on the platform: 1, 2, 3, 3, then the right password is refused as locked. Three runs with the right password whose first step comes before any of them gives the password (three `tnctl` started at once): 3, all three refused as locked, sudo logs 3 incorrect attempts; one after the other the same runs leave 0. With `account required pam_faillock.so` five runs with a wrong password never get past 1. |
| `TargetpwAndRootpw` | `targetpw`, `rootpw`: sudo asks for root's password. `tnctl` says so (not "locked"), once per session; the login's password was offered to sudo once: 1 incorrect attempt in sudo's log, 2 failed logins against root (1 with sudo-rs, one more with sudo 1.9.9), none against the login. A password that is not the login's never reaches sudo. |
| `SSHItselfFails` | ssh ends with 255 before anything runs on the device (a ProxyCommand that prints one of sudo's sentences and gives up, as wg-proxy does with "Your tnctl login has expired"; a port nobody listens on): `StreamRoot` and `RunRoot` both answer "cannot reach", never a sudo refusal; the session keeps nothing, sudo was not asked, and the same session works when the connection is back. |
| `NoPassword` | sudo asks and the platform has no password, or one with a line break: nothing is sent to sudo, not even an empty line. |
| `NoTicket` | `timestamp_timeout=0`: the wrapper reports it with its own exit status (93) and message; the script does not run. |
| `ValidTicket` | sudo is still open from an earlier call: a second session that holds a wrong password runs anyway, the password is not sent, the payload is whole. |
| `InputLog` | `log_input`: 2000 lines on stdin all reach the script, through a session (also when the first `sudo -n` is refused) and straight through the wrapper with the payload already waiting on stdin. sudo logged the input; the password is not in it. |
| `CommandRules` | `sh-np` runs without any password being used; `k3-np` uses the password; `id-pw`, `id-np`, `ns` and `rt` end with sudo's own reason (not allowed, still asks, not in the sudoers, requiretty), never with "rejected", the script does not run, and the refusal is final for the session. With `rt` the password is never sent. Failed logins per run with sudo 1.9.9: `ns` 1, `id-np` 2, the others 0. |
| `OutputOfTheScriptIsItsOwn` | A script that prints `sudo: a password is required` and the wrapper's marker and exits 1 or 92 ran once; it is not run again with the password and the session stays usable. |
| `Locale` | The script gets the login's `LC_ALL` although every sudo runs under `LC_ALL=C`. |
| `TwoCallsFromOneParent` | Two wrapper calls from one parent process on the device, each with its own stdin, for password, global-ticket and NOPASSWD users: both payloads whole, the password in neither. |

In every test the password must not appear in the command's stdout or stderr, in `--debug` output, in the
error text or in sudo's logs.

## Why the wrapper looks the way it does

Found with this rig (sudo 1.8.31, 1.9.9 and 1.9.16p2, sudo-rs 0.2.13):

- **The payload must not share a stdin with `sudo -S`.** `printf 'pw\npayload' | sudo -S cmd` hands the
  password line to `cmd` whenever sudo does not need it (NOPASSWD, a valid ticket), and with a wrong
  password sudo reads the payload's lines as two more guesses. The wrapper reads the line itself and gives
  it to `sudo -S -v` on a pipe of its own.
- **The last sudo must not be exec'ed, must not be the last command, and `sudo -v` must not be in `$(...)`.**
  Without a terminal sudo keeps the ticket per parent process. `sudo -v` and the final `sudo -n` must
  therefore be children of the same shell. `exec sudo -n ...` replaces that shell, bash and zsh exec the last
  command of a `-c` script by themselves, and a command substitution is a shell of its own; either way
  the final sudo answers `a password is required`. Hence `sudo -n sh -c ...` followed by `exit $?`, and
  what `sudo -v` says goes to stderr between two marker lines instead of into a variable.
- **No probe.** `sudo -n true` before the command asks the wrong question (`sh-np`: it needs a password for
  `true`, not for `sh`), costs a failed login per call with sudo 1.9.9 and `pam_faillock`, and with
  `log_input` and the payload on its stdin it takes the payload. The command itself is the question.
- **`sudo -n -v`, not `sudo -n true`, asks "did sudo keep the ticket?"** after `sudo -S -v`. It never goes to
  PAM while the ticket is there, and it tells `nt` (no ticket) from `id-pw` (ticket, but `sh` not allowed).
  As the first question it would be wrong: a login with a NOPASSWD rule that is also in group `sudo` needs
  a password for `-v`.
- **An empty line is a failed attempt.** `printf '\n' | sudo -S -v` logs `1 incorrect password attempt`, so
  nothing is sent when there is no password.
- **One wrong password given to sudo is 2 failed logins for `pam_faillock`** (sudo asks PAM again when its
  input has ended; 1 with sudo-rs), and 3 lock the account. `unix_chkpwd`, which `pam_unix` itself runs for
  a process that is not root, answers 7 for a wrong password and counts nothing, so the wrapper asks it
  first and gives sudo only a password it did not refuse. That saves those 2; it does not make a run with
  a wrong password free on sudo 1.9.9 (see the arithmetic below).
- **Only sudo's own lines are sudo's answer.** stderr before the script starts is also where ssh, its
  ProxyCommand and the login shell write. `Your tnctl login has expired. Run: tnctl login` from the proxy has
  "has expired" in it, which read as a locked login; a line with "a password is required" in it made the
  session send the password. The wrapper therefore marks where each sudo starts to speak, "a password is
  required" must be sudo's whole line, and an ssh exit status of 255 before the script started is never
  read at all.
- **sudo's prompt says whose password it wants.** With `targetpw` or `rootpw` the helper finds the login's
  password right and sudo refuses it, which looked like a locked login. The wrapper gives `sudo -S -v` a
  prompt with `%u` and `%p` in it on a marker line; when the two differ, sudo asked for another user's
  password. That is only known after the login's password was offered once.
- **A script's output says nothing about sudo.** `sudo: a password is required` on stderr with exit 1 is
  also what a script can print. If that were read as sudo's refusal the script would run a second time, with
  the password. The root shell's marker line settles it.

## The lock-out arithmetic

Failed logins that one `tnctl` run (one session: one `exec --root`, one `logs`, one `--wait`) leaves with
`pam_faillock` (deny=3, no `account` line), as the tests assert them:

| sudo | right password | wrong, helper | wrong, no helper | no platform password | `targetpw`/`rootpw` (against root) |
| --- | --- | --- | --- | --- | --- |
| 1.8.31, 1.9.16p2 | 0 | 0 | 2 | 0 | 2 |
| sudo-rs 0.2.13 | 0 | 0 | 1 | 0 | 1 |
| 1.9.9 | 1 until sudo accepts the password, then 0 | 1 | 3 (locked) | 1 | 3 |

With sudo 1.9.9 the first step of every run, the `sudo -n` that finds out whether sudo asks, is a failed
login. `sudo -n -v`, `sudo -n -l` and `sudo -n true` count the same there (only `sudo -V` does not), so there
is no free way to ask; the session at least asks once, not once per command. Hence, with sudo 1.9.9 and
`pam_faillock`:

- every run that sudo does not accept a password from adds 1, and 3 such runs lock the login: with a wrong
  password although the helper keeps it from sudo, and with no password on the platform at all;
- 3 runs with the right password that start together lock it too (3 times `sudo -n` before the first
  password); one after the other they do not;
- a locked login gets no further failed logins from `tnctl`, the lock ends after `unlock_time`, and it stops
  only sudo with the login's password: the ssh key login still works, and nothing here touches MQTT, the
  TOE or WireGuard, which do not use the login;
- with `account required pam_faillock.so` every ssh key login clears the count, so runs one after the other
  never add up.

Known limits:

- sudo 1.9.9 with `pam_faillock`: the arithmetic above. `tnctl` cannot avoid the first `sudo -n` without
  either sending the password to a device whose sudo does not ask for it or remembering across runs that a
  device asks; neither is done.
- A login whose rules name single NOPASSWD commands (`id-np`) costs two such questions per run with sudo
  1.9.9; `tnctl` cannot run root commands there anyway.
- A locked login cannot be told from a wrong password without the helper: both are "sudo did not accept",
  and the message names both.
- `targetpw`, `rootpw`, `runaspw`: that sudo wants another user's password is only known from its prompt,
  after the login's password was offered once (failed logins against root, as in the table). A sudo whose
  PAM modules bring their own prompt does not print the wrapper's; such a device is then reported as locked
  (helper) or as "another password, or locked" (no helper). `runaspw` was not run in the rig.
- Whatever runs as the device login user can read the password on its way to sudo (a shell start file runs
  before the wrapper; a `sudo` earlier in `PATH`).
- No Jetson (L4T) was tested, see the table of systems.
