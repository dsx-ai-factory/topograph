# Crusoe Topology Provider

The Crusoe provider discovers the InfiniBand switch fabric of a
[Crusoe Cloud](https://crusoe.ai/) Kubernetes cluster from labels the Crusoe
control plane writes on every Node. It uses in-cluster service account auth, so
no credentials are required.

Crusoe compute is virtualized. A guest VM cannot run `ibnetdiscover` against the
fabric, and there is no per-node metadata service that reports switch identity.
The control plane instead publishes each VM's fabric position as Node labels, so
the provider only has to read the Kubernetes API.

## When to Use This Provider

Use it on Crusoe Managed Kubernetes. On any other platform use the provider
that matches it — see [Supported Providers](../overview.md#supported-providers).

The provider supplies the switch fabric hierarchy, so pair it with
`topology/tree`. On rack-scale NVLink systems it also supplies an accelerator
domain per node, which `topology/block` uses — see
[NVLink domains](#nvlink-domains).

## How It Works

1. Lists Nodes, filtered by `nodeSelector` when one is set.
2. Reads `crusoe.ai/ib.partition.id` and `crusoe.ai/pod.id` from each Node and
   turns them into fabric tiers.
3. Reads `nvidia.com/gpu.clique` and turns it into an accelerator domain.
4. Returns the canonical graph.

### Fabric tiers

Tiers are closest-first, so tier 0 is nearest the compute node:

| Tier | Source | Meaning |
|---|---|---|
| 0 | `crusoe.ai/pod.id` | Rail-optimized pod — hosts wired to a shared set of InfiniBand leaf switches, one per GPU rail |
| 1 | `crusoe.ai/ib.partition.id` | InfiniBand partition — the isolated fabric the pod belongs to |
| 2 | `crusoe` | A synthetic root above every partition |

The root tier is not a switch. Slurm needs one common ancestor so a job can
span partitions, and so nodes with no InfiniBand still belong to the tree.

### Nodes without InfiniBand

Nodes that carry neither label get the placeholder tiers `cpu-pod` and
`cpu-partition` under the same root. That keeps them schedulable in one Slurm
tree alongside the InfiniBand nodes.

A node the request names but the `nodeSelector` excludes has no tiers at all, so
it appears under `no-topology` in the generated file rather than disappearing.

### NVLink domains

On rack-scale NVLink systems such as GB200 and GB300 NVL72, the fabric a job
wants to stay inside is the NVLink partition, not the InfiniBand partition. It
is advertised as `nvidia.com/gpu.clique`, with the value
`<clusterUUID>.<cliqueID>`.

Two components can publish that label. NVIDIA GPU Feature Discovery does, and so
does the compute-domain kubelet plugin in the NVIDIA DRA driver when
`kubeletPlugin.containers.computeDomains.gpuCliqueLabelEnabled=true`. GFD is
bundled with the Kubernetes device plugin and is deprecated on clusters that run
the DRA driver, so on those clusters the kubelet plugin owns the label. Both
write the same key and the same `<clusterUUID>.<cliqueID>` value, so the
provider reads them identically.

The provider groups on the whole value. That is the NVL Partition: the set of
nodes Fabric Manager placed in one IMEX domain, which is the granularity a job
must stay inside to get NVLink bandwidth. It can be finer than the physical NVL
Domain named by the `<clusterUUID>` prefix alone — an x72 rack split into two
x36 halves carries two clique values, and each becomes its own block. A clique
never spans racks, so a block never does either.

Each distinct clique becomes one accelerator domain, published verbatim so
`accelerator.topograph.run/domain` can be compared directly against
`nvidia.com/gpu.clique` on the node. `topology/block` renders one block per
domain:

```text
# block001=29d9a0b8-948d-4a61-8b9e-fbbbf06c521b.32766
BlockName=block001 Nodes=gpu-[01-02]
# block002=29d9a0b8-948d-4a61-8b9e-fbbbf06c521b.32767
BlockName=block002 Nodes=gpu-[03-04]
BlockSizes=2,4
```

The InfiniBand partition is deliberately **not** used as a fallback domain. A
partition can span many racks while a clique cannot, so keying blocks on the
partition would let Slurm spread one job across racks and quietly fall back to
InfiniBand instead of NVLink. Nodes with no clique label get no domain and are
scheduled by the tree alone.

A GPU SKU that ships without InfiniBand still carries a clique, so it
contributes a block even though it has no fabric tiers.

## Prerequisites

- A Crusoe Cloud Kubernetes cluster
- For fabric tiers, nodes labelled by the Crusoe control plane with
  `crusoe.ai/ib.partition.id` and `crusoe.ai/pod.id`
- For NVLink blocks, a component publishing `nvidia.com/gpu.clique` on NVLink
  hosts: either NVIDIA GPU Feature Discovery, or the NVIDIA DRA driver with
  `kubeletPlugin.containers.computeDomains.gpuCliqueLabelEnabled=true`

Neither set is required on every node. A node with only the InfiniBand labels
gets fabric tiers and no block. A node with only a clique gets a block and the
placeholder tiers.

## Parameters

| Parameter | Type | Required | Description |
|---|---|---|---|
| `nodeSelector` | `map[string]string` | No | Label selector limiting which Nodes take part. Omit it and every Node in the cluster is included. |
| `trimTiers` | `int` | No | Drop this many tiers from the top of the fabric path. `0`–`2`. |

## Configuration

Select the provider through the Helm chart. A Slurm-on-Kubernetes cluster pairs
it with the [Slinky engine](../engines/slinky.md):

```yaml
provider:
  name: crusoe
  params:
    nodeSelector:
      slurm.crusoe.ai/compute-node-type: "true"

engine:
  name: slinky
  params:
    namespace: slurm
    plugin: topology/tree
    topologyConfigmapName: slurm-topology
    topologyConfigPath: topology.conf
    podSelector:
      matchLabels:
        app.kubernetes.io/component: compute
```

Set a `nodeSelector` on a shared cluster. Without one, control-plane and system
nodes land in `cpu-partition` and appear in `topology.conf`.

No credentials are required. The provider uses the in-cluster service account.

## Verifying the Output

Check that the labels are present before generating topology:

```bash
kubectl get nodes -o json | jq '.items[] | {
  name: .metadata.name,
  partition: .metadata.labels["crusoe.ai/ib.partition.id"],
  pod: .metadata.labels["crusoe.ai/pod.id"],
  clique: .metadata.labels["nvidia.com/gpu.clique"]
}'
```

A `404` from topology generation means no requested node matched the selector.
Confirm the `nodeSelector` and the instance list in the request.

Read back the generated file:

```bash
kubectl get configmap slurm-topology -n slurm -o jsonpath='{.data.topology\.conf}'
```

## Simulation

`crusoe-sim` renders topology from a model file instead of a live cluster. The
model attaches Crusoe labels to switches and the loader propagates them to the
nodes, so the simulation runs the same code as production. See
`tests/models/crusoe-small.yaml`, which covers two InfiniBand partitions plus
nodes with no InfiniBand labels.
