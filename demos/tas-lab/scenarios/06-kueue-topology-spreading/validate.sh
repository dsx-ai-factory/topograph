#!/usr/bin/env bash

# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -euo pipefail
here="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
source "$here/../../lib/common.sh"

result="$state_dir/kueue-topology-spreading.result"
val() { { grep "^$1=" "$result" || true; } | cut -d= -f2-; }

echo "Scenario 06: Kueue topology spreading of LeaderWorkerSet groups"
[[ -s "$result" ]] || { echo "no result file; run run.sh first"; exit 1; }

echo " without the spreading annotation"
check "both groups admitted" 2 "$(val baseline.workloads_admitted)"
check "each group stays in one spine (group 0)" 1 "$(val baseline.group0.spines)"
check "each group stays in one spine (group 1)" 1 "$(val baseline.group1.spines)"
check "both groups share the same spine" "$(val baseline.group0.spine)" "$(val baseline.group1.spine)"

echo " with the spreading annotation (rule on tier-1, maxShareAllowingPlacement 0.5, Required)"
check "both groups admitted" 2 "$(val spread.workloads_admitted)"
check "each group stays in one spine (group 0)" 1 "$(val spread.group0.spines)"
check "each group stays in one spine (group 1)" 1 "$(val spread.group1.spines)"
check "the groups are in different spines" differ \
    "$([[ "$(val spread.group0.spine)" != "$(val spread.group1.spine)" ]] && echo differ || echo same)"
check "all four pods Running" 4 "$(( $(val spread.group0.running) + $(val spread.group1.running) ))"
finish
