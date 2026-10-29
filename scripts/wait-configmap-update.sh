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

set -euo pipefail

if (( $# != 3 )); then
    echo "usage: $0 <namespace> <configmap-name> <resource-version>" >&2
    exit 2
fi

namespace="$1"
config_map_name="$2"
resource_version="$3"
kube_context="${KUBE_CONTEXT:-kind-topograph}"
wait_timeout="${TOPOLOGY_WAIT_TIMEOUT:-3m}"
updated_resource_version=""

echo "Waiting up to $wait_timeout for ConfigMap $namespace/$config_map_name" \
    "to change from resourceVersion $resource_version"

IFS= read -r updated_resource_version < <(
    kubectl --context "$kube_context" \
        --request-timeout="$wait_timeout" \
        --namespace "$namespace" \
        get configmaps \
        --field-selector "metadata.name=$config_map_name" \
        --watch \
        -o jsonpath='{.metadata.resourceVersion}{"\n"}' |
        awk -v current="$resource_version" \
            '("rv:" $0) != ("rv:" current) { print; exit }'
) || true

if [[ -z "$updated_resource_version" ]]; then
    echo "Timed out waiting for ConfigMap $namespace/$config_map_name to update" >&2
    exit 1
fi

echo "ConfigMap $namespace/$config_map_name changed to resourceVersion" \
    "$updated_resource_version"
