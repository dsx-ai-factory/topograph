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

if (( $# < 1 || $# > 2 )); then
    echo "usage: $0 <values-file> [<expected-accelerator>]" >&2
    exit 2
fi

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd -- "$script_dir/../.." && pwd)"
values_file="$1"
expected_accelerator="${2:-}"
kube_context="${KUBE_CONTEXT:-kind-topograph}"
test_node="${TEST_NODE:-1202}"
wait_attempts="${WAIT_ATTEMPTS:-60}"

if [[ "$values_file" != /* ]]; then
    values_file="$repo_root/$values_file"
fi

"$repo_root/scripts/install-topograph.sh" "$values_file"

for attempt in $(seq 1 "$wait_attempts"); do
    if kubectl --context "$kube_context" get node "$test_node" -o json |
        jq -e \
            --arg accelerator "$expected_accelerator" \
            --arg tier0 "$EXPECTED_TIER_0" \
            --arg tier1 "$EXPECTED_TIER_1" \
            --arg tier2 "$EXPECTED_TIER_2" '
            .metadata.labels["fabric.topograph.run/tier-0"] == $tier0 and
            .metadata.labels["fabric.topograph.run/tier-1"] == $tier1 and
            .metadata.labels["fabric.topograph.run/tier-2"] == $tier2 and
            (($accelerator == "") or
             (.metadata.labels["accelerator.topograph.run/domain"] == $accelerator))
        ' >/dev/null; then
        exit 0
    fi
    sleep 2
done

echo "::error::Timed out waiting for Topograph labels on node $test_node"
kubectl --context "$kube_context" get node "$test_node" -o yaml
exit 1
