# GPU/NIC Rail Mapping

The `gpu-nic-rail-mapping` node-metadata plugin pairs each rail's NIC with the GPUs that have the best PCIe locality to it, and publishes the result as one `NodeGPUNICRailMapping` custom resource per Node. A consumer such as the SR-IOV DRA driver turns that into NIC device attributes, so Kubernetes DRA can allocate a NIC together with a nearby GPU.

The plugin is independent of `provider.name` and does not change `topology.Graph` or any label Topograph writes. Topograph never creates or modifies `ResourceSlice` objects, and the DRA attribute names and encoding belong to the consuming driver.

## How it works

On every Node, the Node Data Broker:

1. Runs `lldpctl -f json` against the host `lldpd` socket and keeps the interfaces that report a neighbor.
2. Matches each interface name against `interfaceRegex`, skips interfaces that do not match, and expands each match through the `railIDFormat` template. The interface name is the only input to the rail ID: LLDP chassis and port identifiers are ignored. Rails must be unique, and each matching interface resolves to a NIC PCI address through `/sys/class/net/<interface>/device`.
3. Runs `nvidia-smi --query-gpu=index,uuid,pci.bus_id` and `nvidia-smi topo -all -m` in the one GPU Operator pod of the configured DaemonSet that runs on the same Node.
4. For each NIC, picks the matrix row and keeps every GPU tied at the closest path category (`PIX` < `PXB` < `PHB` < `NODE` < `SYS`; `NV#` links are GPU-to-GPU and never rank a NIC).
5. Publishes the result with the status subresource.

The legend of `nvidia-smi topo` names a NIC by RDMA device name (`mlx5_8`) or by interface name. The broker joins on either: the interface name, or the RDMA devices under `/sys/class/net/<interface>/device/infiniband/`. More than one match is an error.

### Data Direct

When the legend has a Data Direct (DD) function for the NIC's interface, its row is authoritative, even when its path category is farther than the physical NIC row. The DD row carries the GPU traffic. Without a DD function, the NIC row is used.

For example, `roce_vf_r0` is NIC2 and DD0 in the legend. NIC2 reaches GPU0 and GPU1 at `NODE`, while DD0 reaches GPU1 at `PXB`. The DD row wins, so rail 0 maps to GPU1:

```
roce_vf_r0 -> rail-0 -> 0000:3b:00.0 -> DD0 -> PXB -> GPU1
```

## The `NodeGPUNICRailMapping` resource

| | |
|---|---|
| Group, version | `topograph.run/v1alpha1` |
| Resource | `nodegpunicrailmappings` (namespaced, status subresource) |
| Name | `node-<node-uid>`; the Node UID, not its name, so a replacement Node cannot collide with stale state |
| Owner | the Node (`ownerReferences`), so Kubernetes garbage collection removes it with the Node |

`spec.nodeRef` names the source Node and is immutable. The discovered mapping is observed state and lives in `status`:

```yaml
apiVersion: topograph.run/v1alpha1
kind: NodeGPUNICRailMapping
metadata:
  name: node-<node-uid>
  namespace: <topograph-namespace>
  labels:
    app.kubernetes.io/instance: <helm-release-name>
    app.kubernetes.io/managed-by: topograph
    topograph.run/node-metadata-plugin: gpu-nic-rail-mapping
    topograph.run/node-uid: <node-uid>
spec:
  nodeRef: {name: <node-name>, uid: <node-uid>}
status:
  sources:
    lldp: "2026-01-02T03:04:05Z"
    nvidiaSmi: "2026-01-02T03:04:06Z"
  nics:
    "0000:3b:00.0":
      interface: roce_vf_r0
      railID: rail-0
      topologyTarget: DD0
      mappingMode: data-direct
      bestPath: PXB
      bestGpus:
        - uuid: GPU-...
          pciAddress: "0000:17:00.0"
    "0000:5e:00.0":
      interface: roce_vf_r1
      railID: rail-1
      mappingMode: none
      bestGpus: []
```

- `nics` is keyed by the normalized host PCI address (`dddd:bb:ss.f`, lowercase). Each `railID` occurs once.
- Identity is always a NIC PCI address and a GPU UUID or PCI address. `topologyTarget` (`NIC2`, `DD0`) is explainability metadata, never an identity.
- `mappingMode` is `data-direct` when a DD row was used, `nic` when the physical NIC row was used, and `none` when there is no usable GPU path. Topograph never invents a pairing to avoid `none`. `topologyTarget` and `bestPath` are omitted for `none`.
- `bestGpus` holds every GPU tied at `bestPath`, sorted by UUID. MIG devices inherit the parent GPU's mapping and are not published separately.
- Use `nodeDataBroker.nodeSelector` or affinity to schedule brokers on the intended GPU nodes. The plugin does not inspect GPU labels or resource capacity; when rail interfaces are present, GPU inventory and topology discovery must succeed.
- The mapping describes PCIe locality, not GPUDirect RDMA readiness or network health.

The CRD schema constrains required fields, PCI-address keys, timestamp formats, and the `mappingMode` and `bestPath` values. The broker enforces the cross-field invariants before publishing.

### Refresh and failure behavior

The broker reconciles at startup and then every `refreshInterval`. Records are deterministic apart from the `sources` timestamps. To avoid one etcd write per Node per interval on an idle cluster, a refresh that finds an unchanged mapping writes nothing until the stored timestamps are an hour old.

| Condition | Result |
|---|---|
| LLDP, `nvidia-smi`, parsing, or publication error | The previous record is kept; the failure is logged and retried with backoff. The broker continues serving `/healthz`, so plugin failures do not affect probes. |
| No or more than one eligible GPU Operator pod on the broker's Node | As above |
| Ambiguous interface, rail, NIC, DD or PCI join | As above |
| A successful collection selects no rail interface | The Node's record is deleted |
| The CRD is not installed yet | The failure is logged and retried with backoff |
| An existing resource fails identity or ownership validation | Topograph refuses to adopt it and leaves it in place |

The Node Observer deletes this release's mapping resources when the plugin is disabled, and removes stale ones (whose Node no longer exists, or whose identity fields disagree) while it is enabled. It deletes only resources that carry the plugin and release labels. It never discovers hardware.

## Configuration

Enable the plugin in the Helm values. It requires `nodeDataBroker.enabled: true`.

```yaml
nodeDataBroker:
  enabled: true

nodeMetadata:
  plugins:
    gpuNicRailMapping:
      enabled: true
      refreshInterval: 10m
      railSource:
        name: host-lldp
        hostLLDP:
          interfaceRegex: '^roce_vf_r([0-9]+)$'
          railIDFormat: 'rail-$1'
          socket: /var/run/lldpd.socket
      gpuSource:
        gpuOperatorNamespace: gpu-operator
        daemonSet: nvidia-device-plugin-daemonset
        container: ""   # empty selects the pod's default container
```

| Value | Meaning |
|---|---|
| `refreshInterval` | How often each broker collects and, when the mapping changed, republishes. Default `10m`. |
| `railSource.name` | `host-lldp`, the only supported source. |
| `railSource.hostLLDP.interfaceRegex` | Selects rail interfaces from those with an LLDP neighbor. Interfaces that do not match are skipped. |
| `railSource.hostLLDP.railIDFormat` | Rail-ID template expanded from the regular expression's groups (`$1`, `${name}`). Must not expand to an empty value. |
| `railSource.hostLLDP.socket` | Host `lldpd` control socket. Default `/var/run/lldpd.socket`. |
| `gpuSource.gpuOperatorNamespace`, `daemonSet`, `container` | The GPU Operator workload whose same-Node pod runs `nvidia-smi`. |

Enabling the plugin does not change `provider.name` or the provider configuration. Providers without a broker annotation handler skip Node annotation updates; the broker still runs the mapping plugin. Run the broker on every Node whose NICs should be published, using `nodeDataBroker.nodeSelector` and `nodeDataBroker.tolerations`.

### What the chart deploys

- The `NodeGPUNICRailMapping` CRD from `crds/`. Helm installs it before namespaced resources but never upgrades or deletes it, so a schema change needs an explicit compatibility and storage-version migration.
- A read-only hostPath mount of the host `/sys` at `/host/sys` and a read-only hostPath mount of the `lldpd` socket's parent directory at `/host/lldpd`. The broker uses the fixed sysfs path `/host/sys`; it is not a plugin configuration option. The chart rewrites the broker's socket path to that mount. The directory uses `DirectoryOrCreate`, so a missing socket or parent directory does not prevent startup; collection failures are logged and retried until `lldpd` is available. Mounting the directory also lets the broker reconnect when `lldpd` replaces its socket on restart. All of `/sys` is mounted because the entries under `/sys/class/net` are relative symlinks into `/sys/devices`.
- The broker image needs `lldpctl`; the Topograph image installs the `lldpd` package for it.
- The broker runs as a non-root user. If the `lldpd` socket is not world-accessible, add the socket's group to `nodeDataBroker.podSecurityContext.supplementalGroups`.

### Permissions

| Who | Where | Access |
|---|---|---|
| Broker | cluster | `get` on Nodes (already granted) |
| Broker | Topograph namespace | `get`, `create`, `delete` on `nodegpunicrailmappings`; `get`, `update`, `patch` on `nodegpunicrailmappings/status` |
| Broker | `gpuOperatorNamespace` only | `get` on the configured DaemonSet (`resourceNames`), `list` on Pods, `create` on `pods/exec`, through a Role and a RoleBinding naming the broker ServiceAccount |
| Observer | Topograph namespace | `get`, `list`, `watch`, `delete` on `nodegpunicrailmappings`, granted even while the plugin is disabled so it can clean up; `list` on Nodes when the plugin is enabled |
| SR-IOV DRA driver | Topograph namespace | `get`, `list`, `watch` on `nodegpunicrailmappings`, granted by the driver's own installation |

Pod and `pods/exec` access is never granted through a ClusterRole or outside `gpuOperatorNamespace`.

Kubernetes RBAC cannot express two of the rules the broker follows, so it enforces them in code: it execs only into a pod controlled by the configured DaemonSet and scheduled on its own Node, and it writes only the resource derived from its own live Node UID. For the same reason, a compromised broker can still `create` or modify another Node's mapping resource in the Topograph namespace.

## Consuming the mapping

A consumer reads `node-<its-node-uid>` and watches it with a `metadata.name` field selector. It must not list or watch every Node's mapping. Before applying `status`, it validates the API version, that `spec.nodeRef.uid` matches its live Node UID, and the Node owner reference. A malformed or unsupported update should leave the last accepted attributes in place. How attributes are withdrawn when the resource is deleted is part of the driver's integration.

## Verification

```bash
kubectl -n topograph get nodegpunicrailmappings
kubectl -n topograph get nodegpunicrailmappings node-<node-uid> -o jsonpath='{.status}' | jq
```

Compare `bestPath` and `bestGpus` with `nvidia-smi topo -all -m` on the Node, on systems with and without DD functions.
