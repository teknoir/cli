# `tnctl ns`

Select the active namespace for the current domain.

## Synopsis

```bash
tnctl ns [namespace]
```

- With an argument, sets the namespace directly.
- Without an argument, fetches the live list of namespaces and opens an interactive fuzzy-finder.

## Behavior

- When `[namespace]` is provided, it is saved immediately for the active domain.
- Otherwise, `tnctl` queries the cloud API for namespaces available to your account, then lets you
  pick one. The selection is saved against the active domain in `~/.tnctl.yaml`.
- Requires a valid session; if none exists for the domain you'll be asked to log in first.

## Examples

```bash
# Set namespace directly
tnctl ns production

# Pick interactively from live namespaces
tnctl ns
```

## See also

- [`domain`](domain.md) — switch domains.
- [Contexts](../concepts/contexts.md) — namespace resolution and precedence.
