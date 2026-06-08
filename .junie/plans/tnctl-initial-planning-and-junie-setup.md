---
sessionId: session-260519-140748-rvfz
isActive: true
---

# Requirements

### Overview & Goals
The goal is to bootstrap the first version of the `tnctl` CLI tool using the latest Go version (1.23+) and industry-standard libraries (`cobra`, `viper`). This version will focus on establishing the project structure, configuration management, and a robust CI/CD pipeline. Per user instructions, this version will stop at providing a functional root command that displays help and global flags, but will lay the foundation for future features like Keycloak authentication and device management.

### Scope
- **Project Identity**: Standardize the binary name as `tnctl`.
- **Legacy Cleanup**: Remove all Firebase-related code and dependencies to start fresh.
- **Golang Upgrade**: Update `go.mod` to Go 1.23.
- **CLI Skeleton**: Implement the `tnctl` root command using `spf13/cobra` with persistent global flags.
- **Configuration Management**: Use `spf13/viper` to automatically create and manage a YAML configuration file (`~/.tnctl.yaml`) on first run.
- **CI/CD Pipeline**: Setup GitHub Actions with GoReleaser for multi-architecture builds (Linux, Darwin, Windows; amd64, arm64).
- **Distribution**: Provide a shell script for easy `curl | sh` installation.
- **Junie Alignment**: Initialize `.junie/AGENTS.md` and specialized skills for CLI development.
- **Documentation**: Refocus the root `README.md` on the CLI tool and move legacy script documentation.

### User Stories
- **Self-Configuration**: As a user, I want `tnctl` to automatically create a default configuration file on its first run so I don't have to manually set it up.
- **Global Identity**: As a user, I want to specify the target domain (e.g., `teknoir.cloud`) via a `-d` or `--domain` flag, knowing it will correctly map to the authentication endpoint (e.g., `auth.teknoir.cloud`).
- **Device Context**: As a user, I want to specify a namespace (`-n`) and device ID (`--device`) globally so that future subcommands can use these values automatically.
- **Ease of Install**: As a developer, I want to install `tnctl` via a simple one-liner `curl` command.
- **Broad Compatibility**: As a user on different platforms (Mac, Linux, Windows), I want to download pre-built binaries for my architecture.

# Technical Design

### Current Implementation
- Entry point is `main.go` using a local `firebaselogin` package.
- Auth is hardcoded to Firebase.
- No formal CLI framework or configuration management.
- `README.md` is a mix of legacy bash script docs and Firebase examples.

### Key Decisions
- **Go Version**: Update to 1.23 to leverage the latest language features and security updates.
- **CLI Framework**: `spf13/cobra` will be used for command structure. All future subcommands will be added under `cmd/`.
- **Configuration Management**: `spf13/viper` will handle reading from `~/.tnctl.yaml`, environment variables, and flags (in that order of precedence).
- **Binary Name**: The project will build as `tnctl`.
- **Authentication**: Keycloak is the chosen provider for future implementation. The current Firebase implementation will be completely removed.
- **Domain Handling**: The `--domain` (or `-d`) flag will accept the base domain. The CLI will internally prefix it with `auth.` for authentication purposes as per user requirement.
- **Global Flags**: `--domain`, `--namespace`, and `--device` will be persistent flags on the root command, making them available to all future subcommands.
- **Installation Path**: The install script will default to `/usr/local/bin/tnctl` but allow overrides.

### Proposed Architecture
- `cmd/root.go`: Root command definition, global flags initialization, and Viper config setup.
- `pkg/config/`: Configuration data models and default values.
- `.github/workflows/release.yml`: GitHub Actions workflow.
- `.goreleaser.yaml`: GoReleaser configuration.
- `scripts/install.sh`: Installation script.

### File Structure
```text
.
├── .github/
│   └── workflows/
│       └── release.yml     # GitHub Actions workflow for GoReleaser
├── cmd/
│   └── root.go             # Root command, global flags, and Viper init
├── pkg/
│   └── config/
│       └── config.go       # Configuration models and default values
├── scripts/
│   ├── install.sh          # One-liner installation script
│   └── README.md           # Documentation for legacy scripts
├── .goreleaser.yaml        # GoReleaser configuration for multi-arch builds
├── .junie/
│   ├── AGENTS.md           # Project guidelines and binary naming rules
│   └── skills/
│       └── cli-command/
│           └── SKILL.md    # Instructions for adding new Cobra commands
├── main.go                 # Minimal entry point
├── go.mod                  # Updated module definition (Go 1.23)
└── README.md               # Refocused CLI documentation
```

# Testing

### Validation Approach
- Verify `tnctl` creates `~/.tnctl.yaml` on first run.
- Verify `tnctl --help` displays correct flags.
- Verify the installation script correctly identifies platform and downloads the binary (mocked or dry-run).
- Run `goreleaser release --snapshot --rm-dist` to verify builds.

### Key Scenarios
1. **Bootstrap**: Run `tnctl` and verify it prints help and creates a config file.
2. **Flags**: Verify `-d`, `-n`, and `--device` are accepted.
3. **CI/CD**: Verify GitHub Actions workflow is valid.

# Delivery Steps

###   Step 1: Project Cleanup and Junie Initialization
The project is cleaned of legacy code and aligned with Junie's development standards.
- Delete the `firebaselogin/` directory and remove all Firebase references from `main.go`.
- Update `go.mod` to specify `go 1.23` and remove unused dependencies.
- Create `.junie/AGENTS.md` containing the project's identity, naming conventions, and domain handling rules.
- Create `.junie/skills/cli-command/SKILL.md` to guide future agents in adding new CLI commands.

###   Step 2: Core CLI and Configuration Bootstrap
The `tnctl` root command is functional and automatically manages its configuration.
- Implement the root command in `cmd/root.go` using `spf13/cobra`.
- Add persistent global flags: `--domain` (`-d`), `--namespace` (`-n`), and `--device`.
- Configure `spf13/viper` to initialize from `~/.tnctl.yaml`.
- Implement logic to create a default YAML config file if it does not exist on first run.

###   Step 3: CI/CD and Distribution Setup
The project supports automated multi-architecture releases and easy installation.
- Create `.goreleaser.yaml` to build for Linux, Darwin, and Windows (amd64 and arm64 architectures).
- Add `.github/workflows/release.yml` to trigger GoReleaser on tag pushes.
- Create `scripts/install.sh` that detects the user's OS/Arch and downloads the latest release from GitHub.

###   Step 4: Documentation Overhaul
The project's documentation is professional and focused on the new CLI tool.
- Rewrite `README.md` to focus exclusively on `tnctl` installation, configuration, and help.
- Ensure the root README includes a "Quick Start" guide and describes the global flags.
- Move existing bash script documentation from the root `README.md` to `scripts/README.md`.