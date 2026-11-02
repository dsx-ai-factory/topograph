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

# Setup step 1: create the TAS lab kind cluster (Kubernetes version from versions.env, both scheduling
# gates on), and the RBAC that lets lib/capture-metrics.sh scrape the scheduler. The nodes carry no
# topology labels yet; Topograph adds them in step 2.

set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/../lib/common.sh"

installed_kind="$(kind version | awk '{print $2}')"
if [[ "$installed_kind" != "$KIND_VERSION" ]]; then
    echo "warning: kind $installed_kind is installed; the lab was tested with $KIND_VERSION (versions.env)" >&2
fi

kind create cluster --config "$lab_dir/setup/kind-config.yaml" --image "$KIND_NODE_IMAGE"
wait_for_api
kubectl wait --for=condition=Ready node --all --timeout=180s
kubectl label node tas-lab-worker tas-lab-worker2 tas-lab-worker3 tas-lab-worker4 tas-lab=worker --overwrite
kubectl apply -f "$lab_dir/setup/metrics-rbac.yaml"

step "Feature gates on the control plane"
for c in kube-apiserver kube-scheduler; do
    kubectl -n kube-system get pod -l component=$c \
        -o jsonpath='{.items[0].spec.containers[0].command}' | tr ',' '\n' | grep feature-gates || true
done
kubectl get nodes
