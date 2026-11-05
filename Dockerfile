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

RUN make build-${TARGETOS}-${TARGETARCH}

FROM ubuntu:24.04@sha256:fe81560990fae86dc4743202b2cb4d3df98d0bcf949173cecef15ce1127ef852

# lldpctl's control-socket protocol depends on the host lldpd build. Use the
# Ubuntu Noble 1.0.18 package family to match hosts running lldpd 1.0.18;
# allow Ubuntu package revisions so distribution fixes are still installed.
# Ubuntu installs lldpcli (target of the lldpctl symlink) as 4750 _lldpd:adm, so
# the non-root, no-privilege-escalation broker (uid 65532) cannot execute it.
# Make it 0755 (this also drops the unused setuid bit); access to the host lldpd
# socket is still governed by the pod's supplementalGroups.
# Ubuntu splits the InfiniBand utilities out of rdma-core.
RUN apt-get update \
    && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
        ca-certificates \
        rdma-core \
        ibverbs-utils \
        infiniband-diags \
        'lldpd=1.0.18-*' \
    && chmod 0755 /usr/sbin/lldpcli \
    && lldpctl -v \
    && command -v ibnetdiscover \
    && rm -rf /var/lib/apt/lists/*

COPY --from=builder /go/src/github.com/dsx-ai-factory/topograph/bin/* /usr/local/bin/

LABEL org.opencontainers.image.documentation="https://github.com/dsx-ai-factory/topograph/blob/main/docs/overview.md" \
    org.opencontainers.image.authors="NVIDIA CORPORATION" \
    org.opencontainers.image.vendor="NVIDIA"
