# topograph

A Helm chart for deploying [topograph](https://github.com/dsx-ai-factory/topograph) on Kubernetes.

Topograph discovers the physical network topology of a cluster (NVLink domains, InfiniBand / Ethernet switch fabric, cloud rack topology) and exposes it to workload schedulers — Slurm, Kubernetes, and Slurm-on-Kubernetes (Slinky) — by applying node labels and/or writing scheduler-specific topology configuration.

## Prerequisites

- **Helm**: 3.10+ or 4.x. The chart has been verified against Helm 3.20.0 and Helm 4.1.4, with byte-identical `helm template` output under both.
- **Kubernetes**: 1.27 or later
- **Provider-specific prerequisites**: see the [provider documentation](https://github.com/dsx-ai-factory/topograph/tree/main/docs/providers) in the main repository for each provider's setup.

## Installation

From the Helm chart repository:

```bash
helm repo add topograph https://dsx-ai-factory.github.io/topograph
helm repo update
helm install topograph \
  topograph/topograph \
  --version <chart-version> \
  --namespace topograph --create-namespace
```

From a local source checkout:

```bash
helm install topograph charts/topograph \
  --namespace topograph --create-namespace
```

To see available chart versions:

```bash
helm search repo topograph/topograph --versions
```

## Configuration

The default `values.yaml` ships the `test` provider + `k8s` engine, suitable for a smoke-test install. Production installs select a real provider:

```yaml
provider:
  name: test       # or aws, gcp, oci, nebius, netq, infiniband-k8s, ...
engine:
  name: k8s        # or nfd, slurm, slinky, graph
```

The DRA provider and the Kubernetes, NFD, and Slinky engines can share
deployment-level Kubernetes client rate limits:

```yaml
kubeClient:
  qps: 50
  burst: 100
```

Configure these limits through `kubeClient`; the chart passes them to the
Topograph deployment as `KUBE_QPS` and `KUBE_BURST`.

For the full list of values and their defaults, see [`values.yaml`](./values.yaml). Example values files for specific deployment patterns:

- [`values.k8s.ib-example.yaml`](./values.k8s.ib-example.yaml) — InfiniBand provider on Kubernetes
- [`values.k8s.gcp-service-account-example.yaml`](./values.k8s.gcp-service-account-example.yaml) — GCP provider with a service account key mounted from a Secret
- [`values.k8s.gcp-federated-workload-identity-example.yaml`](./values.k8s.gcp-federated-workload-identity-example.yaml) — GCP provider using Workload Identity Federation
- [`values.k8s.gateway-api-example.yaml`](./values.k8s.gateway-api-example.yaml) — exposing the Topograph API via Gateway API (`HTTPRoute`) instead of `Ingress`
- [`values.slinky.tree-example.yaml`](./values.slinky.tree-example.yaml), [`values.slinky.block-example.yaml`](./values.slinky.block-example.yaml), [`values.slinky.partition-example.yaml`](./values.slinky.partition-example.yaml) — Slinky engine variants

To use an existing Kubernetes ServiceAccount while still letting the chart create the required RBAC, disable only ServiceAccount creation and provide the existing account name:

```yaml
serviceAccount:
  create: false
  name: existing-topograph-sa
rbac:
  create: true
```

Set `rbac.create=false` only when ClusterRoles and ClusterRoleBindings are managed outside the chart.
The same requirement applies independently to `nodeObserver.serviceAccount` and
`nodeDataBroker.serviceAccount`: when `create=false`, `name` must identify the
existing account. To intentionally bind permissions to the namespace's default
ServiceAccount, set `name: default` explicitly.

### Values validation

The chart ships a [`values.schema.json`](./values.schema.json) that validates the most error-prone fields at install time — the `provider.name` and `engine.name` enums, type and range constraints on `kubeClient`, `replicaCount`, `image.pullPolicy`, `service.type`, `service.port`, and `verbosity`, and the expected shapes of `serviceAccount`, `rbac`, `ingress`, `serviceMonitor`, and related nested objects. Invalid values are rejected by `helm install` and `helm template` with a clear schema-validation error.

The schema is deliberately narrow: per-provider credential requirements (which fields a given provider needs in its credentials map) are documented in prose in `docs/providers/<name>.md` rather than enforced in the schema, because the credential field sets evolve with upstream provider changes and are hard to keep accurate in a schema.

## Testing

Once installed, verify the deployment is functional with `helm test`:

```bash
helm test topograph --namespace topograph
```

The chart ships two `helm test` hook pods:

- **`test-healthz`** — probes the Topograph API's `/healthz` endpoint via the in-cluster Service and expects HTTP 200
- **`test-metrics`** — probes `/metrics`, expects HTTP 200 plus the `topograph_version` Prometheus metric in the response body (sanity check that the Prometheus registry is wired up)

Both test pods are removed automatically on success (`helm.sh/hook-delete-policy: before-hook-creation,hook-succeeded`). On failure the pod persists so operators can inspect logs; the next `helm test` invocation replaces it.

### Air-gapped environments

By default, the test pods reuse the main topograph image. Topograph's default image is Alpine-based and ships with busybox `wget`, which the test probes use — so `helm test` works without pulling any additional image, including in air-gapped environments where only mirrored images are reachable.

If your mirrored image lacks busybox `wget`, override the test image to point at one that does, via `tests.image.repository` and `tests.image.tag`. You can also disable the tests entirely with `tests.enabled=false`.

## Components

The main chart directly manages all three runtime workloads:

- **Topograph API server** — serves topology generation and retrieval requests
- **`node-data-broker`** — DaemonSet that collects per-node attributes (NVLink clique IDs, etc.) as node annotations for the Kubernetes engine
- **`node-observer`** — watches configured node/pod changes and Topograph API readiness, waits for desired node-data-broker replicas when the broker is enabled, then triggers topology regeneration

The component templates live under `templates/nodeDataBroker` and `templates/nodeObserver`. Their settings use the top-level `nodeDataBroker` and `nodeObserver` values keys; the broker is enabled by default.

The API server, node-observer, and node-data-broker containers all support `env`, `initContainers`, and `lifecycle` overrides for deployment-specific integration hooks.

## References

- **Project documentation site**: <https://topograph.docs.buildwithfern.com/topograph>
- **Main repository**: <https://github.com/dsx-ai-factory/topograph>
- **Provider-specific setup**: `docs/providers/` in the main repository
- **Engine documentation**: `docs/engines/k8s.md`, `docs/engines/nfd.md`, `docs/engines/slinky.md`, `docs/engines/slurm.md`, `docs/engines/graph.md`
- **Node-labels reference**: `docs/reference/node-labels.md`
- **Contributing**: see [`CONTRIBUTING.md`](https://github.com/dsx-ai-factory/topograph/blob/main/CONTRIBUTING.md) in the main repository

## License

Apache License 2.0. See [`LICENSE`](https://github.com/dsx-ai-factory/topograph/blob/main/LICENSE) in the main repository.
