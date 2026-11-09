---
name: teknoir-k3s-apps
description: Build and run your own apps on a TeknoirBot computer. It covers the inner k3s, the apps namespace and its admission rules, buildctl to the bot builder, the bot registry, Deployments and Services, port-forward into the bot browser, and cleanup. Use when asked to build an image, write a Dockerfile, deploy, run, restart, scale or debug an app inside a TeknoirBot, or to view a service on the bot computer. For a robot configuration use teknoir-config; for device telemetry use teknoir-mqtt-streams.
license: Apache-2.0
metadata:
  teknoir-kit-version: "0.1.0"
---

# Apps on a TeknoirBot's computer

Paths in this skill are relative to this skill's directory.

## What exists

A TeknoirBot's computer is a virtual Device (`tb-<bot>`) with its own single-node k3s. You reach
it with the in-cluster `kubectl` the desktop gives you.

- **The `apps` namespace** is yours: PodSecurity **baseline** enforced (with a restricted
  warning). Baseline refuses a privileged container, host namespaces, `hostPath`, `hostPort`,
  added capabilities and unsafe sysctls.
- **Admission policy** on Services in `apps`: only `ClusterIP`; no `NodePort`, `LoadBalancer`,
  `ExternalName` and no `externalIPs`. Nothing outside the VM can reach an app.
- **What your role may do** there: get, list, watch, create, update, patch and delete Pods,
  Services, ConfigMaps, Secrets, PersistentVolumeClaims, Deployments, StatefulSets, DaemonSets,
  ReplicaSets, Jobs and CronJobs; read `pods/log`; `pods/exec` and `pods/portforward`;
  `deployments/scale` and `statefulsets/scale`, so `kubectl scale` works.
- **What it may not do**, anywhere: RBAC, namespaces, service accounts, HelmCharts, CRDs,
  ingresses, network policies, HPAs, PDBs. Nothing in `kube-system` or `teknoir-build`. Reads
  outside `apps`: `nodes`, `namespaces` and `events`.
- **There is no kubeconfig context.** `kubectl` runs in-cluster as the agent's service account,
  whose default namespace is `teknoir-desktop`, where you have no role: **every call passes
  `-n apps`**. Never `kubectl config set-context`.
- **The builder** is `buildkitd` in `teknoir-build`, reached as
  `tcp://buildkitd.teknoir-build:1234` (`$BUILDKIT_HOST`). Only the desktop pod may connect to
  it; a pod in `apps` gets `connection refused`.
- **The registry** is `registry.teknoir-build:5000`. Push to
  `registry.teknoir-build.svc.cluster.local:5000/<app>:<tag>`; the node pulls the same image as
  `localhost:5000/<app>:<tag>`. Use the push name in the build and the pull name in the
  Deployment — **never** the Service name in an image: kubelet does not resolve cluster DNS there
  and the pod sits in `ImagePullBackOff`.

## Build

```bash
scripts/teknoirbot-build ./app hello
scripts/teknoirbot-build ./app hello --tag 20261109-1200 --file app/Dockerfile.pi
```

It prints, as its last line, the pull name to put in the Deployment: `image: localhost:5000/...`.

- **A new tag for every build.** The Deployment uses `imagePullPolicy: IfNotPresent`; with a
  reused tag (or `:latest`) the pod keeps running the old image and nothing tells you.
- **`HOME` must be writable** for buildctl; the script sets a temporary `DOCKER_CONFIG` when it is
  not. In the desktop it is `/home/desktop`.
- **Keep the context small** and add a `.dockerignore`: the whole context is streamed to the
  builder.
- The base image is pulled by the builder from upstream, not through the platform's Harbor mirror.
  Anonymous Docker Hub pulls can hit a rate limit when many bots share one egress address.
- amd64 only; no `--platform` is needed.
- Start from `templates/Dockerfile.python`: a pinned slim Python base, a non-root user 10001, and
  `CMD` to replace. Copy it as `Dockerfile` next to your code.

For anything else, the command is
`buildctl --addr "$BUILDKIT_HOST" build --frontend dockerfile.v0 --local context=<dir> --local dockerfile=<dir> --opt filename=Dockerfile --output type=image,name=registry.teknoir-build.svc.cluster.local:5000/<app>:<tag>,push=true,registry.insecure=true`,
which is what the script runs.

## Deploy

Copy `templates/deployment.yaml` and `templates/service.yaml`, replace `<app>` and `<image>`, and

```bash
kubectl -n apps apply -f deployment.yaml -f service.yaml
kubectl -n apps rollout status deploy/<app>
kubectl -n apps logs deploy/<app> --tail 100
kubectl -n apps get pods,events
```

- **Restart** after a new image with a new tag: `kubectl -n apps rollout restart deploy/<app>`.
  `kubectl scale deploy/<app> --replicas 2` works; `statefulsets/scale` too.
- **Resources**: the template asks for 25m CPU and 64Mi and limits 500m and 256Mi. Keep limits
  small: the VM is 4 GiB or 8 GiB and shares it with the desktop, the browser, the builder and the
  registry. A limit sum above the VM's memory gets something OOM-killed, possibly k3s.
- **`readOnlyRootFilesystem: true`** is in the template; mount an `emptyDir` at `/tmp` for
  anything that writes there (Python does).
- **A PVC** works (`persistentvolumeclaims` is allowed, class `local-path`); its size is not
  enforced, so it only documents intent.
- **A ConfigMap or Secret** for settings is allowed; mount it read-only.
- **Environment and flags go in the Deployment**, not in a shell you type: the image usually has
  no shell, and typing into a running pod leaves nothing to reproduce.

## View it

```bash
kubectl -n apps port-forward svc/<app> 8080:80 &
DESKTOP_BROWSER_URL=http://localhost:8080 /usr/local/bin/desktop-browser
scrot /tmp/view.png    # optional: a screenshot you can read
```

`port-forward` binds in the desktop container, where the browser runs, so `localhost` is shared.
There is no ingress controller and no NodePort, so this is the only way to see an app; a person
watching the desktop sees the page too. Stop it with `kill %1`.

## Clean up

The VM's disk is 20 GiB by default (40 GiB on a test bot). The builder's cache, the registry's
volume and the containerd copy of every image all live on it, plus your home at
`/home/desktop`.

| What | Command |
| --- | --- |
| A Deployment and Service | `kubectl -n apps delete deploy/<app> svc/<app>` |
| A volume | `kubectl -n apps delete pvc/<name>` |
| Old image tags in the registry | `curl -s http://registry.teknoir-build.svc.cluster.local:5000/v2/_catalog` and `/v2/<app>/tags/list`; delete a manifest by digest with `curl -s -X DELETE .../v2/<app>/manifests/<digest>` |
| The builder's cache | `buildctl du`, then `buildctl prune --keep-storage 2000` |

- **Delete a tag after every successful redeploy**, and keep one known-good tag.
- **Deleting a tag frees no disk until the registry runs garbage collection**, which happens when
  its pod starts (that is, when the VM restarts or a person restarts it). Say so when the disk
  keeps filling; do not promise the space back.
- **The builder keeps its cache under 8 GB by itself** (it does not grow without bound), so a
  prune is for when a build fails for space, not a routine.
- Watch `df -h /home/desktop` and `kubectl -n apps get events` before a big build: a full disk
  puts the node under disk pressure, new pods stay `Pending`, and recovering needs a person.

## Other paths — a person does this step today

- **Push to Harbor.** There is no push credential for the agent, and the registry the bot builds
  into is reachable only from inside the VM. Hand the person the built image and the app source.
- **Commit to git** so a device's configuration picks the app up. The agent may `git clone` and
  `git status`, but not push: a push needs a credential a person owns.
- **Add a Deployment to a device's own configuration** with `tnctl config` (`teknoir-config`). A
  bot may add a local item of a workload kind to a device it can see (only Pod, Service,
  ConfigMap, Secret, PVC, Deployment, StatefulSet, DaemonSet, ReplicaSet, Job, CronJob, Ingress,
  NetworkPolicy, PDB and HPA `autoscaling/v2`, nothing breaking baseline, no `LoadBalancer`,
  `NodePort` or `externalIPs`). The image must be in a registry the device can pull from, and
  `localhost:5000/...` is not that: the app lives on the bot's computer only.
- **Deploy a bot-built app to a real robot** is not possible today.

## People on a laptop

On a laptop there is no bot builder and no bot registry: `docker build`, `nerdctl build` or
`buildx` with the person's own runtime, push to a registry the device can pull from (Docker Hub,
the teamspace's Harbor path, or their own), then add the Deployment with `tnctl config add
<robot> -f deploy.yaml --wait=applied` or `tnctl config set <robot> <resource> --image
repo:tag --wait`. `scripts/teknoirbot-build` exits 2 there, because `BUILDKIT_HOST` is unset —
that is the pointer to this section, not an error to fix. `tnctl port-forward <device> 8080:<id>`
reaches a service on the device over Tunneling.

## Hazards and the errors you will see

| Symptom | Cause | Fix |
| --- | --- | --- |
| `ImagePullBackOff`, `lookup registry.teknoir-build.svc.cluster.local: no such host` | the Service name in the image | use `localhost:5000/<app>:<tag>` |
| The pod runs the old code, no error | a reused tag with `IfNotPresent` | build a new tag and `rollout restart` |
| `dial tcp 10.53.x.x:1234: connect: connection refused` | building from a pod in `apps`, which may not reach the builder | build from the desktop |
| `#2 ERROR: mkdir /.docker: permission denied` | `HOME` unset or unwritable | `export HOME=/home/desktop`, or let the script set `DOCKER_CONFIG` |
| `violates PodSecurity "baseline:latest": privileged (...)` | a privileged or host-level pod | follow the template's security context; the ReplicaSet carries the refusal, so read `kubectl -n apps get rs,events` |
| `ValidatingAdmissionPolicy 'teknoirbot-apps' ... denied request: only ClusterIP Services are allowed` | a NodePort, LoadBalancer or ExternalName Service | `type: ClusterIP` |
| `Forbidden ... cannot list resource "pods" in the namespace "teknoir-build"` | a call outside `apps` | stay in `apps`; the builder and registry are not yours |
| `Forbidden` on a plain `kubectl get pods` | no `-n apps` | always pass `-n apps` |
| `Pending`, `1 node(s) had untolerated taint {node.kubernetes.io/disk-pressure: }` | the disk is full | delete images, tags and PVCs; a person may need to re-create the bot |
| `OOMKilled` in `kubectl describe pod` | limits too high for the VM | lower the limits and the request |
| `toomanyrequests: You have reached your unauthenticated pull rate limit` | Docker Hub during `FROM` | pin a smaller base image, or ask a person for a Docker Hub token |
| `error: failed to list workers ... dial unix /run/buildkit/buildkitd.sock` | `BUILDKIT_HOST` unset | set it, or run on a bot, not a laptop |
| `Forbidden` on `helm`, `docker`, `nerdctl`, `git push` | not installed, not permitted | none of them exist here; use the paths above |

## Safety

- Never print, copy or mount the bot's key Secret except into the relay of a processor, read-only,
  and never read it back with `kubectl`.
- Read before you write: `kubectl -n apps get -o yaml` and `--dry-run=server -o yaml` on an apply
  show what will change.
- Keep every name in `apps` prefixed with your app so cleanup is obvious.
- `kubectl apply`, `delete`, `patch` and `scale` ask for approval on a bot; the builder build asks
  too. Do not work around a refusal.
- On Codex and OpenCode no guard hook sees your shell: these rules and the platform's checks are
  the only protection.

## Bot mode

In bot mode (`tnctl whoami -o json` reports `"mode":"bot"`) this skill is the main way to run
anything: the bot has no shell on a device and no `ssh`, `exec` or `port-forward` over Tunneling.
Everything above applies to the bot's own computer, and only there.

- `kubectl` and `buildctl` are the bot's tools on its own VM; they do not reach another device.
- The agent's role, the builder and the registry exist only on a bot created with the agent
  enabled. On an older bot they are missing: `kubectl` fails with no service account token and
  `kubectl -n apps` says the namespace does not exist. Tell the person that the bot must be
  deleted and re-created; their home is lost when it is.
- A bot that sees `Forbidden` for everything may have lost its key: a person must re-create it.
- An app that must read the platform's MQTT stream needs the bot key in `apps`, copied there by
  `../teknoir-mqtt-streams/scripts/teknoirbot-app-key`. The copy is as sensitive as the key itself
  and stays inside the bot's own computer. Never `kubectl get` or `kubectl describe` that Secret
  with `-o yaml`, `-o json` or `-o jsonpath`; never `kubectl exec` into a pod that mounts it;
  never print its env or files. Mount it read-only only into the relay. Delete it
  (`kubectl -n apps delete secret <name>`) when the relay is gone.
- The bot's own VM detail, and what a bot may not do at all, is in
  [bot-vm.md](../teknoir-platform/references/bot-vm.md).
