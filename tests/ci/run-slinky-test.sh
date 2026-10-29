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

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd -- "$script_dir/../.." && pwd)"
kube_context="${KUBE_CONTEXT:-kind-topograph}"
topology_wait_timeout="${TOPOLOGY_WAIT_TIMEOUT:-5m}"

verify_topology_config() {
    local namespace="$1"
    local config_map_name="$2"
    local expected_file="$3"
    local config_map
    local topology_config

    if ! config_map="$(kubectl --context "$kube_context" \
        --namespace "$namespace" get configmap "$config_map_name" -o json 2>/dev/null)"; then
        echo "Error: ConfigMap $namespace/$config_map_name does not exist or cannot be read" >&2
        return 1
    fi

    topology_config="$(jq -r '.data["topology.conf"] // empty' <<<"$config_map")"
    if [[ -z "$topology_config" ]]; then
        echo "Error: topology.conf in ConfigMap $namespace/$config_map_name is empty or missing" >&2
        return 1
    fi

    echo "Content of topology.conf in ConfigMap $namespace/$config_map_name:"
    printf '%s\n' "$topology_config"

    diff -u "$expected_file" <(printf '%s\n' "$topology_config") >/dev/null
}

get_configmap_resource_version() {
    local namespace="$1"
    local config_map_name="$2"

    kubectl --context "$kube_context" --namespace "$namespace" \
        get configmap "$config_map_name" \
        -o jsonpath='{.metadata.resourceVersion}'
}

wait_for_topology_config() {
    local namespace="$1"
    local config_map_name="$2"
    local expected_file="$3"
    local description="$4"
    local resource_version="$5"

    if KUBE_CONTEXT="$kube_context" \
        TOPOLOGY_WAIT_TIMEOUT="$topology_wait_timeout" \
        "$repo_root/scripts/wait-configmap-update.sh" \
            "$namespace" "$config_map_name" "$resource_version"; then
        if verify_topology_config "$namespace" "$config_map_name" "$expected_file"; then
            return 0
        fi
        echo "::error::$description did not match after the ConfigMap update"
    else
        echo "::error::Timed out waiting for $description"
    fi

    diff -u "$expected_file" \
        <(kubectl --context "$kube_context" --namespace "$namespace" \
            get configmap "$config_map_name" \
            -o jsonpath='{.data.topology\.conf}' 2>/dev/null) || true
    kubectl --context "$kube_context" --namespace "$namespace" \
        get configmap "$config_map_name" -o yaml || true
    return 1
}

echo "Running Slinky test: OCI simulation provider with tree topology"
resource_version="$(get_configmap_resource_version slurm slurm-config-extra)"
"$repo_root/scripts/install-topograph.sh" \
    "$script_dir/values.slinky-oci-sim-tree.yaml"
wait_for_topology_config \
    slurm \
    slurm-config-extra \
    "$script_dir/expected.slinky-oci-sim-tree.conf" \
    "OCI Slinky tree topology output" \
    "$resource_version"
