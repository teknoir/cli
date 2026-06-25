# `tnctl add`

Add a resource to your local configuration. Today this provides the `add domain` subcommand.

## `tnctl add domain`

Register a new Teknoir domain and switch to it.

### Synopsis

```bash
tnctl add domain <domain>
```

`<domain>` is required (exactly one argument).

### Behavior

- Creates an empty credentials entry for the domain under `auths` (if it doesn't already exist).
- Sets the new domain as active and ensures it has a `default` namespace.
- Persists the change to `~/.tnctl.yaml`.

After adding a domain, run [`tnctl login`](login.md) to authenticate against it.

### Examples

```bash
# Add a customer environment and switch to it
tnctl add domain customer.teknoir.cloud

# Then authenticate
tnctl login
```

## See also

- [`domain`](domain.md) — switch between already-configured domains.
- [Contexts](../concepts/contexts.md) — how domains and namespaces work together.
