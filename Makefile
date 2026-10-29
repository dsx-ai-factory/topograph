# Copyright (c) 2024, NVIDIA CORPORATION.  All rights reserved.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

LINTER_BIN ?= golangci-lint
DOCKER_BIN ?= docker
GOOS ?= $(shell uname | tr '[:upper:]' '[:lower:]')
GOARCH ?= $(shell arch | sed 's/x86_64/amd64/')
PLATFORMS ?= linux/arm64,linux/amd64
TARGETS := topograph node-observer node-data-broker kwok-nodes
CMD_DIR := ./cmd
OUTPUT_DIR := ./bin

# Published image repository. image-push and docker-buildx refuse to push a
# release-like tag to it unless IMAGE_TAG is set explicitly (see
# guard-release-image-tag below).
RELEASE_IMAGE_REPO := ghcr.io/dsx-ai-factory/topograph
IMAGE_REPO ?=$(RELEASE_IMAGE_REPO)
# Version stamped into the binaries; also the default image tag and deb/rpm
# version. Resolved in order:
#   1. a release tag (v*) pointing exactly at HEAD, for tag builds, which check
#      out a detached HEAD; other tags such as docs/vX.Y.Z are ignored.
#      --tags is needed because CI checkouts carry the release tag as a
#      lightweight ref, and --dirty appends "-dirty" (vX.Y.Z-dirty) when
#      tracked files are modified;
#   2. the current branch name, for branch builds such as release-X.Y;
#   3. the short commit SHA, for any other detached checkout;
#   4. "unknown" when git or the .git directory is unavailable, for example
#      when building from a source tarball.
# A GIT_REF from the command line or environment takes precedence. The second
# assignment evaluates the git commands once instead of on every reference.
GIT_REF ?=$(shell git describe --tags --exact-match --dirty --match 'v[0-9]*' 2>/dev/null || \
  { ref=$$(git rev-parse --abbrev-ref HEAD 2>/dev/null) && [ "$$ref" != HEAD ] && echo "$$ref"; } || \
  git rev-parse --short HEAD 2>/dev/null || \
  echo unknown)
GIT_REF := $(GIT_REF)
IMAGE_TAG ?=$(GIT_REF)

# Recipe guard for image-push and docker-buildx: on a release tag checkout
# IMAGE_TAG defaults to vX.Y.Z (or vX.Y.Z-dirty), and pushing that to
# RELEASE_IMAGE_REPO would overwrite the image the Docker workflow published.
# Fails unless IMAGE_TAG was set explicitly or IMAGE_REPO points elsewhere.
define guard-release-image-tag
@if [ "$(IMAGE_REPO)" = "$(RELEASE_IMAGE_REPO)" ] && [ "$(origin IMAGE_TAG)" = file ]; then \
  case "$(IMAGE_TAG)" in v[0-9]*) \
    echo "error: refusing to push the release-like tag $(IMAGE_REPO):$(IMAGE_TAG);" \
      "release images are published by the Docker workflow." \
      "Set IMAGE_TAG explicitly or use another IMAGE_REPO." >&2; \
    exit 1;; \
  esac; \
fi
endef

.PHONY: build
build:
	@set -e; for target in $(TARGETS); do \
	  echo "Building $${target} for $(GOOS)/$(GOARCH)"; \
	  CGO_ENABLED=0 go build -a -trimpath -o $(OUTPUT_DIR)/$${target} \
	    -ldflags '-extldflags "-static" -X github.com/dsx-ai-factory/topograph/internal/version.Version=$(GIT_REF)' \
	    $(CMD_DIR)/$${target}; \
	done

# Builds binaries for the specified platform.
# Usage: make build-<os>-<arch>
# Example: make build-linux-amd64, make build-darwin-amd64, make build-darwin-arm64, make build-linux-arm64
.PHONY: build-%
build-%:
	@GOOS=$$(echo $* | cut -d- -f 1) GOARCH=$$(echo $* | cut -d- -f 2) $(MAKE) build

.PHONY: clean
clean:
	scripts/clean-build.sh

.PHONY: test
test:
	@echo running tests
	go test -coverprofile=coverage.out -covermode=atomic -race ./...

HELM_BIN ?= helm
HELM_UNITTEST_VERSION ?= v1.1.1

# Install the helm-unittest plugin if it is not already present. --verify=false
# is required when installing from the git repo under Helm 4 (no webhook GPG
# verification); it is harmless under Helm 3.
.PHONY: helm-unittest-plugin
helm-unittest-plugin:
	@if ! $(HELM_BIN) plugin list 2>/dev/null | grep -qE '^unittest'; then \
	  echo "Installing helm-unittest plugin $(HELM_UNITTEST_VERSION)..."; \
	  $(HELM_BIN) plugin install https://github.com/helm-unittest/helm-unittest.git --version $(HELM_UNITTEST_VERSION) --verify=false; \
	fi

# Lint the umbrella chart and run the helm-unittest suites under
# charts/topograph/tests/ (assertions + full-render snapshots).
.PHONY: chart-test
chart-test: helm-unittest-plugin
	$(HELM_BIN) lint charts/topograph
	$(HELM_BIN) unittest charts/topograph

# Refresh the helm-unittest snapshots under charts/topograph/tests/__snapshot__/
# after intentional template or values changes (review before commit).
.PHONY: chart-test-update-snapshot
chart-test-update-snapshot: helm-unittest-plugin
	$(HELM_BIN) unittest -u charts/topograph

.PHONY: fmt
fmt:
	go fmt ./...

.PHONY: vet
vet:
	go vet ./...

.PHONY: lint
lint:
	@base=$$(git merge-base HEAD refs/remotes/origin/main) || { \
	  echo "error: cannot determine lint base from origin/main; ensure the reference exists: git fetch origin main:refs/remotes/origin/main" >&2; \
	  exit 1; \
	}; \
	$(LINTER_BIN) run --new-from-rev "$$base" ./...

.PHONY: qualify
qualify: fmt vet lint test
	@echo "All quality checks passed."

.PHONY: mod
mod:
	go mod tidy

# License headers, through addlicense at a pinned version. New headers use the
# standard NVIDIA SPDX and Apache 2.0 notice in scripts/license-header.tmpl;
# the check accepts any existing copyright or license header, so older forms
# keep passing.
# Files are listed through git, so untracked trees such as localdev/ and other
# worktrees are never walked; `grep -v '/$$'` drops nested repositories, which
# git lists as directories. vendor/, testdata/ and the helm-unittest snapshots
# are skipped. Helm templates are skipped by license-headers only: a `#` header
# renders into the manifest, so they carry a {{/* */}} header added by hand,
# which the check still recognizes.
ADDLICENSE ?= go run github.com/google/addlicense@v1.2.0
LICENSE_HEADER_FILES = git ls-files --cached --others --exclude-standard | grep -v '/$$'
LICENSE_HEADER_IGNORE := -ignore 'vendor/**' -ignore '**/testdata/**' -ignore '**/__snapshot__/**'

.PHONY: license-headers
license-headers:
	$(LICENSE_HEADER_FILES) | xargs $(ADDLICENSE) -f scripts/license-header.tmpl \
	  $(LICENSE_HEADER_IGNORE) -ignore 'charts/topograph/templates/**'

.PHONY: license-header-check
license-header-check:
	$(LICENSE_HEADER_FILES) | xargs $(ADDLICENSE) -check $(LICENSE_HEADER_IGNORE)

.PHONY: coverage
coverage: test
	go tool cover -func=coverage.out

.PHONY: image-build
image-build:
	$(DOCKER_BIN) build --build-arg TARGETOS=$(GOOS) --build-arg TARGETARCH=$(GOARCH) -t $(IMAGE_REPO):$(IMAGE_TAG) -f ./Dockerfile .

# The guard runs before the build, so image-build is invoked as a sub-make
# (with the resolved repository and tag) rather than listed as a prerequisite.
.PHONY: image-push
image-push:
	$(guard-release-image-tag)
	$(MAKE) image-build IMAGE_REPO=$(IMAGE_REPO) IMAGE_TAG=$(IMAGE_TAG)
	$(DOCKER_BIN) push $(IMAGE_REPO):$(IMAGE_TAG)

.PHONY: docker-buildx
docker-buildx:
	$(guard-release-image-tag)
	- $(DOCKER_BIN) buildx create --name=topograph-builder
	$(DOCKER_BIN) buildx use topograph-builder
	$(DOCKER_BIN) buildx build --platform $(PLATFORMS) -t $(IMAGE_REPO):$(IMAGE_TAG) -f ./Dockerfile --push .
	- $(DOCKER_BIN) buildx rm topograph-builder

.PHONY: ssl
ssl:
	SSL_DIR=ssl ./scripts/configure-ssl.sh

.PHONY: deb rpm
deb rpm: build
	@if [ "$(GIT_REF)" = unknown ]; then \
	  echo "warning: no git metadata found, so the package version is \"unknown\";" \
	    "pass GIT_REF=vX.Y.Z to set it." >&2; \
	fi
	ARCH=$(GOARCH) scripts/build-$@.sh $(GIT_REF) $(PACKAGE_REVISION)
