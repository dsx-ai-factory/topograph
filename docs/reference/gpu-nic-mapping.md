# GPU/NIC Locality and Switch Discovery

The `gpu-nic-mapping` node-metadata plugin pairs each PCI-backed NIC with the GPUs that have the best PCIe locality to it and reports its LLDP-advertised switch name when available, and publishes the result as one `NodeGPUNICMapping` custom resource per Node. A consumer such as the SR-IOV DRA driver turns that into NIC device attributes, so Kubernetes DRA can allocate a NIC together with a nearby GPU.

Rail-ID discovery and grouping are outside the plugin's scope.

The plugin is independent of `provider.name` and does not change `topology.Graph` or any label Topograph writes. Topograph never creates or modifies `ResourceSlice` objects, and the DRA attribute names and encoding belong to the consuming driver.

## How it works

On every Node, the Node Data Broker:

1. Enumerates PCI-backed NIC interfaces under `/sys/class/net`, resolving each through `/sys/class/net/<interface>/device`. Only NICs with LLDP neighbors are published; virtual interfaces and devices on other buses are skipped. Interface names need no naming convention or regular expression.
2. Runs `lldpcli -f json0 show neighbors details` against the host `lldpd` socket and reads the remote chassis system name for each interface as optional `switchName` metadata. Interfaces without neighbors are excluded. A neighbor without a chassis name leaves `switchName` absent and does not prevent GPU discovery. Multiple NICs can share a switch name. Repeated identical names are deduplicated; conflicting nonempty names for one interface fail the refresh. Chassis IDs and port IDs are never substituted for a missing name, and switch names do not establish rail identity.
3. Reads the GPUs and their PCI paths to every RDMA NIC and Data Direct function from host sysfs, as described in [GPU and PCI topology discovery](#gpu-and-pci-topology-discovery). It does not run `nvidia-smi` and needs no access to GPU Operator pods.
4. For each NIC, picks the topology row and keeps every GPU tied at the closest path category (`PIX` < `PXB` < `PHB` < `NODE` < `SYS`).
5. Publishes the result with the status subresource.

The topology names each NIC row by its RDMA device name (`mlx5_8`) and each Data Direct row by the RDMA device of the NIC it serves. For each row type (Data Direct first, then NIC), the broker matches in this order:

1. The interface name or RDMA devices under `/host/sys/class/net/<interface>/device/infiniband/`.
2. An exact PCI address, resolving the row's device through `/host/sys/class/infiniband/<name>/device` or `/host/sys/class/net/<name>/device`.
3. An explicit PF/VF relationship from `/host/sys/bus/pci/devices/<VF>/physfn`. This supports a collected PF with a VF in the topology, or a collected VF with its PF in the topology. Sibling VFs do not match each other merely because they share a PF.

More than one candidate at the first matching level is an error, including multiple VFs of a collected PF; the broker does not pick an arbitrary VF even if their current best GPUs agree. Missing devices or an absent `physfn` provide no fallback match; malformed identities and other lookup errors fail the refresh and retain the previous record. The output key always remains the collected interface's PCI address. For example, if `eth_r0_p0` is PF `0001:03:00.0` and the topology lists `mlx5_0` at VF `0001:03:00.4` whose `physfn` points to that PF, the broker uses the VF's row but publishes under `0001:03:00.0`.

This fallback needs no configuration and works with ordinary NICs as well as SR-IOV devices. A related function's row describes PCIe locality, not GPUDirect readiness or network connectivity.

### Data Direct

When a NIC has a ConnectX-8 Data Direct (DD) function, the DD row is authoritative, even when its path category is farther than the physical NIC row. The DD row carries the GPU traffic. Without a DD function, the NIC row is used.

For example, `roce_vf_r0` is NIC2 and has the Data Direct function DD0. NIC2 reaches GPU0 and GPU1 at `NODE`, while DD0 reaches GPU1 at `PXB`. The DD row wins, so this NIC maps to GPU1. For illustration, assume LLDP advertises `leaf-01`:

```
roce_vf_r0 -> 0000:3b:00.0 -> DD0 -> PXB -> GPU1; switchName: leaf-01
```

### GPU and PCI topology discovery

The broker builds the same path categories that `nvidia-smi topo -m` prints, for the NIC and Data Direct rows against the GPU columns, from the host PCI hierarchy:

- **GPUs** are PCI display controllers with NVIDIA's vendor ID that are bound to the `nvidia` driver; a GPU bound to `vfio-pci` for passthrough is skipped. The UUID comes from the driver's `/proc/driver/nvidia/gpus/<pci-address>/information`. These procfs entries belong to the host kernel and are visible in the broker's own `/proc`; no host procfs mount is needed.
- **NICs** are PCI functions with an RDMA device under `/sys/bus/pci/devices/<pci-address>/infiniband/`, as in the `nvidia-smi` NIC legend.
- **Path categories** come from each function's position in `/sys/devices`:

  | Category | The path between the NIC and the GPU |
  |---|---|
  | `PIX` | crosses at most one PCIe switch, or a root port, below their common point |
  | `PXB` | crosses more than one PCIe switch, without reaching the host bridge |
  | `PHB` | meets only at a PCIe host bridge (`pciDDDD:BB`) |
  | `NODE` | crosses host bridges whose functions the kernel places on the same NUMA node |
  | `SYS` | crosses NUMA nodes, or a function has no NUMA node (`numa_node` is `-1`) |

  A PCIe switch, its upstream port and its downstream ports, counts as one bridge.
- **Data Direct functions** are PCI functions `15b3:2100`, the ConnectX-8 DMA function. Each is paired with the network functions that carry the same `V3` UUID in their PCI Vital Product Data (VPD), and with the virtual functions of those physical functions, as rdma-core's `rdma_topo` does. A Data Direct function that cannot be paired fails the refresh rather than falling back to the NIC row.

The kernel makes PCI VPD readable by root only. The chart therefore adds a `vpd-reader` init container that runs the broker image as UID 0, with every capability dropped and host sysfs mounted read-only. It reads the VPD of Data Direct functions and of Mellanox physical network functions once, records any per-function read error, and writes the result to an emptyDir at `/var/run/topograph/vpd.json`. The long-running broker stays non-root and reads VPD only from that file, and only on Nodes that have Data Direct functions. VPD is set by adapter firmware, so a read at pod start stays valid; restart the broker pod after replacing an adapter.

`topologyTarget` aliases (`NIC0`, `DD0`) are numbered in PCI address order, NICs first and then Data Direct functions in the order of the NIC they serve. They can differ from the numbering `nvidia-smi topo -m` prints for the same device.

## The `NodeGPUNICMapping` resource

| | |
|---|---|
| Group, version | `topograph.run/v1alpha1` |
| Resource | `nodegpunicmappings` (namespaced, status subresource) |
| Name | `node-<node-uid>`; the Node UID, not its name, so a replacement Node cannot collide with stale state |
| Owner | the Node (`ownerReferences`), so Kubernetes garbage collection removes it with the Node |

`spec.nodeRef` names the source Node and is immutable. The discovered mapping is observed state and lives in `status`:

```yaml
apiVersion: topograph.run/v1alpha1
kind: NodeGPUNICMapping
metadata:
  name: node-<node-uid>
  namespace: <topograph-namespace>
  labels:
    app.kubernetes.io/instance: <helm-release-name>
    app.kubernetes.io/managed-by: topograph
    topograph.run/node-metadata-plugin: gpu-nic-mapping
    topograph.run/node-uid: <node-uid>
spec:
  nodeRef: {name: <node-name>, uid: <node-uid>}
status:
  sources:
    lldp: "2026-01-02T03:04:05Z"
    pciTopology: "2026-01-02T03:04:06Z"
  nics:
    "0000:3b:00.0":
      interface: roce_vf_r0
      switchName: leaf-01
      topologyTarget: DD0
      mappingMode: data-direct
      bestPath: PXB
      bestGpus:
        - uuid: GPU-...
          pciAddress: "0000:17:00.0"
    "0000:5e:00.0":
      interface: roce_vf_r1
      # switchName omitted when LLDP has no chassis name
      mappingMode: none
      bestGpus: []
```

- `nics` is keyed by the normalized host PCI address (`dddd:bb:ss.f`, lowercase). `switchName` is optional, may be shared by NICs, and is not a globally unique switch identity. No rail ID is derived or published.
- Identity is always a NIC PCI address and a GPU UUID or PCI address. `topologyTarget` (`NIC2`, `DD0`) is explainability metadata, never an identity.
- `mappingMode` is `data-direct` when a DD row was used, `nic` when a NIC row was used, and `none` when there is no usable GPU path. Topograph never invents a pairing to avoid `none`. `topologyTarget` and `bestPath` are omitted for `none`.
- `bestGpus` holds every GPU tied at `bestPath`, sorted by UUID. MIG devices inherit the parent GPU's mapping and are not published separately.
- Use `nodeDataBroker.nodeSelector` or affinity to schedule brokers on the intended GPU nodes. The plugin does not inspect GPU labels or resource capacity; when neighbor-bearing PCI-backed NICs are present, GPU inventory and topology discovery must succeed.
- The mapping describes PCIe locality, not GPUDirect RDMA readiness or network health.

The CRD schema constrains required fields, PCI-address keys, timestamp formats, and the `mappingMode` and `bestPath` values. The broker enforces the cross-field invariants before publishing.

### Refresh and failure behavior

The broker reconciles at startup and then every `refreshInterval`. Records are deterministic apart from the `sources` timestamps. To avoid one etcd write per Node per interval on an idle cluster, a refresh that finds an unchanged mapping writes nothing until the stored timestamps are an hour old.

| Condition | Result |
|---|---|
| LLDP, GPU or PCI topology, parsing, or publication error | The previous record is kept; the failure is logged and retried with backoff. The broker continues serving `/healthz`, so plugin failures do not affect probes. |
| No GPU bound to the `nvidia` driver, or a GPU without a UUID in `/proc/driver/nvidia` | As above |
| A Data Direct function without readable VPD, or without a network function that shares its VPD `V3` UUID | As above |
| Conflicting switch names or ambiguous interface, NIC, DD or PCI join | As above |
| A successful collection finds no PCI-backed NICs with LLDP neighbors | The Node's record is deleted |
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
    gpuNicMapping:
      enabled: true
      refreshInterval: 10m
      switchSource:
        name: host-lldp
        hostLLDP:
          socket: /var/run/lldpd.socket
```

| Value | Meaning |
|---|---|
| `refreshInterval` | How often each broker collects and, when the mapping changed, republishes. Default `10m`. |
| `switchSource.name` | `host-lldp`, the only supported source. |
| `switchSource.hostLLDP.socket` | Host `lldpd` control socket. Default `/var/run/lldpd.socket`. |

GPU discovery has no settings: it reads host sysfs and the NVIDIA driver's procfs entries.

Enabling the plugin does not change `provider.name` or the provider configuration. Providers without a broker annotation handler skip Node annotation updates; the broker still runs the mapping plugin. Run the broker on every Node whose NICs should be published, using `nodeDataBroker.nodeSelector` and `nodeDataBroker.tolerations`.

### What the chart deploys

- The `NodeGPUNICMapping` CRD from `crds/`. Helm installs it before namespaced resources but never upgrades or deletes it. A development installation using the previous rail-based API must install `charts/topograph/crds/nodegpunicmappings.yaml`, update its consumers and RBAC to `NodeGPUNICMapping` / `nodegpunicmappings`, and use the `gpu-nic-mapping` management-label value. Configure `nodeMetadata.plugins.gpuNicMapping` with `switchSource` and remove the regex and template settings. Stop the previous brokers before rollout; the new brokers create separate resources and do not migrate or clean up the previous API's objects. Remove only the previous release-owned mapping objects after consumers have switched; remove the previous CRD only after no releases or consumers use it. Consumers read optional `switchName` instead of a rail ID. Future schema upgrades require an explicit compatibility and storage-version migration.
- A read-only hostPath mount of the host `/sys` at `/host/sys` and a read-only hostPath mount of the `lldpd` socket's parent directory at `/host/lldpd`. The broker uses the fixed sysfs path `/host/sys`; it is not a plugin configuration option. The chart rewrites the broker's socket path to that mount. The directory uses `DirectoryOrCreate`, so a missing socket or parent directory does not prevent startup; collection failures are logged and retried until `lldpd` is available. Mounting the directory also lets the broker reconnect when `lldpd` replaces its socket on restart. All of `/sys` is mounted because the entries under `/sys/class/net` are relative symlinks into `/sys/devices`.
- The broker image needs `lldpcli`; the Topograph image installs the `lldpd` package for it.
- The broker runs as a non-root user. If the `lldpd` socket is not world-accessible, add the socket's group to `nodeDataBroker.podSecurityContext.supplementalGroups`.
- The `vpd-reader` init container, which runs as UID 0 to read PCI VPD (see [GPU and PCI topology discovery](#gpu-and-pci-topology-discovery)), and the `gpu-nic-vpd` emptyDir it shares with the broker. Its `securityContext` overrides the pod's `runAsNonRoot`, so the release namespace must admit a container that runs as root: Pod Security `baseline` does, `restricted` does not. User-supplied `nodeDataBroker.initContainers` run after it.

### Permissions

| Who | Where | Access |
|---|---|---|
| Broker | cluster | `get` on Nodes (already granted) |
| Broker | Topograph namespace | `get`, `create`, `delete` on `nodegpunicmappings`; `get`, `update`, `patch` on `nodegpunicmappings/status` |
| Observer | Topograph namespace | `get`, `list`, `watch`, `delete` on `nodegpunicmappings`, granted even while the plugin is disabled so it can clean up; `list` on Nodes when the plugin is enabled |
| SR-IOV DRA driver | Topograph namespace | `get`, `list`, `watch` on `nodegpunicmappings`, granted by the driver's own installation |

The plugin grants no Pod or `pods/exec` access: GPU discovery reads only the host's sysfs and procfs.

Kubernetes RBAC cannot express that the broker writes only the resource derived from its own live Node UID, so the broker enforces it in code. For the same reason, a compromised broker can still `create` or modify another Node's mapping resource in the Topograph namespace.

## Consuming the mapping

A consumer reads `node-<its-node-uid>` and watches it with a `metadata.name` field selector. It must not list or watch every Node's mapping. Before applying `status`, it validates the API version, that `spec.nodeRef.uid` matches its live Node UID, and the Node owner reference. A malformed or unsupported update should leave the last accepted attributes in place. How attributes are withdrawn when the resource is deleted is part of the driver's integration.

## Verification

```bash
kubectl -n topograph get nodegpunicmappings
kubectl -n topograph get nodegpunicmappings node-<node-uid> -o jsonpath='{.status}' | jq
```

On the host, compare `switchName` with the LLDP chassis system name:

```bash
for ifc in /sys/class/net/*; do
  name=$(basename "$ifc")
  peer=$(lldpcli -f json0 show neighbors ports "$name" details 2>/dev/null | jq -r '.lldp[]?.interface[]?.chassis[]?.name[]?.value // empty')
  echo "$name ${peer:--}"
done
```

Here `-` is a diagnostic placeholder, never a published switch name. Production collection distinguishes LLDP command or parsing errors from successful queries without names; errors preserve the previous record. A successful refresh with a neighbor but no chassis name removes any previously stored `switchName` while retaining the NIC's GPU mapping. If a neighbor disappears, that NIC is omitted from the next complete status; if no eligible NICs remain, the Node's record is deleted. LLDP neighbor presence is a scope filter, not proof of network health or whether an excluded NIC is in use.

Compare `bestPath` and `bestGpus` with `nvidia-smi topo -all -m` on the Node, on systems with and without DD functions. Compare rows by device name, not by alias: `topologyTarget` numbering can differ from `nvidia-smi`. On a node with Data Direct functions, rdma-core's `rdma_topo topology` (rdma-core v61 or later, as root) lists the same DMA function, NIC and GPU pairing.

If the broker reports a VPD error, check the `vpd-reader` init container's log: it names every function whose VPD it could not read.

The broker logs each successful NIC selection at the default verbosity, including the interface, PCI address, optional switch name, topology target, mapping mode, best path, and selected GPU identities. A NIC without a mapping is logged with the reason: no topology matrix, no matching topology device, or no usable GPU paths. These are collection decisions; a later failure can still prevent the refresh from being published.

Set the Helm `verbosity` value to `4` to also log whether each topology match used a name, an exact PCI address, or a PF/VF relationship. PF/VF logs include both PCI addresses and the direction from the collected function to the matrix function. At verbosity `2` and above, the topology summary includes the GPU count and the NIC and Data Direct row counts, which helps identify a Node whose RDMA devices are not visible in sysfs.
