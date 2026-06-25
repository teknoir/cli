# `tnctl bootstrap`

Generate bootstrap (provisioning) scripts for a device.

## Synopsis

```bash
tnctl bootstrap [device-name] [flags]
```

The device may be given as an argument, via the `--device` global flag, or selected interactively.

## Flags

| Flag | Default | Description |
| --- | --- | --- |
| `--skip-upload` | `false` | Skip uploading the generated scripts. |
| `--docker-secret` | `""` | Contents of the `.dockerconfigjson` to embed. |

Plus all [global flags](index.md#global-flags).

## What it generates

For the selected device, three scripts are written to the current directory:

| File | Purpose |
| --- | --- |
| `bootstrap_agent_<device>.sh` | Provision the device as an agent. |
| `bootstrap_server_<device>.sh` | Provision the device as a server. |
| `bootstrap_docker_server_<device>.sh` | Provision the device as a Docker-based server. |

The scripts are rendered from templates and populated with the device's decoded settings
(RSA keypair, username/password, public SSH key), the domain/namespace, the device ID, a freshly
generated K3S token, and the optional Docker secret.

## Requirements

A domain and namespace must be set, and you must be authenticated.

## Examples

```bash
# Generate scripts for a specific device
tnctl bootstrap edge-01

# Pick a device interactively and embed a Docker pull secret
tnctl bootstrap --docker-secret "$(cat dockerconfig.json)"

# Generate but do not upload
tnctl bootstrap edge-01 --skip-upload
```

!!! note
    Upload to a secure bucket is not yet implemented; without `--skip-upload` the command currently
    prints a placeholder notice.

## See also

- [`device`](device.md) — inspect a device before provisioning.
