# Topograph NFD Engine

The `nfd` engine publishes Topograph topology through
[Node Feature Discovery](https://kubernetes-sigs.github.io/node-feature-discovery/)
custom resources instead of writing Kubernetes node labels directly.

It creates:

- one `NodeFeature` per topology node, carrying Topograph topology as
  `spec.features.attributes.topograph.network.elements` (empty for a node the
  provider returned no topology for)
- one `NodeFeatureGroup` per distinct fabric tier, accelerator domain, or
  accelerator sub-domain value

NFD master evaluates those features and writes matching nodes to
`NodeFeatureGroup.status.nodes`.

Fabric topology is variable-depth. The engine publishes `fabric-tier-N` for
every discovered tier, where tier 0 is closest to the node and higher tiers
progress outward; there is no fixed number of fabric attributes or groups.
Accelerator domain and optional sub-domain attributes are published separately when
the provider supplies them.

## When to Use

Use `engine: nfd` only when a downstream component already consumes NFD
`NodeFeatureGroup` objects. For native Kubernetes scheduling with
`podAffinity.topologyKey`, use the [`k8s`](./k8s.md) engine instead.

## Install NFD

The `NodeFeatureGroupAPI` feature gate is **disabled by default** in NFD. It has
been Alpha since NFD v0.16 and must be enabled explicitly before using this
engine. See the upstream
[feature-gate reference](https://kubernetes-sigs.github.io/node-feature-discovery/master/reference/feature-gates.html#nodefeaturegroupapi)
for its current status.

The following example limits the NFD worker to nodes labeled
`nfd-enabled=true`. Label each intended worker node first:

```bash
kubectl label node <node-name> nfd-enabled=true
```

Then install NFD with the `NodeFeatureGroupAPI` feature gate enabled:

```bash
helm repo add nfd https://kubernetes-sigs.github.io/node-feature-discovery/charts

helm repo update

helm install nfd nfd/node-feature-discovery \
  --namespace node-feature-discovery \
  --create-namespace \
  --set-string worker.nodeSelector.nfd-enabled=true \
  --set featureGates.NodeFeatureGroupAPI=true
```

Omit `worker.nodeSelector.nfd-enabled` if the NFD worker should run on all
eligible nodes.

## Configuration

```yaml
provider:
  name: infiniband-k8s
engine:
  name: nfd
  params:
    nodeSelector:
      nvidia.com/gpu.present: "true"
    cleanup: true
    acceleratorDomainSourceLabel: example.com/accelerator-domain
kubeClient:
  qps: 50
  burst: 100
nfdNamespace: node-feature-discovery
```

Parameters:

| Parameter | Required | Default | Description |
|---|---:|---|---|
| `nodeSelector` | No | all nodes | Limits the Kubernetes nodes used as provider input. Same meaning as the `k8s` engine selector. |
| `cleanup` | No | `true` | Deletes stale Topograph-managed `NodeFeature` and `NodeFeatureGroup` objects that are no longer present in the generated topology. If generation produces no topology values, the engine returns an error and preserves the existing topology. |
| `acceleratorDomainSourceLabel` | No | none | Existing Kubernetes Node label used as the authoritative accelerator-domain source. Values replace the provider domain and suppress the provider sub-domain for matching nodes. |

`nfdNamespace` is a deployment-level Helm value, not an engine request
parameter. It must be the namespace where NFD master runs because NFD updates
`NodeFeatureGroup.status` there. The Helm value defaults to
`node-feature-discovery`.

When `rbac.create` is enabled, the chart creates a `Role` and `RoleBinding` in
that namespace and configures the Topograph deployment with the same value
through `NFD_NAMESPACE`. Outside Helm, set `NFD_NAMESPACE` on the Topograph
process. The NFD engine returns an error if the variable is unset or blank.

Topology requests cannot select an NFD namespace; the deployment environment is
authoritative.

### Kubernetes API rate limiting

The NFD engine uses a typed Kubernetes client to list Nodes and a dynamic client
to reconcile `NodeFeature` and `NodeFeatureGroup` objects. Helm configures their
shared token-bucket limiter through `kubeClient.qps` and `kubeClient.burst`,
which become `KUBE_QPS` and `KUBE_BURST` in the Topograph deployment. The
configured QPS is the aggregate request rate rather than a separate allowance
for each client. If only QPS or burst is configured, the other value uses the
client-go default. Without either setting, each client retains its client-go
default limiter. Outside Helm, set the environment variables directly.

## Generated Objects

For a node on a three-tier fabric, the engine writes a `NodeFeature` like:

```yaml
apiVersion: nfd.k8s-sigs.io/v1alpha1
kind: NodeFeature
metadata:
  name: topograph-node-node-a-...
  namespace: node-feature-discovery
  labels:
    nfd.node.kubernetes.io/node-name: node-a
    app.kubernetes.io/managed-by: topograph
    topograph.run/engine: nfd
spec:
  features:
    attributes:
      system.name:
        elements:
          nodename: node-a
      topograph.network:
        elements:
          accelerator-domain: nvl3
          accelerator-sub-domain: nvl3.rack01
          fabric-tier-0: leaf-12
          fabric-tier-1: spine-2
          fabric-tier-2: core-1
```

For each distinct value, it writes a matching `NodeFeatureGroup`:

```yaml
apiVersion: nfd.k8s-sigs.io/v1alpha1
kind: NodeFeatureGroup
metadata:
  name: topograph-fabric-tier-0-leaf-12-...
  namespace: node-feature-discovery
  labels:
    app.kubernetes.io/managed-by: topograph
    topograph.run/engine: nfd
    topograph.run/group-type: fabric-tier-0
  annotations:
    topograph.run/label-key: fabric.topograph.run/tier-0
    topograph.run/label-value: leaf-12
spec:
  featureGroupRules:
    - name: fabric-tier-0 equals leaf-12
      matchFeatures:
        - feature: topograph.network
          matchExpressions:
            fabric-tier-0:
              op: In
              value: ["leaf-12"]
```

Topograph includes `system.name.elements.nodename` so NFD can populate group
membership even when an NFD worker does not run on the node, as with simulated
KWOK nodes. Topograph does not write `status.nodes`; NFD owns status updates.

When `acceleratorDomainSourceLabel` is configured and a Kubernetes node has a
non-empty value for that label, the engine uses the value as the authoritative
`accelerator-domain` NFD attribute instead of the provider domain. The matching
`NodeFeatureGroup` records the configured key as its source label. The
provider-supplied `accelerator-sub-domain` is suppressed for that node, while all
fabric-tier attributes are still published. Nodes without the configured label
retain their provider domain and sub-domain. When the parameter is omitted, no
existing Kubernetes label receives special treatment.

## Caveats

`NodeFeatureGroup` objects enumerate topology groups. A scheduler that wants to
place a workload within one leaf switch must still decide which leaf group to
use. This is different from native pod affinity, where the scheduler can compare
candidate nodes using a single `topologyKey`.

Large clusters may create many CRs and large `status.nodes` arrays. The design
notes in [`docs/design/nfd-engine-sdd.md`](../design/nfd-engine-sdd.md) include
the storage estimate and possible future NFD improvements such as group unions
and compressed node-name ranges.
