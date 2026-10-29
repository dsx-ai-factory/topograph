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

# Demonstrates DRA topology discovery and Slinky configuration using KWOK nodes.

set -e

demo_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"

cd "$demo_dir/../.."

source demos/utils.sh

step "make build TARGETS=kwok-nodes"

step "delete_cluster"

step "./scripts/create-kind-kwok-cluster.sh -m ./tests/models/large.yaml"

step "kubectl --context \"$KUBE_CONTEXT\" get node"

step "./demos/dra-slinky/update-labels.sh"

step "kubectl --context \"$KUBE_CONTEXT\" get node srv4101 -o yaml | yq '.metadata.labels'"

step "KUBE_CONTEXT=\"$KUBE_CONTEXT\" ./scripts/deploy-slinky.sh --compute-mode kwok"

step "kubectl --context \"$KUBE_CONTEXT\" -n slurm get cm slurm-config-extra -o yaml"

resource_version="$(kubectl --context "$KUBE_CONTEXT" -n slurm \
    get cm slurm-config-extra -o jsonpath='{.metadata.resourceVersion}')"

step "KUBE_CONTEXT=\"$KUBE_CONTEXT\" ./scripts/install-topograph.sh demos/dra-slinky/values.dra-slinky.kwok.yaml"

step "KUBE_CONTEXT=\"$KUBE_CONTEXT\" ./scripts/wait-configmap-update.sh slurm slurm-config-extra \"$resource_version\""

# step "kubectl --context \"$KUBE_CONTEXT\" -n topograph logs -l app.kubernetes.io/name=topograph"

step "kubectl --context \"$KUBE_CONTEXT\" -n slurm get cm slurm-config-extra -o jsonpath='{.data.topology\.conf}' | grep -v '#'"
