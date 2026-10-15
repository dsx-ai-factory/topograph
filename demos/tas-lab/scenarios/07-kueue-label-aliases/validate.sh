#!/usr/bin/env bash

# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -euo pipefail
here="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
source "$here/../../lib/common.sh"

result="$state_dir/label-aliases.result"
val() { { grep "^$1=" "$result" || true; } | cut -d= -f2-; }

echo "Scenario 07: rack and block aliases written by Topograph"
[[ -s "$result" ]] || { echo "no result file; run run.sh first"; exit 1; }

echo " mapping A: rack = tier-0 (leaf), block = tier-1 (spine)"
check "all four nodes carry the rack alias" 4 "$(val A.rack_labels)"
check "fabricLabels replaces the default tier-N keys" 0 "$(val A.default_tier_labels_left)"
check "required rack, 2 pods: not admitted (a rack is one node)" False "$(val a-req-rack.admitted)"
check "required block, 2 pods: admitted" True "$(val a-req-block.admitted)"
check "baseline: both groups share a rack" "$(val A-baseline.group0.rack)" "$(val A-baseline.group1.rack)"
check "spread: the groups are in different racks" differ \
    "$([[ "$(val A-spread.group0.rack)" != "$(val A-spread.group1.rack)" ]] && echo differ || echo same)"
check "spread: each group stays in one rack" "1,1" "$(val A-spread.group0.racks),$(val A-spread.group1.racks)"

echo " mapping B: rack = tier-1 (spine), the same manifest as in A"
check "required rack, 2 pods: now admitted (a rack is a spine)" True "$(val b-req-rack.admitted)"
finish
