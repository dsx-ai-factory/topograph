# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

## [Unreleased]

### Changed

- **BREAKING:** The Go module and canonical repository moved from `github.com/NVIDIA/topograph` to `github.com/dsx-ai-factory/topograph`. Repository links, published container and Helm chart locations, build metadata, and examples now use the `dsx-ai-factory` organization.
- Go toolchain upgraded from **1.26.6** to **1.27.1** across the module, container build, and CI workflows; the CI linter is upgraded to `golangci-lint` **v2.13.2** for Go 1.27 compatibility.
- The `nscale` provider's Slurm auto-discovery runs `pdsh` across the current Slurm node list and queries each node's own Instance Metadata Service (IMDS) for its server ID (`serverID`) and region, merging the results into the instance-to-node and node-to-region maps.
- The `nscale` provider's Radar API topology response field is now read as `server_id` instead of `instance_id`, matching the IMDS `serverID` field it is merged with.
- The `nscale` provider's `region` credential, when set, now restricts Slurm auto-discovery to nodes whose IMDS region (`regionID`) matches it; nodes with a different or missing IMDS region are excluded from the topology query and logged as a warning.

### Removed

- `nscale` provider `instanceApiUrl` parameter and its instance API client — instance and region discovery for Slurm auto-discovery now comes from each node's IMDS instead.

### Added

- SHA-256 checksum files published beside Helm chart packages and included
  with their corresponding GitHub release assets.
- Tag-driven official releases that publish the Helm chart, checksum, build
  provenance, and GitHub release from the canonical release commit.
- Signed SLSA build provenance for published container images and Helm chart
  packages. Release workflows bind each artifact to its source commit and
  workflow using GitHub artifact attestations; container provenance is also
  published to GHCR alongside the image.
- Crusoe provider now derives accelerator domains from `nvidia.com/gpu.clique`, so `topology/block` renders one Slurm block per distinct clique value on rack-scale NVLink systems. The domain is the clique verbatim, matching how the InfiniBand and Lambda AI providers publish theirs. That value is the NVL Partition, which can be finer than the physical NVL Domain when one rack is split into several cliques. The InfiniBand partition is deliberately not used as a fallback domain: a partition can span many racks while a clique cannot, so keying blocks on the partition would let Slurm spread one job across racks and fall back to InfiniBand. Nodes without the clique label get no accelerator domain and are scheduled by the switch tree alone, leaving existing GPU classes unchanged.
- Crusoe provider (`crusoe`) and its simulation variant (`crusoe-sim`) discover the InfiniBand switch fabric on Crusoe Cloud from the `crusoe.ai/ib.partition.id` and `crusoe.ai/pod.id` Node labels the Crusoe control plane publishes. Crusoe compute is virtualized, so a guest VM cannot run `ibnetdiscover` and there is no per-node metadata service for switch identity. Fabric tiers are the rail-optimized pod, the InfiniBand partition, and a synthetic root above them. Nodes without those labels are placed under placeholder tiers beneath the same root, so one Slurm tree spans a heterogeneous cluster. Use it with `topology/tree`.
- Shared optional provider parameter `imdsUrl` (`providers.GetIMDSURL`, `topology.KeyIMDSURL`) for overriding a provider's default Instance Metadata Service URL. Only the `nscale` provider consumes it so far, falling back to its built-in IMDS URL when unset.
- README badges replaced with a consistent flat-square Shields.io row: Docs, Go CI, Coverage, Chart Tests, K8s Tests, Release, and License — each linked to its source and using a shared dark label color aligned with the Topograph logo palette.
- Documentation diagrams for architecture, Kubernetes, Slinky, Slurm topology formats, and engine outputs now ship as community-variant SVG and PNG assets with automatic dark/light mode switching (`<picture>` / `prefers-color-scheme` in docs; `#gh-light-mode-only` / `#gh-dark-mode-only` in README).
- Topograph logo variants for light mode (`topograph-logo-color`), dark mode (`topograph-logo-color-dark`), reversed black, and reversed white added to `docs/assets/`. The `README.md` logo now switches between color and color-dark automatically based on the viewer's GitHub theme preference.
- Fern sidebar now includes a direct link to `CHANGELOG.md` on GitHub under the Reference section, so release notes are discoverable from the docs site without navigating the repository.
- CI check (`fern-docs-ci.yml`) verifies that every `.md` file under `docs/` (outside `docs/design/`) is listed in `docs/index.yml`, preventing orphaned pages from being authored but never published.

### Fixed

- The `Go` workflow now also runs on pushes to `main`, so Codecov receives a coverage report for every merged commit. Merges are squashed, so the SHA that lands on `main` no longer matches the `pull-request/<n>` SHA that uploaded coverage; without a `main` trigger, Codecov's `main` branch had gone stale since 2026-07-06 and the README coverage badge reported a figure roughly six points below actual coverage.
- `nscale` provider's Slurm auto-discovery now issues a single `pdsh` sweep to fetch IMDS metadata for the node list, instead of running it twice (once each for `Instances2NodeMap` and `GetInstancesRegions`).
- `node-data-broker`'s per-node self-annotation for the `nscale` provider now honors the configured `imdsUrl` parameter instead of always querying the default IMDS endpoint, matching the Slurm auto-discovery path's behavior.
- `nscale` provider's IMDS `pdsh` command no longer masks a failed `curl` behind `echo`'s always-zero exit status; a node whose IMDS request fails now reports that failure to `pdsh` instead of producing an empty response that silently drops the node.
- GCP and OCI simulation providers now handle partial final pagination pages without indexing past the available instances.
- Non-positive `pageSize` configuration values now emit a warning and use the provider default instead of reaching provider APIs and simulation pagination loops.
- The node observer now regenerates topology for every deletion its informers report. Deletions that happen while a watch is disconnected arrive as `cache.DeletedFinalStateUnknown` tombstones, which carry a nil object when the resource has already left the informer store. The node, pod, API server, and node-data-broker delete handlers all required the tombstone to unwrap to the watched type, so those deletions were dropped and the topology stayed stale until an unrelated event arrived.

### Security

- Main Topograph API server ClusterRole rules are gated by the selected engine and provider: `nodes`, `pods`, `daemonsets`, and `configmaps` permissions render only when the engine or provider reaches the Kubernetes API, and the ClusterRole and ClusterRoleBinding are omitted entirely for non-Kubernetes combinations such as the `test` provider with the `slurm` engine.
- Node RBAC permissions in the main Topograph API server ClusterRole are collapsed into a single rule following least privilege: the Kubernetes engine receives Node `[get, list, patch]`, the dynamic Slinky engine (`useDynamicNodes: true`) receives Node `[list, patch]`, and other node-consuming configurations receive Node `[list]` without unnecessary `patch`, `get`, or `update` access.

---

## [v1.0.0] - 2026-08-18

### Added

- The DRA provider now accepts the shared `provider.params.accelerator` Kubernetes-label configuration used by `infiniband-k8s`, allowing a custom Node label to supply accelerator domains while preserving `nvidia.com/gpu.clique` as the default when the section is omitted.
- Pluggable accelerator-domain discovery for InfiniBand providers, independently selectable from fabric discovery with `nvidia-smi`, an explicitly configured Kubernetes Node label, or no accelerator source. Discovery is disabled when `accelerator` is omitted or empty; a non-empty section must set `source` explicitly. Helm defaults the `nvidia-smi` workload location to the `gpu-operator` namespace and `nvidia-device-plugin-daemonset` DaemonSet when those values are omitted.
- Helm `kubeClient.qps` and `kubeClient.burst` values for tuning the DRA provider and the Kubernetes, NFD, and Slinky engine clients through deployment-level `KUBE_QPS` and `KUBE_BURST` settings. Blank values are treated as unset, surrounding whitespace is accepted, and invalid values fail loading with HTTP 400.
- Accelerator sub-domain support across the canonical graph and the Kubernetes, NFD, and graph engines, including the exported `topology.KeyTopologyXclrSubDomain` constant for the `accelerator.topograph.run/sub-domain` label key.
- Slurm and Slinky block topology configurations can set `blockName.nodeNameRegexp` and `blockName.format` to derive unique block names from site-specific node naming conventions.
- `govulncheck` job in the Go CI workflow for symbol-level vulnerability scanning on pull requests.
- **NFD engine** (`engine: nfd`) that publishes Topograph topology as NFD `NodeFeature` and `NodeFeatureGroup` custom resources, including the `system.name/nodename` attribute needed to populate group status. Cleanup preserves the last published topology when generation produces no objects.
- OCI provider rack-aware accelerator domains ([#429](https://github.com/dsx-ai-factory/topograph/pull/429)).
- OCI image labels and Helm chart metadata for documentation, authorship, maintainers, discovery, and Artifact Hub ([#377](https://github.com/dsx-ai-factory/topograph/pull/377)).
- Helm `env`, `initContainers`, and `lifecycle` overrides across the API server, node-observer, and node-data-broker containers.
- Lambda provider Kubernetes integration, including node-data-broker discovery from Node provider IDs and regions and workload identity through `lambda-pod-identity-webhook` for short-lived API credentials ([#375](https://github.com/dsx-ai-factory/topograph/pull/375)).
- `kwok-nodes` utility and interactive Kubernetes/KWOK demos for rendering model-derived Node manifests and exercising the test, DRA, OCI simulation, and NFD configurations locally. Generated metadata preserves required KWOK selectors and maps model hostnames to Topograph instance IDs.
- Opt-in Helm `NetworkPolicy` covering all three components — API server, node-observer, node-data-broker (`networkPolicy.enabled`, default `false`): denies ingress except intra-release traffic (so the node-observer can reach the API server) and, when `networkPolicy.metricsScraperNamespace` is set, the Prometheus scraper namespace (set this to the namespace Prometheus runs in, which may differ from `serviceMonitor.namespace`). Egress stays unconstrained until `networkPolicy.extraEgress` is set, at which point an `Egress` policyType is added with DNS (scoped to the kube-dns pods, overridable via `networkPolicy.dnsPodSelector` for CoreDNS deployments with a non-standard label) and intra-release allows plus the supplied rules (which must include the kube-apiserver and provider endpoint under default-deny egress); `networkPolicy.extraIngress` appends custom ingress rules.

### Changed

- **BREAKING:** Fabric topology now uses variable, closest-first tiers labeled `fabric.topograph.run/tier-N`, while accelerator topology uses `accelerator.topograph.run/domain` and optional `accelerator.topograph.run/sub-domain`. `InstanceTopology.FabricTiers` and graph conversion support arbitrary fabric depth, and accelerator domains populate `Graph.Domains`. The fixed `leaf`, `spine`, and `core` keys and process-wide Helm/CLI label overrides are replaced by optional `fabricLabels` and `acceleratorLabel` parameters on the Kubernetes engine. All other Topograph-owned Kubernetes labels and annotations now use the `topograph.run` domain; consumers must update topology keys, selectors, allowlists, scheduling policies, and metadata lookups.
- **BREAKING:** The Kubernetes, NFD, and Slinky engines now use the optional `acceleratorDomainSourceLabel` parameter to select an existing Kubernetes Node label as the authoritative accelerator-domain source. There is no default source label, so `nvidia.com/gpu.clique` is no longer implicitly authoritative. The Slinky `useGpuCliqueLabel` parameter has been removed. The k8s engine does not allow `acceleratorLabel` and `acceleratorDomainSourceLabel` to be configured together.
- InfiniBand providers now query NVL partition IDs with the `nvidia-smi` CSV query interface, merge identical per-GPU rows, reject unavailable (`N/A`) fields, and normalize the result to `ClusterUUID.CliqueId`.
- The node-observer now processes its topology-generation triggers through a client-go rate-limiting work queue, coalescing event bursts into a single cluster-wide reconciliation while preserving trigger and retry behavior. It remains active after informer startup and retriggers generation when an API-server pod is deleted.
- **BREAKING:** The public Go API now represents accelerator locality with `XclrDomain` and optional `XclrSubDomain` names: `InstanceTopology` uses `XclrDomainID` and `XclrSubDomainID`, `k8s.TopologyLabelKeys` exposes corresponding fields and keys, and `topology.KeyTopologyAccelerator` is renamed to `topology.KeyTopologyXclrDomain`.
- The Slurm topology-update trigger script now accepts AWS, GCP, OCI, Nebius, NetQ, Nscale, Lambda, and bare-metal InfiniBand providers.
- Simulation models now declare compute nodes through `blocks[].nodes`, define inherited accelerator topology through `switches[].annotations` and `blocks[].annotations`, and treat node names as hostnames while generating `i-`-prefixed instance IDs. Empty leaf-switch definitions may be omitted and are created from references in the parent hierarchy. The older separate `nodes` and `capacity_blocks` sections have been removed ([#394](https://github.com/dsx-ai-factory/topograph/pull/394)).
- The node-observer and node-data-broker are now rendered directly by the main Topograph Helm chart instead of local subcharts. Their existing `node-observer.*` and `node-data-broker.*` values paths are unchanged.
- **BREAKING:** The Helm chart now ships a hardened default security context across the API server, node-observer, and node-data-broker: non-root (`runAsNonRoot`, UID/GID `65532`), `seccompProfile: RuntimeDefault`, `allowPrivilegeEscalation: false`, `readOnlyRootFilesystem: true`, and all capabilities dropped. Operators who relied on root, a writable root filesystem, or added capabilities must override the relevant keys (see the migration note below).
- Go toolchain bumped to **1.26.6** (`go.mod`, `Dockerfile`, CI) to address reachable stdlib vulnerabilities reported by `govulncheck`.
- Slinky partition discovery now prefers the Slinky controller pod and falls back to a login pod, so clusters without optional login pods can still discover partitions ([#362](https://github.com/dsx-ai-factory/topograph/pull/362)).
- Slinky node resolution and label-backed block-domain generation now emit actionable diagnostics for missing Ready Slurm pod mappings, missing source labels or instance annotations, and empty Kubernetes node selections, including the relevant selectors, metadata checks, skip counts, and node names.

### Fixed

- Block topology generation now rejects an empty accelerator-domain set instead of leaking the internal `-1` minimum-size sentinel as `BlockSizes=-1` when automatic block-size inference is enabled.
- AWS provider authentication now uses the standard SDK credential chain when explicit credentials are absent, enabling EKS Pod Identity and IRSA with automatic temporary-credential refresh instead of forcing EC2 instance-role credentials.
- Kubernetes engine label reconciliation and Slinky dynamic-node reconciliation now reuse listed Nodes, skip unchanged metadata without per-node GETs, and patch only changed topology values, substantially reducing client-side throttling on large clusters.
- Corrected DRA provider guidance to document its Slinky-only block-topology scope, dependency on pre-existing `nvidia.com/gpu.clique` labels, and inability to guide placement across NVLink partitions without backend-fabric topology.
- The DRA provider now matches nodes using the `topograph.run/instance` annotation instead of assuming the instance ID equals the Kubernetes node name.
- The node-observer now discovers an optional node-data-broker through `NODE_DATA_BROKER_NAME` and `NODE_DATA_BROKER_NAMESPACE`, gates topology generation on its desired and ready replica counts, and reports an actionable error when an enabled broker has zero desired replicas. Helm injects these variables only when `nodeDataBroker.enabled=true`.
- Helm node-observer now targets the rendered Topograph Service fullname in `generateTopologyUrl`.
- Lambda provider client now matches the Lambda topology API request and response contract: required `region` query parameter, `{data, page_token}` envelope, `page_token` pagination, and `networkPath` object mapping ([#374](https://github.com/dsx-ai-factory/topograph/pull/374)).
- OCI provider pagination now detects repeated page tokens and avoids looping over duplicate result pages ([#464](https://github.com/dsx-ai-factory/topograph/pull/464)).
- Slinky engine now skips pods without a resolvable Slurm node name instead of adding an empty instance-to-node mapping ([#380](https://github.com/dsx-ai-factory/topograph/pull/380)).

### Removed

- Periodic node annotation refreshes, including the `--refresh-interval` broker flag and `nodeDataBroker.refreshInterval` Helm value. The broker now applies annotations once at startup.

### Security

- Helm now requires an explicit ServiceAccount name when creation is disabled for Topograph, node-observer, or node-data-broker, preventing cluster-scoped RBAC from being silently bound to the namespace's default ServiceAccount.
- Opt-in `ValidatingAdmissionPolicy` and binding to restrict the `node-data-broker` ServiceAccount to only updating its host Node resource, mitigating privilege escalation via node spec or annotation manipulation (audit F1 compliance).
- RBAC permissions now follow least privilege: unused verbs were removed from the API server, node-data-broker, and node-observer ClusterRoles; the Kubernetes and Slinky engines receive Node `patch` without Node `update`; and node-observer Node list/watch access is granted only when `trigger.nodeSelector` is configured.
- Upgraded `golang.org/x/text` from `v0.38.0` to `v0.39.0` to address a dependency vulnerability.
- NFD engine permissions for `NodeFeature` and `NodeFeatureGroup` resources are scoped to the deployment-level `nfdNamespace`. Helm configures the namespace at runtime through `NFD_NAMESPACE`, and the engine rejects a missing or blank value.

### Migration (Helm — hardened security context)

The chart's hardened defaults are a breaking change for two deployment shapes; override only the affected keys/component:

| If you run | Override |
|--------|----------|
| `infiniband-k8s` (broker reads `/sys/class`) | A **complete** privileged override on `node-data-broker` — `securityContext: { privileged: true, allowPrivilegeEscalation: true, readOnlyRootFilesystem: false, runAsNonRoot: false, runAsUser: 0 }` plus `podSecurityContext.runAsNonRoot: false`. A partial override (only `privileged: true`) is rejected at admission because the default `allowPrivilegeEscalation: false` remains. Both shipped IB examples (`values.k8s.ib-example.yaml` and `values.slinky.ib.block-example.yaml`) are updated to the complete form. |
| `engine: slurm` or `engine: graph` in-cluster (writes `topology.conf`) | `securityContext.readOnlyRootFilesystem: false` and a writable volume at the configured output path. |

The default `k8s`/`slinky` engines and all other providers need no change.

[Full changelog](https://github.com/dsx-ai-factory/topograph/compare/v0.5.0...v1.0.0)

---

## [0.5.0] - 2026-06-30

### Added

- **Graph engine** (`engine: graph`) for canonical topology graph output ([#314](https://github.com/dsx-ai-factory/topograph/pull/314)).
- Helm **`namespace`** value to install all chart resources into a namespace other than the release namespace ([#345](https://github.com/dsx-ai-factory/topograph/pull/345)).
- Helm **ConfigMap mounts** for the node-data-broker DaemonSet (`node-data-broker.configMapMounts`) ([#347](https://github.com/dsx-ai-factory/topograph/pull/347)).
- Deployment **checksum annotation** so Topograph rolls when its ConfigMap changes ([#346](https://github.com/dsx-ai-factory/topograph/pull/346)).
- **Empty block complementing** for block-topology output ([#343](https://github.com/dsx-ai-factory/topograph/pull/343)).
- **Helm chart tests** (`make chart-test`) using helm-unittest ([#336](https://github.com/dsx-ai-factory/topograph/pull/336), [#361](https://github.com/dsx-ai-factory/topograph/pull/361)).
- Downstream packaging knobs for deb/rpm builds ([#333](https://github.com/dsx-ai-factory/topograph/pull/333)).
- Fern docs **global NVIDIA theme** adoption ([#339](https://github.com/dsx-ai-factory/topograph/pull/339)).
- Nscale provider documentation ([#326](https://github.com/dsx-ai-factory/topograph/pull/326)).

- **node-data-broker** runs as the DaemonSet main container instead of an init container plus a `curlimages/curl` placeholder ([#368](https://github.com/dsx-ai-factory/topograph/pull/368)). The `node-data-broker-initc` binary applies node annotations at startup, serves `/healthz`, and stays running until the pod receives SIGTERM.
- New `node-data-broker.port` Helm value (default `8080`) for the broker health HTTP server.
- New `node-data-broker.refreshInterval` Helm value (default `5m`) to re-apply node annotations periodically after startup. Set to `0` to disable periodic refresh.
- New `node-data-broker.startupProbe` settings (default `failureThreshold: 30`, `periodSeconds: 10`, i.e. a 5-minute startup budget) so slow providers such as InfiniBand `ibnetdiscover` can finish before liveness/readiness probes take effect.
- Startup, liveness, and readiness probes on the node-data-broker container, all targeting `/healthz`.
- New CLI flags on `node-data-broker-initc`: `--port` and `--refresh-interval`.

### Changed

- **Slinky engine**: `nvidia.com/gpu.clique` can override provider accelerator domains when present on a node ([#342](https://github.com/dsx-ai-factory/topograph/pull/342)).
- **Kubernetes engine**: prefer the GPU clique label for accelerator domains when configured ([#341](https://github.com/dsx-ai-factory/topograph/pull/341)).
- **Node observer** watches the Topograph API pod and triggers topology regeneration when the API becomes Ready after startup or a container restart ([#367](https://github.com/dsx-ai-factory/topograph/pull/367)).
- Helm image tags default to the chart **`appVersion`** when unset ([#360](https://github.com/dsx-ai-factory/topograph/pull/360)).
- Providers reuse the shared retrying HTTP helper; string-map config parsing replaced with mapstructure ([#356](https://github.com/dsx-ai-factory/topograph/pull/356), [#355](https://github.com/dsx-ai-factory/topograph/pull/355)).
- Node attribute handling simplified in the canonical graph ([#349](https://github.com/dsx-ai-factory/topograph/pull/349)).
- Helm install docs updated to use the chart repository ([#357](https://github.com/dsx-ai-factory/topograph/pull/357)).

- The node-data-broker DaemonSet image defaults to `ghcr.io/dsx-ai-factory/topograph` instead of `curlimages/curl`.
- `node-data-broker-initc` reuses a single in-cluster Kubernetes clientset for the initial apply and all periodic refreshes.
- InfiniBand provider documentation updated for the flattened Helm values layout.

### Fixed

- **Nebius provider**: read instance metadata from IMDS ([#353](https://github.com/dsx-ai-factory/topograph/pull/353)).
- **Chart RBAC**: support pre-existing ServiceAccounts when RBAC creation is managed separately ([#364](https://github.com/dsx-ai-factory/topograph/pull/364)).
- **API server**: preserve replacement timer in the trailing-delay request queue; snapshot queue completion results correctly ([#348](https://github.com/dsx-ai-factory/topograph/pull/348), [#351](https://github.com/dsx-ai-factory/topograph/pull/351)).
- Chart-test CI pins a compatible **Helm** version ([#359](https://github.com/dsx-ai-factory/topograph/pull/359)).
- deb/rpm build scripts: quote paths derived from environment variables ([#334](https://github.com/dsx-ai-factory/topograph/pull/334), [#335](https://github.com/dsx-ai-factory/topograph/pull/335)).
- Fern docs CI and version-registration edge cases ([#338](https://github.com/dsx-ai-factory/topograph/pull/338), [#327](https://github.com/dsx-ai-factory/topograph/pull/327)).
- Minor Helm template fixes ([#331](https://github.com/dsx-ai-factory/topograph/pull/331)).

### Removed

- The node-data-broker init container (`init-node-labels`), the `initc` values block, the `node-data-broker.initImage` Helm helper, and the `tail -f /dev/null` placeholder command.
- Dependency on the `curlimages/curl` image for the node-data-broker subchart.

### Migration (Helm — node-data-broker)

If you override node-data-broker settings today, update your values as follows:

| Before | After |
|--------|-------|
| `node-data-broker.initc.extraArgs` | `node-data-broker.extraArgs` |
| `node-data-broker.initc.image.*` | `node-data-broker.image.*` (now drives the sole container) |
| `node-data-broker.command` (`tail -f /dev/null`) | Remove — no longer needed |
| `node-data-broker.initc.enabled` | Remove — broker always runs when the subchart is enabled |

Example:

```yaml
# Before
node-data-broker:
  initc:
    extraArgs:
      - gpu-operator-namespace=my-namespace

# After
node-data-broker:
  extraArgs:
    - gpu-operator-namespace=my-namespace
  refreshInterval: 5m   # optional; default shown
```

**InfiniBand (`infiniband-k8s`) deployments** that override the broker image to `ghcr.io/dsx-ai-factory/topograph/ib` for `ibnetdiscover` should continue to do so until IB tooling is folded into the main Topograph image.

[Full changelog](https://github.com/dsx-ai-factory/topograph/compare/v0.4.0...v0.5.0)

---

## [0.4.0] - 2026-05-14

### Added

- **Nscale provider** ([#239](https://github.com/dsx-ai-factory/topograph/pull/239)).
- **Gateway API** support via optional `HTTPRoute` template ([#276](https://github.com/dsx-ai-factory/topograph/pull/276)).
- **Slinky dynamic nodes reconciliation** ([#241](https://github.com/dsx-ai-factory/topograph/pull/241)).
- Slinky **`slurmConfigUpdateMode`** parameter ([#300](https://github.com/dsx-ai-factory/topograph/pull/300)).
- Slinky **`podSelector`** support in partition topologies ([#295](https://github.com/dsx-ai-factory/topograph/pull/295)).
- **DSX provider simulator** ([#287](https://github.com/dsx-ai-factory/topograph/pull/287)).
- Simulation models refactored to support explicit and implicit node configuration ([#309](https://github.com/dsx-ai-factory/topograph/pull/309), [#311](https://github.com/dsx-ai-factory/topograph/pull/311)).
- **Versioned Fern documentation** with CI version stamping and frozen content at publish time ([#313](https://github.com/dsx-ai-factory/topograph/pull/313), [#316](https://github.com/dsx-ai-factory/topograph/pull/316)).
- Helm **`values.schema.json`**, **`helm test`** hook pods, and an expanded chart README ([#275](https://github.com/dsx-ai-factory/topograph/pull/275)).
- Authoritative **node labels and annotations** reference ([#254](https://github.com/dsx-ai-factory/topograph/pull/254)).
- **`make qualify`** pre-push aggregator (fmt, vet, lint, test) ([#256](https://github.com/dsx-ai-factory/topograph/pull/256)).
- **`AGENTS.md`** and **`.claude/CLAUDE.md`** for AI coding agents ([#253](https://github.com/dsx-ai-factory/topograph/pull/253)).
- Kubernetes and Slurm **get-started quickstarts** ([#292](https://github.com/dsx-ai-factory/topograph/pull/292)).
- InfiniBand, NetQ, and DRA provider documentation ([#243](https://github.com/dsx-ai-factory/topograph/pull/243)).
- `CODE_OF_CONDUCT`, `SECURITY.md`, and pull request template ([#273](https://github.com/dsx-ai-factory/topograph/pull/273)).

### Changed

- Refactored the canonical **topology graph** to reduce complexity ([#306](https://github.com/dsx-ai-factory/topograph/pull/306)).
- Removed obsolete **toposim** tooling and **protobuf** definitions ([#310](https://github.com/dsx-ai-factory/topograph/pull/310)).
- Documentation reorganized into overview, providers, engines, and reference sections ([#267](https://github.com/dsx-ai-factory/topograph/pull/267)).
- Helm chart declares **`kubeVersion: ">=1.27.0-0"`** on the umbrella chart and subcharts ([#291](https://github.com/dsx-ai-factory/topograph/pull/291)).
- Go toolchain and dependencies updated ([#277](https://github.com/dsx-ai-factory/topograph/pull/277)).
- Node observer **retries failed topology requests** after a configurable delay ([#242](https://github.com/dsx-ai-factory/topograph/pull/242)).

### Fixed

- Topology spec output for the **switch hierarchy** ([#246](https://github.com/dsx-ai-factory/topograph/pull/246)).
- Slinky partition discovery RBAC and **`useDynamicNodes`** interaction ([#319](https://github.com/dsx-ai-factory/topograph/pull/319), [#320](https://github.com/dsx-ai-factory/topograph/pull/320)).
- Slurm/Slinky topology edge cases ([#297](https://github.com/dsx-ai-factory/topograph/pull/297)).
- Flatten multi-line provider error messages for logging ([#301](https://github.com/dsx-ai-factory/topograph/pull/301)).
- Fern docs CI, preview artifacts, and custom-domain configuration ([#308](https://github.com/dsx-ai-factory/topograph/pull/308), [#290](https://github.com/dsx-ai-factory/topograph/pull/290), [#304](https://github.com/dsx-ai-factory/topograph/pull/304)).
- Docker BuildKit proxy settings on NV GitHub runners ([#296](https://github.com/dsx-ai-factory/topograph/pull/296)).

[Full changelog](https://github.com/dsx-ai-factory/topograph/compare/v0.3.0...v0.4.0)

---

## [0.3.0] - 2026-03-24

### Added

- **Lambda provider** and simulator ([#198](https://github.com/dsx-ai-factory/topograph/pull/198), [#200](https://github.com/dsx-ai-factory/topograph/pull/200)).
- **Lookup endpoint** for topology queries ([#218](https://github.com/dsx-ai-factory/topograph/pull/218)).
- **Request aggregation** keyed by payload hash (FNV-64) ([#217](https://github.com/dsx-ai-factory/topograph/pull/217), [#219](https://github.com/dsx-ai-factory/topograph/pull/219)).
- **NetQ provider**: programmatic OPID fetch and NVLink domain discovery ([#180](https://github.com/dsx-ai-factory/topograph/pull/180), [#186](https://github.com/dsx-ai-factory/topograph/pull/186)).
- **GCP provider**: external API authentication and **federated workload identity** for Kubernetes deployments ([#204](https://github.com/dsx-ai-factory/topograph/pull/204), [#224](https://github.com/dsx-ai-factory/topograph/pull/224)).
- **Slurm engine**: dynamic nodes support (later partially reverted) and flat YAML fallback when partition topology is absent ([#202](https://github.com/dsx-ai-factory/topograph/pull/202), [#235](https://github.com/dsx-ai-factory/topograph/pull/235)).
- **Kubernetes**: node selector for topology triggers, configurable topology node label names, **ServiceMonitor**, and InfiniBand example values ([#184](https://github.com/dsx-ai-factory/topograph/pull/184), [#221](https://github.com/dsx-ai-factory/topograph/pull/221), [#199](https://github.com/dsx-ai-factory/topograph/pull/199), [#191](https://github.com/dsx-ai-factory/topograph/pull/191)).
- **Integration test** payloads and harness ([#203](https://github.com/dsx-ai-factory/topograph/pull/203), [#205](https://github.com/dsx-ai-factory/topograph/pull/205)).
- **`/ib` container image** with InfiniBand diagnostic tools ([#190](https://github.com/dsx-ai-factory/topograph/pull/190)).
- Default **provider and engine params** in Helm values ([#234](https://github.com/dsx-ai-factory/topograph/pull/234)).
- Topology **upper-tier trimming** option ([#233](https://github.com/dsx-ai-factory/topograph/pull/233)).
- Topograph **version label** on Prometheus metrics ([#211](https://github.com/dsx-ai-factory/topograph/pull/211)).

### Changed

- **AWS SDK** upgraded for Secondary Networks support ([#214](https://github.com/dsx-ai-factory/topograph/pull/214)).
- Kubernetes client libraries upgraded to **Kubernetes 1.34** ([#201](https://github.com/dsx-ai-factory/topograph/pull/201)).
- HTTP helper functions simplified; accurate HTTP error propagation on topology requests ([#177](https://github.com/dsx-ai-factory/topograph/pull/177), [#197](https://github.com/dsx-ai-factory/topograph/pull/197)).
- Backend switch tier naming normalized ([#208](https://github.com/dsx-ai-factory/topograph/pull/208)).
- **`model_path`** replaced with **`modelFileName`** in simulation config ([#212](https://github.com/dsx-ai-factory/topograph/pull/212)).
- Nebius provider enhanced with Go SDK integration ([#227](https://github.com/dsx-ai-factory/topograph/pull/227)).
- Helm subchart defaults simplified ([#238](https://github.com/dsx-ai-factory/topograph/pull/238)).

### Fixed

- Node observer triggers topology discovery when **watched pods become Ready** ([#183](https://github.com/dsx-ai-factory/topograph/pull/183)).
- **`/v1/topology`** returns **HTTP 202 Accepted** while a request is still in progress ([#192](https://github.com/dsx-ai-factory/topograph/pull/192), [#193](https://github.com/dsx-ai-factory/topograph/pull/193), [#210](https://github.com/dsx-ai-factory/topograph/pull/210)).
- HTTP client option to **skip TLS verification** for lab environments ([#181](https://github.com/dsx-ai-factory/topograph/pull/181)).
- Slinky login-pod discovery requires **Running** state ([#188](https://github.com/dsx-ai-factory/topograph/pull/188)).
- Slurm block ordering ([#207](https://github.com/dsx-ai-factory/topograph/pull/207)).
- GCP project ID resolution from credentials ([#220](https://github.com/dsx-ai-factory/topograph/pull/220)).
- DRA provider rejects empty domain sets ([#189](https://github.com/dsx-ai-factory/topograph/pull/189)).
- Partition topology omits nodes with missing data ([#229](https://github.com/dsx-ai-factory/topograph/pull/229)).
- Kubernetes resource requests and limits in the Helm chart ([#231](https://github.com/dsx-ai-factory/topograph/pull/231)).
- NVIDIA device plugin DaemonSet name and namespace configurable for node-data-broker ([#237](https://github.com/dsx-ai-factory/topograph/pull/237)).
- Retry logic and HTTP error logging in the API server ([#196](https://github.com/dsx-ai-factory/topograph/pull/196), [#209](https://github.com/dsx-ai-factory/topograph/pull/209)).
- Request latency histogram buckets tuned for percentile accuracy ([#215](https://github.com/dsx-ai-factory/topograph/pull/215)).

[Full changelog](https://github.com/dsx-ai-factory/topograph/compare/v0.1.0...v0.3.0)

---

## [0.1.0] - 2025-10-30

Initial release.

### Added

- Core **Topograph API server** with `/v1/generate` and asynchronous topology retrieval.
- **Providers**: AWS, GCP, OCI, Nebius, InfiniBand (bare-metal and Kubernetes), and DRA.
- **Engines**: Kubernetes (node labels), Slurm (`topology.conf`), and Slinky (ConfigMap).
- **Kubernetes components**: node-observer Deployment and node-data-broker DaemonSet (Helm subcharts).
- **Helm chart** for deploying Topograph on Kubernetes.
- Provider and engine documentation under `docs/providers/` and `docs/engines/`.
- Container images published to `ghcr.io/dsx-ai-factory/topograph`.
- Debian and RPM packaging targets.

[Release notes](https://github.com/dsx-ai-factory/topograph/releases/tag/v0.1.0)

---

[Unreleased]: https://github.com/dsx-ai-factory/topograph/compare/v0.5.0...HEAD
[0.5.0]: https://github.com/dsx-ai-factory/topograph/compare/v0.4.0...v0.5.0
[0.4.0]: https://github.com/dsx-ai-factory/topograph/compare/v0.3.0...v0.4.0
[0.3.0]: https://github.com/dsx-ai-factory/topograph/compare/v0.1.0...v0.3.0
[0.1.0]: https://github.com/dsx-ai-factory/topograph/releases/tag/v0.1.0
