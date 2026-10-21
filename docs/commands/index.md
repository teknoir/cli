# Command Reference

`tnctl` groups its functionality into focused subcommands. Run any command with `--help` for inline
usage, or follow the links below for full documentation.

## At a glance

| Command | Aliases | Description |
| --- | --- | --- |
| [`login`](login.md) | | Log in to the Teknoir platform via OAuth2 device flow. |
| [`add`](add.md) | | Add a resource (currently `add domain`). |
| [`domain`](domain.md) | | Select the active domain. |
| [`ns`](ns.md) | | Select the active namespace from the teamspaces of your account. |
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

## Who may do what

`tnctl` asks the platform's fleet API for everything: the teamspaces your account is in, the devices of your
organisation units, and the device login. The platform decides; `tnctl` shows what it answers. What each command
needs in the teamspace:

| Command | Needs |
| --- | --- |
| `ns`, `devices`, `device`, `robots`, `robot`, `config get/diff/links`, `logs`, `wg status`, `port-forward <device>` (list services) | Any device role (viewer, developer or admin). |
| `config set/unset/delete/reset/try/untry/wait/promote`, `exec`, `port-forward` (forward a service over WireGuard) | Developer role; opening a session also needs permission to open terminals (`pods/exec`). |
| `ssh`, `bootstrap`, `ansible*`, legacy `port-forward`, `socks-proxy` | Developer role **and** permission to open terminals: they read the device login (user name, key, password) from the platform, which gives it only to those. A viewer gets a clear `403`. |

A device outside your organisation units does not exist for you: the platform answers `404`, the same as for
a name that is not there.

### Errors that name the cause

| You see | Cause | What helps |
| --- | --- | --- |
| `Your tnctl login has expired. Run: tnctl login` (exit 6, `-o json` code `session_expired`) | The platform refused the stored refresh token (revoked, user disabled, idle for more than the offline idle timeout, login service reset). | `tnctl login`. `tnctl login --check` tests the stored login. |
| `not logged in or token expired: run tnctl login` (exit 6) | No token, or the platform rejected it (HTTP 401). | `tnctl login`. |
| `not allowed for this account or teamspace: <reason>` (exit 6) | Your account is fine but may not do this (HTTP 403), for example a viewer asking for a device login (`need the developer role`). | **Logging in again does not help.** Ask a teamspace admin for the role the message names. |
| `the platform refused the request: not logged in, the token was rejected, or your account may not do this here` (exit 6) | An older gateway answers 401 and 403 alike with a redirect to the login page, so `tnctl` cannot tell which one it was. | `tnctl login` first; if that does not help, ask a teamspace owner. |
| `this platform does not support tnctl <version> yet` (exit 2) | The platform has no fleet API (production and airgapped installs on the classic portal). | Use `tnctl v0.0.9-beta` there (see [Installation](../installation.md#older-versions)) or ask your platform admin to update. |
| `too many requests` (HTTP 429, exit 9) | Device logins are limited to 30 calls per minute and user. | Wait the `Retry-After` time. |

Every request carries `User-Agent: tnctl/<version> (<os>/<arch>)`, so the platform's logs show which `tnctl`
made a call.

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
