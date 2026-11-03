# `tnctl device`

Print detailed status for a single device in the current namespace.

**Alias:** `dev`

## Synopsis

```bash
tnctl device [device]
```

- With a device name, fetches and prints that device's status.
- Without a name, lists devices in the namespace and opens an interactive fuzzy-finder to choose one.

A device outside your organisation units, or one that does not exist, is a `404`.

## Requirements

A domain and namespace must be set (via flags or config). Otherwise:

```text
domain and namespace are required. Please set them via flags or config
```

## Output

Renders a color-coded status view including device details and health/alerts. When the device is disabled,
the `Tunnel` row shows the disabled state in red and an `Access` row says who disabled it, when and why
(`disabled`, `disabling`, `enabling`, `not fully disabled` and `enabling failed`).

## Disable, enable or remove a device

```bash
tnctl device disable <device> [--reason TEXT] [--wait] [--timeout DUR]
tnctl device enable  <device> [--wait] [--timeout DUR]
tnctl device remove  <device> --yes
```

Disabling a device withdraws its MQTT key, closes its MQTT sessions and turns Tunneling off, so `ssh`,
`exec`, `port-forward` and `socks-proxy` can no longer reach it. Its apps and k3s keep running. Enabling it
registers the key again and turns Tunneling back on; it reconnects by itself within a few minutes.

Only a **teamspace admin** can disable, enable or remove devices. Everyone else gets `403` and exit code 6.

- `--reason TEXT` is optional: one line of at most 200 characters. A longer reason, or one with a control
  character, is refused on the client with exit 2 and nothing is sent.
- `--wait` follows device-controller until it confirms the change (default `--timeout 3m`; exit 4 when it
  runs out — the change is made and continues). Exit 7 means device-controller reports it could not finish,
  for example because Tunneling is still on; a failed enable reports exit 7 too. An error the device still carries from before the disable (a failed enable) is not taken as
  final until it has lasted 15 seconds; an error device-controller reports for this disable ends the wait at once. Exit 8 means another admin
  enabled the device while a disable was being followed, or disabled it while an enable was.
- Disabling an already disabled device, and enabling an already enabled one, change nothing and exit 0
  (`noop` in `-o json`).
- `remove` is only for a device that backs **no** robot. A device that backs one is refused with exit 5 and
  the platform's message: remove the robot instead, because deleting only the device makes the robot create
  it again with new keys. Without `--yes` nothing is removed: tnctl prints what would go to stderr and exits
  2. The removal deletes the device's keys, MQTT access and tunnels and cannot be undone.

!!! note
    A device literally named `disable`, `enable` or `remove` cannot be reached as `tnctl device <name>`: the
    subcommand is found first. Reach it with `tnctl device -- <name>`, or pick it with `tnctl device` (no
    argument).

```bash
tnctl device disable vision-01 --reason "Lost or stolen"
tnctl device enable vision-01 --wait
tnctl device remove spare-01 --yes
```

With `-o json`, disable and enable print one object of tnctl's own:
`{"device":"<d>","result":"ok"|"noop","disabled":{...}}` (`disabled` is absent once the device is enabled).
Remove prints `{"device":"<d>","deviceDeleted":true,"result":"ok"}`.

## Exit codes of disable, enable and remove

| Exit | Meaning |
| --- | --- |
| 0 | ok (also when the device already was in the requested state) |
| 1 | error |
| 2 | usage: bad arguments, a bad `--reason`, `remove` without `--yes`, or the platform is too old for these commands |
| 3 | not found: no such device |
| 4 | timeout: `--wait` ran out of time (the change is made and continues) |
| 5 | rejected: the device backs a robot (`remove`), or the platform refused the change |
| 6 | not logged in, token rejected, or not a teamspace admin |
| 7 | device-controller reports it could not finish the disable or the enable (`--wait`) |
| 8 | conflict: another admin enabled the device while `--wait` followed a disable, or disabled it while `--wait` followed an enable |

## Examples

```bash
# Detailed status for a specific device
tnctl device edge-01

# Pick a device interactively
tnctl dev
```

## See also

- [`devices`](devices.md) — summary list of the whole fleet.
- [`ssh`](ssh.md) — connect to the device.
