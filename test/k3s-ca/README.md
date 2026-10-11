# k3s custom-CA test harness

Reproduces, and guards against, the **k3s CA hash validation change**
([k3s-io/k3s#14699](https://github.com/k3s-io/k3s/pull/14699), backport #14708,
CVE fix): `pkg/clientaccess/token.go` now prunes every certificate without the
`keyCertSign` key usage from the CA bundle before it builds the trust pool.

k3s **v1.34.12, v1.35.9, v1.36.5, v1.37.1 and later refuse to start** with the
custom CA that Teknoir devices use: `server.sh.tmpl` copies the device's own
certificate (`rsa_public.pem`, which has no `keyCertSign`) to
`server/tls/root-ca.pem` and issues the intermediate and leaf CAs below it. The
log says `Failed to validate connection to cluster ... CA cert validation failed:
... x509: certificate signed by unknown authority`.

The harness runs **real `rancher/k3s` containers** (and, for the OpenSSL
compatibility suite, plain distro containers) and decides `READY` or `FAIL`. It
is used by this repo (`cli`, `pkg/templates/server.sh.tmpl` and
`pkg/templates/scripts/tn-k3s-ca.sh`) and is vendored by `bootstrap-device`.

Requirements: `docker` (with the daemon reachable), `go` (builds two small
static tools; stdlib only), `curl`, and network access (Docker Hub for the
`rancher/k3s` and distro images, `update.k3s.io` to resolve `stable`). Arm
images in `openssl-compat` need `qemu-user-static` with binfmt registered.

```
./run.sh --suite control            # reproduce the bug: 50 cases, about 6 min at -j 4
./run.sh --suite all -j 4           # everything
./run.sh --list --suite agent       # show the plan, touch nothing
go test ./test/k3s-ca/...           # offline unit tests (no docker)
```

Typical runtimes on a 32-core host at `-j 4`, with the images already pulled:
`control` about 6 min (a rejected legacy case takes 52 s because the failure
must repeat for 45 s; a Ready server takes 10 s), `agent` + `legacy-with-cert`
about 2 min, `remediation` about 3 min per generator/helper pair plus S6/S7,
`openssl-compat` about 10 min, almost all of it the `linux/arm/v7` image under
qemu (the first run also builds the cached helper images). Each case has its
own timeout (150 s server, 180 s agent, 240 s for restarts and upgrades), so a
hung k3s cannot stall a run.

## What a case does

1. Generate a device certificate with one of the **generators** (below).
2. In a helper container (`debian:bookworm` + openssl, selectable) build
   `server/tls` with the selected **CA mode**.
3. Start `rancher/k3s:<version>` as a server with `--privileged --tmpfs /run
   --tmpfs /var/run`, the data dir and `/etc/rancher` bind-mounted, hostname
   `srv-<case>`, args `server --token <raw hex> --disable traefik --tls-san
   srv-<case>` (the same raw-token style as the templates).
4. **READY** = `kubectl get --raw /readyz` is `ok` and the node is `Ready`.
   **FAIL** = `CA cert validation failed` keeps repeating for more than 45 s
   (or the container exits / a 150 s timeout hits, reported as `EXITED` /
   `TIMEOUT`).
5. Optionally start an agent (`K3S_URL=https://srv-<case>:6443`, the raw
   `K3S_TOKEN`), 180 s timeout. `--agent-os-trust` mounts a bundle of the
   system CAs plus `rsa_public.pem` and sets `SSL_CERT_FILE`, which mimics
   `update-ca-certificates` in `agent.sh.tmpl`.
6. A replica of the k3s check (`cmd/k3sverify`) is run against the prepared tls
   dir and its prediction is stored in the note. A disagreement with what k3s
   did on a post-fix version is flagged `PREDICTION MISMATCH`.

### Generators (`--gens`)

| name | what it is |
|---|---|
| `bs-o111` | the exact Backstage command (`validator-create-device.ts`: `openssl req -x509 ... -days 365 ...`) in `debian:bullseye`, OpenSSL 1.1.1 (the production image base). No keyUsage |
| `bs-o3` | the same command with OpenSSL 3.x (`debian:bookworm`) |
| `go-sha1` | replica of device-controller `generateRSAKeyAndCert` (main@6c718b6): KU DigitalSignature+KeyEncipherment, EKU ServerAuth, serial 1, SHA-1 SKI (Go < 1.25) |
| `go-sha256` | the same with the truncated SHA-256 SKI Go >= 1.25 produces |
| `expired` | Backstage command with validity 2024-03..2025-03 (`debian:trixie`, OpenSSL 3.5 `-not_before/-not_after`) |
| `fixed-bs` | Backstage generator after the fix: explicit `-config` with CA:TRUE, KU incl. keyCertSign/cRLSign, 7300 days |
| `fixed-go` | device-controller generator after the fix (`gencert-go -fixed`) |
| `ext` | your own pair, see "External device certs" |

### CA modes

| mode | what builds `server/tls` |
|---|---|
| `legacy` | `legacy-ca.sh`: a frozen, verbatim copy of the CA block of `server.sh.tmpl` on `origin/main@7bb1bea` (`cp rsa_public.pem root-ca.pem`). The **control** |
| `proper` | a k3s-style root with `keyCertSign` over the device key, then the same intermediate/leaf code. Proves the harness itself is sound: READY everywhere |
| `lib` | `tn_k3s_ca_setup` from the library under test (`$TN_K3S_CA_LIB`) |

## Suites (`--suite`)

| suite | scenario | expected |
|---|---|---|
| `control` (S1) | legacy x {bs-o111, bs-o3, go-sha1, go-sha256, expired} x versions, plus the `proper` control | legacy: READY on v1.34.11, FAIL on every post-fix version; proper: READY everywhere |
| `fresh` (S2) | `lib` x the same generators x versions | READY on all |
| `legacy-with-cert` (S3) | legacy scripts with the **fixed** device certs `fixed-bs`, `fixed-go` x {v1.34.11, v1.36.5, v1.37.1, stable} | READY: the generator fix alone rescues new devices that run old scripts |
| `agent` (S4) | server + 1 agent (same version, raw token), `--ca-mode`, with and without `--agent-os-trust` | agent node Ready |
| `remediation` (S5-S7) | persistent data dirs: **S5** bootstrap legacy on v1.34.11 with an agent and a marker ConfigMap, remediate, upgrade through the fix to v1.36.5 (`check` = 0, second remediate = no-op); **S6** bricked (v1.34.11 -> v1.36.5 swap, datastore initialised): rotate through the running post-fix server; **S7** hung fresh bootstrap (legacy CA directly on v1.36.5, `server/token` never written): nothing to rotate, `prepare` regenerates the PKI in place and k3s starts from it. The downgrade fallback (pre-fix patch -> remediate -> upgrade) runs only if S6/S7 fail | see the table printed by the run |
| `rerun` (S8) | `lib` setup on v1.36.5, Ready, setup again on the initialised dir: tls sha256 unchanged, k3s restarts Ready | |
| `host` (S9, S10) | as root on the host, with `K3S_BIN='docker exec <srv> k3s'`, `TN_K3S_RESTART_CMD='docker restart <srv>'`, data dir bind-mounted at an identical path (needs passwordless sudo). **S9** `remediate --phase all`. **S10** `tn_k3s_ca_setup` sourced under `set -e`/noglob on a device bootstrapped with the old root repairs the CA itself, k3s then upgrades to v1.36.5 with the marker intact, and a second setup is a no-op | |
| `openssl-compat` | the library's setup, run twice per generator under `/bin/sh` (dash) in distro containers, no k3s (see below) | PASS everywhere |
| `all` | everything above | |

Default versions: `v1.34.11` (pre-fix control), `v1.34.12`, `v1.35.9`,
`v1.36.5`, `v1.37.1` and `stable` (resolved at runtime from
`update.k3s.io/v1-release/channels/stable` and deduplicated against the list;
today stable is v1.36.5). `--versions` accepts `v1.34.11`, `1.36.5`,
`v1.36.5+k3s1`, `stable`, `latest`, or a channel such as `v1.33` (newest patch;
results for unfixed minors below 1.34 are reported as `INFO`).

### openssl-compat

`compat/assert.sh` runs inside each image: for every generator it calls
`tn_k3s_ca_setup` twice on a scratch dir and checks (with `k3sverify` plus the
local `openssl` for the parts where its version matters): root CA:TRUE critical,
KU with keyCertSign critical, no EKU, AKI absent or equal to SKI, SKI equal to
the original, subject DER and `RawIssuer == RawSubject` along the chain, public
key matches the private key, notAfter about now + 7300 days, no duplicate
extensions, the replica of k3s v1.36.5 `appendCertsFromPEM`/`hashCA` verifies
`server-ca.crt` through the pruned pool, `openssl verify` of the chain, and
`rsa_public.pem` / `rsa_private.pem` byte-identical before and after.

For every generator without `keyCertSign` it also builds a device the old way (`legacy-ca.sh` plus a
`server/token`), checks that `check` returns 2, runs `remediate --phase all` with a fake `K3S_BIN`
that only records its arguments (`rotate-ca` must be called) and a no-op restart, and then
verifies the result with `k3sverify` and `check` = 0: the remediation text handling (`awk`, `find`,
`sort`, `tar`, `openssl verify`) on the same distro/arch matrix.

Default images: debian bullseye, bookworm, trixie; ubuntu 18.04, 20.04, 22.04,
24.04; `linux/arm64` ubuntu 20.04 and 22.04 (Jetson); `linux/arm/v7`
debian:bookworm (Raspberry Pi OS 32-bit, 32-bit `time_t`); `linux/arm64`
debian:bookworm (Raspberry Pi OS 64-bit). Override with `--compat-images`, for
example `--compat-images "debian:bookworm linux/arm64/ubuntu:22.04"`.

## Options

```
--suite S[,S...]        control|fresh|legacy-with-cert|agent|remediation|rerun|host|openssl-compat|all
--versions "V ..."      --gens "G ..."      --ca-mode lib|proper (agent suite)
--agent-os-trust yes|no|both                --helpers "ubuntu:20.04 debian:bookworm" (remediation)
--compat-images "..."   --helper-image IMG  -j N (default 4)
--device-cert F --device-key F              --expect READY|FAIL (legacy cases on post-fix k3s)
--out DIR (default $TMPDIR/tn-k3sca-<runid>)
--keep                  keep containers, work dirs and the generated keys
--list                  print the plan only
```

Output: `$OUT/results.tsv` (`suite case k3s gen ca expect outcome verdict secs
note`; verdict `PASS`, `MISMATCH`, `ERROR`, `INFO`, `SKIP`), per-case logs in
`$OUT/logs/`, k3s logs as `<case>.server.log` / `.agent.log`. The exit code is
non-zero on any `MISMATCH`/`ERROR`, if `PRIVATE KEY` appears in any log or
result, if a labelled container or network is left behind, or if the
`k3d-teknoir-dev` containers changed.

## Hooks (environment)

The harness knows nothing about the library beyond this contract; everything
is skipped with a message (a `SKIP` row) when the hook is absent.

| variable | meaning |
|---|---|
| `TN_K3S_CA_LIB` | path of `tn-k3s-ca.sh` (cli: `pkg/templates/scripts/tn-k3s-ca.sh`; bootstrap-device: `templates/04_tn_k3s_ca_lib.sh`). It is **sourced** (`. file`, no arguments, must not run anything) in a shell that has `set -e`, `set -o noglob`, `umask 027` and `info`/`warn`/`fatal` as in `header.sh.tmpl` (`shim.sh`), with `CONFIG_PATH` (a dir holding `rsa_public.pem` and `rsa_private.pem`), `DATA_DIR` (explicit; a run without it resolves the data dir of k3s, see the top of the library), `PRODUCT=k3s`, `_DEVICE_ID`, `_DOMAIN` set, and must provide `tn_k3s_ca_setup`. As a program (`sh tn-k3s-ca.sh check\|remediate [--phase prepare\|rotate\|finalize\|all] [--data-dir D]`) it is the remediation tool |
| `TN_REMEDIATE_PREPARE` | command run in a helper container (root, data dir at `/var/lib/rancher/k3s`, the library at `/tn/tn-k3s-ca.sh`, device files at `/etc/teknoir`, `DATA_DIR` and `K3S_BIN=k3s` in the environment). Default: `sh /tn/tn-k3s-ca.sh remediate --phase prepare --data-dir /var/lib/rancher/k3s` |
| rotate command | not a variable: for an initialised datastore the prepare phase must leave the exact command in `$DATA_DIR/server/.tn-ca-rotate-*/ROTATE_CMD` (the newest one is used). The harness runs it with `docker exec <server> sh -c "$(cat ROTATE_CMD)"`. A datastore that k3s never initialised (S7) has nothing to rotate: prepare may leave no `ROTATE_CMD` there, and only there (S5/S6 fail without it) |
| `TN_REMEDIATE_FINALIZE` | like PREPARE for the finalize phase. The harness then runs `docker restart <server>` |
| `TN_REMEDIATE_CHECK` | command whose exit code must be 0 after remediation. Default: `sh /tn/tn-k3s-ca.sh check --data-dir /var/lib/rancher/k3s` |
| `HELPER_IMAGE` | image preparing the tls dir (default `debian:bookworm`) |
| `T_SERVER`, `T_AGENT`, `T_UPGRADE`, `FAIL_AFTER` | timeouts in seconds (150, 180, 240, 45) |
| `TN_K3S_IMAGE` | image repository (default `rancher/k3s`) |

## External device certs (`--device-cert`, `--device-key`)

Used to test certificates produced by the real generators (Backstage
`validator-create-device.ts`, device-controller) instead of the replicas:

```
./run.sh --suite legacy-with-cert --device-cert rsa_public.pem --device-key rsa_private.pem \
         --versions "v1.34.11 v1.36.5 v1.37.1 stable"
```

Giving both options adds the generator `ext` and makes it the default for every
suite. The expected outcome of the `legacy` cases (today's scripts, which copy the
certificate verbatim to `root-ca.pem`) is **derived from the certificate**:
`run.sh` inspects it, prints `device cert 'ext': keyCertSign=yes|no -> legacy scripts expected ...`
and exports `EXT_CERTSIGN`. With `keyCertSign` the legacy scripts are READY on every
version (the acceptance mode for the WP3-WP5 generators: a correct fix is PASS); without it they
are READY on pre-fix k3s and FAIL on post-fix k3s. `lib` and `proper` cases are READY either way.
`--expect READY|FAIL` overrides the derived value for the legacy cases on post-fix k3s (for
a certificate that the keyCertSign rule cannot explain; pre-fix k3s stays READY).

## Safety and cleanup

* Every container and network carries the label `tn-k3sca=<runid>` and the name
  prefix `tn-k3sca-`. On exit, `INT` and `TERM` only objects with this run's
  label are removed (`docker ps -a --filter label=tn-k3sca` and `docker network
  ls --filter label=tn-k3sca` are empty afterwards). There is no `docker prune`
  and nothing outside the label is touched; `k3d-teknoir-dev` is checked before
  and after.
* Root-owned data dirs are removed with a throwaway container.
* The generated keys are throwaway, live in `$OUT/gen` and are deleted at the
  end (unless `--keep`). The run fails if a log or result contains
  `PRIVATE KEY`.
* The helper images `tn-k3sca-helper:*` (distro + openssl) are a cache, labelled
  `tn-k3sca-cache=1`, and survive runs: `docker image rm $(docker images -q
  tn-k3sca-helper)` removes them.
* `run.sh` is one brace group, so editing it during a long run is harmless.

## Files

```
run.sh                 entry point
lib/common.sh          logging, version maths, expected outcomes, results, job pool
lib/docker.sh          labelled containers/networks, cleanup, helper images
lib/gen.sh             device cert generators
lib/cases.sh           case flow, waiting, persistent scenario helpers, remediation hooks
lib/suites.sh          the suites and scenarios
legacy-ca.sh           frozen control: today's template CA block as tn_legacy_ca
proper-ca.sh           control with a keyCertSign root
shim.sh                the bootstrap script environment (header.sh.tmpl)
compat/assert.sh       assertions run inside the distro containers
cmd/k3sverify          replica of k3s v1.36.5 appendCertsFromPEM/hashCA + root checks (Apache-2.0 derived)
cmd/gencert-go         replica of the device-controller certificate generator
*_test.go              offline unit tests
```
