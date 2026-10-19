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
| [`robots`](robots.md) | | List the robots of the teamspace, or show one (`robot <name>`). |
| [`config`](config.md) | | Read and override a robot's configuration: values, images, versions, revisions. |
| [`exec`](exec.md) | | Run a command on a device over WireGuard and pass its exit code through. |
| [`logs`](logs.md) | | Show the logs of a workload on a robot. |
| [`ssh`](ssh.md) | | Open an interactive SSH session on a device through its tunnel. |
| [`port-forward`](port-forward.md) | | Forward a local port to a service on a device (`port-forward <device>` lists its services). |
| [`wg status`](wg.md) | | Show whether and how a device is reachable through its WireGuard tunnel, and which services it publishes. |
| [`ansible`](ansible.md) | | Run `ansible` / `ansible-playbook` with the devices as inventory (also `ansible-inventory`). |
| [`socks-proxy`](socks-proxy.md) | | Establish a SOCKS5 proxy via a device (legacy SSH tunnel only). |
| [`bootstrap`](bootstrap.md) | | Generate device provisioning scripts. |

## Tunnels

`ssh`, `port-forward` and `socks-proxy` reach a device through its tunnel, chosen automatically: WireGuard
when the device has an active WireGuard tunnel, otherwise the legacy SSH tunnel (deprecated; the
fallback goes away after production has moved to WireGuard). The `Tunnel` column of `tnctl devices` shows
`wireguard`, `legacy`, `off`, or the WireGuard state (for example `enrolling`) while it is being set up.

## Global flags

Every command accepts these persistent flags:

| Flag | Shorthand | Description |
| --- | --- | --- |
| `--domain` | `-d` | Teknoir domain (default `teknoir.cloud`). |
| `--namespace` | `-n` | Target namespace. |
| `--device` | | Target device ID. |
| `--config` | | Path to config file (default `~/.tnctl.yaml`). |
| `--debug` | | Enable verbose debug output (on stderr). |
| `--output` | `-o` | Output format of `robots`, `robot` and `config`: `table` (default), `json` or `yaml`. |

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
