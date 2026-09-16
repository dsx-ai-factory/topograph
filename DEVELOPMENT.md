# Development Guide

Project setup, build/test workflow, and local run instructions for Topograph
contributors. For architecture and the provider/engine boundary, see
[`docs/architecture.md`](docs/architecture.md) and
[`.claude/CLAUDE.md`](.claude/CLAUDE.md) / [`AGENTS.md`](AGENTS.md).

## Quick Start

```bash
git clone https://github.com/dsx-ai-factory/topograph.git
cd topograph
make build                    # produces bin/topograph, bin/node-observer, bin/node-data-broker, bin/kwok-nodes
git fetch origin main:master  # make lint needs a local `master` ref — see "Local Test Loop" below
make qualify                  # fmt + vet + lint + test — run this before every push
```

## Prerequisites

| Tool | Purpose | Notes |
|---|---|---|
| [Go 1.27.1+](https://go.dev/dl/) | Language runtime | See `go.mod` for the exact minimum; newer minor versions are fine |
| `make` | Build automation | Pre-installed on macOS/Linux |
| [golangci-lint](https://golangci-lint.run/usage/install/) | Go linting | `brew install golangci-lint`, or see the [install guide](https://golangci-lint.run/usage/install/) for `go install`/binary options; CI runs it via `golangci/golangci-lint-action@v9` |
| [helm 3.10+ or 4.x](https://helm.sh/docs/intro/install/) | Chart lint/test | Required for `make chart-test`; CI pins `v4.1.1` in `.github/workflows/chart-test.yaml` |
| [docker](https://docs.docker.com/get-docker/) | Container image builds | Only needed for `make image-build` / `make docker-buildx` |

There is no `make dev-env-setup` / tool-manifest step in this repo — install
the tools above with your system package manager and you're ready to build.

## Clone and Build

```bash
git clone https://github.com/dsx-ai-factory/topograph.git
cd topograph
make build                  # host OS/arch: bin/topograph, bin/node-observer, bin/node-data-broker, bin/kwok-nodes
make build-linux-amd64      # cross-compile; also build-darwin-arm64, build-linux-arm64, build-darwin-amd64
make clean                  # remove bin/
```

## Local Test Loop

```bash
make fmt        # go fmt ./...
make vet        # go vet ./...
make lint       # golangci-lint run --new-from-rev, computed from a local `master` ref
make test       # go test -race -coverprofile=coverage.out ./...
make coverage   # human-readable per-package summary (runs test first)
make qualify    # fmt + vet + lint + test, in that order — run this before pushing
```

`make lint` computes its diff base from a local `master` ref
(`git rev-list master.. --count`), but this repo's default branch is `main`
— a fresh clone has no local `master` and the target fails with a revision
error. Work around it by syncing a local `master` ref to `origin/main`
before running `make lint` or `make qualify`. Keep this ref as a
lint-only bookkeeping branch — don't check it out or commit on it:

```bash
git fetch origin main:master
```

This only works while `master` isn't checked out and hasn't diverged from
`origin/main` (git fetch won't fetch into your current branch, and refuses
a non-fast-forward update). If it fails for either reason, switch off
`master` first, then reset it directly instead of fetching into it:

```bash
git branch -f master origin/main
```

Run a single package's tests directly with the standard Go toolchain while
iterating, e.g.:

```bash
go test ./pkg/providers/aws/...
go test -run TestGenerateTopologyConfig ./pkg/providers/aws/...
```

### Helm chart tests

Only needed when you change `charts/topograph/`; CI runs `make chart-test` on
every workflow trigger regardless.

```bash
make chart-test                  # helm lint + helm-unittest suites (charts/topograph/tests/)
make chart-test-update-snapshot  # refresh snapshots after an intentional template/values change — review the diff before committing
```

`make chart-test` installs the `helm-unittest` plugin automatically if it's
missing (pinned version in `Makefile` via `HELM_UNITTEST_VERSION`).

### Dependencies

```bash
make mod        # go mod tidy
```

## Running a Binary Locally

The three long-running service binaries (`topograph`, `node-observer`,
`node-data-broker`) each read a YAML config via `-c`/`-config` and print
their version via `-version`. There's no `AUTO_MODE` / interactive
installer to worry about — just point the binary at a config file:

```bash
# API server — sample config at config/topograph-config.yaml
./bin/topograph -c config/topograph-config.yaml

# Node Observer (Kubernetes-only)
./bin/node-observer -c /path/to/node-observer-config.yaml

# Node Data Broker (Kubernetes-only DaemonSet)
./bin/node-data-broker --config /path/to/node-data-broker-config.yaml
```

`kwok-nodes` is a one-shot generator tool, not a service, and takes no
config file — it reads a `tests/models/` fixture via `-model` and writes a
KWOK node manifest via `-output`:

```bash
./bin/kwok-nodes -model small-tree.yaml -output -
```

The `-model` flag also accepts file paths to use external model files.

For an end-to-end local Kubernetes environment (KWOK-based, no real cluster
needed), see the interactive demos under [`demos/`](demos/) — e.g.
`demos/test-k8s/` and `demos/oci-sim-k8s/`.

## Project Architecture

See the repository map and provider/engine invariants in
[`.claude/CLAUDE.md`](.claude/CLAUDE.md) (section 1) — that document is the
canonical description of `cmd/`, `pkg/`, `internal/`, and `charts/topograph/`
layout and is kept in sync with the actual repo structure. Don't duplicate it
here; update `.claude/CLAUDE.md` and `AGENTS.md` together if the layout
changes.

## Adding a Provider or Engine

Follow the step-by-step checklists in [`.claude/CLAUDE.md`](.claude/CLAUDE.md)
section 4 ("Adding a new provider" / "Adding a new engine"). In short: a
provider lives in `pkg/providers/<name>/`, exposes a `NamedLoader`, and must
be registered in `pkg/registry/registry.go` plus documented in
`docs/providers/<name>.md` and `docs/overview.md`. New engines are rarer and
require maintainer coordination first — see
[`CONTRIBUTING.md`](CONTRIBUTING.md#open-an-issue-first).

## Building Container Images

```bash
make image-build     # builds ghcr.io/dsx-ai-factory/topograph:<current-branch> for host OS/arch
make image-push       # image-build, then docker push
make docker-buildx    # multi-platform build (linux/arm64,linux/amd64) and push via buildx
```

Override the target/tag with `IMAGE_REPO` and `IMAGE_TAG`, e.g.:

```bash
IMAGE_REPO=ghcr.io/myuser/topograph IMAGE_TAG=dev make image-build
```

## Packaging (Slurm deb/rpm)

```bash
make deb    # ARCH=$(GOARCH) scripts/build-deb.sh <git-ref> <package-revision>
make rpm    # ARCH=$(GOARCH) scripts/build-rpm.sh <git-ref> <package-revision>
```

## CI Parity

CI doesn't invoke `make test` / `make lint` directly — it runs the
underlying tools with CI-specific flags, which are close to but not
identical to the local `make` targets:

- `.github/workflows/go.yml` — build; `go test -v -coverpkg=./... -coverprofile=coverage.out -covermode=atomic ./...`
  (coverage-instrumented, but *without* `-race`, unlike `make test`);
  `golangci/golangci-lint-action@v9` (not `make lint`, so it isn't affected
  by the local `master`-ref issue above); and `govulncheck`
- `.github/workflows/chart-test.yaml` — `make chart-test` on every push/PR
- `.github/workflows/k8s-test.yaml` — Kubernetes integration tests (kind +
  KWOK) on `pull-request/*` branches
- `.github/workflows/slinky-test.yaml` — Slinky engine integration tests
- `.github/workflows/docker.yml` — container image build (manual trigger)
- `.github/workflows/helm-release.yaml` — Helm chart release (manual trigger)

`make qualify` and `make chart-test` (when charts changed) passing locally
is a strong signal, not a guarantee of an identical CI run — in particular,
`make test`'s `-race` detector runs checks CI's coverage-only invocation
doesn't, and the reverse is also possible in principle.

## Before Opening a Pull Request

See [`CONTRIBUTING.md`](CONTRIBUTING.md) for the issue-first workflow, DCO
sign-off requirements, and the full pre-push checklist.
