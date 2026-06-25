# `tnctl domain`

Select the active domain from those already configured in `~/.tnctl.yaml`.

## Synopsis

```bash
tnctl domain
```

An interactive fuzzy-finder lists your configured domains. Pick one to make it active.

## Behavior

- Lists configured domains (sorted), displaying real domain names (dots restored).
- Sets the selected domain as active and restores that domain's last-used namespace.
- Persists the change and prints the active namespace, if any.

If no domains are configured, the command instructs you to add one first:

```text
no domains configured. Use 'tnctl add domain <domain>' to add one
```

## Examples

```bash
# Interactively switch domains
tnctl domain
```

## See also

- [`add`](add.md) — add a new domain.
- [`ns`](ns.md) — switch namespace within the active domain.
- [Contexts](../concepts/contexts.md)
