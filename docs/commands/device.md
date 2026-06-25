# `tnctl device`

Print detailed status for a single device in the current namespace.

**Alias:** `dev`

## Synopsis

```bash
tnctl device [device]
```

- With a device name, fetches and prints that device's status.
- Without a name, lists devices in the namespace and opens an interactive fuzzy-finder to choose one.

## Requirements

A domain and namespace must be set (via flags or config). Otherwise:

```text
domain and namespace are required. Please set them via flags or config
```

## Output

Renders a color-coded status view including device details and health/alerts.

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
