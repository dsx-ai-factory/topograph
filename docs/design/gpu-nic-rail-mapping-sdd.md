# High-Throughput GPU/NIC Rail Mapping

Status: Draft

## Summary

GPU nodes reach the rest of the cluster through NICs attached to different
network rails. For data-intensive multi-node workloads, pairing each rail's NIC
with the GPU that has the best PCIe locality to it can improve throughput. No
single existing source describes that pairing.

Topograph discovers the NIC-to-rail binding and the GPU/NIC PCIe locality on
each node, joins them, and publishes the result as node-scoped metadata for the
SR-IOV DRA driver to consume. The driver decides how to express that metadata
as NIC device attributes; Kubernetes DRA then uses the advertised attributes
during allocation.

## Terminology

| Term | Meaning |
|---|---|
| **rail** | One of the parallel network planes a multi-NIC GPU node attaches to, identified by a `railID` derived from the selected host interface name through the configured regular expression and rail-ID template |
| **DD row** | The `nvidia-smi topo` matrix row belonging to an NVIDIA Data Direct function, as opposed to the row for the physical NIC of the same name |
| **node mapping record** | The `status` of the versioned `NodeNICRailMapping` custom resource Topograph publishes for one Kubernetes Node, defined under [Mapping Record](#mapping-record) |

## Problem Statement

Kubernetes and the SR-IOV DRA driver do not independently discover the combined
NIC-to-rail and NIC-to-GPU relationships. LLDP identifies eligible host
interfaces with network neighbors, while a configured interface-name regular
expression and template assign each interface its rail ID. The NVIDIA topology
matrix supplies GPU/NIC PCIe locality. Neither input alone provides the mapping
needed by the NIC device publisher.

Data Direct introduces an additional ambiguity. A NIC's physical topology row
may not represent the data path used for GPU traffic. When a matching Data
Direct function exists, its row can identify a different and closer GPU than
the physical NIC row. Treating the NIC row as authoritative in that case can
publish an incorrect preference.

Topograph must therefore join LLDP and NVIDIA topology data using stable host
interface, NIC PCI, and GPU identities; select the correct NIC or DD row; and
publish the resulting NIC-to-rail-to-GPU relationship for the SR-IOV DRA
driver.

## Goals and Non-Goals

This design defines:

- Rail-to-NIC discovery.
- GPU/NIC path selection with and without DD interfaces.
- A metadata contract that preserves the discovered NIC-to-GPU relation.

It does not define:

- DRA device-attribute names or encoding.
- GPU DRA driver behavior or GPU `ResourceSlice` contents.
- `ResourceClaim` templates or cross-driver matching policy.
- Workload placement or enforcement of application-level rail usage.

## Responsibilities

- **Topograph** discovers node-local rail and GPU/NIC topology data, computes
  the preferred mappings, and publishes node-scoped metadata. This feature is
  independent of Topograph's canonical `topology.Graph`. Topograph does not
  create or modify `ResourceSlice` objects.
- **SR-IOV DRA driver** consumes the node mapping record and creates attributes
  for the NIC devices it manages.
- **Kubernetes DRA** uses the device information supplied by DRA drivers to
  allocate devices to workloads.

### DPU applicability

DPU-backed networking is supported without schema changes when the SR-IOV DRA
driver allocates host-visible NICs or VFs. If the DPU itself is an allocatable
device, an additional integration must map the NIC or DD interface to a stable
DPU identity and translate the node mapping record into DPU attributes. That
translation is outside this design.

## Discovery and Mapping

Discovery draws on two host sources: `lldpctl` to identify neighbor-bearing
interfaces and `nvidia-smi` for GPU inventory and the GPU/NIC/DD topology
matrix. Rail IDs come from configured interface-name matching, not LLDP TLVs.

### 1. Bind rails to NICs

Parse `lldpctl` output, select interfaces with usable neighbor data, match each
interface name against the configured regular expression, and expand the match
through the rail-ID template. LLDP chassis and port identifiers do not
participate in rail identity. Resolve the interface to a stable NIC PCI address,
normalized to lowercase `dddd:bb:ss.f` form.

Rail discovery treats every selected interface independently. The result must
be one-to-one: two NICs cannot resolve to the same expanded rail ID, and one
NIC cannot resolve to multiple rails. Collection fails when an eligible
interface does not match the expression, template expansion is invalid, rail
IDs collide, or any other interface, rail, or PCI join is ambiguous.

### 2. Collect GPU/NIC topology

Collect the GPU inventory and the GPU/NIC/DD topology matrix, then:

- Resolve each temporary GPU matrix index to a stable UUID and PCI address.
- Use the matrix legend to join NIC and DD aliases to the host interfaces
  discovered from LLDP.
- Ignore the NVMe portion of the matrix.

For NIC-to-GPU paths, rank the topology categories from closest to farthest:

```text
PIX < PXB < PHB < NODE < SYS

where: X    is self
       NV#  applies to GPU-to-GPU links only and is never used for
            NIC-to-GPU selection
```

### 3. Select the topology row

For each NIC:

1. Find a DD entry whose legend interface name matches the NIC interface.
2. If found, treat the DD row as authoritative and use it to compare paths to
   GPUs, regardless of how its ranks compare with the physical NIC row.
3. Otherwise, use the NIC row.
4. Retain every GPU tied at the best path rank.

The DD row represents the data path used for GPU traffic, while the physical
NIC row does not when a matching DD function exists. The algorithm therefore
does not compare the best DD rank with the best physical-NIC rank or fall back
to the physical row when the DD rank appears worse. Path ranking selects the
best GPU or GPUs within the authoritative row. The next section works through a
case where the DD row also has better locality.

## Data Direct Example

The following matrix is the DD/GPU/NIC portion of the captured `nvidia-smi topo -all -m` output:

```text
      GPU0  GPU1  GPU2  GPU3  NIC0  NIC1  NIC2  NIC3  NIC4  NIC5  DD0   DD1   DD2   DD3
GPU0  X     NV18  NV18  NV18  SYS   SYS   NODE  NODE  SYS   SYS   NODE  PXB   SYS   SYS
GPU1  NV18  X     NV18  NV18  SYS   SYS   NODE  NODE  SYS   SYS   PXB   NODE  SYS   SYS
GPU2  NV18  NV18  X     NV18  NODE  NODE  SYS   SYS   NODE  NODE  SYS   SYS   NODE  PXB
GPU3  NV18  NV18  NV18  X     NODE  NODE  SYS   SYS   NODE  NODE  SYS   SYS   PXB   NODE
NIC0  SYS   SYS   NODE  NODE  X     PIX   SYS   SYS   NODE  NODE  SYS   SYS   NODE  NODE
NIC1  SYS   SYS   NODE  NODE  PIX   X     SYS   SYS   NODE  NODE  SYS   SYS   NODE  NODE
NIC2  NODE  NODE  SYS   SYS   SYS   SYS   X     NODE  SYS   SYS   NODE  NODE  SYS   SYS
NIC3  NODE  NODE  SYS   SYS   SYS   SYS   NODE  X     SYS   SYS   NODE  NODE  SYS   SYS
NIC4  SYS   SYS   NODE  NODE  NODE  NODE  SYS   SYS   X     NODE  SYS   SYS   NODE  NODE
NIC5  SYS   SYS   NODE  NODE  NODE  NODE  SYS   SYS   NODE  X     SYS   SYS   NODE  NODE
DD0   NODE  PXB   SYS   SYS   SYS   SYS   NODE  NODE  SYS   SYS   X     NODE  SYS   SYS
DD1   PXB   NODE  SYS   SYS   SYS   SYS   NODE  NODE  SYS   SYS   NODE  X     SYS   SYS
DD2   SYS   SYS   NODE  PXB   NODE  NODE  SYS   SYS   NODE  NODE  SYS   SYS   X     NODE
DD3   SYS   SYS   PXB   NODE  NODE  NODE  SYS   SYS   NODE  NODE  SYS   SYS   NODE  X

NIC Legend:

  NIC0: mlx5_8
  NIC1: mlx5_9
  NIC2: roce_vf_r0
  NIC3: roce_vf_r1
  NIC4: roce_vf_r2
  NIC5: roce_vf_r3
  DD0:  roce_vf_r0 data-direct function
  DD1:  roce_vf_r1 data-direct function
  DD2:  roce_vf_r2 data-direct function
  DD3:  roce_vf_r3 data-direct function
```

Take interface `roce_vf_r0`, which LLDP binds to `rail-0`. It appears twice in the NIC legend: once as physical `NIC2` and once as its Data Direct function `DD0`.

The two rows disagree. `NIC2` reaches its closest GPUs at `NODE`, tied between `GPU0` and `GPU1`. `DD0` reaches `GPU1` at `PXB`, which is closer and unambiguous. The DD row therefore wins, and `rail-0` maps to `GPU1`.

Reading the pipeline end to end: `roce_vf_r0 -> rail-0 -> 0000:3b:00.0 -> DD0 -> PXB -> GPU1`

Had `roce_vf_r0` no matching DD entry, selection would use the `NIC2` row and publish both `GPU0` and `GPU1` as tied at `NODE`.

## Topograph Component Design

The mapping is an optional, compiled-in node-metadata plugin. It is independent
of `provider.name` and does not add fields to `topology.Graph`.

| Topograph component | Responsibility |
|---|---|
| Node Data Broker | Discover local NIC rails and GPU locality, compute the mapping, and publish one `NodeNICRailMapping` for its Node |
| Node Observer | Remove Topograph-managed mapping resources when the plugin is disabled and reconcile stale resources not removed by garbage collection |
| Topology provider | Continue producing canonical fabric and accelerator topology independently of the mapping plugin |

```text
host LLDP  -> interface -> NIC PCI address -> rail --+
                                                     +-> join and select -> node mapping record
nvidia-smi -> NIC or DD row -> GPU PCI locality -----+
```

The broker loads the collector when the plugin is enabled. Collection and
publication run independently of the provider-specific annotation operation
that the broker already performs. After its initial reconciliation, the broker
continues running to refresh the record and serve its health endpoint.

The observer receives the plugin's name and enabled state. The broker owns
normal resource creation and status publication. The observer removes only
resources belonging to its Topograph release when the plugin is disabled and
reconciles stale resources that escaped Kubernetes garbage collection. It does
not perform node-local hardware discovery or compute mappings.

## Collection Implementation

### Rail source

The initial rail source runs `lldpctl -f json` against the host `lldpd` socket.
It considers interfaces with usable neighbor data, selects them using a
configured regular expression, and expands each match through a rail-ID
template. The interface name and template expansion are the sole source of the
rail ID; LLDP chassis ID, port ID, and other remote TLVs are not inputs to rail
identity. It resolves each selected interface through:

```text
/sys/class/net/<interface>/device
```

Bond, bridge, VF, representor, and aggregated interfaces are not implicitly
unwrapped. The configured interfaces must resolve to the host PCI functions
represented by the NVIDIA topology matrix.

### GPU topology source

The broker runs these commands in exactly one configured, same-node GPU
Operator pod:

```console
nvidia-smi --query-gpu=index,uuid,pci.bus_id --format=csv,noheader
nvidia-smi topo -all -m
```

The broker selects the pod using the configured DaemonSet's full label selector
and verifies that the selected pod is controlled by that DaemonSet and is
scheduled on the broker's Node. Zero or multiple eligible pods is an error. The
inventory command supplies stable GPU identities; the topology command supplies
GPU, NIC, and DD paths and the legend used for interface joins.

## Mapping Record

The mapping output is one namespaced `NodeNICRailMapping` custom resource per
Node. The CRD belongs to API group `topograph.run`, is initially served as
`v1alpha1`, uses plural resource name `nodenicrailmappings`, and enables the
`status` subresource. Its scope is namespaced so a deployment can restrict
brokers and consumers to the Topograph namespace.

The resource name is `node-<node-uid>`. Using the immutable Node UID rather
than the Node name prevents a replacement Node from colliding with stale state.
`spec.nodeRef` identifies the source Node and is immutable after creation. The
discovered mapping is observed state and therefore lives under `status`, not
`spec`.

The first NIC below is the worked example above; the second shows a NIC with no
usable GPU path:

```yaml
apiVersion: topograph.run/v1alpha1
kind: NodeNICRailMapping
metadata:
  name: node-<node-uid>
  namespace: <topograph-namespace>
  labels:
    app.kubernetes.io/instance: <helm-release-name>
    app.kubernetes.io/managed-by: topograph
    topograph.run/node-metadata-plugin: gpu-nic-rail-mapping
    topograph.run/node-uid: <node-uid>
  annotations:
    topograph.run/node-name: <node-name>
  ownerReferences:
    - apiVersion: v1
      kind: Node
      name: <node-name>
      uid: <node-uid>
spec:
  nodeRef:
    name: <node-name>
    uid: <node-uid>
status:
  sources:
    lldp: <collection-timestamp>
    nvidiaSmi: <collection-timestamp>
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

Contract rules:

- The CRD's `apiVersion` versions the contract. An incompatible schema change
  requires a new served version and, when versions coexist, conversion between
  stored and served forms.
- `spec.nodeRef.uid`, the UID-derived resource name, and the Node owner
  reference must agree. The CRD validates `spec.nodeRef` as immutable.
- Consumers validate `spec.nodeRef.uid` against their live Node UID before
  applying `status`.
- `sources` records when each input was collected; timestamps do not establish
  record freshness by themselves.
- `nics` is keyed by normalized host PCI address and includes the LLDP-derived
  interface and its single rail identity. Each `railID` occurs in exactly one
  NIC record.
- `topologyTarget` records the matrix row used for selection. Matrix labels
  such as `NIC2`, `DD0`, and `GPU1` are discovery-time aliases — explainability
  metadata, never stable device identities. Identity is always a NIC PCI
  address and a GPU UUID or PCI address.
- `mappingMode` is `data-direct` when a matched DD row was used, `nic` when the
  physical NIC row was used, and `none` when no usable GPU path exists. Never
  invent a pairing to avoid `none`.
- `topologyTarget` and `bestPath` are omitted when `mappingMode` is `none`.
- `bestGpus` contains every GPU tied at `bestPath` and is sorted by GPU UUID.
- A non-GPU Node may publish discovered NICs with `mappingMode: none` and empty
  `bestGpus` arrays.
- The CRD schema constrains required fields, PCI-address map keys, timestamp
  formats, `mappingMode` values, and path-rank values. Cross-field invariants
  that OpenAPI cannot express are enforced by the broker before publication
  and revalidated by consumers.

## Publication and Object Ownership

After a successful collection, each broker creates or updates only the
`NodeNICRailMapping` whose name is derived from its live Node UID. On creation,
the broker sets the immutable Node reference, Node owner reference, and
release-scoped management labels. `AlreadyExists` is idempotent only when all
identity and ownership fields match; the broker refuses to adopt an unrelated
or malformed object.

The broker replaces the complete `status` through the status subresource using
optimistic concurrency. A conflict causes it to fetch, revalidate, and retry.
Because each broker writes a different Kubernetes object, an update serializes
only one Node's mapping and cannot contend with or rewrite another Node's
record. Consumers observe either the previous complete status or the new
complete status, never a partial mapping.

A successful collection with no selected rail interfaces deletes the broker's
resource using UID and resource-version preconditions. Discovery, validation,
or publication errors preserve the last valid status. A broker that starts
before the CRD is available reports the publication failure and retries with
backoff.

The Node owner reference lets Kubernetes garbage collection remove the mapping
after Node deletion. The UID-derived name prevents Node-name reuse from
overwriting the old Node's mapping while deletion is in progress. After
informer cache sync, the observer performs a full reconciliation to remove
stale release-owned resources whose referenced Node no longer exists or whose
identity fields disagree.

When the plugin is disabled, the observer deletes only resources in its
namespace that carry the plugin and Helm release labels and pass the same
identity and owner-reference validation used by the broker. The CRD definition
is not deleted because other Topograph releases may still use it.

### SR-IOV DRA consumption

The SR-IOV DRA driver derives `node-<its-node-uid>`, gets that resource, and
starts a namespaced watch restricted by the `metadata.name` field selector. A
driver instance must not list or watch every Node's mapping. It validates the
resource's API version, immutable Node reference, and owner reference before
applying status. It translates the rail and preferred-GPU information into
attributes on the NIC devices it owns; this design intentionally does not
specify those attribute names or values. It retains its last accepted
attributes when a malformed or unsupported update is observed and reports the
rejection through its own status and metrics. How it withdraws attributes when
a valid mapping resource is deleted is part of the driver integration contract.

## Failure and Refresh Behavior

| Condition | Result |
|---|---|
| Non-GPU Node | Publish discovered NICs with no GPU mapping |
| No or multiple eligible GPU Operator pods on a GPU Node | Fail and preserve the previous record |
| Empty successful rail discovery | Remove this Node's record |
| Ambiguous NIC, DD, interface, or PCI join | Fail and preserve the previous record |
| LLDP, GPU inventory, topology, parsing, or publication error | Fail and preserve the previous record |
| NIC has no usable GPU path | Publish the NIC with `mappingMode: none` |
| Existing resource fails identity or ownership validation | Refuse adoption, report the conflict, and preserve the object for investigation |

The broker reconciles once during startup and then at a configured interval
while continuing to serve its health endpoint. Periodic collection detects
relevant NIC, GPU, LLDP, and topology changes without requiring the observer to
watch node-local hardware. A failed refresh preserves the previous value and
marks collection unhealthy; a later successful refresh replaces the record and
restores health. Records are deterministic apart from source timestamps, so an
unchanged hardware mapping leaves all mapping fields unchanged even though the
collection timestamps advance.

One resource per Node keeps each API request and etcd value proportional to one
Node's NIC and GPU count. It also prevents periodic refresh from creating a new
version of a cluster-wide aggregate object. Large-cluster validation must still
measure object count, list/watch memory, update rate, etcd quota consumption,
garbage-collection lag, and consumer restart behavior. Fleet-wide aggregation,
if required, belongs outside this per-cluster publication API.

The record describes physical GPU locality; MIG devices inherit their parent
GPU's mapping but are not published separately. The mapping describes
locality, not operational GPUDirect RDMA or network health.

## Helm Deployment

This section specifies the proposed Helm integration. Helm is one deployment
mechanism; its values, mounts, and RBAC are not feature-internal interfaces.

### Values

The plugin is configured once, independently of the selected provider:

```yaml
provider:
  name: <topology-provider>

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
          railID: 'rail-$1'
      gpuSource:
        gpuOperatorNamespace: gpu-operator
        daemonSet: nvidia-device-plugin-daemonset
```

Helm validates that the broker and observer are deployed, the refresh interval
is valid, exactly one supported rail source is selected, and all
source-specific settings are present. Enabling the plugin does not change
`provider.name` or the provider configuration passed to the broker.

### Workload wiring

The chart packages the `NodeNICRailMapping` CRD under `crds/`. The CRD is a
cluster-scoped API definition, while every mapping resource is namespaced.
Helm installs the definition before namespaced resources but does not delete it
on uninstall. CRD schema upgrades require an explicit compatibility and storage
version migration plan rather than an ordinary templated chart update.

The chart passes the plugin configuration, release identity, mapping namespace,
and Node name to every broker pod. It passes the enabled state, release
identity, and namespace to the observer so disablement cleanup remains possible.
The resource name and API identity are fixed by the contract and are not Helm
values.

The Node Data Broker remains a DaemonSet so discovery runs on every selected
Node. Host LLDP collection mounts the host `lldpd` socket and
`/sys/class/net` read-only. GPU collection uses `pods/exec` in the configured
GPU Operator workload; it does not require the host `nvidia-smi` binary in the
broker container. The chart checksum-annotates broker and observer pod
templates so configuration changes restart the affected workloads.

The operator must configure broker node selectors and tolerations so the
DaemonSet runs on every Node whose NIC metadata should be published. The
observer remains a Deployment and may run on any schedulable Node.

### Permissions

When the plugin is enabled, the broker receives cluster-scoped `get` access to
its Node to obtain and validate the Node UID.

In the configured `gpuOperatorNamespace`, a Role and RoleBinding grant the
broker ServiceAccount from the Topograph namespace:

- `get` on the configured GPU Operator DaemonSet, restricted by
  `resourceNames` to the configured DaemonSet name;
- `list` on Pods, required to evaluate the DaemonSet selector; and
- `create` on `pods/exec`.

In the Topograph namespace, a separate Role grants:

- namespaced `get`, `create`, and `delete` on `nodenicrailmappings`, plus `get`,
  `update`, and `patch` on `nodenicrailmappings/status`.

The broker verifies in code that the exec target belongs to the configured
DaemonSet and is scheduled on the same Node. RBAC cannot express those
relationships. Pod and `pods/exec` permissions are never granted through a
ClusterRoleBinding or in namespaces other than `gpuOperatorNamespace`. When
that namespace differs from the Topograph namespace, the RoleBinding is created
in `gpuOperatorNamespace` and names the broker ServiceAccount in the Topograph
namespace as its subject.

The observer receives:

- `list` and `watch` on Nodes;
- namespace-scoped `get`, `list`, `watch`, and `delete` on
  `nodenicrailmappings`.

Kubernetes authorization cannot restrict `create` to the broker's dynamic,
UID-derived resource name. The broker therefore enforces that it creates or
updates only the resource derived from its own live Node UID. The observer
deletes only resources carrying both the configured release identity and the
mapping-plugin label.

The SR-IOV DRA deployment receives namespaced `get`, `list`, and `watch` on
`nodenicrailmappings`. That permission belongs to the DRA driver's
installation rather than the Topograph chart unless the driver is deployed as
a chart dependency.

Chart tests cover CRD packaging, values validation, conditional and
cross-namespace RBAC, absence of cluster-wide Pod and `pods/exec` grants, host
mounts, generated broker and observer configuration, plugin disablement,
release-scoped cleanup, and broker/observer startup ordering.

## Validation

Unit and integration tests should cover:

- LLDP parsing, interface-regex matching, rail-ID template expansion, proof
  that LLDP TLVs do not affect rail identity, PCI normalization, one-to-one
  NIC/rail validation, and ambiguous joins;
- GPU inventory and topology parsing, authoritative DD row selection regardless
  of comparative rank, non-DD row selection, path ranking, ties, and stable
  identity resolution;
- CRD schema validation, immutable Node references, deterministic status,
  periodic refresh, and concurrent per-object publication;
- resource creation, status-subresource updates, ownership validation, and
  unrelated-object rejection;
- deleted Nodes, garbage collection, observer restart, plugin disablement,
  Node-name reuse, and preconditioned-delete races;
- scale tests for resource count, list/watch behavior, etcd growth, refresh
  throughput, and consumer restart catch-up; and
- composition with representative Kubernetes providers without changing their
  graph output.

Hardware validation should compare the published mapping with
`nvidia-smi topo -all -m` on systems with and without DD interfaces. It should
also cover equal-distance ties, a multi-GPU/multi-rail Node, MIG mode, a
non-GPU Node, and non-rail or bonded interfaces.

## Open Questions

- Which component owns the final NIC device-attribute schema?
- Where does the plugin live in the repository layout — a new node-metadata
  package tree, or inside the existing Node Data Broker package?
- How should the SR-IOV DRA driver withdraw previously advertised attributes
  when a mapping resource is deleted?
- Does the initial implementation need to support allocation of a DPU as a
  device, rather than NICs or VFs backed by a DPU?
