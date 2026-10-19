# tnctl (Teknoir Control)

`tnctl` is the official command-line interface for managing Teknoir cloud resources and edge devices. It provides a seamless way to handle authentication, context switching, and secure remote access.

## Installation

### One-liner (macOS & Linux)

Download and install the latest binary automatically:

```bash
curl -sSL https://raw.githubusercontent.com/teknoir/cli/main/scripts/install.sh | sh
```

### Manual Download

Download the appropriate package for your system from the [GitHub Releases](https://github.com/teknoir/cli/releases) page. We provide:
- **Binaries**: `.tar.gz` (Linux/macOS) and `.zip` (Windows)
- **Linux Packages**: `.deb` (Debian/Ubuntu), `.rpm` (RHEL/CentOS), and `.apk` (Alpine)

### From source

Requires Go 1.23+

```bash
go install .
```

## Quick Start

### 1. Authentication

Log in to the Teknoir platform:

```bash
tnctl login
```

This will open your default browser to complete the login sequence. Use `--domain` if you are targeting a specific environment.

```bash
tnctl login --domain teknoir.cloud
```

### 2. Context & Device Management

`tnctl` uses a persistent configuration to store your active domain and namespace.

- **Switch Domain**:
  ```bash
  tnctl domain
  ```
- **Switch Namespace**:
  ```bash
  tnctl ns [namespace]
  ```
  If no namespace is provided, an interactive fuzzy-finder will appear.
- **Device Status**:
  ```bash
  tnctl device [device]
  ```
  Prints detailed status for a device, including resource usage and alerts. If no device is provided, an interactive fuzzy-finder will appear.
- **List Devices**:
  ```bash
  tnctl devices [labels...]
  ```
  Prints a summary list of all devices in the current namespace. You can filter by metadata labels using `key=value` pairs.
  ```bash
  tnctl devices env=prod region=us-east
  ```

### Robots and per-robot overrides

```bash
tnctl robots [label=value...] [-o json]        # list robots
tnctl robot vision-station-01                  # one robot
tnctl config get vision-station-01             # effective configuration, layer by layer
tnctl config set vision-station-01 notebook --image :pr-123 --wait   # try another image on this robot only
tnctl config set vision-station-01 notebook replicas=2 log.level=debug --wait=applied
tnctl config diff vision-station-01            # what differs from git
tnctl config try vision-station-01 apps/mock-vision feature-x        # another branch of an application
tnctl config reset vision-station-01 --all      # back to git
tnctl exec vision-station-01 -- sudo k3s kubectl get pods -A
tnctl logs vision-station-01 HelmChart/default/notebook -f
```

Overrides win over every git layer, need no commit and are published to the robot by the platform;
`--wait=published|applied|ready` follows them, `-o json` and fixed exit codes make them scriptable by agents.
See [`docs/commands/config.md`](docs/commands/config.md), [`robots`](docs/commands/robots.md),
[`exec`](docs/commands/exec.md) and [`logs`](docs/commands/logs.md).

### 3. Tunneling

Securely connect to your edge devices through their tunnel. `tnctl` picks the tunnel automatically:
WireGuard when the device has an active WireGuard tunnel, otherwise the legacy SSH tunnel (deprecated; this
fallback goes away after production has moved to WireGuard). `tnctl devices` shows the tunnel of each
device in the `Tunnel` column (`wireguard`, `legacy`, `off`, or the WireGuard state such as `enrolling`).
If a device has no tunnel, the commands say: `Tunneling is not active for <device>. It turns on by itself once the device is bootstrapped and online; if it stays off, a teamspace admin turned it off or the device is flagged. See: tnctl wg status <device>`

- **SSH into a device**:
  ```bash
  tnctl ssh [device-name]
  ```
  Over WireGuard the platform can cut access off at once if the device is flagged compromised or fails
  its health check.
- **Port Forwarding**:
  Forward a service of the device to a local port, and list a device's services:
  ```bash
  tnctl port-forward [device]                  # list the device's services
  tnctl port-forward [device] 8080:grafana     # [local-port:]service-id
  tnctl wg status [device]                     # check the WireGuard path
  ```
  Over the legacy SSH tunnel the target is `host:port` instead (`tnctl port-forward [device] 8080:localhost:80`).
- **SOCKS5 Proxy** (legacy SSH tunnel only; not available over WireGuard, forward a service instead):
  ```bash
  tnctl socks-proxy [device] 1080
  ```
- **Copy files**: `scp`/`rsync` over WireGuard use `tnctl wg-proxy` as ProxyCommand, see [`docs/commands/ssh.md`](docs/commands/ssh.md#copying-files-scp-and-rsync).
- **Ansible**:
  Use tnctl as the Ansible inventory (WireGuard where active, else the legacy SSH tunnel), see [`docs/commands/ansible.md`](docs/commands/ansible.md):
  ```bash
  tnctl -d teknoir.cloud -n teknoir-demo ansible all -m ping
  ```

### 4. Bootstrap a device

```bash
tnctl bootstrap my-device                      # writes bootstrap_{agent,server,docker_server}_my-device.sh
tnctl bootstrap my-device --k3s-version v1.34.11+k3s1   # install another k3s release
tnctl bootstrap my-robot --robot my-robot --role vision --agent-role io --server orin.local   # node roles: see docs/commands/bootstrap.md
```

The scripts install a **pinned k3s release** (`v1.36.5+k3s1` by default, the `stable` channel when this
was written and the release the CA tests run against) instead of whatever the `stable` channel happens
to be on the day the script runs. `--k3s-version` takes an exact release, or `stable` / `latest` for the
installer's own channels; at run time `INSTALL_K3S_VERSION=... sh bootstrap_server_my-device.sh` wins.
An agent must run the version of its server: generate both scripts with the same `--k3s-version`.
k3s cannot be downgraded, so do not pin below what a device already runs. The server script also
repairs the k3s CA of devices that were bootstrapped with an older script, see
[`docs/commands/bootstrap.md`](docs/commands/bootstrap.md) and [`docs/troubleshooting.md`](docs/troubleshooting.md).

## Global Flags

These flags are available to all subcommands:

- `-d, --domain`: The Teknoir domain (defaults to `teknoir.cloud`).
- `-n, --namespace`: Target namespace.
- `--device`: Target device ID.
- `--config`: Path to a specific config file (default `~/.tnctl.yaml`).
- `--debug`: Enable verbose debug output (on stderr).
- `-o, --output`: Output format of `robots`, `robot` and `config`: `table` (default), `json` or `yaml`.

## Configuration

The CLI stores its configuration at `~/.tnctl.yaml`. You can also use environment variables with the `TNCTL_` prefix:

```bash
export TNCTL_DOMAIN=teknoir.cloud
export TNCTL_NAMESPACE=my-namespace
```

## Using the templates from Go

The device bootstrap script templates are published as a separate, dependency-free Go module, so other
programs can render the same scripts `tnctl bootstrap` produces without pulling in the CLI's dependencies:

```sh
go get github.com/teknoir/cli/pkg/templates@<commit>
```

```go
import "github.com/teknoir/cli/pkg/templates"

script, err := templates.RenderServer(templates.TemplateData{Domain: "example.com", Namespace: "my-teamspace", DeviceID: "my-device" /* ... */})
```

`RenderServer` validates the input first (`ValidateTemplateData` rejects `"`, `` ` ``, `$` and `\` in every field,
and newlines in every field except `RSAPrivate` and `RSAPublic`). Errors name the field, never the value.
`TemplatesSHA256()` returns a fingerprint of all embedded templates, to detect a stale rendered script.
No tags are published yet, so pin a commit (`go get` resolves it to a pseudo-version).

## License

Apache License 2.0

# Upcoming Features
- [ ] helm add
- [ ] helm repo list
- [x] device <device name>
- [x] devices <metadata labels>
- [ ] device(s) add
- [ ] device(s) deploy
- [ ] device(s) apply
- [ ] device tunneling --enable/--disable
- [ ] device vpn (Wireguard tunneling)
- [ ] device top
- [ ] user (management)


## device apply
* `device <device name> apply <manifest file>`, and if `<device name>` left out of the command, the CLI will prompt you to select a device, and if `<manifest file>` is left out of the command, the CLI will prompt you to select a manifest file from local directory.
* Starting with a simple example:
  * `device apply <manifest file>`, where `<manifest file>` is a YAML file containing any set of supported K8s resources / manifests. Ex: `device apply deployment.yaml`
* Using `devices <metadata labels> apply <manifest file>`, apply the manifest file to all devices in the namespace selected by the current context in the CLI and the metadata labels, after the list of devices has been presented to the user and approved.

## device deploy
* `device <device name> deploy <app name>`, and if `<device name>` left out of the command, the CLI will prompt you to select a device, and if `<app name>` is left out of the command, the CLI will prompt you to select an app listed from the Backstage backend.
* An app is a K3s HelmChart, and you are able to override the default values.yaml file by passing in a custom values.yaml file and a name for the release.
  *  `device <device name> deploy <app name> --values <values.yaml file> --release <release name>`
* Before releasing the app, the CLI will render the HelmChart in pretty yaml and prompt you to confirm the release.
* An alternative `device <device name> deploy <app name> --values <values.yaml file> --release <release name>`
* Using `devices <metadata labels> deploy <app name> --values <values.yaml file> --release <release name>`, deploy the app to all devices in the namespace selected by the current context in the CLI and the metadata labels, after the list of devices has been presented to the user and approved.