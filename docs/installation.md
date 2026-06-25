# Installation

`tnctl` ships as a single, statically-linked binary. Pick the method that suits your platform.

## One-liner (macOS & Linux)

Download and install the latest release automatically:

```bash
curl -sSL https://raw.githubusercontent.com/teknoir/cli/main/scripts/install.sh | sh
```

The script detects your OS and architecture, downloads the matching artifact from
[GitHub Releases](https://github.com/teknoir/cli/releases), and places `tnctl` on your `PATH`.

## Manual download

Grab the appropriate package for your system from the
[GitHub Releases](https://github.com/teknoir/cli/releases) page:

| Platform | Artifacts |
| --- | --- |
| Linux / macOS | `.tar.gz` |
| Windows | `.zip` |
| Debian / Ubuntu | `.deb` |
| RHEL / CentOS | `.rpm` |
| Alpine | `.apk` |

After extracting, move the `tnctl` binary into a directory on your `PATH` (for example `/usr/local/bin`).

=== "Linux / macOS"

    ```bash
    tar -xzf tnctl_*.tar.gz
    sudo mv tnctl /usr/local/bin/
    ```

=== "Debian / Ubuntu"

    ```bash
    sudo dpkg -i tnctl_*.deb
    ```

=== "RHEL / CentOS"

    ```bash
    sudo rpm -i tnctl_*.rpm
    ```

## From source

Requires **Go 1.23+** (this repository builds with Go 1.25).

```bash
git clone https://github.com/teknoir/cli.git
cd cli
go install .
```

This installs `tnctl` into your `$GOBIN` (usually `~/go/bin`). Ensure that directory is on your `PATH`.

## Verify the installation

```bash
tnctl --help
```

You should see the root help output listing the available commands. If the command is not found,
double-check that the install directory is on your `PATH`.

!!! tip "Shell completion"
    `tnctl` is built with [Cobra](https://github.com/spf13/cobra), so it inherits the standard
    `completion` command. Generate completions for your shell, e.g.:

    ```bash
    tnctl completion zsh > "${fpath[1]}/_tnctl"
    ```

## Next steps

Head over to **[Getting Started](getting-started.md)** to authenticate and run your first commands.
