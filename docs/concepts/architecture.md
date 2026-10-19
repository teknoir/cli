# Architecture

`tnctl` is a Go application built on [Cobra](https://github.com/spf13/cobra) (commands) and
[Viper](https://github.com/spf13/viper) (configuration). It acts as a thin, secure client over
Teknoir's cloud APIs and edge-access infrastructure.

## High-level overview

```mermaid
flowchart TD
    subgraph Local
        CLI[tnctl CLI]
        CFG[(~/.tnctl.yaml)]
    end

    subgraph Teknoir Cloud
        KC[Keycloak<br/>auth.&lt;domain&gt;]
        API[Backstage fleet API<br/>&lt;domain&gt;/api/device/fleet]
        DE[Deadend Proxy<br/>deadend-&lt;ns&gt;.&lt;domain&gt;]
    end

    EDGE[Edge Device]

    CLI <-->|read/write| CFG
    CLI -->|OAuth2 device flow| KC
    CLI -->|REST with bearer token| API
    CLI -->|SSL tunnel + SSH| DE
    DE --> EDGE
```

## Source layout

| Path | Responsibility |
| --- | --- |
| `main.go` | Entry point; calls `cmd.Execute()`. |
| `cmd/` | One file per Cobra command (`login`, `ns`, `device`, `ssh`, …). |
| `cmd/root.go` | Root command, persistent flags, config initialization. |
| `pkg/config` | Loading/saving `~/.tnctl.yaml`, domain & namespace resolution. |
| `pkg/auth` | Token validation and refresh. |
| `pkg/api` | HTTP client (User-Agent, 401/403 handling) and the fleet API: teamspaces, devices, device logins, robots, configuration. |
| `pkg/templates` | Text templates for the `bootstrap` provisioning scripts. |
| `pkg/ui` | Terminal rendering of device status and summaries. |

## Request lifecycle

For a typical data command (e.g. `tnctl devices`):

```mermaid
sequenceDiagram
    participant U as User
    participant C as tnctl
    participant Cfg as config
    participant A as auth
    participant API as Cloud API

    U->>C: tnctl devices
    C->>Cfg: Load() (~/.tnctl.yaml)
    C->>A: GetValidToken(domain, auth)
    A-->>C: valid access token (refreshes if needed)
    C->>Cfg: Save() refreshed token (if changed)
    C->>API: GET devices (Bearer token)
    API-->>C: device list / details
    C-->>U: rendered table / status
```

## Authentication

- Uses the OAuth2 **Device Authorization Flow** with PKCE (`S256`).
- Issuer URL: `https://auth.<domain>/auth/realms/<realm>` (default realm `master`).
- Default client ID: `teknoir-cli`.
- Tokens are persisted per-domain under `auths` in the config and **refreshed automatically** when
  expired; refreshed tokens are written back to the config.

## Edge access

Remote-access commands (`ssh`, `port-forward`, `socks-proxy`) do not talk to a custom protocol — they
build an `ssh` invocation that tunnels through the namespace **deadend** proxy
(`deadend-<namespace>.<domain>:2222`) using `ncat --ssl`, then connect to the device's remote-access
port. See **[Remote Access](remote-access.md)** for the full picture.
