# Topograph Node Labels and Annotations

Topograph enriches Kubernetes nodes with labels and annotations that describe their physical network topology. This reference covers every label and annotation key written by Topograph, how values are derived, and how to configure them.

## Labels

Topology labels are set by the [Kubernetes engine](../engines/k8s.md)
(`engine: k8s`). They are intended for use by workload schedulers (e.g. KAI
Scheduler, gang-scheduling plugins, topology-aware bin-packers) and
observability tools to reason about network locality. The
[Slinky engine](../engines/slinky.md) does not write these labels; it writes
Slurm topology configuration to a ConfigMap and can consume the existing
label selected by `acceleratorDomainSourceLabel` for `topology/block`.

### Default label keys

Topograph publishes a variable-depth fabric label family and a two-level
accelerator label hierarchy. Fabric tier `0` is closest to the compute node, and
tier numbers increase outwards. Only entries present in the discovered topology
are written.

| Label key | Topology type | Semantics |
|---|---|---|
| `accelerator.topograph.run/domain` | Accelerator | Accelerator-interconnect locality. Omitted for a node when a configured `acceleratorDomainSourceLabel` supplies the authoritative value. |
| `accelerator.topograph.run/sub-domain` | Accelerator | Optional locality nested within the accelerator domain. Written only when `accelerator.topograph.run/domain` is also present. |
| `fabric.topograph.run/tier-N` | Fabric | Switch-fabric locality at tier `N`. Tier 0 is the switch closest to the node; each higher tier is the next switch tier outward. There is no fixed maximum depth. |

Labels are **additive**: a node can carry every discovered fabric tier and its
accelerator domain and sub-domain simultaneously.

Not all providers supported with the Kubernetes engine produce both topology
types:

| Provider | Accelerator domains | Fabric tiers |
|---|---|---|
| `aws` | Yes (CapacityBlockId) | Yes |
| `crusoe` | Optional (`nvidia.com/gpu.clique` on NVLink hosts) | Yes (rail-optimized pod, InfiniBand partition) |
| `gcp` | Yes (SubblockId) | Yes |
| `lambdai` | Yes (`NVLink.DomainID.CliqueID`) | Yes |
| `oci` | Yes (GpuMemoryFabricId) | Yes |
| `nebius` | No | Yes |
| `nscale` | Yes | Yes |
| `netq` | Yes (NMX `DomainUUID`) | Yes (Spectrum-X switch hierarchy) |
| `infiniband-bm` | Optional (`ClusterUUID.CliqueId` when configured) | Yes (IB switch hierarchy) |
| `infiniband-k8s` | Optional (`ClusterUUID.CliqueId` when configured) | Yes (IB switch hierarchy) |

The OCI API provider can publish both accelerator hierarchy levels when
`additionalData.locationDetails.rack` is available. Other providers currently
publish only the accelerator domain.

The DRA provider is intentionally omitted: its supported use is with the Slinky
engine, where it converts existing `nvidia.com/gpu.clique` labels into Slurm
`topology/block` domains rather than writing Kubernetes topology labels.

**Existing accelerator-domain source labels**: The k8s, NFD, and Slinky engines
can explicitly select any valid Kubernetes Node label key with
`engine.params.acceleratorDomainSourceLabel`. A non-empty value is authoritative
for that node and suppresses the provider accelerator sub-domain. When the
parameter is omitted, engines use provider-supplied accelerator domains and do
not give any Kubernetes label special treatment. This engine setting is
independent of `provider.params.accelerator.kubernetesLabel.key`, which controls
provider discovery.

Some GPU Operator deployments expose `nvidia.com/gpu.clique` on nodes with
Multi-Node NVLink (MNNVL) GPUs; operators may select it explicitly as either an
engine source label or a DRA/`infiniband-k8s` provider discovery label. The `netq`
provider instead uses a `DomainUUID` from the NMX management API—a different
identifier that refers to the same physical domain but cannot be compared as a
string.

[NVIDIA Fabric Manager](https://docs.nvidia.com/datacenter/tesla/fabric-manager-user-guide/) runs at node init on MNNVL-capable hardware, discovers the NVLink fabric across GPUs, and registers each GPU with [NVML](https://docs.nvidia.com/deploy/nvml-api/) (NVIDIA Management Library — a C API that exposes per-GPU state). The GPU Operator's IMEX labeler writes `nvidia.com/gpu.clique` only once NVML reports the node's fabric state as `GPU_FABRIC_STATE_COMPLETED` — meaning Fabric Manager finished initialization successfully and the node is part of an NVLink domain.

On non-MNNVL systems (e.g., DGX B200, B300), the GPU fabric never reaches `GPU_FABRIC_STATE_COMPLETED`, so `nvidia.com/gpu.clique` is not set at all. On these systems, Topograph with an InfiniBand provider is the only source of network topology for scheduling decisions.

### Choosing between the accelerator label and `nvidia.com/gpu.clique` for scheduling

Workload schedulers consuming topology labels may need to choose between
Topograph's `accelerator.topograph.run/domain` and the NVIDIA GPU Operator's
`nvidia.com/gpu.clique`. To make the GPU Operator label authoritative, configure
`engine.params.acceleratorDomainSourceLabel: nvidia.com/gpu.clique`. The k8s
engine then preserves that source label and omits its managed accelerator domain
and sub-domain labels on nodes where the source has a non-empty value. Without
that configuration, Topograph publishes provider-derived accelerator labels
normally:

- **MNNVL hardware + Fabric Manager completed + NVL Partition granularity desired:** use `nvidia.com/gpu.clique`. On the AWS provider this is finer granularity than the accelerator label (which carries the CapacityBlockId, i.e., the NVL Domain). On InfiniBand and Lambda AI providers the two labels carry the same value.
- **MNNVL but Fabric Manager not yet completed, or non-MNNVL hardware:** `nvidia.com/gpu.clique` is absent. Use `accelerator.topograph.run/domain`.
- **Slurm clusters (no Kubernetes node labels):** neither label applies. Consumers read Slurm's `topology.conf` directly.

**Caveats when preferring `nvidia.com/gpu.clique`:**

- The label encodes node identity within MNNVL domains, not fabric proximity between them. NVL Partition is encoded as the full `<ClusterUUID>.<CliqueID>` value; NVL Domain is encoded as the `ClusterUUID` prefix. A scheduler can therefore distinguish racks — two nodes with different `ClusterUUID` are in different NVL Domains — and act on that distinction (same-Domain affinity to pack a job onto a single rack, cross-Domain anti-affinity to spread independent jobs across racks). What the label does **not** encode is the *physical proximity* between Domains: `ClusterUUID`s are opaque identifiers, so the label cannot tell a scheduler which racks share a top-of-rack switch, an aggregation tier, or a core. For cross-rack proximity-aware placement, Topograph populates the following labels from the InfiniBand or NetQ providers regardless of whether `gpu.clique` is present:
    - **Same top-of-rack switch** (cross-rack within a first-tier fabric) — Topograph's fabric tier 0 label.
    - **Same second-tier aggregation** (typically Scalable-Unit / pod-scale grouping above individual racks) — Topograph's fabric tier 1 label.
    - **Same third-tier aggregation** (present in large three-tier fabrics — typically cross-SU grouping in multi-SU SuperPOD deployments) — Topograph's fabric tier 2 label.

  These labels are also relevant for mixed-workload fragmentation avoidance (see [`docs/engines/k8s.md` § Mixed Workload Considerations](../engines/k8s.md#mixed-workload-considerations)).
- The label is refreshed by GPU Feature Discovery at its configured interval (the k8s-device-plugin default is 60s) rather than propagated instantly. Fabric-state changes in the window between refreshes are not yet reflected in the label.
- Persistence of `ClusterUUID` / `CliqueID` across node reboots is administratively controlled via Fabric Manager's `FABRIC_MODE_RESTART` configuration (default: preserve partition configurations). Deployments that disable preservation may see identifiers change across restarts, which can invalidate scheduler state cached on those values.

### Label value behavior

Label values are used as-is when they are 63 characters or shorter (the Kubernetes label value limit). Values longer than 63 characters are replaced with their **FNV-64a hash** rendered as an `x`-prefixed lowercase hex string (e.g., `x3e4f1a2b3c4d5e6f`) to stay within the limit. This means two nodes with the same long switch identifier will carry the same hash value — locality is preserved, but the original identifier is not recoverable from the label alone.

### Configuring label keys

The `k8s` engine accepts an optional closest-first `fabricLabels` array and an
optional singular `acceleratorLabel`. When `fabricLabels` is omitted, Topograph
uses `fabric.topograph.run/tier-N` for every discovered fabric tier. When
provided, only explicitly listed tiers are labeled; additional tiers are
omitted. `acceleratorLabel` defaults to
`accelerator.topograph.run/domain`. Every configured key must be a valid
Kubernetes label key. The accelerator sub-domain key is fixed at
`accelerator.topograph.run/sub-domain`. Fabric and managed accelerator label
values normally come from provider discovery. When
`acceleratorDomainSourceLabel` is configured and a node has a non-empty value,
the k8s engine preserves that existing source label as authoritative and omits
its managed accelerator domain and sub-domain labels for the node. Nodes
without a usable source value retain the provider-derived labels.

### Relationship to upstream standardization

[KEP-4962: Standardizing the Representation of Cluster Network Topology](https://github.com/kubernetes/enhancements/issues/4962) proposed reserved label keys under the `topology.kubernetes.io/` namespace, but it was closed without being adopted. Topograph therefore publishes its topology contract under the project-controlled, vendor-neutral `fabric.topograph.run/*` and `accelerator.topograph.run/*` namespaces. If Kubernetes adopts stable standard topology label keys in the future, Topograph will evaluate aligning with or publishing those keys alongside its project-scoped labels.

A related upstream effort, [KEP-5732: Topology Aware Scheduling](https://github.com/kubernetes/enhancements/issues/5732) (alpha in Kubernetes v1.37), introduces topology-aware workload scheduling that consumes existing node labels via a caller-provided key. It does not define a new label namespace — operators point it at whatever label keys are present, including Topograph's `fabric.topograph.run/*` keys.

## Without Topograph

When Topograph is not deployed, the labels commonly available for topology-aware scheduling are:

| Label key | Source | Semantics |
|---|---|---|
| `topology.kubernetes.io/zone` | Cloud provider / kubelet | Availability zone or data center zone |
| `topology.kubernetes.io/region` | Cloud provider / kubelet | Geographic region |
| `node.kubernetes.io/instance-type` | Cloud provider | VM / instance SKU |
| `topology.k8s.aws/capacity-block-id` | AWS Node Feature Discovery | AWS Capacity Block reservation ID. Per the [EC2 API reference for `InstanceTopology`](https://docs.aws.amazon.com/AWSEC2/latest/APIReference/API_InstanceTopology.html), on UltraServer instances this "identifies instances within the UltraServer domain" — a reservation-scoped grouping, not an NVL Partition identifier. On P6e-GB200 it is co-extensive with one UltraServer (AWS requires reserving the UltraServer as a unit per the [EKS UltraServer guide](https://docs.aws.amazon.com/eks/latest/userguide/ml-eks-nvidia-ultraserver.html)), so it aligns with the NVL Domain. AWS surfaces an explicit NVL Domain label, [`topology.k8s.aws/ultraserver-id`](https://docs.aws.amazon.com/sagemaker/latest/dg/sagemaker-hyperpod-eks-operate-console-ui-governance-tasks-scheduling.html), on SageMaker HyperPod-managed EKS clusters; on plain EKS or self-managed Kubernetes on P6e-GB200, AWS does not apply that label, and the NVL Domain must be derived from `nvidia.com/gpu.clique` (its `<ClusterUUID>.<CliqueID>` value encodes the NVL Domain as the ClusterUUID prefix). Topograph's AWS provider derives `accelerator.topograph.run/domain` from the same `CapacityBlockId` attribute, so on AWS the two labels carry identical string values — Domain-scoped, not Partition-scoped. |
| `topology.k8s.aws/network-node-layer-1` | AWS Node Feature Discovery | AWS network spine |
| `topology.k8s.aws/network-node-layer-2` | AWS Node Feature Discovery | AWS network aggregation |
| `topology.k8s.aws/network-node-layer-3` | AWS Node Feature Discovery | AWS network leaf |
| `oci.oraclecloud.com/host.network_block_id` | OCI | OCI network block |
| `oci.oraclecloud.com/host.rack_id` | OCI | OCI rack |
| `cloud.google.com/gce-topology-block` | GCP | GCP topology block |
| `cloud.google.com/gce-topology-subblock` | GCP | GCP topology sub-block |
| `cloud.google.com/gce-topology-host` | GCP | GCP host |
| `nvidia.com/gpu.clique` | NVIDIA GPU Operator (device plugin) | NVL Partition identifier, formatted `<ClusterUUID>.<CliqueID>`. The `ClusterUUID` prefix identifies the physical NVL Domain (e.g., one GB200 NVL72 rack); the `CliqueID` suffix identifies a Fabric-Manager-assigned logical sub-domain within it. Set only on MNNVL-capable nodes once Fabric Manager completes initialization and NVML reports `NVML_GPU_FABRIC_STATE_COMPLETED`; not present on non-MNNVL systems and may be absent on MNNVL nodes where Fabric Manager init has not completed. Multiple clique values can appear within a single NVL Domain (e.g., an x72 UltraServer split into two x36 halves). |
| `nvidia.com/cuda.driver-version.full` | NVIDIA GPU Operator (GFD) | Full CUDA driver version |
| `nvidia.com/cuda.runtime-version.full` | NVIDIA GPU Operator (GFD) | Full CUDA runtime version |

These labels are set by cloud provider integrations and the NVIDIA GPU Operator's GPU Feature Discovery (GFD) component — not by Topograph.

## Annotations

Topograph sets the following annotations on nodes as internal bookkeeping metadata. These are not intended for scheduler use but may be useful for debugging and observability.

| Annotation key | Semantics |
|---|---|
| `topograph.run/instance` | The cloud instance ID or node identifier as returned by the provider |
| `topograph.run/region` | The provider region associated with this node |
| `topograph.run/cluster-id` | The cluster identifier (where reported by the provider) |

Additional annotations are set on topology ConfigMaps (used by the Slinky engine):

| Annotation key | Semantics |
|---|---|
| `topograph.run/engine` | The engine that generated the ConfigMap |
| `topograph.run/topology-managed-by` | The Topograph instance managing the ConfigMap |
| `topograph.run/last-updated` | Timestamp of the most recent topology update |
| `topograph.run/plugin` | The scheduler plugin that consumes the ConfigMap |
| `topograph.run/block-sizes` | Comma-separated list of block sizes in the topology |
| `topograph.run/slurm-namespace` | The Slurm namespace associated with this topology ConfigMap |

## Integration with NVSentinel

NVSentinel's Metadata Augmentor enriches health events with node labels from a configurable `allowedLabels` list. NVSentinel's defaults do not include Topograph's `accelerator.topograph.run/*` accelerator keys or variable-depth `fabric.topograph.run/tier-N` fabric keys. Operators must therefore add the Topograph keys to `allowedLabels` for NVSentinel to propagate accelerator and fabric locality into health event metadata.

NVSentinel's Metadata Augmentor skips labels that aren't present on a node, so nodes without Topograph (or MNNVL-only labels on non-MNNVL hardware) behave cleanly — no configuration conditionals needed.

Operators can add the current Topograph labels explicitly in `distros/kubernetes/nvsentinel/values.yaml`. Include every fabric tier that downstream consumers need; the following example covers a three-tier fabric:

```yaml
transformers:
  MetadataAugmentor:
    allowedLabels:
      # ... existing labels ...
      # Topograph topology labels (requires Topograph deployed in the cluster)
      - "accelerator.topograph.run/domain"
      - "accelerator.topograph.run/sub-domain"
      - "fabric.topograph.run/tier-0"
      - "fabric.topograph.run/tier-1"
      - "fabric.topograph.run/tier-2"
```

See NVSentinel's [`docs/INTEGRATIONS.md` § Topology Awareness (Topograph)](https://github.com/NVIDIA/NVSentinel/blob/main/docs/INTEGRATIONS.md#topology-awareness-topograph).
