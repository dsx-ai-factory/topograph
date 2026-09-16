# Install on Kubernetes

Topograph installs on a Kubernetes cluster via a Helm chart. This quickstart covers the Kubernetes-facing scheduler engines:

- **[`k8s` engine](#engine-k8s)** — labels Kubernetes nodes with topology keys so schedulers (native `podAffinity`, KAI Scheduler, Kueue TAS, etc.) can make topology-aware placement decisions
- **[`nfd` engine](#engine-nfd)** — publishes topology as Node Feature Discovery `NodeFeature` and `NodeFeatureGroup` custom resources for NFD-aware consumers
- **[`slinky` engine](#engine-slinky)** — writes Slurm topology configuration into a `ConfigMap` for [Slinky](https://github.com/SlinkyProject) (Slurm-on-Kubernetes) deployments

Prerequisites, install flow, and verification are common to all three engines —
they differ only in a few `engine.*` values and in what downstream artifact is
produced.

## Prerequisites

- **Kubernetes**: 1.27 or later
- **Helm**: 3.10+ or 4.x
- **`kubectl`** with permission to install a chart and create a `Namespace`
- **A supported provider** for your environment — see the [provider documentation](../providers/) for per-provider setup (credentials, required cluster state, etc.)
- **For the `nfd` engine only**: Node Feature Discovery installed with the
  Alpha `NodeFeatureGroupAPI` feature gate enabled. It is disabled by default;
  see the [NFD engine installation instructions](../engines/nfd.md#install-nfd).
- **For the `slinky` engine only**: a Slinky cluster already deployed in the target Kubernetes cluster — Topograph does not deploy Slinky itself

## Install

Base install command (pick the engine-specific flags from the two sections below):

```bash
helm repo add topograph https://dsx-ai-factory.github.io/topograph
helm repo update
helm install topograph topograph/topograph \
  --version <chart-version> \
  --namespace topograph --create-namespace \
  --set provider.name=<provider> \
  --set engine.name=<engine> \
  # ... engine-specific flags ...
```

Replace `<provider>` with one of the supported values (`aws`, `gcp`, `oci`,
`nebius`, `nscale`, `netq`, `infiniband-k8s`, `lambdai`). For Slinky
`topology/block` deployments only, `dra` is also supported when every
participating node already has a valid `nvidia.com/gpu.clique` label; see the
[DRA provider documentation](../providers/dra.md). To see available chart
versions, run `helm search repo topograph/topograph --versions`.

Provider-specific credentials and parameters are passed via Helm values. See the [chart README](https://github.com/dsx-ai-factory/topograph/blob/main/charts/topograph/README.md) and [`values.yaml`](https://github.com/dsx-ai-factory/topograph/blob/main/charts/topograph/values.yaml) for the full values shape, plus the example values files shipped in the chart directory.

## Verify

The `helm test` verification is the same regardless of engine:

```bash
helm test topograph --namespace topograph
```

The bundled test hooks probe `/healthz` and `/metrics` inside the cluster and expect HTTP 200 plus the `topograph_version` metric in the response body. A green result confirms the API server is running.

Engine-specific verification (seeing the actual topology output) differs — see each engine's section below.

## Engine: `k8s` <a name="engine-k8s"></a>

Engine-specific install flag:

```bash
  --set engine.name=k8s
```

Nothing else is required — the `k8s` engine labels nodes directly from the default chart deployment. A few seconds after install, topology labels appear on cluster nodes:

```bash
kubectl get nodes --show-labels | grep fabric.topograph.run
```

If labels are missing, inspect the Topograph logs:

```bash
kubectl logs -n topograph -l app.kubernetes.io/name=topograph
```

## Engine: `nfd` <a name="engine-nfd"></a>

Engine-specific install flag:

```bash
  --set engine.name=nfd \
  --set nfdNamespace=node-feature-discovery
```

The `nfd` engine creates `NodeFeature` and `NodeFeatureGroup` objects in the
`nfd.k8s-sigs.io/v1alpha1` API group. NFD owns the group status and populates
`NodeFeatureGroup.status.nodes` after evaluating the feature rules.

To inspect generated groups:

```bash
kubectl get nodefeaturegroups.nfd.k8s-sigs.io -n node-feature-discovery
```

Use this engine only for consumers that understand NFD groups. For native
Kubernetes pod affinity and topology keys, use `engine: k8s`.

## Engine: `slinky` <a name="engine-slinky"></a>

Engine-specific install flags point the `slinky` engine at the Slinky deployment:

```bash
  --set engine.name=slinky \
  --set engine.params.namespace=<slinky-namespace> \
  --set engine.params.topologyConfigmapName=<configmap-name> \
  --set engine.params.topologyConfigPath=topology.conf
```

The full engine-parameter shape (`podSelector`, `plugin`, `blockSizes`, per-partition topologies, …) is documented in the [chart README](https://github.com/dsx-ai-factory/topograph/blob/main/charts/topograph/README.md). Example values files for common Slinky scenarios ship in the chart directory:

- [`values.slinky.tree-example.yaml`](https://github.com/dsx-ai-factory/topograph/blob/main/charts/topograph/values.slinky.tree-example.yaml) — tree topology
- [`values.slinky.block-example.yaml`](https://github.com/dsx-ai-factory/topograph/blob/main/charts/topograph/values.slinky.block-example.yaml) — block topology
- [`values.slinky.partition-example.yaml`](https://github.com/dsx-ai-factory/topograph/blob/main/charts/topograph/values.slinky.partition-example.yaml) — per-partition topologies (Slurm 25.05+)

To confirm the topology was written to the target `ConfigMap`:

```bash
kubectl get configmap -n <slinky-namespace> <configmap-name> -o yaml
```

The key configured via `topologyConfigPath` (by default `topology.conf`) should contain the generated Slurm topology configuration.

## Where to go next

- **[Kubernetes engine reference](../engines/k8s.md)** — configuration, access patterns (`Ingress`, `HTTPRoute`, `NetworkPolicy`, `ServiceMonitor`), mixed workload considerations
- **[NFD engine reference](../engines/nfd.md)** — `NodeFeature` / `NodeFeatureGroup` output, requirements, and caveats
- **[Slinky engine reference](../engines/slinky.md)** — `slinky` engine parameters, `ConfigMap` annotations, tree / block / per-partition usage examples (the chart-level deployment surface is shared with the `k8s` engine and is documented under the Kubernetes engine reference above)
- **[Chart README](https://github.com/dsx-ai-factory/topograph/blob/main/charts/topograph/README.md)** — full values reference, `helm test` details, air-gapped environments, and component layout
- **[Node labels reference](../reference/node-labels.md)** — label key semantics, value behavior (FNV hashing for long values), integration with the NVIDIA GPU Operator, downstream consumer notes (relevant primarily to the `k8s` engine)
- **[Provider documentation](../providers/)** — per-provider prerequisites and configuration
- **[Config and API reference](../api.md)** — `topograph-config.yaml` schema, API endpoints, request/response shape
- **[Architecture](../architecture.md)** — how the API server, Node Observer, Node Data Broker, Provider, and Engine fit together
