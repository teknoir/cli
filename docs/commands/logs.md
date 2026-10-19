# `tnctl logs`

Show the logs of a workload on a robot (`sudo k3s kubectl logs` over the WireGuard tunnel).

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

The exit status is kubectl's. A name that is neither a resource nor a pod is exit 3; an ambiguous name is
exit 2. Needs the robot's WireGuard tunnel, like [`exec`](exec.md).

## See also

- [`exec`](exec.md), [`config wait`](config.md#waiting-for-a-change).
