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
| `--k3s-version` | `v1.36.5+k3s1` | k3s release the server script installs: an exact release (`v1.36.5+k3s1`), or `stable` / `latest` for the installer's channels. The agent script installs the release of its server instead; a release given here explicitly is installed only if it is not newer than the server's. |
| `--k3s-upgrade` | `false` | Let the scripts replace an installed k3s with `--k3s-version` (sets `TN_K3S_UPGRADE=true` as the script's default). Without it a re-run **keeps** the installed k3s. See [Re-running a script](#re-running-a-script-the-installed-k3s-is-kept). |
| `--force` | `false` | Only with `--k3s-upgrade`: allow an upgrade over more than one minor version (`TN_K3S_FORCE=true`). A downgrade is never allowed. |
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

## The k3s version

The server and agent scripts install a **pinned** k3s release: `INSTALL_K3S_VERSION` is set from
`--k3s-version`, default `v1.36.5+k3s1` (the `stable` channel of
<https://update.k3s.io/v1-release/channels> when this was written, and the release that the CA tests in
`test/k3s-ca` prove with the re-issued root). Without a pin the installer takes whatever `stable` is on
the day the script runs, and that moved the fleet onto a k3s that refused the device CA once already.

| `--k3s-version` | What the script does |
| --- | --- |
| *(default)* or `v1.36.5+k3s1` | `INSTALL_K3S_VERSION=v1.36.5+k3s1` |
| any other `vX.Y.Z+k3sN` | that release |
| `stable` | nothing: the old behaviour, the installer picks the `stable` channel |
| `latest` | `INSTALL_K3S_CHANNEL=latest` |

- At run time, `INSTALL_K3S_VERSION=v1.xx.y+k3sN sh bootstrap_server_<device>.sh` overrides the pin.
- The pin is the release for a **new server**. A device that already has k3s keeps what it has, see
  below.
- **An agent follows its server.** A kubelet must never be newer than the apiserver it joins, so the
  agent script does not install the pin. It reads the release of the server and installs exactly that
  one. k3s does **not** answer `/version` to an anonymous caller (its apiserver runs with
  `anonymous-auth=false`: HTTP 401 on every release, checked with v1.30, v1.34 and v1.36), and the join
  token only opens the supervisor endpoints, so the **server script publishes the release it runs**
  as `<data dir>/server/static/teknoir/k3s-version` after its installer has run. k3s serves
  `server/static` without authentication, so an agent reads
  `https://<K3S_URL>/static/teknoir/k3s-version` (and, for a server that does allow it, `/version`).
  A release that you asked for (`--k3s-version`, or `INSTALL_K3S_VERSION` when running the script) is
  installed if it is not newer than the server's (older gets a warning) and **refused** if it is,
  unless `TN_K3S_FORCE=true`. A channel (`stable`, `latest`) cannot be compared: the server's release
  is installed instead. If the release of the server cannot be read, the script **stops** and says
  why, unless a release was asked for (it is then installed as asked, with a warning) or
  `TN_K3S_FORCE=true`: the server is down or not reachable (tried three times), or it was
  bootstrapped by a script from before this change and publishes nothing. **For such a server, run
  its bootstrap script again**: that keeps k3s as it is and publishes the release. An out-of-band
  upgrade of k3s leaves the file at the older release, which is the safe direction. An OS image build
  (`OS_BUILD`) cannot reach a server and installs the pin.
- Raise the default in `pkg/templates/bootstrap_templates.go` (`DefaultK3SVersion`) together with the
  harness runs in `test/k3s-ca` for the new release.

## Re-running a script: the installed k3s is kept

A bootstrap script is meant to be run again (to repair the CA, refresh the manifests, ...). It must not
upgrade k3s as a side effect, so the pin only applies to a device **without** k3s. Before it runs the
installer, the server and agent scripts read `k3s --version` and decide:

| State of the device | What the script does |
| --- | --- |
| k3s is not installed | Installs the pin (`INSTALL_K3S_VERSION`); an **agent** installs the release of its server, see above |
| a k3s binary or `server/db` exists, but `k3s --version` gives no release | **Refuses** to install anything (the installer would replace k3s without any of the checks below); `TN_K3S_FORCE=true` goes on as if k3s were not installed |
| k3s is installed, any pin | **Keeps the installed release**: it passes that exact release to the installer (which then only rewrites the unit and the symlinks) and says that the pin was not applied |
| k3s is installed, `--k3s-upgrade` (or `TN_K3S_UPGRADE=true`) | Upgrades to the pin (an agent: to the release of its server), with these checks: a pin **below** the installed release is **refused** (k3s cannot go back, the datastore has been migrated; this also holds with `--force`), and so is a pin more than **one minor version** above (`v1.34` to `v1.36`: upgrade to `v1.35.x` first) unless `--force` (`TN_K3S_FORCE=true`), which turns the refusal into a warning |
| installed, upgrade requested, but the pin is a channel (`stable`, `latest`) | Upgrades, with a warning: the installer picks the release, so downgrade and minor jump cannot be checked. Pin an exact release to get the checks |

Upgrade servers before their agents, and one minor version at a time. The environment of the
script overrides what `tnctl bootstrap` baked in (`TN_K3S_UPGRADE=false sh bootstrap_*.sh`).

**Order in the server script**: (0) the arguments of the installer (`INSTALL_K3S_EXEC`, including a
value you gave: the cgroup step reads it), the data dir and its check against `config.yaml`, (1) this
release decision and the cgroup flag below, (2) the CA repair, (3) the k3s installer. A refusal in (0)
or (1) happens before anything is changed ("Nothing was changed"). The CA is still repaired before the installer, so an upgrade never meets a root that the
new k3s would reject. Should a later step ever refuse after the CA step has changed the device (the CA
rotated, files replaced, k3s restarted), the message lists exactly that, instead of "Nothing was
changed".

## cgroup v1 devices

Kubernetes 1.35 and later make the kubelet **refuse to start on a cgroup v1 host**
(`failCgroupV1` defaults to true) and k3s does not change that. Devices on Jetson L4T r32/r35,
Ubuntu 18.04 or 20.04 are cgroup v1 (or hybrid). Both scripts therefore check
`stat -fc %T /sys/fs/cgroup`: anything but `cgroup2fs` is v1 or hybrid. On such a host, when the k3s
that will run is **v1.31 or later** (the kept installed release, or the pin, or the release of the
server, or a channel; the kubelet knows the flag since 1.31, and an older one would not start with an
unknown argument), the script writes

```yaml
# /etc/rancher/k3s/config.yaml.d/99-teknoir-cgroupv1.yaml
kubelet-arg+:
  - "fail-cgroupv1=false"
```

and warns that cgroup v1 is deprecated. The flag only matters from v1.35, but it is set from v1.31 so
that a later upgrade (also one made by hand, outside the scripts) does not stop the kubelet. The file
is written atomically (temporary file, then `mv`). `kubelet-arg+` **appends** to the `kubelet-arg` of
`config.yaml` and the other drop-ins, so the arguments of the device owner are kept; the file is
rewritten only when it differs, so a re-run changes nothing. A `fail-cgroupv1` that the device sets
itself in a file that k3s reads (`config.yaml` or another drop-in) is respected and our file is removed.
A flag only in `INSTALL_K3S_EXEC` reaches the unit when the installer has run, so our file stays (or is
written) until then: the same value twice is harmless, and the command line wins. For an installed k3s
older than v1.31 that is being upgraded, the flag is written after the installer (`tn_inst_finish`), as the
old kubelet cannot take it. On cgroup v2 the file is removed. An OS image build (`OS_BUILD=true`) runs on another machine than the
device and cannot tell: the flag is set there, it is harmless on cgroup v2. `TN_K3S_CGROUPV1=true|false`
forces the answer. The way out is cgroup v2 (kernel command line `systemd.unified_cgroup_hierarchy=1`,
or a newer OS release); then delete the file.

The flag is written **before** the CA step, which may restart k3s: a k3s that a cgroup v1 host made
exit (an old script upgraded it to v1.35 or later) restarts itself every few seconds and reads the
flag then, so it is up for `rotate-ca` and the re-run ends with a working k3s. `rotate-ca` is tried
again for about a minute while the unit is starting.

The server script no longer replaces an existing `/etc/rancher/k3s/config.yaml` on a re-run: it adds
`disable`, `node-name` and (for a non-default data dir) `data-dir` when they are missing.

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
| k3s runs, but from another data dir than the one found (no `server/token` there) | Stops before anything is changed: generating a CA in the wrong directory would not touch the CA that k3s uses. Pass `DATA_DIR=` |
| k3s too old for `rotate-ca` (before v1.24.12, v1.25.8, v1.26.3) | Stops before anything is changed, with advice to upgrade k3s one minor version at a time to a release that has `rotate-ca` but is older than v1.34.12 |

The data dir is the one k3s uses on the device, resolved the way k3s does: `--data-dir`/`-d` in the
`ExecStart` of the `k3s.service` unit, the last `data-dir` in `/etc/rancher/k3s/config.yaml.d/*.yaml`,
`data-dir` in `/etc/rancher/k3s/config.yaml`, `K3S_DATA_DIR` of the unit, then
`/var/lib/rancher/k3s`. Set `DATA_DIR` to override it. The server script writes the same data dir to
`config.yaml` when it is not the default, so the installer's unit keeps using it, and puts the Teknoir
manifests there. If the data dir found (from the `ExecStart`, `K3S_DATA_DIR` or `DATA_DIR`) differs from
the `data-dir` of `config.yaml` or `config.yaml.d`, the script **stops** before anything is changed: the
installer writes a unit without `--data-dir`, after which k3s would start from the `config.yaml`
directory, with a new CA, and leave the cluster behind. Make them agree and run the script again.

`k3s certificate rotate-ca` and the restart wait for a slow device: a stop, start or restart of k3s may
take `TN_K3S_SERVICE_TIMEOUT` seconds (default **600**), and after a timeout the unit is watched for
`TN_K3S_READY_GRACE` (120) more seconds before the restart is called failed.

See [Troubleshooting](../troubleshooting.md#bootstrapping-k3s) for devices that hang or were
bricked by a k3s upgrade. The agent script has no CA step (an agent has no CA of its own); it follows the k3s release of its server, see above.

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
