# FAQ

??? question "What is `tnctl`?"
    `tnctl` (Teknoir Control) is the official command-line interface for managing Teknoir cloud
    resources and edge devices — authentication, context switching, fleet inspection, and secure
    remote access.

??? question "What does the `-d`/`--domain` flag do?"
    It selects the Teknoir environment, e.g. `teknoir.cloud`. The CLI derives the authentication
    endpoint by prefixing the domain with `auth.` (so `auth.teknoir.cloud`).

??? question "Where are my credentials stored?"
    In `~/.tnctl.yaml`, under a per-domain entry in the `auths` map. Tokens are refreshed
    automatically when they expire. See **[Configuration](configuration.md)**.

??? question "How do I work with multiple environments?"
    Add each environment with `tnctl add domain <domain>`, log in to each, and switch between them
    with `tnctl domain`. Each domain remembers its own active namespace.

??? question "What settings take precedence?"
    Flags > environment variables (`TNCTL_*`) > config file > defaults. See
    **[Configuration](configuration.md#precedence)**.

??? question "Do I need anything besides the binary for remote access?"
    A local `ssh` client. Tunneling must be on for the device (see Tunneling on the device page in the
    Teknoir console). Only the legacy SSH tunnel, a deprecated fallback, also needs `ncat` (from Nmap). See **[Remote Access](concepts/remote-access.md)**.

??? question "Can I script `tnctl` non-interactively?"
    Yes. Provide explicit arguments (device/namespace names) and flags to skip the interactive
    fuzzy-finder, and rely on environment variables (`TNCTL_DOMAIN`, `TNCTL_NAMESPACE`, `TNCTL_DEVICE`).

??? question "How do I get shell completions?"
    `tnctl` is built with Cobra, so it provides a `completion` subcommand for bash, zsh, fish, and
    PowerShell.

??? question "Where can I report issues or contribute?"
    On the [GitHub repository](https://github.com/teknoir/cli).
