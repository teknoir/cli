# Prometheus: metrics and recipes

Every series of a device carries the label `device_id` (the device's name) and, for a placed device,
`org_level_<n>` labels. Query through the teamspace's Prometheus:

```bash
q='pod_running{device_id="edge-01"} == 0'
tnctl api GET "<teamspace>/prometheus/query?query=$(jq -rn --arg q "$q" '$q|@uri')" -o json
```

`query_range` takes `&start=&end=&step=` as well. Write `device_id="<name>"` in every selector: an
account limited to org units is refused when a selector does not name the devices it reads. The query
text may be at most 4096 characters, the answer at most 10 MiB, and the platform gives Prometheus 10
seconds.

## What a device reports

The robot's agent builds these every scrape and the platform writes them into the teamspace's
Prometheus. A series that the device stops reporting disappears about 90 seconds later; only
`device_last_seen` and `device_updated_at` stay for a year.

| Metric | Value and unit | Labels |
|---|---|---|
| `device_last_seen` | unix seconds of the last state or event message; 0 = never reported | `device_id` |
| `device_updated_at` | unix seconds of the last configuration message | `device_id` |
| `pod_running` | 1 when the container's reason is `Running`, else 0 | `name`, `namespace`, `node`, `version`, `reason`, the pod's labels |
| `pod_restarts` | restarts of the container | the same |
| `pod_ready_containers`, `pod_total_containers` | counts | the same |
| `pod_age` | nanoseconds | the same |
| `pod_restarted_ago` | nanoseconds since the last restart, only when it restarted | the same |
| `pod_cpu_usage` | **millicores**, only while running | the same |
| `pod_memory_usage` | **bytes**, only while running | the same |
| `pod_resource_requests_cpu`, `pod_resource_limits_cpu` | millicores, 0 = not set | the same |
| `pod_resource_requests_memory`, `pod_resource_limits_memory` | bytes, 0 = not set | the same |
| `pod_net_tx`, `pod_net_rx` | counters | the same |
| `node_ready` | 1 when the Ready condition is true | `name` (the k3s node name) |
| `node_cpu_usage`, `node_memory_usage` | **millicores** and **bytes**, from metrics-server | `name` |
| `node_cpu_allocatable`, `node_cpu_capacity` | millicores | `name` |
| `node_memory_allocatable`, `node_memory_capacity` | bytes | `name` |
| `node_cpu_requests`, `node_cpu_limits`, `node_memory_requests`, `node_memory_limits` | request and limit sums, millicores and bytes | `name` |
| `node_storage_capacity`, `node_storage_usage`, `node_storage_available` | bytes | `name` |
| `node_net_tx`, `node_net_rx`, `node_if_net_tx`, `node_if_net_rx` | counters; `rate()` of them is KB/s | `name` (+ the interface) |
| `toe_build_info` | always 1 | `version`, `commit`, `capabilities` - the robot agent's version |
| `DCGM_FI_DEV_GPU_UTIL`, `_GPU_TEMP`, `_FB_USED`, `_FB_FREE`, `_POWER_USAGE`, ... | the GPU's own numbers (`_FB_*` in MiB) | `gpu`, `modelName`; empty when the GPU exporter is not deployed |
| `up` | 1 while the target answers, from Prometheus itself (no `device_id`: a caller limited to org units is refused for it) | `job`, `instance` |

`reason` is the Kubernetes reason (`Running`, `Pending`, `CrashLoopBackOff`, `ImagePullBackOff`,
`ErrImagePull`, `ContainerCreating`, `OOMKilled`, `Error`, `Completed`, `Terminating`, `NotReady`,
`SchedulingGated`, `Init:...`, `ExitCode:N`).

The robot's agent builds each sample as a record whose metric-name field is **`metricId`** (for
example `node_cpu_usage`) with its `description`, `labels` and `value`; the platform turns that into a
Prometheus series named `metricId` with the label `device_id` added (label names with `.`, `/` or `-`
become `_`). Use the metric name; never query `metricId` as a label.

There is no kube-state-metrics: no `kube_*` series.

## Recipes

Replace `edge-01` with the device's name. Each selector is pinned to one device.

| # | Question | Query |
|---|---|---|
| 1 | Seconds since the device last reported (empty = never) | `time() - max(device_last_seen{device_id="edge-01"} > 0)` |
| 2 | Is it online (under 300 s)? | `(time() - max(device_last_seen{device_id="edge-01"} > 0)) < 300` |
| 3 | Firing alerts on the device, by rule and pod | `count by (alertname, name, reason) (ALERTS{device_id="edge-01",alertstate="firing"})` |
| 4 | How long each alert has been firing (seconds) | `time() - ALERTS_FOR_STATE{device_id="edge-01"}` |
| 5 | Pods not running, with the Kubernetes reason now (no 5 minute wait) | `pod_running{device_id="edge-01"} == 0` |
| 6 | Pods whose containers are not all ready | `pod_ready_containers{device_id="edge-01"} != pod_total_containers{device_id="edge-01"}` |
| 7 | Pods that restarted, and how often | `max by (namespace, name) (pod_restarts{device_id="edge-01"}) > 0` |
| 8 | Pod memory and CPU against the limits (a fraction; above 0.9 alerts) | `pod_memory_usage{device_id="edge-01"} / (pod_resource_limits_memory{device_id="edge-01"} > 0)`, and the same with `pod_cpu_usage` / `pod_resource_limits_cpu` (both millicores) |
| 9 | Node CPU and memory (a fraction) | `max by (name) (node_cpu_usage{device_id="edge-01"} / node_cpu_allocatable{device_id="edge-01"})`, and the same with `node_memory_*` |
| 10 | Disk used (a fraction) and free (GB) | `max by (name) (node_storage_usage{device_id="edge-01"} / node_storage_capacity{device_id="edge-01"})`, `node_storage_available{device_id="edge-01"} / 1e9` |
| 11 | Nodes not ready, and how many joined | `node_ready{device_id="edge-01"} == 0`, `count(node_ready{device_id="edge-01"})` |
| 12 | The robot agent's version and capabilities | `toe_build_info{device_id="edge-01"}` |

Read the answer with jq:

```bash
<the answer> | jq -r '.data.result[] | [.metric.name // .metric.alertname, .value[1]] | @tsv'
```

A trend uses `query_range` with `start`, `end` and `step`, for example a node's CPU over six hours:
`query=max(node_cpu_usage{device_id="edge-01"} / node_cpu_allocatable{device_id="edge-01"})&start=<from>&end=<to>&step=5m`.
The retention of the teamspace's Prometheus is finite; older history may need Thanos in that
installation.

## Thresholds the platform uses

| Signal | Threshold | Alert |
|---|---|---|
| device silence | 180 s | `lost` |
| online | under 300 s | the robot's phase |
| pod running | `pod_running == 0` for 5 min | `PodNotRunning` |
| pod restarts | `rate(pod_restarts[15m]) > 0.2` for 15 min | `DevicePodFrequentRestarts` |
| pod readiness | containers ready not equal for 5 min | `DevicePodNotReady` |
| pod CPU or memory | above 90 % of the limit for 15 min | `DevicePodHighCPUUsage`, `DevicePodHighMemoryUsage` |
| node CPU or memory | above 90 % of allocatable for 15 min | `NodeHighCPUUsage`, `NodeHighMemoryUsage` |
| disk | above 75 % used and under 20 GB free, or above 90 % used, for 1 min | `LowDiskSpace` |
| node readiness | `node_ready == 0` for 5 min | `NodeNotReady` |
