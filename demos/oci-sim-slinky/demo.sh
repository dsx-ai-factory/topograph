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

# Demonstrates OCI-sim fabric discovery and Slinky tree topology on kind.

set -e

demo_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"

cd "$demo_dir/../.."

source demos/utils.sh

step "make build TARGETS=kwok-nodes"

step "delete_cluster"

step "kind create cluster --name \"${KUBE_CONTEXT#kind-}\" --wait 120s"

step "kubectl --context \"$KUBE_CONTEXT\" get node"

step "KUBE_CONTEXT=\"$KUBE_CONTEXT\" ./scripts/deploy-slinky.sh --compute-mode none"

step "kubectl --context \"$KUBE_CONTEXT\" -n slurm get cm slurm-config-extra -o yaml"

resource_version="$(kubectl --context "$KUBE_CONTEXT" -n slurm \
    get cm slurm-config-extra -o jsonpath='{.metadata.resourceVersion}')"

step "KUBE_CONTEXT=\"$KUBE_CONTEXT\" ./scripts/install-topograph.sh demos/oci-sim-slinky/values.oci-sim-slinky.yaml"

step "KUBE_CONTEXT=\"$KUBE_CONTEXT\" ./scripts/wait-configmap-update.sh slurm slurm-config-extra \"$resource_version\""

step "kubectl --context \"$KUBE_CONTEXT\" -n slurm get cm slurm-config-extra -o jsonpath='{.data.topology\.conf}' | grep -v '#'"
