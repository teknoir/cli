# `tnctl robots` and `tnctl robot`

List the robots of the teamspace and show one. A robot is one edge computer, or a k3s cluster of
computers, with a Device behind it. Both commands read the fleet API of the platform, as you.

## `tnctl robots`

```bash
tnctl robots [label=value...] [-o json|yaml]
```

One row per robot: name, type, phase, ready/total computers, number of overrides (and how old the
oldest is) and the fleets it belongs to. Labels narrow the list; all must match.

```bash
tnctl robots
tnctl robots site=karlstad
tnctl -n teknoir-demo robots -o json
```

```text
NAME                TYPE    PHASE   NODES  OVERRIDES            FLEETS
vision-station-01   vision  Online  1/1    2 (oldest 2d)        test
amr-02              amr     Offline 0/0    -                    -
```

With no matching robot the exit code is 0 (a note goes to stderr; `-o json` prints `[]`), in every format.

## `tnctl robot`

```bash
tnctl robot <name> [-o json|yaml]
```

Shows the type, phase and its reason, the backing device, labels, fleets, computers with their k3s role
and state, configuration layers, the number of overrides and the number of effective resources.
The configuration itself is under [`tnctl config get`](config.md).

```bash
tnctl robot vision-station-01
tnctl robot vision-station-01 -o json | jq .phase
```

## Disable, enable or remove a robot

```bash
tnctl robot disable <robot> [--reason TEXT] [--wait] [--timeout DUR]
tnctl robot enable  <robot> [--wait] [--timeout DUR]
tnctl robot remove  <robot> --yes
```

A robot reaches the platform through its device: its MQTT key and sessions, and Tunneling. Disabling a robot
withdraws all three, so `ssh`, `exec`, `port-forward` and `socks-proxy` can no longer reach it. k3s and the
apps keep running on the hardware. Enabling it registers the MQTT key again and turns Tunneling back on; the
robot reconnects by itself within a few minutes.

Only a **teamspace admin** can disable, enable or remove robots. Everyone else gets `403` and exit code 6.

- `--reason TEXT` is optional. It must be one line of at most 200 characters; a longer reason, or one with a
  line break, is refused on the client with exit 2 and nothing is sent. The reason is stored with the
  disable: to change it, enable and disable again.
- `--wait` follows device-controller until it confirms the change (default `--timeout 3m`; exit 4 when it
  runs out — the change is made and continues). Exit 7 means device-controller reports it could not finish,
  for example because Tunneling could not be turned off. Enabling that fails reports exit 7 as well. An error the device still carries from before the disable (a failed enable) is not taken as
  final until it has lasted 15 seconds; an error device-controller reports for this disable ends the wait at once. Exit 8
  means another admin enabled the robot while a disable was being followed, or disabled it while an enable
  was.
- Disabling a robot that is already disabled, and enabling one that is already enabled, change nothing and
  exit 0; `-o json` reports `"result":"noop"`.
- `remove` removes the robot and, when the device is its own, the device with its keys, MQTT access and
  tunnels. Without `--yes` it prints what would be removed to stderr and exits 2; nothing is removed. k3s
  and the apps keep running on the hardware until you uninstall them.

When the robot runs on a device that belongs to another robot (the platform returns `deviceOwner`), disable,
enable and the device's removal are that robot's. `robot disable` and `robot enable` refuse with exit 5 and
name the owner robot; `robot remove` removes only the robot and keeps the device. `tnctl robot <robot>` shows
a `Device owner` row in that case.

!!! note
    A robot literally named `disable`, `enable` or `remove` cannot be reached as `tnctl robot <name>`: the
    subcommand is found first. Reach it with `tnctl robot -- <name>`.

```bash
tnctl robot disable vision-station-01 --reason "Lost or stolen"
tnctl robot disable vision-station-01 --wait
tnctl robot enable vision-station-01 --wait
tnctl robot remove vision-station-01 --yes
```

With `-o json`, disable and enable print one object of tnctl's own:
`{"robot":"<r>","device":"<d>","result":"ok"|"noop","disabled":{...}}` (`disabled` is absent once the
device is enabled). Remove prints `{"robot":"<r>","device":"<d>","deviceDeleted":true,"result":"ok"}`.

## Output and exit codes

`-o json` and `-o yaml` print the platform's answer unchanged (also fields this version of tnctl does not
know), except `disable`, `enable` and `remove`, which print the object described above. Data goes to stdout,
everything else to stderr. With `-o json`, errors are
`{"error":{"code":"not_found","message":"..."}}` on stderr.

| Exit | Meaning |
| --- | --- |
| 0 | ok |
| 1 | error |
| 2 | usage |
| 3 | not found (`tnctl robot`: no such robot) |
| 4 | timeout: `--wait` ran out of time (the change is made and continues) |
| 5 | rejected: the robot's device belongs to another robot, or the platform refused the change |
| 6 | not logged in or token rejected (run `tnctl login`), or not allowed: disable, enable and remove need a teamspace admin, and logging in again does not help |
| 7 | device-controller reports it could not finish the disable or the enable (`--wait`) |
| 8 | conflict: another admin enabled the robot while `--wait` followed a disable, or disabled it while `--wait` followed an enable |

A missing or rejected login comes back from the platform as a redirect to the login page; `tnctl` does not
follow it and tells you to run `tnctl login` (exit 6).

## See also

- [`config`](config.md) - read and override a robot's configuration.
- [`devices`](devices.md) - the same teamspace seen as plain devices.
