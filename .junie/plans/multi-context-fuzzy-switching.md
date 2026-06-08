---
sessionId: session-260519-161820-160n
isActive: true
---

# Requirements

### Overview & Goals
The CLI configuration needs to support managing multiple environments simultaneously, removing the need to log in repeatedly when switching between different domains. Furthermore, the CLI must provide a seamless, interactive way to switch between domains, namespaces, and devices using fuzzy matching.

### Scope
- **In Scope:** 
  - Overhaul the configuration structure to support multiple domains and auth credentials.
  - Add interactive commands `domain`, `ns`, and `device` (`dev` alias) with fuzzy matching.
  - Add `add domain` command to register new domains.
  - Update `login` and global flag resolution to work with the new domain-aware configuration.
- **Out of Scope:**
  - Actual integration with the Teknoir API to fetch live lists of namespaces and devices (since the API client isn't implemented in the CLI yet). These will be stubbed with placeholders that can easily be wired up later.

### User Stories
- As a developer, I want my configuration file to hold tokens for multiple domains so that I don't have to re-authenticate when changing targets.
- As an operator, I want the golden user experience path to be like kubectl but with krew plugins installed (`ctx` and `ns` commands), making the experience more interactive.
- As an operator, I want to type `tnctl domain` and use a fuzzy finder to quickly select my target domain.
- As an operator, I want to type `tnctl add domain <domainname>` to add a new domain to the config, querying for the client-secret.
- As an operator, I want to type `tnctl ns` or `tnctl dev` to interactively select a namespace or device to focus my subsequent commands on.

# Technical Design

### Current Implementation
The configuration (`pkg/config/Config`) currently holds flat fields for a single domain (`Domain`, `Namespace`, `Device`, `AccessToken`, etc.). When `tnctl login` completes, it overwrites these global fields.

### Key Decisions
- **Domain-Based Config:** We will use a flat config for the active selections (`Domain`, `Namespace`, `Device`). There is no concept of a "Context" that groups them.
- **Decoupled Auth:** Authentication credentials (`AccessToken`, `RefreshToken`, etc.) and Keycloak settings will be stored in an `Auths` map, keyed by the domain.
- **Interactive UI:** We will integrate `github.com/ktr0731/go-fuzzyfinder` (or a similar tool like `promptui`) to provide a native, interactive fuzzy-finding experience directly in the terminal for the `domain`, `ns`, and `device` commands.

### Proposed Data Models
```go
type AuthConfig struct {
	AccessToken  string `mapstructure:"access_token" yaml:"access_token,omitempty"`
	RefreshToken string `mapstructure:"refresh_token" yaml:"refresh_token,omitempty"`
	Expiry       string `mapstructure:"expiry" yaml:"expiry,omitempty"`
	Realm        string `mapstructure:"realm" yaml:"realm,omitempty"`
	ClientID     string `mapstructure:"client_id" yaml:"client_id,omitempty"`
	ClientSecret string `mapstructure:"client_secret" yaml:"client_secret,omitempty"`
}

type Config struct {
	Domain    string                `mapstructure:"domain" yaml:"domain"`
	Namespace string                `mapstructure:"namespace" yaml:"namespace"`
	Device    string                `mapstructure:"device" yaml:"device"`
	Auths     map[string]AuthConfig `mapstructure:"auths" yaml:"auths"`
}
```

### Proposed Changes
1. **Config Migration:** Adjust `config.go` to the new structs. The existing `Domain`, `Namespace`, etc., remain as the active context, but credentials move to the `Auths` map.
2. **Global Flags:** Update `cmd/root.go`. When resolving the active domain, the CLI will look at flags first, then fall back to the config's `Domain`.
3. **Login Command:** Update `cmd/login.go`. Upon successful login, save credentials in `Auths[domain]`.
4. **New Commands:** 
   - `cmd/domain.go`: 
     - `tnctl domain`: If no args, open fuzzy finder listing keys from `config.Auths`. On select, update `config.Domain`.
     - `tnctl add domain <domain_name>`: Prompt for `client-secret` interactively, save it to `Auths[domain_name]`, and set as active `Domain`.
   - `cmd/ns.go`: Use a stubbed `FetchNamespaces(domain)` function. If no args, open fuzzy finder. On select, update the `Namespace` of the active config.
   - `cmd/device.go`: Use a stubbed `FetchDevices(domain, namespace)` function. If no args, open fuzzy finder. On select, update the `Device` of the active config.

### Delivery Plan

1. **Refactor `pkg/config/Config` and Update Root Command**
   - Refactor `pkg/config/Config` to include the `Auths` map.
   - Update `cmd/root.go` to handle the new flat config structure where `Domain`, `Namespace`, `Device` are the active state, and credentials are in `Auths`.

2. **Implement `tnctl domain` and `tnctl add domain`**
   - Create `cmd/domain.go` to handle domain listing/switching and adding new domains.
   - Implement interactive prompts/fuzzy finding for switching.
   - Implement interactive prompts for adding a new domain (querying for client-secret).

3. **Implement `tnctl ns` and `tnctl device`**
   - Create `cmd/ns.go` and `cmd/device.go`.
   - Implement stubbed fetching and fuzzy selection for namespaces and devices.