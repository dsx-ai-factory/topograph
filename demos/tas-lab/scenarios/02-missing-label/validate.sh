#!/usr/bin/env bash

# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -euo pipefail
here="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
source "$here/../../lib/common.sh"

result="$state_dir/missing-label.result"
val() { { grep "^$1=" "$result" || true; } | cut -d= -f2-; }

echo "Scenario 02: a node that lacks the constraint key"
[[ -s "$result" ]] || { echo "no result file; run run.sh first"; exit 1; }
check "the gang stays Pending although the unlabelled node is free" 1 "$(val missing.pending)"
check "no pod is placed on the unlabelled node" 0 "$(val missing.on_unlabelled)"
check_gt "placements were generated" "$(metric_delta missing-label '^scheduler_generated_placements_total')"
check "the unlabelled node adds no placement (nvl-a, nvl-c per attempt)" 0 "$(( $(metric_delta missing-label '^scheduler_generated_placements_total') % 2 ))"
finish
