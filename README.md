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

### 3. Remote Access

Securely connect to your edge devices via Teknoir's proxy infrastructure.

- **SSH into a device**:
  ```bash
  tnctl ssh [device-name]
  ```
- **Port Forwarding**:
  Forward a local port to the device or its network:
  ```bash
  tnctl port-forward [device] 8080:localhost:80
  ```
- **SOCKS5 Proxy**:
  Establish a SOCKS5 proxy to access the device's entire network:
  ```bash
  tnctl socks-proxy [device] 1080
  ```

### 4. Bootstrap a device

```bash
tnctl bootstrap my-device                      # writes bootstrap_{agent,server,docker_server}_my-device.sh
tnctl bootstrap my-device --k3s-version v1.34.11+k3s1   # install another k3s release
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
- `--debug`: Enable verbose debug output.

## Configuration

The CLI stores its configuration at `~/.tnctl.yaml`. You can also use environment variables with the `TNCTL_` prefix:

```bash
export TNCTL_DOMAIN=teknoir.cloud
export TNCTL_NAMESPACE=my-namespace
```

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