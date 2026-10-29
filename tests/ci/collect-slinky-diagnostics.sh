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

set -u

kube_context="${KUBE_CONTEXT:-kind-topograph}"
topograph_namespace="${TOPOGRAPH_NAMESPACE:-topograph}"
topograph_release="${TOPOGRAPH_RELEASE:-topograph}"
operator_namespace="${SLINKY_OPERATOR_NAMESPACE:-slinky}"
slurm_namespace="${SLURM_NAMESPACE:-slurm}"

run() {
    printf '\033[33m+'
    printf ' %q' "$@"
    printf '\033[0m\n'
    "$@" || true
}

run kubectl --context "$kube_context" get nodes --show-labels

run kubectl --context "$kube_context" --namespace "$slurm_namespace" \
    get pods -o wide

run kubectl --context "$kube_context" --namespace "$topograph_namespace" \
    get pods -o wide

run kubectl --context "$kube_context" --namespace "$topograph_namespace" logs \
    --selector "app.kubernetes.io/instance=$topograph_release" \
    --all-containers --prefix --tail=200

run kubectl --context "$kube_context" --namespace "$operator_namespace" logs \
    --selector app.kubernetes.io/instance=slurm-operator \
    --all-containers --prefix --tail=200

run kubectl --context "$kube_context" --namespace "$slurm_namespace" \
    get configmap slurm-config-extra -o yaml
