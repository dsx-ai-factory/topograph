# Kubernetes Node Labels

The `k8s-node-labels` provider discovers network-fabric tiers from existing Kubernetes
Node labels. It needs no cloud credentials or fabric-management service. The
configured labels are authoritative; the provider does not infer missing topology.

## Prerequisites and credentials

Run Topograph in the cluster with a ServiceAccount allowed to list Nodes. The Helm
chart grants this permission when `provider.name` is `k8s-node-labels`. Keep the Node
Data Broker enabled so engines can resolve Node identity and region annotations.
Kubernetes client rate limits use the shared `KUBE_QPS` and `KUBE_BURST`
settings described in the deployment configuration.

## Parameters and Slinky example

`topologyLabels` is a required, nonempty list of distinct Kubernetes label keys,
ordered closest tier first. `nodeSelector` is an optional map selecting eligible
Nodes. A single label creates a single fabric tier; additional keys add ancestors.
`requireReady` defaults to `false`, retaining NotReady Nodes to avoid topology
churn during brief outages. `onMissingLabel` defaults to `skip`; set it to `fail`
to reject an incomplete topology rather than omit an affected Node.

```yaml
provider:
  name: k8s-node-labels
  params:
    topologyLabels:
      - example.com/rack
      - topology.kubernetes.io/zone
    nodeSelector:
      workload: slurm
engine:
  name: slinky
  params:
    namespace: slurm
    podSelector:
      matchLabels:
        app.kubernetes.io/component: slurmd
    plugin: topology/tree
    topologyConfigmapName: slurm-topology
    topologyConfigPath: topology.conf
nodeObserver:
  enabled: true
  topograph:
    trigger:
      podSelector:
        matchLabels:
          app.kubernetes.io/component: slurmd
```

These are Helm values. The provider returns the canonical graph; Slinky resolves
ready slurmd Pods and writes the ConfigMap using its existing translation path.
DRA behavior is unchanged. This provider discovers fabric tiers only and does not
interpret rack or zone labels as NVLink domains.

## Kubernetes output

Pair `provider.name: k8s-node-labels` with `engine.name: k8s` to translate
operator-managed rack and zone labels into Topograph's managed fabric-tier labels
for Kubernetes schedulers. The provider reads the source labels; the engine
writes the output labels. Configure source keys that differ from the engine's
managed output keys so discovery does not read back its own generated values.

```yaml
provider:
  name: k8s-node-labels
  params:
    topologyLabels: [example.com/rack, topology.kubernetes.io/zone]
engine:
  name: k8s
```

## Eligibility and validation

Only Nodes requested by the engine, matching the provider selector, and not being
deleted enter the graph. Set `requireReady: true` to also require `Ready=True`.
A cordoned Node may still run
workloads and remains eligible. An empty engine selection produces an empty graph.
The broker's instance annotation identifies a Node; without it the provider falls
back to the Kubernetes Node name. Slinky still needs the broker identity and region
annotations for its own resolution step.

Every included Node must have a nonempty, valid value for every configured label.
A missing or invalid value omits that Node with a warning, or fails generation
when `onMissingLabel: fail` is configured. If the engine requested Nodes but none
have usable topology, generation fails and preserves the last successful output.
With the `k8s` engine, a successful partial result clears managed output labels
on engine-selected Nodes omitted from the graph; excluded Nodes are not added
back as fabric vertices. An entirely unusable nonempty selection still fails
and preserves existing output.

An intentionally empty engine selection still produces an empty graph. Invalid
labels on excluded Nodes do not block generation. Every switch ID has a
`switch_` prefix and a digest of its full ancestor path, including the outermost
tier. IDs are checked against requested instance IDs to avoid collisions with
Node identity annotations. They are valid Kubernetes label values, distinguish
repeated rack labels across zones, and remain stable when unrelated Nodes join
or leave unless a new instance ID conflicts with an existing switch ID.

## Updates and verification

The Node Observer derives its Node selector and default watched topology labels
from the provider configuration. Set `trigger.nodeLabels` to override the watched
labels; other providers can use this field without a Node selector. Omitting
`trigger.nodeLabels` or setting it to `null` uses the provider defaults. An
explicit empty list disables the default label watch. An explicit,
nonempty `trigger.nodeSelector` must match
`provider.params.nodeSelector`. Additions, deletions, selector entry/exit, relevant
label changes and broker identity changes request regeneration. Readiness changes
request regeneration when `requireReady: true` or `trigger.nodeReadiness: true`
is configured.
Unrelated label changes and readiness heartbeats do not.

Inspect the resulting ConfigMap with:

```bash
kubectl -n slurm get configmap slurm-topology -o yaml
```

Changing a configured rack or zone label should move the corresponding Slurm node
within `topology.conf`. Deleting a Node removes its membership. A Node becoming
unready removes its membership only when `requireReady: true` is configured.
