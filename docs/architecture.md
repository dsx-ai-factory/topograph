# Architecture

Topograph consists of five major components:

1. **API Server**
2. **Node Observer**
3. **Node Data Broker**
4. **Provider**
5. **Engine**

<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="assets/topograph-arch-community-dark.png" />
    <img src="assets/topograph-arch-community-light.png" width="100%" alt="Topograph architecture diagram" />
  </picture>
</p>

## Design invariant

Providers differ per environment. The canonical `topology.Graph` is stable. Engines only translate; they never discover.

Three consequences follow, and they are the reason the component split is shaped the way it is:

- **Adding an environment means adding a provider.** A new CSP, a new fabric-management tool, or a new source of accelerator-domain identity is a new package under `pkg/providers/`, registered in `pkg/registry/registry.go`. It changes no engine and does not change the graph.
- **Adding an output format means adding an engine.** A new workload manager or file format is a new package under `pkg/engines/`, registered in the same file. Every provider's output is already translatable into it, because every provider produces the same graph.
- **Reading the fabric inside an engine is a design error**, and so is emitting scheduler-specific output from a provider. Either one couples one environment to one scheduler and destroys the "any provider with any engine" property that the split buys.

### The canonical graph

`topology.Graph` (in `pkg/topology/`) has three fields:

| Field | Meaning |
|---|---|
| `Tiers *Vertex` | Root of the switch hierarchy. Leaf vertices are compute nodes; interior vertices are switches. |
| `Domains DomainMap` | Accelerator/block domains mapped to their hosts. This is the source for `topology/block` output. |
| `Instances map[string]Instance` | Optional per-instance metadata keyed by instance ID. Engines that do not need instance-oriented output ignore it. |

`Vertex` is deliberately small: `Name` (the compute node name), `ID` (the CSP instance ID for a node, or the switch ID), and `Vertices` (its children). Because every provider returns this shape and every engine consumes it, a change to `Vertex` ripples through all of them, which is why `pkg/topology/` changes are discussed in an issue before they are written.

### The provider boundary

Providers implement one method, defined in `pkg/providers/providers.go`:

```go
type Provider interface {
    GenerateTopologyConfig(ctx context.Context, pageSize *int, instances []topology.ComputeInstances) (*topology.Graph, *httperr.Error)
}
```

Most providers assemble a `topology.ClusterTopology` and let it produce the graph. For each instance they populate:

- `InstanceTopology.FabricTiers`, closest-first, so index 0 is the switch nearest the compute node. The slice length is that instance's fabric depth. There is no fixed depth, and instances in one cluster may differ.
- `InstanceTopology.XclrDomainID`, the accelerator domain, when the environment has one.
- `InstanceTopology.XclrSubDomainID`, an optional sub-domain nested inside that domain.

`ClusterTopology.ToGraph` then builds the vertex forest and the domain map. Requested instances for which the provider returned no topology are collected under a single `no-topology` vertex, and each one raises the `topograph_missing_topology` gauge described under [Metrics](#metrics).

Network-fabric discovery and accelerator-domain discovery may be composed independently through `pkg/accelerator`. Providers that use this facility stay responsible for combining both dimensions into one graph; the engine never sees the two sources separately. Other providers may obtain both dimensions directly from their native topology source.

The return type is `*httperr.Error`, not `error`. The status code it carries becomes the status the API server reports for the whole request, so a provider chooses it deliberately. A plain `error` at this boundary throws that information away and is not accepted.

### The engine boundary

Engines implement two methods, defined in `pkg/engines/engines.go`: `ResolveComputeInstances`, which produces the instance-to-node mapping the provider needs, and `GenerateOutput`, which takes the finished `*topology.Graph` and returns bytes. `GenerateOutput` receives a graph and engine parameters. It receives no provider handle and no fabric source, which is the invariant enforced in code.

## Components

### 1. API Server

The API Server receives topology generation requests and returns results asynchronously. Requests are aggregated over a configurable delay window so that a burst of node changes (common during cluster scaling events) produces a single topology update rather than a storm.

### 2. Node Observer

The Node Observer is used in Kubernetes deployments. It is a controller that monitors configured node and pod changes, and it also watches the Topograph API pod. When the node-data-broker is enabled, it additionally watches the broker DaemonSet's lifecycle, desired replica count, and readiness. Relevant events enqueue one cluster-wide topology key in a rate-limited work queue, coalescing bursts into idempotent reconciliation. Reconciliation waits for the broker DaemonSet's ready replica count to match its desired count before asking the API Server to generate a new topology configuration, and it requeues failed requests.

### 3. Node Data Broker

The Node Data Broker is also used when Topograph is deployed in a Kubernetes cluster. It collects relevant node attributes and stores them as node annotations.

### 4. Provider

The Provider interfaces with CSPs or on-premises tools to retrieve topology-related data from the cluster and converts it into an internal representation. Providers may compose network-fabric discovery with an independent accelerator-domain source; for example, the InfiniBand provider can combine `ibnetdiscover` with `nvidia-smi`, a Kubernetes Node label, or no accelerator source.

### 5. Engine

The Engine translates this internal representation into the format expected by the workload manager.

## Workflow

- The API Server listens on the port and notifies the Provider about incoming requests. In Kubernetes, the incoming requests are sent by the Node Observer, which watches selected node/pod status and API-server readiness.
- The Provider receives notifications and invokes CSP API to retrieve topology-related information.
- The Engine converts the topology information into the format expected by the user cluster (e.g., SLURM or Kubernetes).

## API reference

Topograph is a Go module, `github.com/dsx-ai-factory/topograph`. Its exported API is documented as godoc and browsable on pkg.go.dev:

- <https://pkg.go.dev/github.com/dsx-ai-factory/topograph>

The packages an external caller is most likely to import are `pkg/topology` (the canonical `Graph`, `Vertex`, `DomainMap`, and the topology constants), `pkg/providers` (the `Provider` interface and the registry types), and `pkg/engines` (the `Engine` interface). Packages under `internal/` are not importable from outside the module by design.

The canonical types carry godoc comments: `Graph`, `Vertex`, `FabricTier`, `DomainMap`, and `BlockVertex` each document what they mean and how they are ordered, and those comments are what pkg.go.dev renders. Coverage is not yet uniform across the whole exported surface; several exported helpers in `pkg/topology` and `pkg/providers` still have none. Exported types and functions you add or change should carry a doc comment, because that comment is the published reference for them.

## Documentation and versioning

`docs/` is the source of truth for public documentation. It is published through Fern to <https://docs.nvidia.com/topograph>. `docs/index.yml` drives the sidebar and `fern/docs.yml` declares the site and its versions; `fern/` holds site configuration and theme assets only, never page content.

The site is versioned per release, and readers select a version from the site's version picker:

- `.github/workflows/publish-fern-docs.yml` publishes on a `vX.Y.Z` tag push, or on manual dispatch for a chosen tag.
- Each published version serves content frozen at its tag. The workflow extracts `docs/` from `refs/tags/vX.Y.Z` rather than from `main`, so the pages under a version match the source of that release. There is no "docs built from main" entry.
- On a release tag the workflow generates `fern/versions/vX.Y.Z.yml` from that tag's `docs/index.yml`, inserts the version into `fern/docs.yml` sorted by semver descending, prunes the registry to the three most recent versions, and opens a PR so the registry change lands on `main`.
- The newest version is displayed with a "Latest" prefix at publish time. That stamp is applied to the published site only and is not persisted into `fern/docs.yml`.
- Pre-release tags such as `v0.5.0-rc1` publish, but do not register a new version.

CI (`.github/workflows/fern-docs-ci.yml`) fails if any `docs/**/*.md` outside `docs/design/` is missing from `docs/index.yml`. `docs/design/` is a drafting area for work in progress and is not published.

## Operations

This section covers running Topograph as a service. It describes what the code actually exposes; the request and configuration schemas themselves are in [Config and API](./api.md), and the label keys are in [Node Labels and Annotations](./reference/node-labels.md).

### Endpoints

The API Server serves everything on one port (`http.port` in the config, `49021` in the sample config and in the Helm chart's `service.port`):

| Endpoint | Method | Purpose |
|---|---|---|
| `/healthz` | GET | Liveness and readiness. Returns `200` with body `OK`. |
| `/v1/generate` | POST | Submit a topology request. Returns `202` and the request ID immediately. |
| `/v1/topology?uid=<id>` | GET | Fetch a result: `200` when complete, `202` while in progress, `404` for an unknown or evicted ID, or the status the request failed with. |
| `/v1/lookup` | POST | Takes the same body as `/v1/generate` and returns the stored result for that body's hash, without queueing new work. |
| `/metrics` | GET | Prometheus exposition. |

`/healthz` answers as soon as the HTTP server is listening. It does not test provider reachability, credentials, or the last generation result, so a healthy pod tells you nothing about whether topology generation is working. Use the metrics and logs below for that.

The Helm chart wires `/healthz` into both `livenessProbe` and `readinessProbe` on the API Server Deployment. The node-data-broker DaemonSet serves its own `/healthz` on `nodeDataBroker.port` (`8080` by default) and uses it for its startup, liveness, and readiness probes. The broker applies its node annotations first and only then starts that endpoint, so a Ready broker pod means the annotations for that node have been written.

### Metrics

`pkg/metrics/` registers five Prometheus collectors, all under the `topograph` subsystem:

| Metric | Type | Labels | When it is emitted |
|---|---|---|---|
| `topograph_http_request_duration_seconds` | histogram | `method`, `path`, `proto`, `from`, `status` | Every HTTP request, from the logging middleware. Default buckets. |
| `topograph_request_duration_seconds` | histogram | `provider`, `engine`, `status` | Once per topology-generation attempt, and once for each request rejected while being read or validated. Buckets run from 1s to 30s; anything slower falls into `+Inf`. |
| `topograph_version` | gauge | `version` | Set to 1 on each HTTP request. Use it to confirm which build is running. |
| `topograph_missing_topology` | gauge | `provider`, `node` | Set to 1 for each requested node the provider returned no topology for. |
| `topograph_validation_error_total` | counter | `type` | Registered, but nothing in the tree increments it today, so no series appears. |

`topograph_request_duration_seconds` is the one to alert on. Split it by `status`: a rising non-`200` rate names the provider and the engine involved. Because the API Server retries a failed generation, one logical request can produce several observations.

Two caveats on `topograph_missing_topology`: it is only ever set, never cleared, so an existing series means "this node was missing at some point since the process started", not "this node is missing now". Restarting the API Server resets it. The same nodes appear in the generated topology under the `no-topology` vertex, which is the authoritative current view.

The chart ships an opt-in `ServiceMonitor` (`serviceMonitor.enabled`, scraping `/metrics` on the `http` port every 15s by default) and an opt-in `NetworkPolicy` that admits the scraper's namespace when `networkPolicy.metricsScraperNamespace` is set.

### Aggregation delay

`requestAggregationDelay` is required in the API Server config and has no built-in default; both `config/topograph-config.yaml` and the chart's `config.requestAggregationDelay` use `15s`. The API Server hashes each request body and holds it in a trailing-delay queue: submitting a request starts a timer for that hash, and submitting an identical request before the timer fires cancels and restarts it. Generation begins only when the delay elapses with no newer identical request.

When updates seem slow, this is usually why:

- The floor on end-to-end latency is `requestAggregationDelay` measured from the **last** identical request, plus the provider's own query time. With `15s`, a result is never available sooner than 15 seconds after node churn stops.
- Continuous churn starves the queue. If events arrive faster than the delay, the timer keeps restarting and nothing is produced. The signature in the logs is many `Submit request; delay processing by <delay>` lines with no matching `Processing request ID <hash>` line.
- The request hash covers only the provider name and parameters and the engine name and parameters. Requests that agree on those collapse into one, even when their node lists or credentials differ. The Node Observer always sends the same body (the provider and engine from its own config), which is what makes coalescing effective in Kubernetes.
- Results are kept in an LRU of the last 100 request IDs. Polling an older ID returns `404`.

### Where each engine writes its output

| Engine | Where the output goes |
|---|---|
| `slurm` | Writes the Slurm topology config to the path in the `topologyConfigPath` engine parameter and returns `OK`. With no path set it returns the generated text as the response body, so `GET /v1/topology` hands you the file. With `reconfigure: true` it then runs `scontrol reconfigure`. |
| `k8s` | Writes node labels directly through the Kubernetes API: `fabric.topograph.run/tier-N` closest-first, `accelerator.topograph.run/domain`, and `accelerator.topograph.run/sub-domain`. The `fabricLabels` parameter overrides the fabric tier keys and `acceleratorLabel` overrides the accelerator domain key; the sub-domain key is always `accelerator.topograph.run/sub-domain`. Returns `OK`. A failed label write surfaces as `502`. |
| `nfd` | Creates or updates NFD `NodeFeature` and `NodeFeatureGroup` objects in the namespace named by the `NFD_NAMESPACE` environment variable, which is required; the Helm chart sets it from `nfdNamespace` and rejects an `env.NFD_NAMESPACE` override. Returns `OK nodeFeatures=<n> nodeFeatureGroups=<m>`. With `cleanup` enabled it refuses to apply an empty result rather than deleting the existing topology. |
| `slinky` | Writes the Slurm topology into the ConfigMap named by `topologyConfigmapName` in `namespace`, under the key given by `topologyConfigPath`. `configUpdateMode: none` skips the ConfigMap write. With `useDynamicNodes: true`, it also reconciles the `topology.slinky.slurm.net/spec` annotation on Kubernetes nodes, including when the ConfigMap write is skipped. |
| `graph` | Returns instance-oriented JSON as the response body, or writes it to `topologyConfigPath` and returns `OK`. |

An engine that returns `OK` has already applied its side effect. The body is a completion marker, not the topology.

### When topology is not regenerating

In a Kubernetes deployment, work through these in order. Each step names the log line or signal that confirms or rules it out.

1. **Is the Node Observer past its startup gate?** On start it polls the API Server's `/healthz` every 2 seconds for up to 1 minute. It logs `Waiting for topograph to start at <url>` until it succeeds, then `Topograph API is ready at <url>`. If the API never answers, it returns `topograph API not ready at <url> after <elapsed>` and the process exits non-zero, so the pod restarts. An observer in CrashLoopBackOff with that message points at the API Server Service or pod, not at the provider.

2. **Is the node-data-broker DaemonSet ready?** When `NODE_DATA_BROKER_NAME` and `NODE_DATA_BROKER_NAMESPACE` are set, the observer will not request a topology until the DaemonSet's `status.numberReady` equals `status.desiredNumberScheduled`. While it waits it requeues every 10 seconds and logs, at `-v=2`, `Waiting for the node-data-broker DaemonSet to become ready before topology generation`. A DaemonSet with zero desired replicas logs `node-data-broker DaemonSet <ns>/<name> has 0 desired replicas; check its node selector, affinity, and tolerations`, which names the fix. A broker pod that never becomes Ready failed to collect or write its node annotations, and its own logs carry the provider error.

3. **Does anything trigger the observer at all?** It watches only what its config selects: `trigger.nodeSelector`, `trigger.podSelector`, and `apiServer.podSelector`. Node adds and deletes, trigger-pod readiness transitions, API Server pod readiness and container restarts, and broker DaemonSet readiness or desired-count changes each enqueue one cluster-wide key. If no selector matches anything, nothing is ever enqueued and the topology never regenerates. At `-v=4` every accepted event logs an `Informer added/updated/deleted ...` line, so an empty log at that verbosity means the selectors are the problem.

4. **Are generation requests failing to reach the API Server?** A failed POST logs `Topology reconciliation failed: failed to send topology generation request: <err>` and is requeued through an exponential rate limiter seeded with `retryDelay` (5 minutes by default). Note that the observer only ever sees the `202` handshake: `/v1/generate` returns before the provider runs, so a provider failure never appears in observer logs.

5. **Is generation itself failing?** That is visible only on the API Server. The queue logs `HTTP <code>: <message>` when a request completes with an error, and `topograph_request_duration_seconds` carries the same status alongside the provider and engine labels. Missing or expired credentials, upstream API quota, and unreachable fabric tooling all land here.

6. **Are only some nodes missing?** Check `topograph_missing_topology` and the `no-topology` vertex in the generated output. Those are nodes the request asked about that the provider returned no topology for, which usually means the instance is outside the queried scope (region, cluster, or selector) rather than that the provider failed.

### Reading a failed provider call

The provider interface returns `*httperr.Error` rather than `error` so that the status the provider chose propagates intact. That code becomes three things: the HTTP status stored with the queued request and returned by `GET /v1/topology?uid=<id>`, the `status` label on `topograph_request_duration_seconds`, and the input to the retry decision.

The API Server makes up to five generation attempts: one initial attempt and four retries. It retries only status codes `408`, `429`, `500`, `502`, `503`, and `504`, waiting 2s, 4s, 8s, and 16s between attempts. Other status codes fail immediately. Before each retry, it logs `Attempt <n> failed with error: <err>. Retrying in <wait>`.

These generation retries do not honor `Retry-After` because the API Server does not receive the upstream response headers. Provider HTTP calls made through `internal/httpreq.DoRequestWithRetries` do honor `Retry-After`, given as seconds or an HTTP date, up to a maximum of 5 minutes. Without a valid header, provider HTTP retries wait 500ms, 1s, 2s, and 4s. If the provider still fails, the API Server may retry the whole generation, so the two retry loops can compound.

What the statuses mean in practice:

| Status | Typical cause |
|---|---|
| `400` | Malformed request body, an unknown provider or engine name, or invalid provider/engine parameters. Rejected before any upstream call, and not retried. |
| `401` | Provider authentication failed. In-tree examples: the NetQ login, OCI API authentication, and Nebius SDK construction. Not retried; fix the credentials. |
| `500` | A local failure, such as writing the output file or generating the config. Retried. |
| `502` | An upstream call failed. A transport-level failure in `internal/httpreq` maps to `502`, as does a failed Kubernetes write in the `k8s` and `nfd` engines. Retried. |
| `404` | From `/v1/topology` when the request ID is unknown or has been evicted from the 100-entry result LRU, and from `/v1/lookup` when the request body's hash is unknown or evicted. |

Because a request that exhausts its retries stores the final error against its hash, `GET /v1/topology?uid=<id>` (or a `/v1/lookup` with the same body) returns the provider's own message. That message, not the status alone, is what identifies the failing call.
