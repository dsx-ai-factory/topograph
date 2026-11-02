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

set -e

KUBE_CONTEXT="${KUBE_CONTEXT:-kind-topograph}"

kubectl --context "$KUBE_CONTEXT" get nodes -l kwok.x-k8s.io/node=fake \
    -o jsonpath='{range .items[*]}{.metadata.name}{"\n"}{end}' |
while read -r node; do
    value=$(kubectl --context "$KUBE_CONTEXT" get node "$node" \
        -o jsonpath='{.metadata.annotations.accelerator\.topology\.test/domain}')
    if [ -n "$value" ]; then
        kubectl --context "$KUBE_CONTEXT" label node "$node" \
            "nvidia.com/gpu.clique=$value" "kubernetes.io/os=linux" --overwrite
    fi
done
