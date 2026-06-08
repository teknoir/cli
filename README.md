# tnctl (Teknoir Control)

`tnctl` is the official command-line interface for managing Teknoir cloud resources and edge devices.

## Installation

### One-liner (macOS & Linux)
```bash
curl -sSL https://raw.githubusercontent.com/teknoir/cli/main/scripts/install.sh | sh
```

### From source
Requires Go 1.23+
```bash
go install .
```

## Quick Start

On first run, `tnctl` will create a default configuration file at `~/.tnctl.yaml`.

### Authentication
Log in to the Teknoir platform:
```bash
tnctl login
```
This will open your default browser to complete the login sequence.

### View Help
```bash
tnctl --help
```

### Global Flags
These flags are available to all subcommands:
- `-d, --domain`: The Teknoir domain (defaults to `teknoir.cloud`).
- `-n, --namespace`: Target namespace (defaults to `default`).
- `--device`: Target device ID.
- `--config`: Path to a specific config file.

## Configuration
The CLI uses `viper` for configuration management. It looks for a file named `.tnctl.yaml` in your home directory.
This file stores your preferences and authentication tokens (`access_token`, `refresh_token`).
You can also set environment variables with the `TNCTL_` prefix (e.g., `TNCTL_DOMAIN=example.cloud`).

Precedence:
1. Flags
2. Environment Variables
3. Config File
4. Defaults

## Distribution
`tnctl` is automatically built for multiple architectures (Linux, Darwin, Windows) using GoReleaser. Releases can be found on the [GitHub Releases](https://github.com/teknoir/cli/releases) page.
