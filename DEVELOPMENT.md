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
make qualify                  # fmt + vet + lint + test — run this before every push
```

## Prerequisites

| Tool                                                      | Purpose                | Notes                                                                                                                                                                                 |
| --------------------------------------------------------- | ---------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| [Go 1.27.3+](https://go.dev/dl/)                          | Language runtime       | See `go.mod` for the exact minimum; newer minor versions are fine                                                                                                                     |
| `make`                                                    | Build automation       | Pre-installed on macOS/Linux                                                                                                                                                          |
| [golangci-lint 2.14.0+](https://golangci-lint.run/usage/install/) | Go linting             | `brew install golangci-lint`, or see the [install guide](https://golangci-lint.run/usage/install/) for `go install`/binary options; CI runs it via `golangci/golangci-lint-action` in `go.yml` |
| [helm 3.10+ or 4.x](https://helm.sh/docs/intro/install/)  | Chart lint/test        | Required for `make chart-test`; CI pins `v4.1.1` in `.github/workflows/chart-test.yaml`                                                                                               |
| [docker](https://docs.docker.com/get-docker/)             | Container image builds | Only needed for `make image-build` / `make docker-buildx`                                                                                                                             |

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
make lint       # golangci-lint run --new-from-rev, computed from origin `main` ref
make test       # go test -race -coverprofile=coverage.out ./...
make coverage   # human-readable per-package summary (runs test first)
make qualify    # fmt + vet + lint + test, in that order — run this before pushing
```

`make lint` requires the local `origin/main` remote-tracking reference. If it
is missing, the target stops before running the linter and prints a recovery
command:

```bash
git fetch origin main:refs/remotes/origin/main
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

### License headers

```bash
make license-headers       # add the standard license header to files that have none
make license-header-check  # list files without a header; CI runs this
```

Both run [addlicense](https://github.com/google/addlicense) at a pinned
version through `go run`, over the files git tracks plus new files it does not
ignore, so a new file is covered before you `git add` it. The header
text is `scripts/license-header.tmpl`. `make license-headers` skips Helm
templates (`charts/topograph/templates/`), which take the header by hand as a
`{{- /* ... */ -}}` comment so it does not render into the manifest.

The header is NVIDIA's combined SPDX and Apache 2.0 notice. Its year is the
year the file was created: `make license-headers` uses the current year, which
is right for new files. When adding a header to an older file by hand, use the
year of its first commit (`git log --diff-filter=A --format=%ad -- <file>`).
NVIDIA's copyright guidance warns against post-dating a notice.

## Running a Binary Locally

The three long-running service binaries (`topograph`, `node-observer`,
`node-data-broker`) each read a YAML config via `-c`/`--config` and print
their version via `--version`. There's no `AUTO_MODE` / interactive
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
config file — it reads a `tests/models/` fixture via `--model/-m` and writes a
KWOK node manifest via `--output`:

```bash
./bin/kwok-nodes --model small-tree.yaml --output -
```

The `--model` flag also accepts file paths to use external model files.

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
make image-build     # builds ghcr.io/dsx-ai-factory/topograph:<version> for host OS/arch
make image-push       # image-build, then docker push
make docker-buildx    # multi-platform build (linux/arm64,linux/amd64) and push via buildx
```

`<version>` is `GIT_REF`, the same value stamped into the binaries' `--version`
output (see the comment above `GIT_REF` in the `Makefile`): the `vX.Y.Z` tag
at `HEAD` (`vX.Y.Z-dirty` when tracked files are modified), else the current
branch name, else the short commit SHA, else `unknown` when no git metadata is
available. Pass `GIT_REF=...` to set it for `make build`, `make deb`,
`make rpm` and the default image tag. The image build is the exception: the
Dockerfile runs `make` again inside the build without `GIT_REF`, so the
binaries in the image always take their version from the git metadata in the
build context, and `make image-build GIT_REF=...` changes only the tag. Passing
the value into the Docker build is tracked in #606.

Override the target/tag with `IMAGE_REPO` and `IMAGE_TAG`, e.g.:

```bash
IMAGE_REPO=ghcr.io/myuser/topograph IMAGE_TAG=dev make image-build
```

Release images are published by the Docker workflow. On a release tag
checkout, `make image-push` and `make docker-buildx` therefore refuse to push a
default `v*` tag to `ghcr.io/dsx-ai-factory/topograph`; set `IMAGE_TAG`
explicitly or push to another `IMAGE_REPO`.

## Packaging (Slurm deb/rpm)

```bash
make deb    # ARCH=$(GOARCH) scripts/build-deb.sh <version> <package-revision>
make rpm    # ARCH=$(GOARCH) scripts/build-rpm.sh <version> <package-revision>
```

The package version is the same `GIT_REF` value as the default image tag,
normalized
by `scripts/build-deb.sh` and `scripts/build-rpm.sh` (for example, the leading
`v` is dropped). When no git metadata is available (for example a source
tarball) it is `unknown` and `make` prints a warning; build from a tag
checkout or pass `GIT_REF=vX.Y.Z`. `PACKAGE_REVISION` sets the optional
package release.

## CI Parity

CI doesn't invoke `make test` / `make lint` directly. It runs the
underlying tools with CI-specific flags, which are close to but not
identical to the local `make` targets.

Pull request checks run on the `pull-request/<n>` branch that copy-pr-bot
pushes for each PR; the first four also run on `main`:

- `.github/workflows/go.yml`: build; `go test -v -coverpkg=./... -coverprofile=coverage.out -covermode=atomic ./...`
  (coverage-instrumented, but _without_ `-race`, unlike `make test`);
  golangci-lint through `golangci/golangci-lint-action`; `make vet`; a
  `go fmt` check; and `govulncheck ./...`
- `.github/workflows/license-scan.yml`: `go-licenses check ./...` against
  the Apache-2.0, BSD-2-Clause, BSD-3-Clause, ISC, and MIT allow list, and
  `make license-header-check`
- `.github/workflows/workflow-lint.yaml`: when `.github/**` changes, plus
  weekly and on demand so zizmor's online audits catch new advisories
  against pinned actions. actionlint fails the run on any finding; zizmor
  uploads its security findings to code scanning without failing the run
- `.github/workflows/workflow-tests.yaml`: when `.github/**` changes, plus on
  demand, `node --test '.github/tests/**/*.test.mjs'`: unit tests for the
  scripts inside workflows, the security rules every workflow keeps, and the
  check that every label a form or workflow applies is in `.github/labels.yml`
- `.github/workflows/chart-test.yaml`: `make chart-test`
- `.github/workflows/k8s-test.yaml`: Kubernetes integration tests (kind +
  KWOK); can also be run manually
- `.github/workflows/slinky-test.yaml`: Slinky engine integration tests; can
  also be run manually
- `.github/workflows/fern-docs-ci.yml`: Fern docs checks when `docs/` or
  `fern/` changes
- `.github/workflows/fern-docs-preview-build.yml` and
  `.github/workflows/fern-docs-preview-comment.yml`: build a Fern docs
  preview when `docs/` or `fern/` changes and post its link on the PR

`.github/workflows/pr-title.yaml` runs on the pull request itself rather than
on the `pull-request/<n>` branch, since it reads only the title: it checks that
the title follows the Conventional Commits format in `CONTRIBUTING.md`. It has
no local equivalent.

Release and maintenance workflows:

- `.github/workflows/docker.yml`: on manual dispatch, builds the
  `linux/amd64` and `linux/arm64` image, uploads it to NVIDIA staging, and
  attests its provenance
- `.github/workflows/publish-fern-docs.yml`: on a `vX.Y.Z` tag push or
  manual dispatch, publishes the versioned docs site
- `.github/workflows/helm-release.yaml`: on manual dispatch, packages the Helm
  chart and uploads it to NGC
- `.github/workflows/scorecard.yaml`: OpenSSF Scorecard analysis on pushes
  to `main`, weekly, and on demand

After internal security approval, the chart is promoted unchanged to the public
Helm repository at `https://helm.ngc.nvidia.com/nvidia/topograph`, as described in
[RELEASE.md](RELEASE.md).

Every `actions/setup-go` step reads the Go version from `go.mod`, so a
toolchain bump there also moves the CI jobs that use it. The container
image build does not: `Dockerfile` pins its own `golang` builder image,
which Dependabot's `docker` ecosystem bumps, so keep it on the same Go
version as `go.mod`. Every action is pinned to a commit SHA, and
Dependabot keeps those pins current.

To lint workflow changes before pushing, run
[actionlint](https://github.com/rhysd/actionlint) and
[zizmor](https://docs.zizmor.sh/) from the repository root, at the
versions pinned in `workflow-lint.yaml`. actionlint picks up
`.github/actionlint.yaml`, which lists the self-hosted runner labels. CI
turns off actionlint's shellcheck and pyflakes integrations, so pass the
same flags to get the same result. zizmor runs its online audits (action
references and known advisories) only when it has a GitHub token, as it
does in CI; without one it checks offline findings only. CI also passes
`--no-exit-codes`, because the findings accepted in `workflow-lint.yaml`
would otherwise fail the run:

```bash
actionlint -shellcheck= -pyflakes=
GH_TOKEN="$(gh auth token)" zizmor --no-exit-codes .
```

To run the workflow tests as CI does, run this from the repository root. It
needs Node 22 or later, the version on `ubuntu-latest`, and
[yq](https://github.com/mikefarah/yq) v4 on `PATH`; no npm install is needed.
Keep the glob quoted so that Node expands it, not the shell:

```bash
node --test '.github/tests/**/*.test.mjs'
```

`make qualify` and `make chart-test` (when charts changed) passing locally
is a strong signal, not a guarantee of an identical CI run. In particular,
`make test`'s `-race` detector runs checks CI's coverage-only invocation
doesn't, and the reverse is also possible in principle.

## Before Opening a Pull Request

See [`CONTRIBUTING.md`](CONTRIBUTING.md) for the issue-first workflow, DCO
sign-off requirements, and the full pre-push checklist.
