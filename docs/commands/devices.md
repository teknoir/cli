# `tnctl devices`

Print a summary list of all devices in the current namespace, optionally filtered by metadata labels.

**Alias:** `devs`

## Synopsis

```bash
tnctl devices [metadata labels...]
```

Each label is a `key=value` pair; multiple labels narrow the results (AND semantics).

The platform filters: you see the devices of your organisation units in the teamspace, and the label filter is
applied by the platform too (at most 20 labels).

The columns come from the platform: `Tunnel` is `wireguard`, `legacy`, `off` or the WireGuard state while it is
being set up (for example `enrolling`), `CPU`, `Memory` and `Storage` are what the device last reported, `Alerts`
and `Apps` are counts.

## Requirements

A domain and namespace must be set (via flags or config), otherwise the command errors out asking you
to provide them.

## Examples

```bash
# All devices in the current namespace
tnctl devices

# Filter by labels
tnctl devices env=prod region=us-east

# Short alias
tnctl devs env=staging
```

## Errors

- Invalid label format (missing `=`):

  ```text
  invalid label format: <arg>. Expected key=value
  ```

- No matching devices: a clear message indicates whether labels were applied.

## See also

- [`device`](device.md) — detailed status for a single device.
