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

## Output and exit codes

`-o json` and `-o yaml` print the platform's answer unchanged (also fields this version of tnctl does not
know). Data goes to stdout, everything else to stderr. With `-o json`, errors are
`{"error":{"code":"not_found","message":"..."}}` on stderr.

| Exit | Meaning |
| --- | --- |
| 0 | ok |
| 1 | error |
| 2 | usage |
| 3 | not found (`tnctl robot`: no such robot) |
| 6 | not logged in, token rejected or not allowed: run `tnctl login` |

A missing or rejected login comes back from the platform as a redirect to the login page; `tnctl` does not
follow it and tells you to run `tnctl login` (exit 6).

## See also

- [`config`](config.md) - read and override a robot's configuration.
- [`devices`](devices.md) - the same teamspace seen as plain devices.
