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

### 2. Context Management

`tnctl` uses a persistent configuration to store your active domain, namespace, and device.

- **Switch Domain**:
  ```bash
  tnctl domain
  ```
- **Switch Namespace**:
  ```bash
  tnctl ns [namespace]
  ```
  If no namespace is provided, an interactive fuzzy-finder will appear.
- **Switch Device**:
  ```bash
  tnctl device [device]
  ```
  If no device is provided, an interactive fuzzy-finder will appear.

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
