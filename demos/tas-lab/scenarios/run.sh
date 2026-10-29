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

# Runs the Chainsaw scenarios against the lab cluster with the lab's settings.
#
#   scenarios/run.sh                                 every scenario
#   scenarios/run.sh scenarios/05-kueue-label-keys   one scenario (path relative to demos/tas-lab)
#   scenarios/run.sh --include-test-regex 'chainsaw/too-big'   cases whose name matches (the regex
#                                                    starts with "chainsaw/", Go's -run syntax)
#
# The scenarios share one cluster and change the labels of every node, so run one thing at a time
# and never two runs at once.
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/../lib/common.sh"

# Absolute lab directory for the test scripts (see templates/multitier-labels.yaml).
export TAS_LAB_DIR="$lab_dir"
export PATH="$lab_dir/.bin:$PATH"
command -v chainsaw >/dev/null || { echo "chainsaw is not installed; run setup/chainsaw.sh" >&2; exit 1; }

# A directory argument may be relative to where you are or to demos/tas-lab; pass it on absolute, so
# the result does not depend on the current directory. Only a directory inside scenarios/ counts: a
# flag value that happens to name another directory (a report path, say) is passed on unchanged.
# With no directory given, run every scenario.
args=()
has_dir=false
for arg in "$@"; do
    dir=""
    for candidate in "$arg" "$lab_dir/$arg"; do
        [[ -d "$candidate" ]] && dir="$(cd -- "$candidate" && pwd)" && break
    done
    if [[ "$dir" == "$lab_dir/scenarios" || "$dir" == "$lab_dir/scenarios/"* ]]; then
        args+=("$dir"); has_dir=true
    else
        args+=("$arg")
    fi
done
$has_dir || args+=("$lab_dir/scenarios")
exec chainsaw test --config "$lab_dir/scenarios/chainsaw-config.yaml" --kube-context "$ctx" "${args[@]}"
