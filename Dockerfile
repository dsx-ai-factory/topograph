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
FROM golang:1.27.4@sha256:5e8afd493a7124a8586e134d9b532731161d4b9becf9dee5a5956b107426f93d AS builder

WORKDIR /go/src/github.com/dsx-ai-factory/topograph
COPY . .

ARG TARGETOS
ARG TARGETARCH

RUN make build-${TARGETOS}-${TARGETARCH}

FROM ubuntu:24.04@sha256:fe81560990fae86dc4743202b2cb4d3df98d0bcf949173cecef15ce1127ef852

# Match the host lldpd version; the default allows Ubuntu package fixes.
ARG LLDP_VERSION=1.0.18-*

# lldpctl points to lldpcli; change its mode from 4750 to 0755 for the non-root
# broker. Socket access still requires the appropriate supplementalGroups.
# Ubuntu splits the InfiniBand utilities out of rdma-core.
RUN apt-get update \
    && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
        ca-certificates \
        rdma-core \
        ibverbs-utils \
        infiniband-diags \
        "lldpd=${LLDP_VERSION}" \
    && chmod 0755 /usr/sbin/lldpcli \
    && lldpctl -v \
    && command -v ibnetdiscover \
    && rm -rf /var/lib/apt/lists/*

COPY --from=builder /go/src/github.com/dsx-ai-factory/topograph/bin/* /usr/local/bin/

LABEL org.opencontainers.image.documentation="https://github.com/dsx-ai-factory/topograph/blob/main/docs/overview.md" \
    org.opencontainers.image.authors="NVIDIA CORPORATION" \
    org.opencontainers.image.vendor="NVIDIA"
