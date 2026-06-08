# Project Guidelines: tnctl

## Identity
- **Binary Name**: `tnctl`
- **Module Name**: `teknoir/cli`
- **Purpose**: A command-line interface for managing Teknoir cloud resources and edge devices.

## Development Rules
- **Modern Go**: Use Go 1.23+ features. Follow "Go Modern Language Guidelines".
- **CLI Framework**: Always use `spf13/cobra` for commands and `spf13/viper` for configuration.
- **Binary Naming**: Ensure all documentation and scripts refer to the tool as `tnctl`.

## Domain Handling
- When a user provides a base domain (e.g., `teknoir.cloud`) via the `--domain` or `-d` flag, the CLI should internally derive the authentication endpoint by prefixing it with `auth.` (e.g., `auth.teknoir.cloud`).

## Configuration
- Default configuration file: `~/.tnctl.yaml`.
- Use Viper to manage precedence: Flags > Environment Variables > Config File > Defaults.
