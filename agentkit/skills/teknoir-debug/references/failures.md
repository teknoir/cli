# Failures in detail

The long tables behind [../SKILL.md](../SKILL.md): what an image pull failure, a crash loop and a
refused virtual device each look like, and what to do about them.

## ImagePullBackOff and friends

The pod's `waiting` state is `ImagePullBackOff`, `ErrImagePull`, `ErrImageNeverPull` or the reason is
`InvalidImageName`. The container never started, so it has **no logs**: the events are the evidence.

```bash
tnctl inspect pod <robot> <namespace>/<pod> -n <teamspace> -o json | jq '.events[] | select(.type=="Warning")'
```

| Event message you will see | Cause | Fix |
|---|---|---|
| `manifest unknown`, `not found`, `does not exist` | the tag is wrong, or the image was never pushed | check the tag with `tnctl config get <robot> <resource>`; the config skill changes it |
| `unauthorized`, `authentication required`, `pull access denied`, `403` | the image is private and the robot has no credential for that registry, or the credential expired | the image must be pullable by the robot: an admin, or the registry the robot already logs in to |
| `no such host`, `dial tcp ... i/o timeout`, `TLS handshake timeout` | the robot cannot reach the registry (DNS, egress, an airgapped robot without a mirror) | network or the Harbor mirror: a person's job |
| `InvalidImageName` | the image string is not a valid reference (a space, a stray `@`) | fix the value; `--dry-run` shows the effective image |
| `failed to resolve` plus a `toomanyrequests` line | the registry is rate-limiting | use a tag that is already cached, or another registry |

A moving tag (`:latest`) that was already pulled does not change the pod: nothing rolls out. Use a new
tag or a digest.

## CrashLoopBackOff and other exits

The container starts and stops. `inspect pod` has the count, the last termination reason and the exit
code; `inspect logs --previous` has what the app printed before it died.

```bash
tnctl inspect pod <robot> <namespace>/<pod> -n <teamspace> -o json | jq '.pod.containers[]'
tnctl inspect logs <robot> <namespace>/<pod> -n <teamspace> --previous --tail 100
```

| What you see | Cause | Fix |
|---|---|---|
| last state `OOMKilled` (exit 137), `oomKilled: true` | the container passed its memory limit | raise `resources.limits.memory` (teknoir-config), or cut the load |
| exit 1 or 2 in the previous log | the app's own error (a bad config value, a missing file, a failed dependency) | the log line above the exit tells you |
| exit 126 or 127 | the command or entrypoint is wrong or missing in the image | fix the image or the `command`/`args` |
| `CreateContainerConfigError` | a referenced ConfigMap or Secret does not exist, or a key is missing | add the item (`config add`) or fix the name |
| `FailedMount` | a volume or a PVC is missing or bound to the wrong node | a storage problem: the pod's events name the volume |
| `FailedScheduling` | no node has the CPU, memory or taint tolerance | look at `node_cpu_limits`/`node_memory_limits` against `node_*_allocatable`, or an `UntoleratedTaint` line in the event |
| the container is `not-ready` but running | the readiness probe fails | `inspect logs` plus the probe's `Unhealthy` event |
| restarts without a clear reason, `pod_restarted_ago` small | a liveness probe kills it | compare the probe's `initialDelaySeconds` with the app's start time |

Exit 137 without `oomKilled` is usually SIGKILL from a liveness probe or a node under memory
pressure. A pod that is `Terminating` for a long time is waiting on a finalizer or a stuck unmount.

## A virtual device that is NotAdmitted

`NotAdmitted` is not a robot phase. It is the state of a **virtual device** whose request the platform
refused, and of a TeknoirBot whose computer could not be admitted. The reason is in the device's
`Admitted` condition, which the fleet API does not return, so a robot on such a device just shows
`WaitingForDevice`, `Registered` or `Offline`.

| Reason | Cause |
|---|---|
| `MissingSpec` | `spec.virtual` is missing, or its cpu, memory or disk cannot be read |
| `NameTooLong` | the name is too long for a virtual device's DNS label |
| `NoAllowance` | the teamspace has no virtual-device allowance (its `<teamspace>-vdev` namespace, quota or annotation is missing or invalid) |
| `ExceedsMax` | cpu, memory or disk is above the maximum for one device |
| `AllowanceExceeded` | the teamspace allowance is used up; for a device that already has a VM this is a warning and the VM keeps running |
| `QuotaExceeded`, `CreateRefused` | Kubernetes refused the VM (a ResourceQuota, or a webhook) |
| `RegistryCredentialMissing` | the VM cannot pull from the registry |
| `VirtualDevicesDisabled`, `RuntimeMissing` | virtual devices are off in this environment, or the VM runtime is not installed |

Only a platform administrator changes an allowance or the runtime. Tell the person the reason and
send them to the device's or the bot's page in the portal; do not guess.

## A TeknoirBot's computer

A bot's computer is a device named `tb-<bot>`. Its phase is the bot's own (`Pending`, `Suspended`, and
the virtual device's phases), which the fleet API does not return. Its robot, if it has one, shows the
usual symptoms: `WaitingForDevice` or `Offline` with no app to blame. A bot cannot read its own
computer's state; the person reads it on the Bots page.

## Access states of a robot

`RobotAccess.terminal.state` and the app access states, first match wins:

| State | Condition |
|---|---|
| `disabled` | the device is disabled |
| `paused` | the tunnel is `revoked`, `disabled` or `pending` |
| `tunneling-off` | Tunneling is off for the robot |
| `no-exec` | the caller may not open a terminal (`pods/exec`) |
| `offline` | the robot's agent is not connected |
| `outdated` | the robot's agent is too old for the feature |
| `off` | the terminal is not declared, and the caller may turn it on |
| `enable-later`, `cap` | writes are disabled, or the teamspace limit of robots with browser access is reached |
| `starting` | declared, not published or not ready yet: usually under a minute |
| `ready` | it opens now |
| `unavailable` | the device's state could not be read |
