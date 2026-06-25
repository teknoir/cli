# Troubleshooting

Quick fixes for the most common issues. Add `--debug` to almost any command for verbose output,
including resolved URLs and the exact `ssh` invocation used.

## Authentication

??? failure "`no credentials found for domain <domain>. Please log in first`"
    You haven't authenticated against the active domain. Run:

    ```bash
    tnctl login --domain <domain>
    ```

??? failure "Browser doesn't open during `tnctl login`"
    `tnctl` falls back to printing a verification URL and one-time code — open the URL manually and
    enter the code. On headless machines this is expected behavior.

??? failure "Tokens seem expired"
    `tnctl` refreshes access tokens automatically using the stored refresh token. If refresh fails
    (e.g. the refresh token was revoked), simply run `tnctl login` again.

## Context

??? failure "`domain and namespace are required. Please set them via flags or config`"
    Set them once via the interactive commands, or pass flags:

    ```bash
    tnctl domain
    tnctl ns
    # or
    tnctl devices --domain teknoir.cloud --namespace my-namespace
    ```

??? failure "`no domains configured. Use 'tnctl add domain <domain>' to add one`"
    Add and switch to a domain first:

    ```bash
    tnctl add domain teknoir.cloud
    ```

## Devices & remote access

??? failure "`remote access is not enabled for device <name>`"
    Enable Remote Access for the device in the Teknoir Console, then retry `ssh`/`port-forward`/`socks-proxy`.

??? failure "`no devices found in namespace <ns>`"
    Confirm you're in the right namespace (`tnctl ns`) and that the namespace actually contains
    devices (`tnctl devices`). If filtering, double-check your `key=value` labels.

??? failure "SSH / proxy connection hangs or fails"
    - Ensure local `ssh` and `ncat` (Nmap) are installed and on your `PATH`.
    - Re-run with `--debug` to see the full `ssh` command and any `ncat`/proxy errors.
    - Verify network access to `deadend-<namespace>.<domain>:2222`.

## Configuration

??? question "Where is my config?"
    `~/.tnctl.yaml` by default. Override with `--config /path/to/file.yaml`. See
    **[Configuration](configuration.md)**.

??? question "How do I reset everything?"
    Delete `~/.tnctl.yaml` (a fresh default file is recreated on next run) and `tnctl login` again.
