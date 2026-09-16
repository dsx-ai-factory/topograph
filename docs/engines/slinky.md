# Topograph Slinky Engine

## Overview

The **slinky engine** is Topograph's engine for SLURM clusters running on Kubernetes. It is designed to work with the [Slinky project](https://github.com/SlinkyProject/) - an open-source set of integration tools by SchedMD that brings SLURM capabilities into Kubernetes environments.

While the [Slinky project](https://slinky.ai) provides comprehensive SLURM-on-Kubernetes orchestration (operators, schedulers, exporters, etc.), Topograph's slinky engine complements this ecosystem by providing **topology discovery and configuration management** for SLURM clusters running in Kubernetes.

The Slinky engine bridges the gap between Kubernetes infrastructure and SLURM workload management by updating SLURM topology configurations stored in Kubernetes ConfigMaps.

## How It Works

1. **Node Discovery**: Queries Kubernetes nodes and SLURM pods to build a topology map
2. **Topology Generation**: Creates SLURM topology configuration (tree or block format)
3. **ConfigMap Management**: Updates the specified ConfigMap with new topology data including metadata annotations for tracking and debugging

<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="../assets/topograph-slinky-community-dark.png" />
    <img src="../assets/topograph-slinky-community-light.png" width="100%" alt="Topograph Slinky engine flow diagram" />
  </picture>
</p>

## Configuration
Topograph is deployed as a standard Kubernetes application using a [Helm chart](https://github.com/dsx-ai-factory/topograph/tree/main/charts/topograph).
Topograph is configured using a configuration file stored in a ConfigMap and mounted to the Topograph container at `/etc/topograph/topograph-config.yaml`.
In addition, when sending a topology request, the request payload includes additional parameters.
The provider and engine are defined as top-level Helm values, as shown below:

> **Shared with the Kubernetes engine:** because the Topograph API server runs as a Kubernetes workload regardless of the engine, anything about the chart's deployment surface — values-schema validation, `helm test` hooks, access patterns (ClusterIP port-forward, Ingress, Gateway API `HTTPRoute`), Prometheus `ServiceMonitor`, `NetworkPolicy` guidance, and the chart's `README.md` — is shared with the Kubernetes engine and documented authoritatively in [`engines/k8s.md`](./k8s.md#validation-and-testing) and [`engines/k8s.md#exposing-the-topograph-api`](./k8s.md#exposing-the-topograph-api). Those sections apply equally to Slinky deployments.

```yaml
provider:
  # Name of the cloud provider or on-prem environment.
  name: aws
engine:
  name: slinky
  params:
    namespace: ns-slinky                     # Namespace where Slinky is running
    podSelector:                             # Label selector for pods running SLURM nodes
      matchLabels:
        app.kubernetes.io/component: compute
    plugin: topology/block                   # Name of the topology plugin
    blockSizes: [4]                          # (Optional) Block size for the block topology plugin
    blockName:                               # (Optional) Derive block names from node names
      nodeNameRegexp: 'd([0-9]{2})-r([0-9]{2})'
      format: 'domain${1}_rack${2}'
    topologyConfigmapName: slurm-config      # Name of the ConfigMap containing the topology config
    topologyConfigPath: topology.conf        # Key in the ConfigMap for the topology config
```

When `blockSizes` is omitted for `topology/block`, the Slinky engine uses the
same [automatic block-size inference](./slurm.md#automatic-block-size-inference)
as the SLURM engine.

### Per-partition topologies

When per-partition topologies are configured, each entry may declare how its node membership is resolved:

| Field | Behavior |
|---|---|
| `nodes` | Explicit SLURM node list. Takes precedence over `podSelector`. |
| `podSelector` | Kubernetes `LabelSelector` matching the slurmd pods in the partition. The engine lists pods in the engine's `namespace`, filters to `Ready` pods, and reads each pod's SLURM name from the `slurm.node.name` label (falling back to `pod.spec.hostname`). |
| `blockName` | For `topology/block`, derives block names using the required `nodeNameRegexp` and `format` fields. |
| _neither_ | The engine falls back to running `scontrol show partition <name>` inside the controller pod, or a login pod when no controller pod is running (legacy behavior). The controller (`app.kubernetes.io/component: controller`) is always present; login pods are optional. |

`nodes` and `podSelector` are mutually exclusive on the same entry; configuring both returns a validation error at engine load time.

```yaml
engine:
  name: slinky
  params:
    namespace: ns-slinky
    podSelector:
      matchLabels:
        app.kubernetes.io/component: compute
    topologies:
      gpu-partition:
        plugin: topology/block
        blockSizes: [8, 16]
        blockName:
          nodeNameRegexp: 'd([0-9]{2})-r([0-9]{2})'
          format: 'domain${1}_rack${2}'
        podSelector:                                 # partition membership by pod labels
          matchLabels:
            app.kubernetes.io/component: compute
            slurm.partition: gpu
      cpu-partition:
        plugin: topology/tree
        nodes: ["cpu-[001-032]"]                     # explicit list
      default:
        plugin: topology/flat
        clusterDefault: true                         # no podSelector, no nodes → scontrol fallback
```

`blockName.nodeNameRegexp` uses Go regular-expression syntax and may match anywhere in the node name; use anchors when needed. `blockName.format` uses Go regexp expansion syntax, including numeric captures such as `${1}` and named captures such as `${domain}`. Every node in a non-empty block must match and produce the same non-empty name, and names must be unique across blocks. Invalid expressions, unmatched nodes, inconsistent names within a block, and duplicate names are rejected. Empty complemented blocks retain their generated names.

### Using an existing Node label for block topology

Set `acceleratorDomainSourceLabel` when another component already publishes the
desired accelerator domain as a Kubernetes Node label. For `topology/block`,
the Slinky engine uses that label instead of accelerator domains returned by
the provider. There is no default source label, and existing labels receive no
special treatment when the parameter is omitted.

The option only affects block topology. Tree topology still comes from the selected provider, and the engine still maps Kubernetes nodes to Slurm nodes through the configured slurmd pod selector.

```yaml
engine:
  name: slinky
  params:
    namespace: ns-slinky
    podSelector:
      matchLabels:
        app.kubernetes.io/component: compute
    plugin: topology/block
    blockSizes: [8, 16]
    topologyConfigmapName: slurm-config
    topologyConfigPath: topology.conf
    acceleratorDomainSourceLabel: example.com/accelerator-domain
```

Nodes without the configured label retain the existing label-backed behavior:
they are skipped rather than falling back individually to provider domains. If
no usable domains can be built from the configured label and the Topograph
instance annotation, topology generation fails with an actionable `502` error
that reports the configured key and why nodes were skipped. Replacing provider
domains also suppresses provider accelerator sub-domains.

For example, an operator may explicitly select
`nvidia.com/gpu.clique`, but the Slinky engine no longer assumes that key.

### Kubernetes API rate limiting

The Slinky engine uses client-go's default Kubernetes client limits of 5 QPS
and a burst of 10 unless deployment-level limits are configured. Dynamic-node
reconciliation compares each desired topology annotation with the Node objects
returned by the cluster-wide list, skips nodes that are already current, and
patches only changed annotations. This avoids a separate Node GET for every
node during steady-state reconciliation.

Large reconciliations that legitimately change many nodes can still exceed the
default client-side limit. Increase the limits conservatively and monitor API
server latency and throttling:

```yaml
kubeClient:
  qps: 50
  burst: 100
```

The chart exposes these values as `KUBE_QPS` and `KUBE_BURST` to the DRA
provider and the Kubernetes, NFD, and Slinky engines. Outside Helm, set those
environment variables on the Topograph process. Kubernetes client limits are
deployment settings and cannot be overridden by a topology request. Increasing
the limits reduces client-side waiting but does not reduce API-server load;
narrow `nodeSelector` and `podSelector` values remain the preferred first
mitigation.

## ConfigMap Annotations

Slinky automatically adds metadata annotations to managed ConfigMaps for improved observability:

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: slurm-config
  annotations:
    # Topograph metadata
    topograph.run/engine: "slinky"
    topograph.run/topology-managed-by: "topograph"
    topograph.run/last-updated: "2024-01-01T10:11:00Z"
    topograph.run/slurm-namespace: "slurm"
    topograph.run/plugin: "topology/tree"
    topograph.run/block-sizes: "8,16,32"

    # Original annotations preserved
    meta.helm.sh/release-name: slurm
    meta.helm.sh/release-namespace: slurm
data:
  topology.conf: |
    SwitchName=sw1 Switches=sw[2-3]
    SwitchName=sw2 Nodes=node[1-4]
    SwitchName=sw3 Nodes=node[5-8]
```

### Annotation Reference

| Annotation                                 | Description                               |
| ------------------------------------------ | ----------------------------------------- |
| `topograph.run/engine`              | Engine that manages this ConfigMap        |
| `topograph.run/topology-managed-by` | Indicates topograph manages topology data |
| `topograph.run/last-updated`        | RFC3339 timestamp of last update          |
| `topograph.run/slurm-namespace`     | SLURM cluster namespace                   |
| `topograph.run/plugin`              | Topology plugin used (tree/block)         |
| `topograph.run/block-sizes`         | Block sizes for block topology            |

## Usage Examples

Topograph runs autonomously in Kubernetes environments, including Slinky. When the Node Observer detects a selected node or pod change, or sees the Topograph API server become ready after startup or a container restart, it sends topology requests to the API server. The API server then triggers an update to the network topology information within the cluster. However, if you want to manually trigger network topology discovery, you can send HTTP requests to the API server, as shown below.

### Topology Configuration in the Tree Format

```bash
curl -X POST -H "Content-Type: application/json" \
  -d '{
    "provider": {"name": "aws"},
    "engine": {
      "name": "slinky",
      "params": {
        "namespace": "ns-slinky",
        "podSelector": {
          "matchLabels": {
            "app.kubernetes.io/component": "compute"
          }
        },
        "topologyConfigPath": "topology.conf",
        "topologyConfigmapName": "slurm-config"
      }
    }
  }' \
  http://localhost:49021/v1/generate
```

### Topology Configuration in the Block Format

```bash
curl -X POST -H "Content-Type: application/json" \
  -d '{
    "provider": {"name": "aws"},
    "engine": {
      "name": "slinky",
      "params": {
        "namespace": "ns-slinky",
        "podSelector": {
          "matchLabels": {
            "app.kubernetes.io/component": "compute"
          }
        },
        "topologyConfigPath": "topology.conf",
        "topologyConfigmapName": "slurm-config",
        "plugin": "topology/block",
        "blockSizes": [8,16,32]
      }
    }
  }' \
  http://localhost:49021/v1/generate
```
