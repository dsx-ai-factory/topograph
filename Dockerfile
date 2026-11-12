# SPDX-FileCopyrightText: Copyright (c) 2024 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
# http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# Base images are pinned by digest; Dependabot's docker ecosystem
# (.github/dependabot.yml) keeps the tag and digest pairs current.
FROM golang:1.27.3@sha256:e519d3b0a5b285e5aa53ab9eef02bc6bbb9cb04608389b743a136a9ce75f0d1e AS builder

WORKDIR /go/src/github.com/dsx-ai-factory/topograph
COPY . .

ARG TARGETOS
ARG TARGETARCH
# Version to stamp into the binaries, resolved on the host by make image-build
# and make docker-buildx. When it is empty or unknown, make resolves the version
# from the git metadata in the build context instead.
ARG GIT_REF

RUN if [ -n "${GIT_REF}" ] && [ "${GIT_REF}" != unknown ]; then \
      make GIT_REF="${GIT_REF}" build-${TARGETOS}-${TARGETARCH}; \
    else \
      unset GIT_REF; make build-${TARGETOS}-${TARGETARCH}; \
    fi

FROM alpine:3.24@sha256:be2656061b536c1883c4034790e21d0a09c5aaaf51968b89368a3c0371ea2c9a

RUN apk add --no-cache rdma-core

COPY --from=builder /go/src/github.com/dsx-ai-factory/topograph/bin/* /usr/local/bin/

LABEL org.opencontainers.image.documentation="https://github.com/dsx-ai-factory/topograph/blob/main/docs/overview.md" \
    org.opencontainers.image.authors="NVIDIA CORPORATION" \
    org.opencontainers.image.vendor="NVIDIA"
