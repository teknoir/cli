---
name: teknoir-k3s-apps
description: Build and run your own apps on a TeknoirBot computer. It covers the inner k3s, the apps namespace and its admission rules, buildctl to the bot builder, the bot registry, Deployments and Services, checking an app from the council pod, and cleanup. Use when asked to build an image, write a Dockerfile, deploy, run, restart, scale or debug an app inside a TeknoirBot, or to view a service on the bot computer. For a robot configuration use teknoir-config; for device telemetry use teknoir-mqtt-streams.
license: Apache-2.0
metadata:
  teknoir-kit-version: "0.2.0"
---

# Apps on a TeknoirBot's computer

Paths in this skill are relative to this skill's directory.

## What exists

A TeknoirBot's computer is a virtual Device (`tb-<bot>`) with its own single-node k3s. You reach
it with the in-cluster `kubectl` of the council pod you run in (`teknoirbot-council` in
`teknoir-desktop`).

- **The `apps` namespace** is yours: PodSecurity **baseline** enforced (with a restricted
  warning). Baseline refuses a privileged container, host namespaces, `hostPath`, `hostPort`,
  capabilities added beyond the default set, and unsafe sysctls.
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
  `teknoirbot-agent`, whose default namespace is `teknoir-desktop` — the namespace of the council
  pod you run in, where you have no role: **every call passes `-n apps`**. Never `kubectl
  config set-context`. Put `-n apps` after the verb (`kubectl get pods -n apps`).
- **The builder** is `buildkitd` in `teknoir-build`, reached as
  `tcp://buildkitd.teknoir-build:1234` (`$BUILDKIT_HOST`). Only the council pod may connect to
  it: building from a pod in `apps` is refused with `connection refused`.
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
  not. In the council pod it is `/work/home`.
- **Check the disk** with `df -h /work` before a big install or build: the work volume is a
  local-path volume on the VM's own disk — the same 20 GiB as the builder cache, the registry and
  the images — and its 5Gi size is not enforced. A full disk stops the Coder and puts the node under
  disk pressure. `/tmp` is 2 GiB and goes with the pod: keep what you need under `/work`.
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
kubectl apply -f deployment.yaml -f service.yaml -n apps
kubectl rollout status deploy/<app> -n apps
kubectl logs deploy/<app> -n apps --tail 100
kubectl get pods,events -n apps
```

- **A new image** (a new tag): put the new `image:` in deployment.yaml and run
  `kubectl apply -f deployment.yaml -n apps` again, or
  `kubectl set image deploy/<app> <app>=localhost:5000/<app>:<tag> -n apps`. A changed image rolls
  the pods by itself. `kubectl rollout restart deploy/<app> -n apps` restarts the same image, for
  example after a ConfigMap or Secret change.
- `kubectl scale deploy/<app> --replicas 2 -n apps` works; `statefulsets/scale` too.
- **Resources**: the template asks for 25m CPU and 64Mi and limits 500m and 256Mi. Keep limits
  small: the VM is 4 GiB or 8 GiB and shares it with the council pod (it requests 512Mi), the
  desktop, the browser, the builder and the registry. A limit sum above the VM's memory gets
  something OOM-killed, possibly k3s.
- **`readOnlyRootFilesystem: true`** is in the template; mount an `emptyDir` at `/tmp` for
  anything that writes there (Python does).
- **A PVC** works (`persistentvolumeclaims` is allowed, class `local-path`); its size is not
  enforced, so it only documents intent.
- **A ConfigMap or Secret** for settings is allowed; mount it read-only.
- **Environment and flags go in the Deployment**, not in a shell you type: the image usually has
  no shell, and typing into a running pod leaves nothing to reproduce.
- **A service that should last is a Deployment**, not a process you leave running: anything you
  start by hand ends with the Coder's task.

## View it

```bash
curl -s http://<app>.apps.svc.cluster.local:<port>/
```

There is no ingress controller and no NodePort, and nothing outside the VM reaches an app, so that
in-cluster URL is how you check one: each Service is `<app>.apps.svc.cluster.local` on its port.
There is no browser, no X server and no screenshot tool in the council pod, so you cannot look at a
page yourself: check what you can with `curl` (the status, the HTML, an API answer), and give the
person the URL to open in the bot's desktop browser — the desktop runs in the same cluster.

## Clean up

The VM's disk is 20 GiB by default (40 GiB on a test bot). The builder's cache, the registry's
volume and the containerd copy of every image all live on it. Your own files live at `/work`
(`HOME=/work/home`, and one directory per chat at `/work/chat-<id>`), a local-path volume on that
same disk: they share its free space, and the volume's 5Gi size is not enforced. `df -h /work` shows
the VM disk's free space. A full disk stops the Coder and puts the node under disk pressure, so
clean up builds and images you no longer need.

| What | Command |
| --- | --- |
| A Deployment and Service | `kubectl delete deploy/<app> svc/<app> -n apps` |
| A volume | `kubectl delete pvc/<name> -n apps` |
| Old image tags in the registry | Put `R=http://registry.teknoir-build.svc.cluster.local:5000;` in front of each command (a variable does not reach the next command); list `curl -s $R/v2/_catalog` and `curl -s $R/v2/<app>/tags/list`; digest `curl -sI -H 'Accept: application/vnd.oci.image.index.v1+json, application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.v2+json, application/vnd.docker.distribution.manifest.list.v2+json' $R/v2/<app>/manifests/<tag>` (read `Docker-Content-Digest`); delete `curl -s -X DELETE $R/v2/<app>/manifests/<digest>` (202; it removes every tag on that digest) |
| The builder's cache | `buildctl du`, then `buildctl prune --keep-storage 2000` |

- **Delete a tag after every successful redeploy**, and keep one known-good tag.
- **Deleting a tag frees no disk until the registry runs garbage collection**, which happens when
  its pod starts (that is, when the VM restarts or a person restarts it). Say so when the disk
  keeps filling; do not promise the space back.
- **The builder keeps its cache under 8 GB by itself** (it does not grow without bound), so a
  prune is for when a build fails for space, not a routine.
- Watch `df -h /work` and `kubectl get events -n apps` before a big build: a full work volume stops
  the Coder, and a full VM disk puts the node under disk pressure, new pods stay `Pending`, and
  recovering needs a person.

## Other paths — a person does this step today

- **Push to Harbor or another registry.** The agent has no push credential, and the image exists
  only in the bot's registry, which nothing outside the VM reaches. Tell the person where the
  source and Dockerfile are (in your work directory under `/work`) and give them the exact build and
  push commands to run on their own machine (see People on a laptop).
- **Commit to git.** The agent may clone, commit locally and show the change (`git diff`,
  `git log -p`), but never pushes. Never ask for, paste or store a git or registry token on the
  bot's computer: its home persists, and its creator (and teamspace admins, unless it acts as its
  creator) open it. Give the person the repository, the branch, the diff and the exact `git`
  commands to push from their own machine.
- **Add a Deployment to a device's own configuration** with `tnctl config` (`teknoir-config`). A
  bot may add a local item of a workload kind to a device it can see (only Pod, Service,
  ConfigMap, Secret, PVC, Deployment, StatefulSet, DaemonSet, ReplicaSet, Job, CronJob, Ingress,
  NetworkPolicy, PDB and HPA `autoscaling/v2`, nothing breaking baseline, no `LoadBalancer`,
  `NodePort` or `externalIPs`). The image must be in a registry the device can pull from, and
  `localhost:5000/...` is not that: the app lives on the bot's computer only. Never the bot's own
  computer `tb-<bot>`: every config write there answers '… is the computer of TeknoirBot <bot>; its
  configuration is managed by the platform.' (exit 8). Do not retry it or use `--replace`; run the
  app in `apps` instead.
- **Deploy a bot-built app to a real robot** is not possible today.

## People on a laptop

On a laptop there is no bot builder and no bot registry: `docker build`, `nerdctl build` or
`buildx` with the person's own runtime, push to a registry the device can pull from (Docker Hub,
ghcr.io, or their own registry; teamspaces cannot push to the platform's Harbor, which only caches
public registries), then add the Deployment with
`tnctl config add <robot> -f deploy.yaml --wait=applied` or
`tnctl config set <robot> <resource> --image repo:tag --wait`.
`scripts/teknoirbot-build` exits 2 there, because `BUILDKIT_HOST` is unset — that is the pointer
to this section, not an error to fix. `tnctl port-forward <device> 8080:<service-id>` reaches
one of the device's Tunneling services (`tnctl port-forward <device>` lists them); a newly added
app is reachable this way only once it is declared as one of them.

## Hazards and the errors you will see

| Symptom | Cause | Fix |
| --- | --- | --- |
| `ImagePullBackOff`, `lookup registry.teknoir-build.svc.cluster.local: no such host` | the Service name in the image | use `localhost:5000/<app>:<tag>` |
| `ImagePullBackOff` … `127.0.0.1:5000: connect: connection refused` right after the computer restarted | the registry is still starting (it cleans up first) | wait a minute and check again; do not rebuild |
| a tag changed under you | the registry has no login; anything in `apps` can push | pin what you deploy by digest (`<image>@sha256:…`) when it matters, and never run untrusted images in `apps` |
| The pod runs the old code, no error | a reused tag with `IfNotPresent` | build a new tag and set it in the Deployment (`kubectl set image deploy/<app> <app>=localhost:5000/<app>:<new-tag> -n apps`) |
| `dial tcp 10.53.x.x:1234: connect: connection refused` | building from a pod in `apps`, which may not reach the builder | build from the council pod (the Coder) |
| `#2 ERROR: mkdir /.docker: permission denied` | `HOME` unset or unwritable | `export HOME=/work/home`, or let the script set `DOCKER_CONFIG` |
| `violates PodSecurity "baseline:latest": privileged (...)` | a privileged or host-level pod | follow the template's security context; the ReplicaSet carries the refusal, so read `kubectl get rs,events -n apps` |
| `ValidatingAdmissionPolicy 'teknoirbot-apps' ... denied request: only ClusterIP Services are allowed` | a NodePort, LoadBalancer or ExternalName Service | `type: ClusterIP` |
| `Forbidden ... cannot list resource "pods" in the namespace "teknoir-build"` | a call outside `apps` | stay in `apps`; the builder and registry are not yours |
| `Forbidden` on a plain `kubectl get pods` | no `-n apps` | always pass `-n apps` |
| `Pending`, `1 node(s) had untolerated taint {node.kubernetes.io/disk-pressure: }` | the disk is full | delete images, tags and PVCs; a person may need to re-create the bot |
| `OOMKilled` in `kubectl describe pod <pod> -n apps` | limits too high for the VM | lower the limits and the request |
| `toomanyrequests: You have reached your unauthenticated pull rate limit` | Docker Hub during `FROM` | wait and build again later, or take the same image from a mirror of Docker's official images (`FROM public.ecr.aws/docker/library/python:3.12.7-slim` or `FROM mirror.gcr.io/library/python:3.12.7-slim`); never ask for or store a Docker Hub token on the bot's computer |
| `error: failed to list workers ... dial unix /run/buildkit/buildkitd.sock` | `BUILDKIT_HOST` unset | set it, or run on a bot, not a laptop |
| `command not found` for `helm`, `docker`, `nerdctl` | not installed | build with `scripts/teknoirbot-build`, deploy with `kubectl` |

## Safety

- Never print, copy or mount the bot's key Secret except into the relay of a processor, read-only,
  and never read it back with `kubectl`.
- Read before you write: `kubectl get deploy/<app> -n apps -o yaml` (never a Secret) shows what
  runs, `kubectl diff -f <file> -n apps` shows what will change (exit 1 means it found
  differences), and `kubectl apply -f <file> -n apps --dry-run=server` shows whether admission
  accepts it.
- Keep every name in `apps` prefixed with your app so cleanup is obvious.
- In the council pod nothing asks before a write: read first (`kubectl diff`, `--dry-run=server`)
  and keep every change in the `apps` namespace.

## Bot mode

In bot mode (`tnctl whoami -o json` reports `"mode":"bot"`) this skill is the main way to run
anything: the bot has no shell on a device and no `ssh`, `exec` or `port-forward` over Tunneling.
Everything above applies to the bot's own computer, and only there.

- `kubectl` and `buildctl` are the bot's tools on its own VM; they do not reach another device.
- The agent's role, the builder and the registry come with the agent: the platform adds them to
  every bot computer whose council runs, and they can arrive a little after the council pod starts.
  If `kubectl get pods -n apps` answers `Forbidden`, or `kubectl get ns apps` answers `NotFound`,
  just after a start, wait a minute and try again; if it stays that way, report it to a person.
  Never suggest re-creating the bot for this.
- Only when tnctl says 'the bot key is missing or empty' or 'the bot API did not accept the bot's
  key' (exit 6) must a person re-create the bot; its home is lost when they do.
- `Forbidden` from `kubectl` comes from the Role in `apps` (see the hazards table), never from the
  bot's key. For `tnctl`, read `.bot.access` in `tnctl whoami -o json`: `none` means the bot's
  access is off, so ask its creator to turn it back on.
- An app that must read the platform's MQTT stream needs the bot key in `apps`, copied there by
  `../teknoir-mqtt-streams/scripts/teknoirbot-app-key`. The copy is as sensitive as the key itself
  and stays inside the bot's own computer. Never `kubectl get` or `kubectl describe` that Secret
  with `-o yaml`, `-o json` or `-o jsonpath`; nor with any other output format (`-o go-template`,
  `-o template`, `-o custom-columns`), nor by listing the Secrets of `apps` with an output format,
  nor with `kubectl edit`: list Secrets only with the default table or `-o name`. Never
  `kubectl exec` into a pod that mounts it; never print its env or files. Mount it read-only only
  into the relay. Delete it (`kubectl delete secret <name> -n apps`) when the relay is gone.
- The bot's own VM detail, and what a bot may not do at all, is in
  [bot-vm.md](../teknoir-platform/references/bot-vm.md).
