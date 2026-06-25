# Command Reference

`tnctl` groups its functionality into focused subcommands. Run any command with `--help` for inline
usage, or follow the links below for full documentation.

## At a glance

| Command | Aliases | Description |
| --- | --- | --- |
| [`login`](login.md) | | Log in to the Teknoir platform via OAuth2 device flow. |
| [`add`](add.md) | | Add a resource (currently `add domain`). |
| [`domain`](domain.md) | | Select the active domain. |
| [`ns`](ns.md) | | Select the active namespace. |
| [`device`](device.md) | `dev` | Print detailed status for a device. |
| [`devices`](devices.md) | `devs` | Print a summary list of devices in the namespace. |
| [`ssh`](ssh.md) | | Open an interactive SSH session on a device. |
| [`port-forward`](port-forward.md) | | Forward a local port to the device or its network. |
| [`socks-proxy`](socks-proxy.md) | | Establish a SOCKS5 proxy via a device. |
| [`bootstrap`](bootstrap.md) | | Generate device provisioning scripts. |

## Global flags

Every command accepts these persistent flags:

| Flag | Shorthand | Description |
| --- | --- | --- |
| `--domain` | `-d` | Teknoir domain (default `teknoir.cloud`). |
| `--namespace` | `-n` | Target namespace. |
| `--device` | | Target device ID. |
| `--config` | | Path to config file (default `~/.tnctl.yaml`). |
| `--debug` | | Enable verbose debug output. |

## Interactive selection

Many commands accept an optional positional argument (e.g. a device or namespace name). **If you omit
it**, `tnctl` launches an interactive fuzzy-finder so you can pick from live results — start typing to
filter, press <kbd>Enter</kbd> to select.

!!! tip
    Combine interactive selection with the saved context for the fastest workflow:

    ```bash
    tnctl ns        # pick a namespace
    tnctl ssh       # pick a device and connect
    ```
