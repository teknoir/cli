# `tnctl logs`

Show the logs of a workload on a robot (`k3s kubectl logs` as root, over the WireGuard tunnel).

```bash
tnctl logs <target> <resource|pod> [-c container] [-f] [--tail N] [--since 10m] [--previous] [--all-containers]
```

`<target>` is a robot or device name. `<resource>` is a Deployment, StatefulSet, DaemonSet, Job or Pod of
the robot's configuration (`Kind/namespace/name`, `Kind/name` or a unique name), or a HelmChart: then the
logs of the release's workload (when the release has several, the error lists them so you can name one).
Anything that is not in the configuration is taken as the name of a pod on the robot; a unique prefix of
the pod name is enough.

```bash
tnctl logs vision-station-01 HelmChart/default/notebook --tail 50
tnctl logs vision-station-01 Deployment/default/whoami -f
tnctl logs vision-station-01 notebook-77f875b6c-45s8v -c notebook --previous
```

| Flag | Description |
| --- | --- |
| `-c, --container` | Container name. |
| `-f, --follow` | Follow the log until you stop it. |
| `--tail N` | Lines from the end. Default 200, `-1` for all. |
| `--since D` | Only logs newer than this, such as `30s`, `10m`, `2h`. |
| `--previous` | The previous instance of the container (after a crash). |
| `--all-containers` | All containers of the pod. |

The exit status is kubectl's (ssh's 255, "cannot reach the robot", is exit 1). A name that is neither a resource nor a pod is exit 3; an ambiguous name is
exit 2; a disabled device is exit 5 (`-o json` code `device_disabled`). Needs the robot's WireGuard tunnel, like [`exec`](exec.md).

kubectl runs as root through sudo, the way `tnctl exec --root` does: only when sudo on the robot asks for the
login user's password does `tnctl` send the one the platform holds, as the first stdin line of the encrypted
ssh session. If root is not to be had the command exits 1 with the reason; see
[Running as root](exec.md#running-as-root). An ssh that fails before kubectl starts is `cannot reach <robot>
over WireGuard: <what ssh said>` (exit 1), never a refusal of sudo's.

Do not repeat a `logs` that sudo refused: on a robot with sudo 1.9.9 (Ubuntu 22.04) that locks accounts
(`pam_faillock`) every `tnctl` run that sudo does not accept a password from is one failed login, and the
third locks the login's password (not the robot's connection to the platform). Three `logs` started at the
same moment do that even with the right password; start them one after the other. See
[Failed logins and `pam_faillock`](exec.md#failed-logins-and-pam_faillock).

## See also

- [`exec`](exec.md), [`config wait`](config.md#waiting-for-a-change).
