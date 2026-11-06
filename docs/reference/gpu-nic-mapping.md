# GPU/NIC Locality and Switch Discovery

The `gpu-nic-mapping` node-metadata plugin pairs each PCI-backed NIC with the GPUs that have the best PCIe locality to it and reports its LLDP-advertised switch name when available, and publishes the result as one `NodeGPUNICMapping` custom resource per Node. A consumer such as the SR-IOV DRA driver turns that into NIC device attributes, so Kubernetes DRA can allocate a NIC together with a nearby GPU.

Rail-ID discovery and grouping are outside the plugin's scope.

The plugin is independent of `provider.name` and does not change `topology.Graph` or any label Topograph writes. Topograph never creates or modifies `ResourceSlice` objects, and the DRA attribute names and encoding belong to the consuming driver.

## How it works

On every Node, the Node Data Broker:

1. Enumerates PCI-backed NIC interfaces under `/sys/class/net`, resolving each through `/sys/class/net/<interface>/device`. Only NICs with LLDP neighbors are published; virtual interfaces and devices on other buses are skipped. Interface names need no naming convention or regular expression.
2. Runs `lldpcli -f json0 show neighbors details` against the host `lldpd` socket and reads the remote chassis system name for each interface as optional `switchName` metadata. Interfaces without neighbors are excluded. A neighbor without a chassis name leaves `switchName` absent and does not prevent GPU discovery. Multiple NICs can share a switch name. Repeated identical names are deduplicated; conflicting nonempty names for one interface fail the refresh. Chassis IDs and port IDs are never substituted for a missing name, and switch names do not establish rail identity.
3. Runs `nvidia-smi --query-gpu=index,uuid,pci.bus_id` and `nvidia-smi topo -all -m` in the one GPU Operator pod of the configured DaemonSet that runs on the same Node.
4. For each NIC, picks the matrix row and keeps every GPU tied at the closest path category (`PIX` < `PXB` < `PHB` < `NODE` < `SYS`; `NV#` links are GPU-to-GPU and never rank a NIC).
5. Publishes the result with the status subresource.

The legend of `nvidia-smi topo` names a NIC by RDMA device name (`mlx5_8`) or by interface name. For each legend type (Data Direct first, then NIC), the broker matches in this order:

1. The interface name or RDMA devices under `/host/sys/class/net/<interface>/device/infiniband/`.
2. An exact PCI address, resolving the legend device through `/host/sys/class/infiniband/<name>/device` or `/host/sys/class/net/<name>/device`.
3. An explicit PF/VF relationship from `/host/sys/bus/pci/devices/<VF>/physfn`. This supports a collected PF with a VF in the matrix, or a collected VF with its PF in the matrix. Sibling VFs do not match each other merely because they share a PF.

More than one candidate at the first matching level is an error, including multiple VFs of a collected PF; the broker does not pick an arbitrary VF even if their current best GPUs agree. Missing devices or an absent `physfn` provide no fallback match; malformed identities and other lookup errors fail the refresh and retain the previous record. The output key always remains the collected interface's PCI address. For example, if `eth_r0_p0` is PF `0001:03:00.0` and the matrix lists `mlx5_0` at VF `0001:03:00.4` whose `physfn` points to that PF, the broker uses the VF's matrix row but publishes under `0001:03:00.0`.

This fallback needs no configuration and works with ordinary NICs as well as SR-IOV devices. The configured GPU source pod must still expose NIC/DD entries in its NVIDIA topology output; host sysfs alone cannot supply missing matrix paths. A related function's row describes PCIe locality, not GPUDirect readiness or network connectivity.

### Data Direct

When the legend has a Data Direct (DD) function for the NIC's interface, its row is authoritative, even when its path category is farther than the physical NIC row. The DD row carries the GPU traffic. Without a DD function, the NIC row is used.

For example, `roce_vf_r0` is NIC2 and DD0 in the legend. NIC2 reaches GPU0 and GPU1 at `NODE`, while DD0 reaches GPU1 at `PXB`. The DD row wins, so this NIC maps to GPU1. For illustration, assume LLDP advertises `leaf-01`:

```
roce_vf_r0 -> 0000:3b:00.0 -> DD0 -> PXB -> GPU1; switchName: leaf-01
```

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
    nvidiaSmi: "2026-01-02T03:04:06Z"
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
| LLDP, `nvidia-smi`, parsing, or publication error | The previous record is kept; the failure is logged and retried with backoff. The broker continues serving `/healthz`, so plugin failures do not affect probes. |
| No or more than one eligible GPU Operator pod on the broker's Node | As above |
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
      gpuSource:
        gpuOperatorNamespace: gpu-operator
        daemonSet: nvidia-device-plugin-daemonset
        container: ""   # empty selects the pod's default container
```

| Value | Meaning |
|---|---|
| `refreshInterval` | How often each broker collects and, when the mapping changed, republishes. Default `10m`. |
| `switchSource.name` | `host-lldp`, the only supported source. |
| `switchSource.hostLLDP.socket` | Host `lldpd` control socket. Default `/var/run/lldpd.socket`. |
| `gpuSource.gpuOperatorNamespace`, `daemonSet`, `container` | The GPU Operator workload whose same-Node pod runs `nvidia-smi`. |

Enabling the plugin does not change `provider.name` or the provider configuration. Providers without a broker annotation handler skip Node annotation updates; the broker still runs the mapping plugin. Run the broker on every Node whose NICs should be published, using `nodeDataBroker.nodeSelector` and `nodeDataBroker.tolerations`.

### What the chart deploys

- The `NodeGPUNICMapping` CRD from `crds/`. Helm installs it before namespaced resources but never upgrades or deletes it. A development installation using the previous rail-based API must install `charts/topograph/crds/nodegpunicmappings.yaml`, update its consumers and RBAC to `NodeGPUNICMapping` / `nodegpunicmappings`, and use the `gpu-nic-mapping` management-label value. Configure `nodeMetadata.plugins.gpuNicMapping` with `switchSource` and remove the regex and template settings. Stop the previous brokers before rollout; the new brokers create separate resources and do not migrate or clean up the previous API's objects. Remove only the previous release-owned mapping objects after consumers have switched; remove the previous CRD only after no releases or consumers use it. Consumers read optional `switchName` instead of a rail ID. Future schema upgrades require an explicit compatibility and storage-version migration.
- A read-only hostPath mount of the host `/sys` at `/host/sys` and a read-only hostPath mount of the `lldpd` socket's parent directory at `/host/lldpd`. The broker uses the fixed sysfs path `/host/sys`; it is not a plugin configuration option. The chart rewrites the broker's socket path to that mount. The directory uses `DirectoryOrCreate`, so a missing socket or parent directory does not prevent startup; collection failures are logged and retried until `lldpd` is available. Mounting the directory also lets the broker reconnect when `lldpd` replaces its socket on restart. All of `/sys` is mounted because the entries under `/sys/class/net` are relative symlinks into `/sys/devices`.
- The broker image needs `lldpcli`; the Topograph image installs the `lldpd` package for it.
- The broker runs as a non-root user. If the `lldpd` socket is not world-accessible, add the socket's group to `nodeDataBroker.podSecurityContext.supplementalGroups`.

### Permissions

| Who | Where | Access |
|---|---|---|
| Broker | cluster | `get` on Nodes (already granted) |
| Broker | Topograph namespace | `get`, `create`, `delete` on `nodegpunicmappings`; `get`, `update`, `patch` on `nodegpunicmappings/status` |
| Broker | `gpuOperatorNamespace` only | `get` on the configured DaemonSet (`resourceNames`), `list` on Pods, `create` on `pods/exec`, through a Role and a RoleBinding naming the broker ServiceAccount |
| Observer | Topograph namespace | `get`, `list`, `watch`, `delete` on `nodegpunicmappings`, granted even while the plugin is disabled so it can clean up; `list` on Nodes when the plugin is enabled |
| SR-IOV DRA driver | Topograph namespace | `get`, `list`, `watch` on `nodegpunicmappings`, granted by the driver's own installation |

Pod and `pods/exec` access is never granted through a ClusterRole or outside `gpuOperatorNamespace`.

Kubernetes RBAC cannot express two of the rules the broker follows, so it enforces them in code: it execs only into a pod controlled by the configured DaemonSet and scheduled on its own Node, and it writes only the resource derived from its own live Node UID. For the same reason, a compromised broker can still `create` or modify another Node's mapping resource in the Topograph namespace.

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

Compare `bestPath` and `bestGpus` with `nvidia-smi topo -all -m` on the Node, on systems with and without DD functions.

The broker logs each successful NIC selection at the default verbosity, including the interface, PCI address, optional switch name, topology target, mapping mode, best path, and selected GPU identities. A NIC without a mapping is logged with the reason: no topology matrix, no matching topology device, or no usable GPU paths. These are collection decisions; a later failure can still prevent the refresh from being published.

Set the Helm `verbosity` value to `4` to also log whether each topology match used a name, an exact PCI address, or a PF/VF relationship. PF/VF logs include both PCI addresses and the direction from the collected function to the matrix function. At verbosity `2` and above, the topology summary includes NIC and Data Direct legend counts, which helps identify a GPU source container that exposes no NIC entries.

### NVIDIA CLI compatibility

The broker first runs `nvidia-smi topo -all -m` to preserve Data Direct information on versions that support it. If NVIDIA explicitly reports `Option "-all" is not recognized.`, it logs the compatibility retry and runs `nvidia-smi topo -m`. Other command failures do not trigger a fallback. No configuration change is required. Data Direct entries present in either output remain authoritative, but the broker cannot use entries that the older CLI does not expose.

Some NVIDIA CLI versions emit ANSI formatting, such as an underlined matrix header, even without a terminal. The broker strips these control sequences before parsing the matrix and legend.
