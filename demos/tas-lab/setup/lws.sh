#!/usr/bin/env bash

# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# Setup step 4: install LeaderWorkerSet (LWS_VERSION in versions.env). Kueue's LeaderWorkerSet
# integration is on by default, so LWS groups become Kueue Workloads that TAS places.

set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/../lib/common.sh"

wait_for_api 30

kubectl apply --server-side --force-conflicts -f \
    "https://github.com/kubernetes-sigs/lws/releases/download/$LWS_VERSION/manifests.yaml"
kubectl -n lws-system rollout status deploy/lws-controller-manager --timeout=180s
