# `tnctl socks-proxy`

Establish a SOCKS5 proxy through a device, giving you access to the device's entire network.

!!! warning "Legacy SSH tunnel only"
    `socks-proxy` works only over the legacy SSH tunnel (deprecated; it goes away after production has moved
    to WireGuard). On a device with an active WireGuard tunnel it fails with:

    ```text
    SOCKS is not available over WireGuard. Forward one of the device's services instead: tnctl port-forward <device> <service-id>
    ```

    List a device's services with `tnctl port-forward <device>`.

## Synopsis

```bash
tnctl socks-proxy [device] [port]
```

The device and/or port can be given positionally, or selected/defaulted automatically.

## Flags

| Flag | Shorthand | Description |
| --- | --- | --- |
| `--port` | `-p` | Local port for the SOCKS5 proxy. |

Plus all [global flags](index.md#global-flags).

## Argument handling

- A numeric positional argument is treated as the **port**; a non-numeric one as the **device name**.
- If no device is given (and `--device` is unset), an interactive picker is shown.
- If no port is given (via argument or `--port`), a random port between 8000 and 65000 is chosen.

## Examples

```bash
# Device + explicit port
tnctl socks-proxy my-device 1080

# Only a port (device picked interactively)
tnctl socks-proxy 1080

# Flag form
tnctl socks-proxy --port 1080
```

Press <kbd>Ctrl</kbd>+<kbd>C</kbd> to stop the proxy.

## Using the proxy

`tnctl` prints example browser commands, e.g. launching Chrome with an isolated profile:

```bash
google-chrome --proxy-server="socks5://127.0.0.1:1080" --user-data-dir=$(mktemp -d)
```

## Requirements

A set domain/namespace, an active legacy tunnel for the device, and local `ssh`/`ncat`. If the device has
no tunnel at all, the message is `Tunneling is not active for <device>. Turn it on under Tunneling on the
device page in the Teknoir console.` See **[Remote Access](../concepts/remote-access.md)**.

## See also

- [`port-forward`](port-forward.md) — forward a single port instead.
