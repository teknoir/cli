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

## The k3s CA

The server scripts (`bootstrap_server_<device>.sh`) use the device certificate (`rsa_public.pem`)
and key from the device's settings as the **root of the cluster CA** and issue the intermediate and
leaf CAs below it. k3s v1.34.12, v1.35.9, v1.36.5, v1.37.1 and later only accept a root with the
`keyCertSign` key usage, so the script **re-issues the root from the same key and subject** with
`keyCertSign` and CA:TRUE (valid 20 years; the device files are not modified). The code is
[`pkg/templates/scripts/tn-k3s-ca.sh`](https://github.com/teknoir/cli/blob/main/pkg/templates/scripts/tn-k3s-ca.sh),
embedded in the script and usable stand-alone (`sh tn-k3s-ca.sh check|remediate`).

Re-running a server script is safe:

| State of the device | What the script does with the CA |
| --- | --- |
| k3s never started | Creates the PKI (keeps a valid one) |
| k3s started, root is fine | Leaves it alone |
| k3s started, root has no `keyCertSign` (bootstrapped by an older script) | Repairs it first with `k3s certificate rotate-ca` and restarts k3s, so the k3s installer that follows cannot brick the device. `TN_K3S_CA_AUTO_REMEDIATE=false` stops with advice instead; a failed repair stops the script |

See [Troubleshooting](../troubleshooting.md#bootstrapping-k3s) for devices that hang or were
bricked by a k3s upgrade. The agent script is unchanged: an agent has no CA of its own.

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
