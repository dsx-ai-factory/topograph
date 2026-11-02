#!/usr/bin/env bash

# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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

# Setup step 4: install LeaderWorkerSet (LWS_VERSION in versions.env). Kueue's LeaderWorkerSet
# integration is on by default, so LWS groups become Kueue Workloads that TAS places.

set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/../lib/common.sh"

wait_for_api 30

kubectl apply --server-side --force-conflicts -f \
    "https://github.com/kubernetes-sigs/lws/releases/download/$LWS_VERSION/manifests.yaml"
kubectl -n lws-system rollout status deploy/lws-controller-manager --timeout=180s
