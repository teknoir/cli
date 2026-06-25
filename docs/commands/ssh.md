# `tnctl ssh`

Open an interactive SSH session on an edge device through Teknoir's secure proxy.

## Synopsis

```bash
tnctl ssh [device-name]
```

The device may be given as an argument, via the `--device` global flag, or selected interactively if
omitted.

## Requirements

- A domain and namespace must be set.
- **Remote Access** must be enabled for the device in the Teknoir Console, otherwise:

  ```text
  remote access is not enabled for device <name>. Please enable it in the Teknoir Console
  ```

- Local `ssh` and `ncat` must be installed.

## How it works

`tnctl` fetches the device's credentials, writes the decoded RSA key to a temporary `0600` file, and
launches `ssh` through the namespace deadend proxy (`deadend-<namespace>.<domain>`) wrapped in TLS via
`ncat --ssl`. The temporary key is removed on exit. If the device exposes a password, it is printed
once for convenience. See **[Remote Access](../concepts/remote-access.md)** for the full transport.

## Examples

```bash
# Connect to a named device
tnctl ssh edge-01

# Use the --device flag
tnctl --device edge-01 ssh

# Pick interactively
tnctl ssh
```

!!! tip
    Add `--debug` to print the exact `ssh` command being executed.

## See also

- [`port-forward`](port-forward.md), [`socks-proxy`](socks-proxy.md)
- [Remote Access](../concepts/remote-access.md)
