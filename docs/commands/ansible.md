# ansible

Run Ansible against the devices of a namespace, with `tnctl` as the dynamic inventory. Login and domain/namespace
selection are the same as for every other `tnctl` command, and devices are reached over WireGuard (ZTNA) where it is
active, else through the legacy SSH tunnel.

```bash
tnctl ansible [args...]            # runs: ansible -i <inventory> args...
tnctl ansible-playbook [args...]   # runs: ansible-playbook -i <inventory> args...
tnctl ansible-inventory --list     # the inventory itself (Ansible dynamic-inventory protocol)
tnctl ansible-inventory --host <device>
```

tnctl replaces the Teknoir Ansible inventory plugin (`inventory.py` and `install.sh` in
[teknoir/ansible](https://github.com/teknoir/ansible)), which is deprecated. The playbooks in that repo run through
`tnctl ansible-playbook`.

`ansible` / `ansible-playbook` must be installed and in `PATH`. With `ansible-core` alone, also install the
collections the Teknoir playbooks use: `ansible-galaxy collection install ansible.posix community.general`. All arguments are passed through untouched; only
tnctl's own `-d`, `-n`, `--config` and `--debug` are accepted in front of them.

```bash
tnctl -d teknoir.cloud -n teknoir-demo ansible all -m ping
tnctl ansible-playbook site.yml --limit device_group_test
tnctl ansible teknoir_demo -b -m command -a 'uptime'
```

## Inventory

Hosts are the devices of the active domain and namespace (`-d`/`-n`, or the saved context). Groups:

- the namespace, with `-` and `.` replaced by `_` (`teknoir-demo` is `teknoir_demo`);
- every label as `<label>_<value>`, with `-` and `.` replaced by `_` (`device-group=test` is `device_group_test`).

A device needs a login name and a private key in the catalog. The password is optional; it only sets
`ansible_become_pass` / `ansible_sudo_pass` (always with `ansible_become_user=root`, `ansible_become_flags=-E`).
A device without login data, or without a usable transport (see below), is left out of the inventory and its groups,
with one line on stderr per device (`tnctl: device "x" is not reachable (...)`), so a run only targets devices it can
reach.

`ansible_teknoir_login_user` holds the platform user, also when you log in as someone else with
`-e ansible_user=...`.

## Become (sudo)

`-b` runs tasks as root through sudo. The device bootstrap gives the platform user passwordless sudo when the platform
holds no password for it (key-only login, the default); otherwise the inventory supplies the device password from the
platform. A device bootstrapped before passwordless sudo existed needs one re-bootstrap (see `playbooks/rebootstrap-device.yml`
in teknoir/ansible) or `-e ansible_user=<another sudoer>` until then.

## synchronize (rsync)

`ansible.posix.synchronize` runs its own `ssh` for rsync. Pass `use_ssh_args: true` so it uses the inventory's transport
(the WireGuard `ProxyCommand` or the tunnel), and `verify_host: true` so it keeps the host key check: without it the
module puts `StrictHostKeyChecking=no` in front of the inventory's options, and ssh uses the first value.

```bash
tnctl ansible <device> -b -m synchronize -a "src=/var/log/syslog dest=./ mode=pull use_ssh_args=yes verify_host=yes"
```

Private keys are written to `<user cache dir>/tnctl/ansible/<domain>/<namespace>/<device>.pem` (directories `0700`,
files `0600`) and rewritten when the catalog key changes.

## Transports

`TNCTL_ANSIBLE_TRANSPORT` selects the transport:

| Value | Behaviour |
| --- | --- |
| `auto` (default) | WireGuard when the device annotation `ztna.teknoir.org/wireguard` is `enabled` and its WireGuard status is `active`; otherwise the SSH tunnel when it is open. |
| `wireguard` | WireGuard only; other devices get no host variables. |
| `tunnel` | The legacy SSH tunnel only (`ncat --ssl` through `deadend-<namespace>.<domain>:2222`). |

The chosen transport is in the host variable `ansible_teknoir_transport`.

With WireGuard, `ansible_ssh_args` carries a `ProxyCommand` that runs [`tnctl wg-proxy`](wg.md) (the same one as
`tnctl ssh --wireguard`), a per-device known_hosts file filled from the host key the hub vouches for, and
`StrictHostKeyChecking=yes`. SSH connection sharing (`ControlPersist=120s`) keeps one WireGuard session per host for a
play. The hub URL is `https://wg.<domain>`; override it with `TNCTL_WG_HUB`, and the TURN server with
`TNCTL_WG_TURN_SERVER`.

| Variable | Description |
| --- | --- |
| `TNCTL_ANSIBLE_TRANSPORT` | `auto`, `wireguard` or `tunnel`. |
| `TNCTL_WG_HUB` | WireGuard hub URL (default `https://wg.<domain>`). |
| `TNCTL_WG_TURN_SERVER` | TURN `host:port` override. |
| `TNCTL_NAMESPACE` | Namespace, as for other commands. |

## Using the inventory without `tnctl ansible`

`tnctl ansible` writes a small executable shim at `<cache>/tnctl/ansible/<domain>/<namespace>/inventory`:

```sh
#!/bin/sh
exec '/usr/local/bin/tnctl' -d teknoir.cloud -n teknoir-demo ansible-inventory "$@"
```

Point plain Ansible at it, for example in `ansible.cfg`:

```ini
[defaults]
inventory = ~/.cache/tnctl/ansible/teknoir.cloud/teknoir-demo/inventory
```

or write your own shim with the domain and namespace you want. Because the inventory calls `tnctl`, an expired login
is refreshed (or reported) there; run `tnctl login` first if needed.
