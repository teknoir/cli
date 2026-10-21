# Troubleshooting

Quick fixes for the most common issues. Add `--debug` to almost any command for verbose output,
including resolved URLs and the exact `ssh` invocation used.

## Authentication

??? failure "`no credentials found for domain <domain>. Please log in first`"
    You haven't authenticated against the active domain. Run:

    ```bash
    tnctl login --domain <domain>
    ```

??? failure "Browser doesn't open during `tnctl login`"
    `tnctl` falls back to printing a verification URL and one-time code — open the URL manually and
    enter the code. On headless machines this is expected behavior.

??? failure "`Your tnctl login has expired. Run: tnctl login` (exit 6, `-o json` code `session_expired`)"
    `tnctl` refreshes access tokens automatically using the stored refresh token. This message means the
    platform refused the refresh token: it was revoked, your user was disabled, it was unused for longer than
    the offline idle timeout (30 days by default), or the platform's login service was reset. Run
    `tnctl login` again (`--no-browser` on a headless host). `tnctl login --check` tests the stored login
    without logging in and exits 6 when it has expired. A network error while renewing is not this message:
    it is shown as it is and retrying helps.

??? failure "`tnctl login` warns that the login response has no refresh token"
    The platform did not grant the `offline_access` scope, so this login cannot be renewed and ends when the
    access token expires (minutes). Ask a platform admin to allow `offline_access` for the `teknoir-cli` client.

??? failure "`not logged in or token expired: run tnctl login`"
    The platform answered 401: there is no token, or it was rejected or has expired. Run `tnctl login`.
    The message adds the platform's reason in brackets when it gave one.

??? failure "`not allowed for this account or teamspace: ...`"
    The platform answered 403: you are logged in, but your account may not do this in this teamspace. The text
    after the colon is the platform's reason, for example `The login keys of devices need the developer role in
    teamspace <ns>` (a viewer ran `ssh`, `bootstrap` or `ansible`), or that you may not open terminals.
    **Logging in again does not help.** Ask a teamspace admin for the role the message names. See
    [who may do what](commands/index.md#who-may-do-what).

??? failure "`the platform refused the request: not logged in, the token was rejected, or your account may not do this here`"
    An older gateway turns both 401 and 403 into a redirect to the login page, so `tnctl` cannot tell them
    apart. Run `tnctl login` first; if the message stays, you are missing a role: ask a teamspace owner.

??? failure "`this platform does not support tnctl <version> yet`"
    The platform has no fleet API (`/api/device/fleet/teamspaces` answers 404): production and airgapped
    installs on the classic portal. Install `tnctl v0.0.9-beta` for them (see
    [Installation](installation.md#older-versions)) or ask your platform admin to update the platform.

??? failure "`too many requests` when reading device logins (HTTP 429)"
    The platform allows 30 device-login calls per minute and user; `ansible-inventory` makes one call per
    200 devices. Wait the number of seconds in `Retry-After` and run the command again.

## Context

??? failure "`domain and namespace are required. Please set them via flags or config`"
    Set them once via the interactive commands, or pass flags:

    ```bash
    tnctl domain
    tnctl ns
    # or
    tnctl devices --domain teknoir.cloud --namespace my-namespace
    ```

??? failure "`no domains configured. Use 'tnctl add domain <domain>' to add one`"
    Add and switch to a domain first:

    ```bash
    tnctl add domain teknoir.cloud
    ```

## Devices & tunneling

??? failure "`Tunneling is not active for <name>`"
    Turn Tunneling on for the device under Tunneling on the device page in the Teknoir console, wait until
    the device has connected, then retry `ssh`/`port-forward`/`socks-proxy`. `tnctl devices` shows the state in
    the `Tunnel` column.

??? failure "`tnctl: <name> has no active WireGuard tunnel; using the legacy SSH tunnel`"
    Not an error: the device only has the deprecated legacy SSH tunnel, which is used as a fallback.

??? failure "`SOCKS is not available over WireGuard` / `--to is not available over WireGuard`"
    Over WireGuard only the device's services can be forwarded: `tnctl port-forward <device>` lists them,
    `tnctl port-forward <device> [local-port:]<service-id>` forwards one.

??? failure "`no devices found in namespace <ns>`"
    Confirm you're in the right namespace (`tnctl ns`) and that the namespace actually contains
    devices (`tnctl devices`). If filtering, double-check your `key=value` labels.

??? failure "SSH / proxy connection hangs or fails"
    - Ensure local `ssh` and `ncat` (Nmap) are installed and on your `PATH`.
    - Re-run with `--debug` to see the full `ssh` command and any `ncat`/proxy errors.
    - Verify network access to `deadend-<namespace>.<domain>:2222`.

## Bootstrapping & k3s

??? failure "k3s never becomes Ready: `Failed to validate connection to cluster ... CA cert validation failed ... x509: certificate signed by unknown authority`"
    k3s v1.34.12, v1.35.9, v1.36.5, v1.37.1 and later (k3s-io/k3s#14699, a CVE fix) drop every
    certificate without the `keyCertSign` key usage from the CA bundle. The device certificate of
    the platform (`/etc/teknoir/rsa_public.pem`) is the **root** of the cluster CA, and the
    certificates generated before this was fixed never had `keyCertSign`. The get.k3s.io `stable`
    channel has been v1.36.5 since 2026-10-01, so a new device bootstrapped with an old script, or an
    old device whose k3s was upgraded, hits this.

    The bootstrap scripts generated by a `tnctl` with this fix re-issue the root from the **same key
    and subject** with `keyCertSign` (valid 20 years); the device files in `/etc/teknoir` are never
    changed, and everything signed below the old root stays valid. Check that your `tnctl` has it:
    `grep -c tn_k3s_ca_setup bootstrap_server_<device>.sh` prints a number above 0.

    Find out where the device is with the CA tool (`tn-k3s-ca.sh`, run as root on the device; it is
    [`pkg/templates/scripts/tn-k3s-ca.sh`](https://github.com/teknoir/cli/blob/main/pkg/templates/scripts/tn-k3s-ca.sh)
    in this repository and is embedded in every generated `bootstrap_server_*.sh`):

    ```bash
    sh tn-k3s-ca.sh check     # exit 0 = ok, 2 = needs rotation, 3 = needs regeneration, 4 = foreign CA or no server data found
    ```

    Without `--data-dir`, the tool finds the k3s data dir the way k3s does (`--data-dir`/`-d` in the
    `ExecStart` of `k3s.service`, then the last `data-dir` in `/etc/rancher/k3s/config.yaml.d/*.yaml`,
    then `data-dir` in `/etc/rancher/k3s/config.yaml`, then `K3S_DATA_DIR` of the unit, then
    `/var/lib/rancher/k3s`) and tells which one it used (`data dir ... (source)` on stderr). If a k3s
    **server unit exists but the data dir it found has no `server/tls/server-ca.crt`**, `check` is never
    "ok": it exits 4 (`no-server-data`) and names the directory it looked in. Either k3s has not
    started on this device yet, or its data dir is somewhere the tool cannot see; pass `--data-dir DIR`.

    | Situation | How to tell | What to do |
    | --- | --- | --- |
    | **Fresh bootstrap that hangs**: k3s was never Ready on this device | `check` says `needs-regeneration`; `<data dir>/server/token` (`/var/lib/rancher/k3s/server/token`) does not exist | Run the fixed `bootstrap_server_*.sh` again, or `sh tn-k3s-ca.sh remediate`. Nothing was ever stored in the cluster, so there is nothing to rotate: k3s is stopped while `server/tls` is replaced (the old dir is moved aside, the result is validated, and the old dir is put back if anything fails), the old files are saved as `server/tls-backup-tn-*.tar`, and k3s is started again. `rotate-ca` cannot be used here, it needs the token a started server writes |
    | **Bricked by an upgrade**: the device worked, then k3s was upgraded and does not start | `check` says `needs-rotation`; k3s logs the error above | `sh tn-k3s-ca.sh remediate`. k3s must be running (a k3s that loops on the error is fine, it listens). The tool stages the new CA files, runs `k3s certificate rotate-ca` against the running server, copies the files and restarts k3s. Workloads and the datastore are kept. Details below |
    | **Still works, not upgraded yet** | `check` says `needs-rotation`; `k3s --version` shows v1.34.11, v1.35.8, v1.36.4 or v1.37.0 or older | Do it now: `sh tn-k3s-ca.sh remediate`, or run the fixed bootstrap script again (it repairs the CA before it touches k3s; `TN_K3S_CA_AUTO_REMEDIATE=false` makes it stop with advice instead) |
    | **`foreign`** | `check` says `foreign` | The root is not the Teknoir device CA (no matching key in `server/tls/root-ca.key` or `/etc/teknoir/rsa_private.pem`). The tool does not touch it |

    `remediate` runs in phases, which you can also run one at a time (`--phase`): `prepare`
    (offline: checks, backs up `server/tls`, stages `server/.tn-ca-rotate-<time>/` with a `ROTATE_CMD`),
    `rotate` (runs `ROTATE_CMD` against the running server), `finalize` (copies the files, only after
    the rotation succeeded), `all` (default, plus a k3s restart). It is safe to run again: when the
    root is fine it does nothing.

    `rotate-ca` makes k3s verify the new CA bundle against the old one. An old root that has expired,
    or that has a key usage without `keyCertSign` (the certificates the device-controller made), cannot
    vouch for anything any more, so for those the tool verifies the new bundle itself with openssl and
    adds `--force` to `rotate-ca` (it says so when it stages the rotation). A root without any key usage
    (the Backstage certificates) is rotated without `--force`.

    Also with `--force`: an old root that is not yet valid, has no `basicConstraints` or `CA:FALSE`, and,
    generally, any old root against which openssl cannot verify the old chain while the new chain
    verifies. The reason is printed when the rotation is staged.

    A k3s older than v1.24.12 / v1.25.8 / v1.26.3 has no `certificate rotate-ca`. The tool stops
    before it changes anything and says so: that k3s still accepts the current root (the `keyCertSign`
    check only came with v1.34.12 and later), so upgrade it one minor version at a time to a release
    with `rotate-ca` that is older than v1.34.12 (v1.27 to v1.34.11), then run `remediate`, then go on.

    **When the CA was changed but a later step failed** (`remediate` exits 5; the bootstrap script stops
    with "the k3s CA of this device WAS changed"): `rotate-ca` succeeded, so the datastore of k3s holds
    the new CA. If copying the files failed, fix the cause and run `sh tn-k3s-ca.sh remediate --phase
    finalize`; if the restart failed, look at `systemctl status k3s` / `journalctl -u k3s` and restart
    it. Running `remediate` again finalizes a completed rotation instead of rotating twice.

    **Rollback limits.** The tool saves `server/tls` as `server/tls-backup-tn-<time>.tar` (the newest 3
    are kept; they hold keys, mode 600). A rotation also writes the new CA into the datastore of k3s,
    which the tool does not back up: after a successful `rotate-ca`, restoring the tar alone does not go
    back (files and datastore would disagree). A real rollback needs a copy of `server/db` taken before
    with k3s stopped. Before `rotate-ca` has succeeded nothing on disk is changed. A leftover staging dir
    `server/.tn-ca-rotate-*` (it holds copies of the keys) is removed by the next `remediate` or
    bootstrap run once the CA is fine.

    If `rotate-ca` fails, nothing was changed on disk. Use the fallback: put the last release before
    the fix of **the same minor** in place, remediate, and upgrade again.

    | Your minor | Last release without the check |
    | --- | --- |
    | v1.34 | v1.34.11 |
    | v1.35 | v1.35.8 |
    | v1.36 | v1.36.4 |
    | v1.37 | v1.37.0 |

    ```bash
    systemctl stop k3s
    # k3s-arm64 / k3s-armhf on ARM devices; replace the version with the row for your minor
    curl -fL -o /usr/local/bin/k3s 'https://github.com/k3s-io/k3s/releases/download/v1.36.4%2Bk3s1/k3s'
    chmod 755 /usr/local/bin/k3s
    systemctl start k3s
    sh tn-k3s-ca.sh remediate
    ```

    Then upgrade k3s as usual.

    Devices with several k3s servers (embedded etcd) were not part of the tests; remediate the
    servers one by one and read the [k3s documentation](https://docs.k3s.io/cli/certificate) for
    `rotate-ca` first. Agent nodes need nothing: they have no CA of their own and pick up the new
    root from the server.

??? question "Which k3s version do the bootstrap scripts install?"
    A pinned one, `v1.36.5+k3s1` by default (`tnctl bootstrap --k3s-version` changes it, `stable`
    restores "whatever the installer's stable channel is today", and `INSTALL_K3S_VERSION=... sh
    bootstrap_*.sh` overrides it when the script runs) on a device **without** k3s. A device that
    already runs k3s **keeps its version** when the script is run again; upgrading takes
    `tnctl bootstrap --k3s-upgrade` (or `TN_K3S_UPGRADE=true`). A new **agent** installs the version of
    its **server** (read from `https://<server>:6443/static/teknoir/k3s-version`, which the server script publishes; `/version` is only tried second, k3s does not answer it anonymously), never a newer one. See
    [`tnctl bootstrap`](commands/bootstrap.md#the-k3s-version).

??? failure "The agent script stops with `cannot read the k3s release of the server`"
    The agent asks its server for its k3s release before it installs anything, because a kubelet must
    not be newer than the apiserver. k3s does not answer `/version` anonymously (401), so the server
    script publishes the release it runs at `https://<server>:6443/static/teknoir/k3s-version`. The
    message tells which of two things is wrong. **The server is down, `K3S_URL` is wrong or the device
    cannot reach it**: fix that and run the script again. **The server answered but publishes nothing**
    (`curl -k https://<server>:6443/static/teknoir/k3s-version` gives 404): it was bootstrapped by an
    older script; run its bootstrap script again (it keeps k3s as it is and publishes the release). Or
    say which release the server runs: `INSTALL_K3S_VERSION=v1.xx.y+k3sN` (`tnctl bootstrap
    --k3s-version`), or `TN_K3S_FORCE=true` to install the script's default anyway.

    **This is a change for a fleet.** Servers bootstrapped by older scripts publish no release, so a
    **new** agent (a node that has no k3s yet) stops until the server's script has been run again, or
    until `INSTALL_K3S_VERSION` says what the server runs. Running the server's script again is the
    right fix anyway: it publishes the release and, on a server that still has the old root CA
    (the device certificate without `keyCertSign`), it also repairs it first (`k3s certificate
    rotate-ca`, then a restart of k3s), which a k3s upgrade to v1.34.12 / v1.35.9 / v1.36.5 / v1.37.1 or
    later would otherwise turn into a device that no longer starts. Agents that already run k3s are not
    affected: a re-run keeps the installed k3s and does not ask the server.

??? failure "The agent script stops with `refusing to install k3s ... on this agent`"
    You asked for a release that is newer than the server's. Drop `--k3s-version` /
    `INSTALL_K3S_VERSION` (the agent then gets the server's release), upgrade the server first, or set
    `TN_K3S_FORCE=true`.

??? failure "The script stops with `its k3s version cannot be read`"
    A k3s binary or `server/db` exists, but `k3s --version` gives no release (a broken or half-copied
    binary, a data dir without the binary). The script does not install over it blind. Look at
    `k3s --version`, repair or remove k3s on purpose, or set `TN_K3S_FORCE=true`.

??? failure "The server script stops with `... sets data-dir: ...`"
    k3s runs from one data dir (`ExecStart --data-dir`, `K3S_DATA_DIR` or `DATA_DIR`), but `config.yaml`
    or `config.yaml.d` names another. The k3s installer would write a unit without `--data-dir`, and k3s
    would then start from the config directory. Make them agree (usually: set `data-dir:` in
    `/etc/rancher/k3s/config.yaml` to where the cluster is) and run the script again. Nothing was
    changed.

??? failure "The script says `k3s ... is kept; the requested ... is NOT installed`"
    Intended: the device already has k3s and a re-run does not upgrade it. To upgrade, run the
    script with `TN_K3S_UPGRADE=true` (or generate it with `tnctl bootstrap --k3s-upgrade`), one minor
    version at a time and servers before agents.

??? failure "The script stops with `refusing to downgrade k3s` or `more than one minor version`"
    Both stop before the k3s installer runs, and before the CA step, so nothing is changed (should
    the message ever list changes made earlier in the run, those are exactly the ones that happened). **Downgrade**: the pin is older than the installed
    release, and k3s cannot go back (its datastore has been migrated); pin the installed release or a
    newer one. **More than one minor version** (for example `v1.34.11` to `v1.36.5`): upgrade to
    `v1.35.x` first and run the script again, or set `TN_K3S_FORCE=true` (`tnctl bootstrap --k3s-upgrade
    --force`) if you have a backup of `/var/lib/rancher/k3s/server/db` and accept the risk. See
    [Re-running a script](commands/bootstrap.md#re-running-a-script-the-installed-k3s-is-kept).

??? failure "k3s does not start on a Jetson / Ubuntu 18.04 / 20.04: `kubelet is configured to not run on a host using cgroup v1`"
    Kubernetes 1.35 and later (k3s v1.35+) refuse cgroup v1 by default. Scripts from a `tnctl` with
    this fix set the kubelet argument `fail-cgroupv1=false` (for k3s v1.31 and later) for you in
    `/etc/rancher/k3s/config.yaml.d/99-teknoir-cgroupv1.yaml` (check with `stat -fc %T /sys/fs/cgroup`:
    `tmpfs` means cgroup v1 or hybrid, `cgroup2fs` means v2). To fix a device by hand, create that file
    with the content shown in [`tnctl bootstrap`](commands/bootstrap.md#cgroup-v1-devices) and
    `systemctl restart k3s`. cgroup v1 is deprecated upstream: the lasting fix is cgroup v2 (kernel
    command line `systemd.unified_cgroup_hierarchy=1`, or a newer OS release).

??? failure "The script stops with `k3s ... is active and ready, but the data dir ... was never initialised`"
    The data dir the script found (it tells where it came from) has no `server/token`, but the k3s unit
    is running: k3s uses another data dir (a `--config` file, a drop-in the tool does not read). A CA
    generated in the wrong directory would not change the CA that k3s uses, so nothing was changed.
    Find the real directory (`systemctl cat k3s`, `ps aux | grep k3s`) and run the script again with
    `DATA_DIR=/that/dir`.

??? failure "Remediation ends with `the restart of k3s did not finish within 600s`"
    The CA **was** changed (the message says so and what to look at), but `systemctl restart k3s`
    did not return, and the unit was not active a further 120 s later either: the k3s unit waits for
    ever for a k3s that cannot start. The wait is capped (`TN_K3S_SERVICE_TIMEOUT`, seconds, default
    600; then `TN_K3S_READY_GRACE`, default 120, for the unit to become active). Look at `journalctl -u k3s -n 50`, fix the cause, and
    restart k3s yourself. Do not restore the backup tar.

??? failure "Bootstrap fails with `string too long maxsize=64`"
    The generated CA certificates carry the device ID and the domain in their common name, which is
    limited to 64 characters. Fixed: the name is cut to 64 characters in scripts from a `tnctl` with
    the CA fix. Regenerate the script.

## Configuration

??? question "Where is my config?"
    `~/.tnctl.yaml` by default. Override with `--config /path/to/file.yaml`. See
    **[Configuration](configuration.md)**.

??? question "How do I reset everything?"
    Delete `~/.tnctl.yaml` (a fresh default file is recreated on next run) and `tnctl login` again.
