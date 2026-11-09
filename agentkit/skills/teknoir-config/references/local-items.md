# Local items: kinds, limits, and what a bot may not add

A local item is one Kubernetes resource stored on one robot's Device and published to the robot with
the rest of its configuration. It wins over a git copy with the same `Kind/namespace/name` and stays
until it is removed. The commands are in [../SKILL.md](../SKILL.md).

`tnctl config kinds` is the authority: it prints the platform's own catalog, and the platform may
change it. This file says what the catalog and the API answer today.

## Kinds `config add` accepts

| API version | Kinds | Note |
|---|---|---|
| `helm.cattle.io/v1` | HelmChart, HelmChartConfig | k3s installs the chart on the robot; removing the item uninstalls the release and what it created. A HelmChartConfig must have the name and namespace of the chart it configures. |
| `apps/v1` | Deployment, StatefulSet, DaemonSet, ReplicaSet | A DaemonSet runs one pod on every node of the robot. |
| `batch/v1` | Job, CronJob | Most of a finished Job cannot be changed: change the name to run it again. |
| `v1` | Pod, Service, ConfigMap, Secret, PersistentVolumeClaim, LimitRange, ResourceQuota, ServiceAccount | Prefer a Deployment over a bare Pod. A ConfigMap holds text: quote numbers and `true`/`false`. A Secret keeps plain text in `stringData` and stores it base64 in `data`, **in the Device in the cloud**: everyone who can read the Device can read it. A ServiceAccount is metadata only: it applies no `imagePullSecrets`. |
| `networking.k8s.io/v1` | Ingress, NetworkPolicy | |
| `autoscaling/v2` | HorizontalPodAutoscaler | `autoscaling/v1` is refused. |
| `policy/v1` | PodDisruptionBudget | |
| `v1`, cluster-scoped | Namespace | Removing a Namespace item deletes everything in that namespace on the robot. |

## Kinds that are refused

`<Kind> cannot be added here: <reason>.` The reason is one of:

| Kinds | Reason |
|---|---|
| Role, RoleBinding, ClusterRole, ClusterRoleBinding | the robot's agent drops their rules, subjects and roleRef, and it runs as cluster-admin |
| ValidatingWebhookConfiguration, MutatingWebhookConfiguration, StorageClass, PriorityClass, RuntimeClass, PodTemplate, Endpoints, EndpointSlice | the item's settings are top-level fields, which the robot's agent drops |
| PersistentVolume, IngressClass, Node, Event, Lease | a cluster-level object is not managed as a robot config item |
| CustomResourceDefinition | there is no schema to check it with, and a custom resource whose CRD is missing on the robot stops the robot's whole configuration from applying |
| ReplicationController | superseded by ReplicaSet and Deployment: use a Deployment |
| List | one resource per item: add the resources one at a time |
| every other kind, including custom resources | `<Kind> (<apiVersion>) cannot be added here: no schema to check it with; a custom resource whose CRD is missing on the robot stops the robot's whole configuration from applying.` |

## Names and fields the platform refuses

- `Deployment toe`, `Deployment mqtt`, `Service mqtt`, `mqtt-dashboard`, `toe-metrics-service`,
  `ServiceAccount teknoir`, `teknoir-controller-manager`, `Secret artifact-registry-secret`: these
  belong to the robot's agent and cannot be added or changed here.
- k3s's own objects in `kube-system` (`HelmChart traefik`, `Deployment coredns`, ...): installed by
  k3s, cannot be added or changed here.
- The bootstrap's Secret `teknoir-registry-secret`, Secret `k3s-serving`, `ServiceAccount default`,
  `ConfigMap kube-root-ca.crt`: created by the robot's bootstrap.
- `Namespace default`, `kube-system`, `kube-public`, `kube-node-lease`: part of every cluster.
- Any name starting `wg-agent` or `teknoir-config-`: reserved for the platform.
- In `kube-system` only HelmChart, HelmChartConfig, ConfigMap and Secret are accepted.
- A name with a dot, or over 253 characters; a namespace that is not a DNS label of at most 63
  characters; `metadata.name` missing.
- A field the robot's agent drops, with the hint: `'<field>' is dropped by the robot's agent: it
  applies only apiVersion, kind, metadata, data, binaryData, type, spec, status of an item`.
  `stringData` works on a Secret only.
- A Namespace that does not exist on the robot yet (a warning for most kinds, an error for a kind the
  robot applies before Namespace items), unless the same file adds the Namespace.
- A selector that does not match the pod template's labels, an empty selector, a duplicate container
  or port name, a container name or port name that is not valid, a Service with several ports where
  one has no name.
- A schema error: `Missing required field ...`, `Invalid type. Expected: integer, given: string
  (spec.replicas)`, `Does not match the pattern ...`, `Not a date and time ...`.

## Limits

| Limit | Value | Message |
|---|---|---|
| one manifest | 256 KiB | refused by the platform |
| a request body | 1 MiB | the JSON parser refuses it |
| the Device after the write | 1.2 MiB (1228 KiB) | `This would make the <robot>'s Device N KiB. At most 1228 KiB (1.2 MiB) fits, because the cluster stores a Device in at most 1.5 MiB. Remove something first.` |
| overridden resources per device | 64 | `At most 64 resources can be overridden on one device. Reset some first.` |
| one `valuesContent` | 65536 characters | `valuesContent is too large (N characters, at most 65536).` |
| all `valuesContent` together | 262144 characters | `Overrides on <device> would hold more than 256 KiB of values; reset some first.` |
| images per resource | 32, each at most 255 characters | `At most 32 container images can be overridden on one resource.` |
| tried applications per device | 32 | `At most 32 applications can be tried on another revision on one device. Reset some first.` |
| a note | 512 characters | `The note can be at most 512 characters.` |
| value operations in one write | 200, path at most 500 characters | `At most 200 value operations at a time.` |
| manifests checked at the same time | 2 | `Busy checking other manifests, try again` (HTTP 503, retry after a moment) |

## What a TeknoirBot may add, and what it may not

A bot writes items that the robot's own agent applies with the agent's privileges, so the platform
filters them. The refusal is HTTP 403, tnctl exits **6**, and the message is
`not allowed for this account or teamspace: A TeknoirBot may not add this item: <reason>. (HTTP 403)`.
It applies to `config add`, `add --replace` and `--dry-run` too.

**A bot may add only these kinds:**

| API version | Kinds a bot may add |
|---|---|
| `v1` | Pod, Service, ConfigMap, Secret, PersistentVolumeClaim |
| `apps/v1` | Deployment, StatefulSet, DaemonSet, ReplicaSet |
| `batch/v1` | Job, CronJob |
| `networking.k8s.io/v1` | Ingress, NetworkPolicy |
| `policy/v1` | PodDisruptionBudget |
| `autoscaling/v2` | HorizontalPodAutoscaler |

Everything else is refused for a bot, including HelmChart, HelmChartConfig, Namespace,
ServiceAccount, LimitRange, ResourceQuota, `autoscaling/v1` and any custom resource. A bot therefore
cannot create a namespace: its items go to an existing one, usually `default`.

**The item must also pass the Pod Security Standards baseline check and these:**

| Refused | Reason text |
|---|---|
| kind outside the list | `an item of kind <Kind> (<apiVersion>): a TeknoirBot may add only workloads, Services, ConfigMaps, Secrets, PersistentVolumeClaims, Ingresses, NetworkPolicies, PodDisruptionBudgets and HorizontalPodAutoscalers` |
| a namespace of the agent or k3s | `an item in the namespace <ns>: it holds the robot's agent and k3s` |
| Secret type `kubernetes.io/service-account-token` | `the Secret type kubernetes.io/service-account-token: it carries a service account token` |
| the Secret annotation `kubernetes.io/service-account.name` | `the annotation kubernetes.io/service-account.name: it carries a service account token` |
| a PVC, a StatefulSet `volumeClaimTemplates` or an ephemeral volume with `volumeName` | `<claim>.volumeName: the claim would bind a volume that already exists` |
| the same with `dataSource` or `dataSourceRef` | `<claim>.dataSource: the claim would be filled from another volume` (or `<claim>.dataSourceRef: …`) |
| a Service with `externalIPs` | `spec.externalIPs: the Service would take an address on the robot's network` |
| a Service of type `LoadBalancer` or `NodePort` | `spec.type <type>: the Service would open a port on the robot's own addresses (k3s ServiceLB)` |
| a Service with a `nodePort` | `spec.ports[<i>].nodePort: the Service would open a port on the robot's own addresses` |
| `hostNetwork`, `hostPID` or `hostIPC` | the field name |
| any `hostPath` volume | `hostPath volume "<name>"` |
| the Secrets `teknoir-registry-secret`, `artifact-registry-secret`, `k3s-serving` in a volume, a projected source, `env.secretKeyRef` or `envFrom.secretRef` | `the Secret <name> in <where>: it holds the robot's registry or cluster credentials` |
| a `serviceAccountName` or `serviceAccount` other than `default` | `<field> <name>: a pod may run only as the default service account` |
| `seccompProfile.type: Unconfined` | `seccompProfile Unconfined` (in a container: `... in <label>`) |
| an AppArmor profile other than `RuntimeDefault` or `Localhost`, or the AppArmor annotation outside `runtime/default` and `localhost/<profile>` | `appArmorProfile <type>` (`... in <label>`); for the annotation: `the AppArmor annotation <key> <value>: only runtime/default or localhost/<profile> may be used` |
| `seLinuxOptions.user` or `role` set, or `type` outside `container_t`, `container_init_t`, `container_kvm_t`, `container_engine_t` | `seLinuxOptions.<field> ...` |
| Windows `hostProcess` | `windowsOptions.hostProcess` |
| a sysctl outside the safe set (`kernel.shm_rmid_forced`, `net.ipv4.ip_local_port_range`, `net.ipv4.ip_unprivileged_port_start`, `net.ipv4.tcp_syncookies`, `net.ipv4.ping_group_range`, `net.ipv4.ip_local_reserved_ports`, `net.ipv4.tcp_keepalive_time`, `net.ipv4.tcp_fin_timeout`, `net.ipv4.tcp_keepalive_intvl`, `net.ipv4.tcp_keepalive_probes`) | `sysctl <name>: only the sysctls the Pod Security Standards call safe may be set` |
| `privileged: true` | `privileged <label>` |
| `allowPrivilegeEscalation: true` | `allowPrivilegeEscalation in <label>` |
| a capability outside AUDIT_WRITE, CHOWN, DAC_OVERRIDE, FOWNER, FSETID, KILL, MKNOD, NET_BIND_SERVICE, SETFCAP, SETGID, SETPCAP, SETUID, SYS_CHROOT (`CAP_` prefix allowed) | `capability <cap> in <label>` |
| a `procMount` other than `Default` | `procMount in <label>` |
| a `hostPort` that is not 0 | `hostPort <n> in <label>` |
| a `priorityClassName` starting `system-` or `high-priority-`, or a `priority` | `priorityClassName <name>: a bot's pod may not outrank the robot's agent` / `priority: a bot's pod may not set its own priority` |

`<label>` is `container "<name>"`, `init container "<name>"`, `ephemeral container "<name>"` or
`<kind> without a name`. The pod spec is read from where the kind keeps it (`spec` for a Pod,
`spec.template.spec` for a workload, `spec.jobTemplate.spec.template.spec` for a CronJob).
`<claim>` is `spec`, `spec.volumeClaimTemplates[<i>].spec` or
`volumes[<i>].ephemeral.volumeClaimTemplate.spec`.

A person is not limited to that list; the platform's own validation still applies to them.
