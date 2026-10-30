# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

## [Unreleased]

### Added

- The Node Observer serves `/healthz` on `nodeObserver.port` (default `8080`), and the chart gives its Deployment liveness and readiness probes on it, as the Node Data Broker already has. Set through `healthzPort` in the Node Observer config; without it, no endpoint is served.
- License headers: `make license-headers` adds NVIDIA's standard SPDX and Apache 2.0 header to files that have none, and `make license-header-check`, run by `license-scan.yml`, fails on any file without a copyright or license header, or with an NVIDIA header in one of the two older forms. Every NVIDIA header now uses the standard form, keeping each file's original year; files that had no header now carry one.
- Nautobot provider (`nautobot`) discovers switch fabric topology through the GraphQL API, with required `endpoint`, compute `deviceRole`, and `switchRoles` parameters, optional `location` and `platform` filters, status filters, and required `creds.token` authentication. Switch queries paginate using the configured `pageSize` (default `100`). Discovery starts at selected switches, collects compute endpoints from cable peers, follows connected switches outside the initial location and platform filters to infer fabric tiers, and groups redundant switches into a scheduler tree. Attached compute endpoints can also be in other locations or use other platforms. Duplicate device IDs and switch names fail with HTTP 502 identifying the conflicts; switch-name errors include both device UUIDs. Devices missing an ID or name are skipped with a warning. Oversized pages log a warning and advance the offset by the actual number of returned devices, including skipped records. Transport failures return HTTP 502 with the underlying error.
- `workflow-tests.yaml` runs unit tests for the scripts inside GitHub workflows.
- `conflict-check.yaml` adds the `needs-rebase` label to open pull requests that no longer merge cleanly into `main`, comments once to ask the author to resolve the conflicts, and removes the label once the pull request merges cleanly again. It runs after every push to `main`.

### Changed

- The issue template chooser no longer offers blank issues, and its links now point to NVIDIA PSIRT for security reports, `docs/community.md` for questions, and the documentation site. The feature request form applies `enhancement` instead of `feature request`, both documentation forms apply `documentation` instead of `doc`, a new Question form applies `question`, and every form also applies `needs-triage`.
- Dependabot waits 7 days after a release before opening a version-update PR for Go modules, GitHub Actions and Docker base images (`cooldown` in `.github/dependabot.yml`). Security updates are not delayed.

### Removed

- The Markdown issue templates `documentation-request.md` and `submit-question.md`. The two documentation forms and the Question form replace them.

### Fixed

- `GET /v1/topology` responses now carry `Content-Type: text/plain; charset=utf-8` and `X-Content-Type-Options: nosniff` for completed and pending requests, as error responses already did, instead of leaving the content type to sniffing. A result of an unexpected type returns HTTP 500 instead of panicking the handler.
- The API server now logs a one-line summary of each accepted topology request (provider, engine, region and node counts) instead of every request in full, which lists every node; the full request is logged at verbosity 4. Line breaks in client input can no longer start new log entries: the full request escapes them in each value, and the outbound request URL logged at verbosity 4 is quoted.
- Helm node-observer's `generateTopologyUrl` now prints an integer `service.port` as a number. An integer port, such as `--set service.port=8080`, previously produced an invalid URL ending in `:%!f(int64=8080)/v1/generate`.

## [v1.1.0] - 2026-10-23

### Added

- Firebird provider (`firebird`) discovers arbitrary-depth fabric paths from `GET /topology/nodes` over HTTPS, using leaf-first switch names from `networkPath`, `pageSize` / `pageToken` pagination, and requested-node filtering. Client-credentials authentication uses `POST /auth/token` with `grantType: "client_credentials"`, `clientId`, and `clientSecret`. Each topology generation request obtains a fresh access token and reuses it across all pages; HTTP 401 does not trigger reauthentication. Authentication and topology errors preserve upstream HTTP status codes and structured diagnostics.
- Firebird simulation provider (`firebird-sim`) exercises production topology conversion and token pagination using YAML models, with model-based instance discovery, tier trimming, and API failure injection.
- Crusoe provider (`crusoe`) and simulation variant (`crusoe-sim`) discover fabric tiers from the `crusoe.ai/pod.id` and `crusoe.ai/ib.partition.id` Node labels, with a synthetic root and placeholder tiers for nodes without fabric labels. On NVLink systems, each distinct `nvidia.com/gpu.clique` value supplies an accelerator domain for `topology/block`; nodes without the clique label receive no accelerator domain. Use `topology/tree` for fabric topology.
- `infiniband-sim` provider replays a saved `ibnetdiscover` output file through the production InfiniBand parser, allowing offline switch topology testing with requested-node filtering and no fabric access.
- Configurable Kubernetes instance ID and region sources through `nodeMetadata` (`config.nodeMetadata` in Helm). When set, both `instanceID` and `region` require a source type (`label` or `annotation`) and a valid metadata key; omitted or null `nodeMetadata` preserves both default annotations. Deployments can omit the Node Data Broker when all required metadata is already available and the provider does not require broker pods for discovery. The `infiniband-k8s` provider still requires the broker to run `ibnetdiscover`.
- Optional provider parameter `imdsUrl` to override the default Instance Metadata Service URL, currently supported by `nscale` for both Slurm auto-discovery and Node Data Broker self-annotation.
- Opt-in Helm `NetworkPolicy` covering all three components (`networkPolicy.enabled`, default `false`). Ingress allows intra-release traffic and, when configured, the `networkPolicy.metricsScraperNamespace` namespace; `extraIngress` adds custom rules, including access from ingress or Gateway controllers. Egress remains unconstrained unless `extraEgress` is set, which adds DNS and intra-release allows alongside the supplied rules. Under default-deny egress, include the Kubernetes API server and provider endpoints; `dnsPodSelector` supports custom DNS pod labels.
- SHA-256 checksum files retained with Helm chart packages and signed provenance bundles in the release workflow's `topograph-chart-<version>` GitHub Actions artifact.
- Signed SLSA build provenance for container images and Helm chart packages through GitHub artifact attestations; container provenance is also published alongside the image in NGC staging.
- `demos/tas-lab`, a kind-based lab with eight scenarios using Topograph labels as KEP-5732 `TopologyConstraint` keys and Kueue `Topology` levels, with Kubernetes v1.37 and Kueue v0.20.0 defaults. The lab includes declarative Chainsaw tests and a pinned Chainsaw CLI installer; see `docs/demos/tas-lab.md`.
- "Install on RKE2 (SUSE)" guide (`docs/get-started/rke2-suse.md`) covering GPU Operator placement for `nvidia-smi` discovery, FQDN node names, Pod Security under RKE2's CIS profile, SELinux on SLES, and RoCE fabrics.
- Light/dark documentation diagrams and logo variants, stacked and horizontal logo lockups, consistent README badges, and a changelog link in the documentation sidebar.
- CI check that every Markdown page under `docs/`, outside `docs/design/`, appears in `docs/index.yml`.

### Changed

- **BREAKING:** The Go module and canonical repository moved from `github.com/NVIDIA/topograph` to `github.com/dsx-ai-factory/topograph`.
- **BREAKING:** `topograph` and `node-observer` now use [pflag](https://github.com/spf13/pflag), matching `node-data-broker`. Single-dash long flags such as `-version`, `-logtostderr`, and `-vmodule` require a double dash (`--version`, `--logtostderr`, `--vmodule`); this also applies to other long [klog](https://github.com/kubernetes/klog) flags. The config flag is available as `--config` or `-c`.
- **BREAKING:** `kwok-nodes` now uses pflag. Single-dash long flags such as `-model` and `-output` require a double dash (`--model`, `--output`); `-m`, `-o`, `-g`, and `-p` are new aliases for `--model`, `--output`, `--gpus`, and `--pods`.
- The Helm chart defaults to the public container image `nvcr.io/nvidia/topograph/topograph`. The manual image and chart workflows now upload to NGC staging; chart uploads use `ngc registry chart push` instead of GitHub Pages. Public releases promote approved images to `nvcr.io/nvidia/topograph/topograph` and charts to `https://helm.ngc.nvidia.com/nvidia/topograph`.
- Releases use a dedicated `vX.Y.Z` branch with final chart and changelog metadata. Images and charts complete NVIDIA internal security review before approved artifacts are promoted unchanged to the public NGC repositories; the source tag identifies the approved build commit.
- The `nscale` provider's Slurm auto-discovery obtains server IDs (`serverID`) and regions (`regionID`) from each node's IMDS through a single `pdsh` sweep shared by the instance and region lookups. Remote commands have a 10-second timeout; partial results are retained with warnings when some nodes fail, and failures with no output return an error.
- The `nscale` Radar API topology response now uses `server_id` instead of `instance_id`, matching IMDS `serverID`. A configured `region` credential restricts Slurm auto-discovery to nodes with a matching IMDS region; nodes with different or missing regions are excluded with warnings.
- Helm defaults increase the node-observer CPU limit from `400m` to `1000m` and the node-data-broker CPU request and limit from `100m` to `200m`.
- Updated `golang.org/x/net` from **v0.56.0** in v1.0.0 to **v0.61.0**.

### Fixed

- Release-tag checkouts now stamp binaries, deb/rpm packages, and default image tags with the checked-out `v*` tag, including a `-dirty` suffix for tracked modifications, instead of `HEAD`. Without a release tag, version resolution falls back to the branch name, short commit SHA, then `unknown`; packaging warns when git metadata is unavailable. `make image-push` and `make docker-buildx` require an explicit `IMAGE_TAG` before pushing a default release-like tag to `ghcr.io/dsx-ai-factory/topograph`. Builds use `-trimpath` and stop at the first binary compilation failure ([#609](https://github.com/dsx-ai-factory/topograph/pull/609)).
- The `graph` engine derives instance records, closest-first `network_layers`, and accelerator-domain labels from the canonical graph when providers omit them, and fills missing topology fields in provider-supplied records. This fixes empty instance lists from live providers ([#590](https://github.com/dsx-ai-factory/topograph/pull/590)).
- InfiniBand providers match HCA host names to cluster node names by short host name when at least one name is short, preserving exact matches and avoiding matches between different FQDNs. Ambiguous matches are skipped with warnings. FQDN-registered nodes are retained in the switch tree, and resolved names prevent redundant `ibnetdiscover` runs on nodes in the same fabric.
- GCP and OCI simulation providers handle partial final pagination pages without indexing past the available instances.
- Non-positive `pageSize` configuration values emit a warning and use the provider default.
- The node observer regenerates topology for all informer deletions, including `cache.DeletedFinalStateUnknown` tombstones whose object is unavailable, preventing stale topology after a disconnected watch.
- Slinky `configUpdateMode: skeleton-only` emits only top-level switches for `topology/tree`, without child-switch lists or node names. The top-level tier is determined after provider `trimTiers` is applied.
- `topology/block` drops individual accelerator-domain blocks with invalid or inconsistent `blockName` results, logging a warning while preserving other blocks. If all real blocks are dropped, a per-partition topology falls back to flat output, while cluster-wide block generation returns an error, including when node-less padding blocks remain. Dropped nodes receive no topology spec instead of an empty block name; duplicate formatted names across blocks remain errors.
- Helm `helm test` pods (`test-healthz`, `test-metrics`) set CPU and memory requests and limits through `tests.resources` (defaults: requests `10m` / `16Mi`, limits `100m` / `128Mi`), supporting namespaces whose `ResourceQuota` requires resources. Adjust these values to satisfy any namespace `LimitRange`.
- Helm schema validation accepts provider and engine names as strings and leaves registry validation to the application, so newly registered providers are no longer rejected by a stale chart enum.
- API server shutdown stops its asynchronous work queue and is safe to call repeatedly.
- The Go workflow runs on pushes to `main`, keeping Codecov coverage and the README badge current after squash merges.

### Removed

- **BREAKING:** The `nscale` provider's `instanceApiUrl` parameter and instance API client. Slurm auto-discovery now uses each node's IMDS; remove `instanceApiUrl` from provider configuration.
- Obsolete, unregistered `cw` provider implementation.

### Security

- Go toolchain upgraded from **1.26.6** to **1.27.3** across the module, container build, and CI to address a reachable standard-library vulnerability reported by `govulncheck`. CI uses `golangci-lint` **v2.14.0** for Go 1.27 compatibility.
- Main API server ClusterRole rules are gated by engine and provider, and the ClusterRole and binding are omitted when no Kubernetes API access is needed. Node permissions use a single rule: `[get, list, patch]` for the Kubernetes engine, `[list, patch]` for dynamic Slinky, and `[list]` for other node-consuming configurations.
- Helm test pods disable service account token mounting because they only probe the Topograph Service.
- Added CodeQL analysis for Go, Grype container image scans for `linux/amd64` and `linux/arm64` with OpenVEX support, and Checkov scans of rendered Helm manifests. Grype and Checkov findings are reported to GitHub code scanning without failing builds solely on findings.
- CI workflows use SHA-pinned actions and reduced token permissions; dependency license checks, digest-pinned container base images, OpenSSF Scorecard, and actionlint/zizmor workflow checks strengthen supply-chain validation.

[Full changelog](https://github.com/dsx-ai-factory/topograph/compare/v1.0.0...v1.1.0)

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

| If you run                                                             | Override                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                     |
| ---------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `infiniband-k8s` (broker reads `/sys/class`)                           | A **complete** privileged override on `node-data-broker` — `securityContext: { privileged: true, allowPrivilegeEscalation: true, readOnlyRootFilesystem: false, runAsNonRoot: false, runAsUser: 0 }` plus `podSecurityContext.runAsNonRoot: false`. A partial override (only `privileged: true`) is rejected at admission because the default `allowPrivilegeEscalation: false` remains. Both shipped IB examples (`values.k8s.ib-example.yaml` and `values.slinky.ib.block-example.yaml`) are updated to the complete form. |
| `engine: slurm` or `engine: graph` in-cluster (writes `topology.conf`) | `securityContext.readOnlyRootFilesystem: false` and a writable volume at the configured output path.                                                                                                                                                                                                                                                                                                                                                                                                                         |

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

| Before                                           | After                                                      |
| ------------------------------------------------ | ---------------------------------------------------------- |
| `node-data-broker.initc.extraArgs`               | `node-data-broker.extraArgs`                               |
| `node-data-broker.initc.image.*`                 | `node-data-broker.image.*` (now drives the sole container) |
| `node-data-broker.command` (`tail -f /dev/null`) | Remove — no longer needed                                  |
| `node-data-broker.initc.enabled`                 | Remove — broker always runs when the subchart is enabled   |

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

[Unreleased]: https://github.com/dsx-ai-factory/topograph/compare/v1.1.0...HEAD
[v1.1.0]: https://github.com/dsx-ai-factory/topograph/compare/v1.0.0...v1.1.0
[v1.0.0]: https://github.com/dsx-ai-factory/topograph/compare/v0.5.0...v1.0.0
[0.5.0]: https://github.com/dsx-ai-factory/topograph/compare/v0.4.0...v0.5.0
[0.4.0]: https://github.com/dsx-ai-factory/topograph/compare/v0.3.0...v0.4.0
[0.3.0]: https://github.com/dsx-ai-factory/topograph/compare/v0.1.0...v0.3.0
[0.1.0]: https://github.com/dsx-ai-factory/topograph/releases/tag/v0.1.0
