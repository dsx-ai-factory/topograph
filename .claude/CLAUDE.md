# CLAUDE.md

This file provides guidance to Claude Code when working with code in this repository.

<!-- This is the canonical source. AGENTS.md is a synced public copy. The first 5 lines differ, and the relative links here carry a ../ prefix because this file sits one directory below the repository root, where AGENTS.md carries them unprefixed. Do not "restore" that difference. -->

## Start here

This file is the short core. It carries the invariants an agent must not break, the
boundaries of what an agent may change on its own, and the conventions that are
specific to this codebase. Everything else lives in the document that already owns
it and is not repeated here:

| You need | Read |
|---|---|
| Prerequisites, build, local test loop, running a binary, container images, packaging, CI parity | [DEVELOPMENT.md](../DEVELOPMENT.md) |
| Issue-first workflow, DCO sign-off, commit and branch conventions, review process, AI-assisted contribution policy | [CONTRIBUTING.md](../CONTRIBUTING.md) |
| What each runtime component does and how a request flows between them | [docs/architecture.md](../docs/architecture.md) |
| Every label and annotation key the Kubernetes engine writes | [docs/reference/node-labels.md](../docs/reference/node-labels.md) |
| Provider selection, supported providers, the "Choosing a Provider" scenario table | `docs/overview.md` |
| API endpoints, request parameters, response fields, config schema | `docs/api.md` |
| How to report a suspected vulnerability | `SECURITY.md` |
| Release history and operator-facing migration notes | `CHANGELOG.md` |

When you change behaviour that one of those documents describes, update that
document. Do not answer the same question here as well; two copies of a rule drift
apart, and the next agent reads the stale one.

## 1. Project Overview and Architecture

Topograph discovers the physical network topology of a cluster (NVLink domains,
InfiniBand/Ethernet switch fabric, cloud rack topology) and exposes it to workload
schedulers: Slurm, Kubernetes, and Slurm-on-Kubernetes (Slinky). It has five runtime
components, the API Server, the Node Observer, the Node Data Broker, the Provider,
and the Engine. [docs/architecture.md](../docs/architecture.md) describes what each one
does and how a request flows between them.

### Key invariant

Providers differ by environment. The canonical `topology.Graph` is stable. Engines
only translate; they do not discover.

Within a provider, network-fabric and accelerator-domain discovery may be composed
independently through `pkg/accelerator`; the provider remains responsible for
combining both dimensions into the canonical graph.

This separation is load-bearing. If you find yourself reading the fabric in an
engine, or emitting scheduler-specific output from a provider, stop and reconsider.

### Repository map

```
cmd/                  # Entry points: topograph, node-observer, node-data-broker, kwok-nodes
pkg/
  accelerator/        # Pluggable accelerator-domain discovery composed by providers
  providers/          # One directory per provider: aws, crusoe, dra, dsx, gcp, infiniband, lambdai, minerva, nebius, netq, nscale, oci, test
  engines/            # One directory per engine: graph, k8s, nfd, slinky, slurm
  topology/           # Canonical Graph, Vertex tree, and topology constants (DO NOT CHANGE CASUALLY)
  registry/           # Central NamedLoader wiring for providers + engines
  translate/          # topology.conf and block/tree generation shared by engines
  server/             # HTTP server and request aggregator
  node_observer/      # Kubernetes Node watcher
  ib/                 # InfiniBand fabric discovery helpers
  config/             # Config file parser
  metrics/            # Prometheus metrics
  models/             # Go types and loader for YAML simulation models (the YAML files live in tests/models/)
  test/               # Cross-package test helpers
internal/             # Shared utilities not part of the public API
  cluset, component, config, exec, files, httperr, httpreq, k8s, kwok, version
charts/topograph/     # Helm chart for all Kubernetes components; tests/ holds the helm-unittest suites + snapshots
CHANGELOG.md          # Release history (Keep a Changelog format); update [Unreleased] for user-facing PRs
docs/                 # Public-facing docs: overview.md, architecture.md, api.md + providers/, engines/, reference/ subdirectories
demos/                # Interactive Kubernetes/KWOK deployment demos
tests/models/         # YAML simulation fixtures
config/               # Sample topograph-config.yaml
scripts/              # Build scripts (deb, rpm, SSL, clean)
localdev/             # Developer-local workspace, not tracked; personal scratch files
```

## 2. What an Agent Is Permitted to Do

### In scope without asking

An agent is permitted to make these changes on its own initiative, as long as the
change ships with tests when it changes behavior, keeps `make qualify` green, and
carries the doc updates named in the Documentation Impact Evaluation table below.

| Task in scope | Files an agent may modify |
|---|---|
| Add a provider, or fix or extend an existing one | `pkg/providers/<name>/`, the one-line registry entry in `pkg/registry/registry.go`, and the docs the provider checklist requires: `docs/providers/<name>.md`, the provider list and "Choosing a Provider" table in `docs/overview.md`, and a `docs/index.yml` entry when the page is new |
| Fix a bug or add tests inside an existing engine, without changing its output format | `pkg/engines/<engine>/`, `pkg/translate/` |
| Server plumbing, config parsing, metrics, shared helpers | `pkg/server/`, `pkg/config/`, `pkg/metrics/`, `pkg/ib/`, `pkg/node_observer/`, `internal/` |
| Command-line entry points and flags | `cmd/` |
| Helm chart templates, values, and the helm-unittest suites and snapshots | `charts/topograph/` |
| Documentation, release notes, simulation fixtures, demos, build scripts | `docs/`, `CHANGELOG.md`, `tests/models/`, `demos/`, `scripts/` |
| Tests for any of the above | any `*_test.go`, `charts/topograph/tests/` |

Stay inside the directories the task names. A change that starts in one provider and
ends up editing `pkg/topology/` is a signal that the task was scoped wrongly, not a
licence to widen it. Preparing a commit is in scope; `git push`, opening or
commenting on a pull request or issue, and publishing an image or chart are the human
contributor's call, not the agent's.

### Do not change without discussion

These structures propagate across every provider and engine. Changing them in a
single PR usually means the PR is too broad. Open an issue and get maintainer
agreement before writing code that touches one of them.

| Surface | Why it's load-bearing |
|---|---|
| `pkg/topology/`: `Graph`, the `Vertex` tree, and topology constants | Every provider returns it; every engine consumes it. A shape change ripples to all of them. |
| Helm `provider.name` / `engine.name` | External contract for operators deploying Topograph. |
| The variable fabric labels `fabric.topograph.run/tier-N` and accelerator labels `accelerator.topograph.run/domain` and `accelerator.topograph.run/sub-domain` | Consumed by downstream projects (KAI Scheduler, NVSentinel, Kueue); fabric tier 0 is closest to the node. |
| Adding a new engine | Implies a new output format that every provider's output must be translatable into. |
| The vulnerability reporting route in `SECURITY.md` | Reports go to NVIDIA PSIRT, not to a GitHub issue. |
| `LICENSE`, `CODEOWNERS`, `MAINTAINERS.md`, `GOVERNANCE.md` | Project governance, owned by the maintainers. |

### Never commit credentials, secrets, API keys, or tokens

Credentials, secrets, API keys, access tokens, passwords, private keys, certificates,
and the values of environment variables must never be committed to this repository.
That holds for Go source, test fixtures, Helm values files, container images, the
config samples under `config/`, commit messages, and PR descriptions alike. Refer to
a secret by its name; never by its value.

- Provider credentials are supplied at runtime through environment variables or a
  Kubernetes Secret. `docs/providers/<name>.md` documents which environment variables
  each provider reads. Document the variable name, never a working value.
- A test that needs a credential uses an obviously fake placeholder that cannot
  authenticate against anything real.
- `localdev/` is untracked on purpose. Keep scratch files that hold real endpoints or
  tokens there, and read `git status` before staging.
- If you find a credential that is already committed, do not describe it in a public
  issue or PR. Report it privately through the route in `SECURITY.md`. It has to be
  rotated, not only deleted, because git history keeps the old value.

## 3. Coding Style and Conventions

Build, lint, and test commands live in [DEVELOPMENT.md](../DEVELOPMENT.md). This section
covers only what the tooling cannot tell you.

### Formatting and linting

- `go fmt ./...` is authoritative; do not hand-format
- `golangci-lint` runs in CI with `--new-from-rev` so only new issues block; fix warnings in code you touch
- Copyright header on every new Go file: `Copyright (c) <year>, NVIDIA CORPORATION.  All rights reserved.` followed by the Apache 2.0 boilerplate matching existing files

### Provider interface

The contract lives in `pkg/providers/providers.go`:

```go
type Provider interface {
    GenerateTopologyConfig(
        ctx context.Context,
        pageSize *int,
        instances []topology.ComputeInstances,
    ) (*topology.Graph, *httperr.Error)
}
```

A provider returns a `*topology.Graph` of the discovered topology. Providers using
`ClusterTopology` populate `InstanceTopology.FabricTiers` closest-first,
`InstanceTopology.XclrDomainID` for the optional accelerator domain, and
`InstanceTopology.XclrSubDomainID` for an optional sub-domain nested within it, then
call `ToGraph`; the fabric path has no fixed depth. `Graph.Tiers` is the fabric
hierarchy, and `Graph.Domains` is the `topology/block` source. Leaf vertices are
compute nodes; interior tier vertices are switches.

### Preferred and deprecated patterns

#### Error type at the provider boundary

Preferred. Return `*httperr.Error` so the API server can propagate a meaningful HTTP
status code to the caller:

```go
func (p *Provider) GenerateTopologyConfig(ctx context.Context, pageSize *int,
	instances []topology.ComputeInstances) (*topology.Graph, *httperr.Error) {

	cluster, err := p.discover(ctx, pageSize, instances)
	if err != nil {
		// The upstream API answered and rejected us: 502, not a bare 500.
		return nil, httperr.NewError(http.StatusBadGateway, err.Error())
	}

	return cluster.ToGraph(NAME, instances, p.trimTiers, false), nil
}
```

Deprecated. A plain `error` is not acceptable at this boundary. The API server has no
status code to work with, so every provider failure collapses into one generic
response and callers cannot tell a bad request from an upstream outage:

```go
// Do not do this.
func (p *Provider) GenerateTopologyConfig(ctx context.Context, pageSize *int,
	instances []topology.ComputeInstances) (*topology.Graph, error) {

	cluster, err := p.discover(ctx, pageSize, instances)
	if err != nil {
		return nil, fmt.Errorf("discovery failed: %w", err)
	}

	return cluster.ToGraph(NAME, instances, p.trimTiers, false), nil
}
```

#### Building the fabric path

Preferred. Hand the switch IDs to the tier helper that matches your source ordering
and let `ToGraph` build the vertex tree. The helper names the ordering, so a provider
whose API returns root-first IDs uses `topology.RootFirstFabricTiers` instead of
reversing a slice by hand:

```go
cluster.Append(&topology.InstanceTopology{
	InstanceID:   node.ID,
	FabricTiers:  topology.ClosestFirstFabricTiers(node.LeafID, node.SpineID, node.CoreID),
	XclrDomainID: node.NVLinkDomainID,
})
```

Deprecated. Assembling `topology.Vertex` values inside a provider, or hardcoding a
tier count, couples the provider to a shape that only `pkg/topology` owns:

```go
// Do not do this: the fabric path has no fixed depth, and building the vertex
// tree is pkg/topology's job, not the provider's.
leaf := &topology.Vertex{ID: node.LeafID}
spine := &topology.Vertex{ID: node.SpineID, Vertices: map[string]*topology.Vertex{node.LeafID: leaf}}
```

### Adding a new provider

1. Create `pkg/providers/<name>/` with at minimum `provider.go` and `provider_test.go`
2. Expose a `NamedLoader` function with signature `func NamedLoader() (string, providers.Loader)`; this is how the registry wires the provider
3. Register in `pkg/registry/registry.go` by adding `<name>.NamedLoader` to the `providers.NewRegistry(...)` call list
4. Add `docs/providers/<name>.md` following the shape of `aws.md` / `netq.md` (prerequisites, credentials, parameters, how it works, verification)
5. Update `docs/overview.md`: add the provider to the "Currently supported providers" list and the "Choosing a Provider" scenario table
6. If the provider has a simulated variant for testing, export a second `NamedLoaderSim` and register it alongside (see `aws`, `gcp`, `oci`, `lambdai`)

### Adding a new engine

Engines are much rarer (five exist: `graph`, `k8s`, `nfd`, `slinky`, `slurm`). Follow
the same registry pattern but register in `engines.NewRegistry(...)`. Coordinate with
maintainers before starting; adding an engine implies a new output format that every
provider's output must be translatable into.

### Anti-patterns

| Don't | Because |
|---|---|
| Read the fabric inside an engine | Engines only translate; discovery belongs in providers |
| Emit scheduler-specific output from a provider | Same invariant in reverse |
| Change `pkg/topology/Vertex` fields without discussion | Every provider and engine depends on the shape |
| Add a new provider in `pkg/providers/<name>/` without also updating `pkg/registry/registry.go` | Orphaned code; provider will not be loadable |
| Modify an AGENTS.md-described surface (new Makefile target, top-level directory, chart template, invariant) without updating `AGENTS.md` + `.claude/CLAUDE.md` in the same PR | Drift between the code and its agent-facing description; the next contributor or agent reads stale guidance |
| Skip DCO sign-off to "fix later" | The DCO bot will block the PR; rebase with `--signoff` is always available |
| Use plain `error` at the provider interface boundary | Must be `*httperr.Error` so the API server returns the correct HTTP status |
| Commit a credential, API key, token, or environment variable value | Anything reaching git history has to be rotated, not just deleted |
| Enable both `ingress.enabled` and `gatewayAPI.enabled` in the same Helm release | Mutually exclusive; deploying both routing resources against the same Service is almost always a misconfiguration. Enforced by `charts/topograph/templates/_validation.tpl`. |
| Add implementation-specific annotations, CRDs, or extensions to `charts/topograph/templates/httproute.yaml` | The default `HTTPRoute` must use only standard `gateway.networking.k8s.io/v1` fields so it renders and functions against any conformant Gateway API implementation. Implementation-specific examples (kgateway `TrafficPolicy`, etc.) belong in `values.k8s.gateway-api-example.yaml` as separate attached resources, not in the chart's default template. |

### Label and annotation reference

Do not invent label or annotation keys in provider code; values flow through the
canonical graph and the engine decides the key. The default keys, the optional
`fabricLabels` and `acceleratorLabel` overrides, and the value semantics are all in
[docs/reference/node-labels.md](../docs/reference/node-labels.md).

## 4. Pull Request Guidelines

Branch naming, the Conventional Commits format, DCO sign-off and how to repair a
missing one, the review process, and the AI-assisted contribution policy are all in
[CONTRIBUTING.md](../CONTRIBUTING.md). What follows is the part specific to keeping this
repository's own documents and gates in step.

### Coverage policy

From `codecov.yml`:

- **Project coverage**: 60% target, 5% threshold for drops
- **Patch coverage**: 50% target, 5% threshold

Coverage checks run on pull requests. A drop below target with no matching uplift in
the touched files will fail the Codecov check.

### GPG signing is optional but recommended

DCO sign-off (`git commit -s`) is required and is covered in `CONTRIBUTING.md`. GPG
signing is separate and optional; configure it once with
`git config --global user.signingkey <key-id>` and
`git config --global commit.gpgsign true`, then use `git commit -s -S`. Signed commits
get a **Verified** badge once the public key is uploaded to your GitHub account.

### Potential security issues

If you discover what appears to be a security vulnerability while working in this
codebase (unauthenticated code path, exposed credential, injection vulnerability,
privilege-escalation path, dependency with a known CVE, or similar), do **not** file a
public GitHub issue or include it in a public PR description. Surface it privately to
the maintainer, who routes it through the NVIDIA PSIRT channels documented in
`SECURITY.md`.

### Documentation structure

`docs/` is the **source of truth** for all public-facing documentation, published to
`https://docs.nvidia.com/topograph` via Fern. `fern/` holds only site config and theme
assets, never doc content.

**`docs/design/`** is a drafting space for design work in progress. Files there are
excluded from the Fern sidebar and are not published to the docs site. Finalized
design decisions should move to the appropriate `docs/` subtree or be captured in code
comments and CHANGELOG entries.

**Every `.md` file added to `docs/` (outside `docs/design/`) must also be added to
`docs/index.yml`**, which drives the Fern sidebar. CI enforces this:
`fern-docs-ci.yml` fails if any `docs/**/*.md` outside `docs/design/` is absent from
`docs/index.yml`.

### Documentation Impact Evaluation

Every PR should be evaluated for documentation impact before pre-push qualification.
The following changes imply specific doc updates in the same PR:

| Change | Docs update required |
|---|---|
| New / changed / removed provider | `docs/providers/<name>.md` + `docs/overview.md` provider list + "Choosing a Provider" scenario table |
| New / changed / removed engine | `docs/engines/<engine>.md` |
| New / changed chart template (Ingress, HTTPRoute, NetworkPolicy, ServiceMonitor, etc.) | `docs/engines/k8s.md` "Exposing the Topograph API" section |
| New / changed chart values schema | `charts/topograph/values.yaml` comments, `NOTES.txt` output, and any docs that reference the values |
| New / changed label or annotation key | `docs/reference/node-labels.md` |
| New / changed API endpoint, request parameter, or response field | `docs/api.md` |
| New / changed config schema (`topograph-config.yaml` fields, defaults, validation) | `docs/api.md` |
| User-facing feature, fix, breaking change, or Helm migration worth calling out in release notes | `CHANGELOG.md` under `[Unreleased]` (Added / Changed / Fixed / Removed); move entries into a version section at release time |
| New invariant or "do not change without discussion" surface | `AGENTS.md` + `.claude/CLAUDE.md` in the same PR |
| New Makefile target, top-level directory, or repository-layout change described by the repository map | `AGENTS.md` + `.claude/CLAUDE.md` in the same PR |
| New build, test, or local-run instruction | `DEVELOPMENT.md` |
| New `.md` file added to `docs/` (outside `docs/design/`) | Add an entry to `docs/index.yml`; CI will fail if omitted |

If a change falls outside these categories, it still warrants a moment's review for
collateral doc drift.

### Pre-push checklist

When filing a PR (`gh pr create` or the GitHub UI), `.github/PULL_REQUEST_TEMPLATE.md`
auto-populates the body with a Description section and a Checklist. Fill in the
Description and tick the checklist items as completed; do not delete or replace the
template wholesale.

- [ ] `make qualify` passes (runs fmt, vet, lint, test)
- [ ] `make chart-test` passes when `charts/topograph/` changed
- [ ] New or changed public behavior is covered by a test
- [ ] Documentation impact evaluated per the table above, and applicable doc updates are included in this PR
- [ ] User-facing changes recorded in `CHANGELOG.md` `[Unreleased]` when applicable
- [ ] `pkg/topology/` changes were discussed in an issue first
- [ ] No credential, API key, token, or environment variable value appears anywhere in the diff
- [ ] Every commit has a DCO sign-off
