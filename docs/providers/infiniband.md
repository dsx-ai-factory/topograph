# InfiniBand Topology Provider

Topograph provides two variations of InfiniBand provider. Both discover the IB fabric switch tree using `ibnetdiscover`, which is useful for any cluster — CPU-only, mixed, or GPU-accelerated — where topology-aware scheduling across an InfiniBand fabric improves workload performance. NVLink domain discovery is an additional capability that applies only to nodes with NVLink-connected NVIDIA GPUs.

**Why automate IB discovery?** Hand-maintaining IB topology — a static `topology.conf` or a set of hand-applied node labels — is feasible at ~32 nodes with a stable network and a careful operator. It does not scale. At 1,000 nodes with InfiniBand fabric churn, NVLink partitions shifting with tenant allocation, and a constant background rate of link degradation and node cycling, manual maintenance becomes the dominant source of scheduling misplacement. Topograph keeps topology data current as the cluster changes, removing that burden.

The choice of which to use depends on the specifics of the deployment environment:

- Use **`infiniband-bm`** for bare-metal clusters (e.g. Slurm)
- Use **`infiniband-k8s`** for Kubernetes clusters

If **NetQ is deployed** in your environment, consider using the [NetQ provider](./netq.md) instead — it discovers topology via the NetQ management API rather than directly from the fabric, which avoids node access requirements and is the standard approach for Spectrum-X environments.

For **Multi-Node NVLink (MNNVL) Kubernetes clusters** (e.g. GB200 NVL72), do not substitute the [DRA provider](./dra.md) when backend-fabric locality matters. The DRA provider is limited to Slinky `topology/block` output, requires `nvidia.com/gpu.clique` to already be present on every participating node, and discovers only NVLink partition membership. It does not discover the InfiniBand or Ethernet switch fabric between partitions, so it cannot guide placement when a workload spans more than one NVLink partition. Use `infiniband-k8s` (or [NetQ](./netq.md), when available) to include that fabric hierarchy.

| | `infiniband-bm` | `infiniband-k8s` |
|---|---|---|
| **Auth** | None | In-cluster service account |
| **Node access** | `pdsh` (SSH-based) | Kubernetes pod exec |
| **Accelerator-domain source** | Configurable: `nvidia-smi` via pdsh, or none | Configurable: `nvidia-smi`, Kubernetes Node label, or none |
| **Target environment** | Bare-metal / Slurm | Kubernetes |

Both variants are presently single-region only (multi-region requests return a `400 Bad Request` error). No CSP credentials are required.

## Output

Both variants produce the same topology representation, and are in turn consumed by whichever engine you configure:

- **Slurm engine** (`engine: slurm`) — writes a `topology.conf` file describing the switch tree, used by the Slurm topology plugin for topology-aware scheduling
- **Kubernetes engine** (`engine: k8s`) — applies `fabric.topograph.run/` labels to nodes reflecting their position in the switch hierarchy and (where applicable) their NVLink domain
- **NFD engine** (`engine: nfd`) — publishes topology as Node Feature Discovery `NodeFeature` and `NodeFeatureGroup` custom resources
- **Slinky engine** (`engine: slinky`) — writes topology data to a Kubernetes ConfigMap for Slurm-on-Kubernetes deployments

See the engine documentation (`docs/engines/`) for details on each output format.

---

## `infiniband-bm` (Bare-Metal)

### Prerequisites

- `pdsh` must be installed on the node running Topograph and able to reach at least one node per IB fabric segment — Topograph discovers the full fabric from a single entry point per segment, so every node does not need to be reachable via pdsh
- `ibnetdiscover` must be available on cluster nodes (invoked via `pdsh` with `sudo`) — part of the standard `infiniband-diags` package (`dnf install infiniband-diags` / `apt install infiniband-diags`), expected to already be present on any properly configured IB system
- NVIDIA GPU driver required on nodes with NVLink-connected GPUs when `accelerator.source: nvidia-smi` is configured. Nodes without accelerator-domain data are still included in the IB switch tree.

### How It Works

1. Runs `sudo ibnetdiscover` via `pdsh` on one node per IB fabric segment to map the full switch tree
2. When the `accelerator` section selects `nvidia-smi`, runs `nvidia-smi --query-gpu=fabric.clusterUuid,fabric.cliqueId --format=csv,noheader` via `pdsh` across all nodes to collect NVLink partition IDs. Identical rows returned for multiple GPUs are merged, unavailable (`N/A`) fields are rejected, and the CSV pair is normalized to `ClusterUUID.CliqueId` — the same format as `nvidia.com/gpu.clique` set by the GPU Operator device plugin on MNNVL systems.
3. Combines the switch tree and any NVLink clique data into the topology graph

### Configuration

No credentials are required. Set `provider: infiniband-bm` in your Topograph config:

```yaml
http:
  port: 49021
  ssl: false

provider: infiniband-bm
engine: slurm
```

Accelerator discovery is independent of InfiniBand fabric discovery and is
disabled when the `accelerator` section is omitted or empty. Bare-metal
deployments support `nvidia-smi` or `none`:

```yaml
provider:
  name: infiniband-bm
  params:
    accelerator:
      source: none
```

### Verifying the Output

After triggering topology generation, query the result endpoint:

```bash
id=$(curl -s -X POST -H "Content-Type: application/json" -d @payload.json http://localhost:49021/v1/generate)
curl -s "http://localhost:49021/v1/topology?uid=$id"
```

For the Slurm engine, verify the generated `topology.conf` reflects the expected switch hierarchy. See the [Slurm engine documentation](../engines/slurm.md) for details.

---

## `infiniband-k8s` (Kubernetes)

### Prerequisites

- Topograph deployed via Helm — when `accelerator.source` is `nvidia-smi`, the node-data-broker DaemonSet collects NVLink partition IDs from each node and stores them as Kubernetes node annotations (`topograph.run/cluster-id`). With `kubernetes-label` or `none`, the broker skips that collection.
- The default **`ghcr.io/dsx-ai-factory/topograph`** image includes **`ibnetdiscover`** (Alpine `rdma-core`). No separate InfiniBand image is required. IB deployments typically run the broker **privileged** and mount host **`/sys/class`** so `ibnetdiscover` can reach IB devices — see [`values.k8s.ib-example.yaml`](../../charts/topograph/values.k8s.ib-example.yaml).
- NVIDIA GPU Operator — standard on NVIDIA GPU Kubernetes clusters; manages the device plugin DaemonSet used to read NVLink clique IDs. Required only for NVLink domain discovery; on clusters without NVLink-connected GPUs this does not apply and the provider will still discover the IB switch tree.

### How It Works

1. Runs `ibnetdiscover` by exec-ing into a node-data-broker pod on each node to map the switch tree
2. When `provider.params.accelerator` is non-empty, resolves accelerator domains independently using its `source`: `nvidia-smi` reads the broker-written `topograph.run/cluster-id` annotation, `kubernetes-label` reads a configured Node label, and `none` explicitly disables accelerator discovery. Omitting the section or using an empty object also disables it. NVL partition IDs use `ClusterUUID.CliqueId`, the same format as `nvidia.com/gpu.clique`.
3. Combines the switch tree and any NVLink clique data into the topology graph

### Configuration

No credentials are required. The provider uses the in-cluster service account automatically.

Set `provider: infiniband-k8s` in your Topograph config:

```yaml
http:
  port: 49021
  ssl: false

provider: infiniband-k8s
engine: k8s
```

### Parameters

#### Topology request parameters

The following optional parameters can be passed in the topology request payload:

| Parameter | Type | Default | Description |
|---|---|---|---|
| `nodeSelector` | `map[string]string` | — | Label selector to filter which nodes participate in topology discovery |
| `accelerator` | `object` | — | Enables and configures accelerator-domain discovery. When omitted or empty, no accelerator domains are discovered. |
| `accelerator.source` | `string` | — | Required when the `accelerator` section is non-empty. Accelerator-domain source: `nvidia-smi`, `kubernetes-label`, or `none`. |
| `accelerator.kubernetesLabel.key` | `string` | — | Required for the `kubernetes-label` source. Kubernetes Node label read as the accelerator-domain ID; no default is assumed. |

For a manual request, keep `accelerator.source` consistent with the source configured for the deployed node-data-broker. In particular, `source: nvidia-smi` reads the broker-written `topograph.run/cluster-id` annotation; it does not execute `nvidia-smi` or change the broker's GPU Operator workload target.

#### Helm node-data-broker settings

The following settings select the GPU Operator workload used by the node-data-broker when `provider.params.accelerator.source` is `nvidia-smi`. They are deployment settings and cannot be changed by a topology request:

| Helm value | Type | Default | Description |
|---|---|---|---|
| `provider.params.accelerator.nvidiaSmi.gpuOperatorNamespace` | `string` | `gpu-operator` | Namespace containing the GPU Operator device-plugin DaemonSet. |
| `provider.params.accelerator.nvidiaSmi.devicePluginDaemonSet` | `string` | `nvidia-device-plugin-daemonset` | Device-plugin DaemonSet used for `nvidia-smi` execution. |

With Helm, configure the accelerator source under `provider.params`. The chart writes the provider configuration to both the node-data-broker and node-observer ConfigMaps, keeping chart-generated topology requests aligned with broker collection. When the source is `nvidia-smi`, omitted `gpuOperatorNamespace` and `devicePluginDaemonSet` values are rendered as `gpu-operator` and `nvidia-device-plugin-daemonset`, respectively. The chart also drops the broker's GPU Operator pod-exec permissions when the source is not `nvidia-smi`:

```yaml
provider:
  name: infiniband-k8s
  params:
    accelerator:
      source: kubernetes-label
      kubernetesLabel:
        key: nvidia.com/gpu.clique
engine:
  name: k8s
```

To use a non-default GPU Operator namespace or device-plugin DaemonSet:

```yaml
provider:
  name: infiniband-k8s
  params:
    accelerator:
      source: nvidia-smi
      nvidiaSmi:
        gpuOperatorNamespace: my-namespace
        devicePluginDaemonSet: my-daemonset
```

The node-data-broker applies node annotations once when its pod starts. Restart the broker pod to re-apply them after relevant node or provider metadata changes.

If `ibnetdiscover` needs extra config files, the chart can render ConfigMaps and mount them into the node-data-broker pods:

```yaml
nodeDataBroker:
  configMapMounts:
    - name: ibdiag
      mountPath: /etc/infiniband-diags/ibdiag.conf
      subPath: ibdiag.conf
      data:
        ibdiag.conf: |-
          CA=smi0
          Port=1
```

Example request payload with `nodeSelector`:

```json
{
  "provider": {
    "name": "infiniband-k8s",
    "params": {
      "nodeSelector": {
        "nvidia.com/gpu.present": "true"
      }
    }
  },
  "engine": {
    "name": "k8s"
  }
}
```

### Verifying the Output

After topology generation, inspect the node labels applied by Topograph:

```bash
kubectl get nodes -o json | jq '.items[].metadata.labels | with_entries(select(.key | startswith("fabric.topograph.run")))'
```

See the [Kubernetes engine documentation](../engines/k8s.md) for details on the label schema.
