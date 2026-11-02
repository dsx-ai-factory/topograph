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

CONTROL_PLANE_LABEL="node-role.kubernetes.io/control-plane"

control_plane_node=$(kubectl --context "$KUBE_CONTEXT" get nodes -l "$CONTROL_PLANE_LABEL" -o jsonpath='{.items[0].metadata.name}')
if [[ -z "$control_plane_node" ]]; then
  echo "No control-plane node found in context: $KUBE_CONTEXT" >&2
  exit 1
fi

kubectl --context "$KUBE_CONTEXT" label node "$control_plane_node" nfd-enabled=true --overwrite

helm repo add --force-update nfd https://kubernetes-sigs.github.io/node-feature-discovery/charts

helm repo update

helm upgrade --install nfd nfd/node-feature-discovery \
  --kube-context "$KUBE_CONTEXT" \
  --namespace node-feature-discovery \
  --create-namespace \
  --set-string worker.nodeSelector.nfd-enabled=true \
  --set featureGates.NodeFeatureGroupAPI=true \
  --wait
