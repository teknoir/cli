# `tnctl ns`

Select the active namespace for the current domain.

## Synopsis

```bash
tnctl ns [namespace]
tnctl ns --list
```

- With an argument, sets the namespace directly.
- Without an argument, fetches the teamspaces of your account and opens an interactive fuzzy-finder.
- `--list` only prints them, one per line: name, your access (`write` for the developer and admin roles, `read`
  for the viewer role) and the display name. Handy for scripts.

## Behavior

- When `[namespace]` is provided, it is saved immediately for the active domain.
- Otherwise, `tnctl` asks the platform for the teamspaces **your account** has a device role in (not every
  teamspace of the platform), then lets you pick one. The selection is saved against the active domain in
  `~/.tnctl.yaml`.
- Requires a valid session; if none exists for the domain you'll be asked to log in first.

If the platform does not have the fleet API yet, the command prints `this platform does not support tnctl
<version> yet` and which older version to use; see [Installation](../installation.md#older-versions).

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
