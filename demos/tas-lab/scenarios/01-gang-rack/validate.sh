#!/usr/bin/env bash

# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -euo pipefail
here="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
source "$here/../../lib/common.sh"

result="$state_dir/gang-rack.result"
val() { { grep "^$1=" "$result" || true; } | cut -d= -f2-; }

echo "Scenario 01: gang + rack constraint"
[[ -s "$result" ]] || { echo "no result file; run run.sh first"; exit 1; }
echo " case 1: the gang"
check "4 pods Running" 4 "$(val gang.running)"
check "pods span one tier-0 domain" 1 "$(val gang.domains)"
check "placements generated (one per tier-0 value)" 2 "$(metric_delta gang-rack '^scheduler_generated_placements_total')"
check "feasible evaluations" 2 "$(metric_delta gang-rack 'evaluations_total.*result=\"feasible\"')"
echo " case 2: the gang next to a plain pod"
check "5 pods Running" 5 "$(val mixed.running)"
check "gang pods span one tier-0 domain" 1 "$(val mixed.gang_domains)"
check "the plain pod adds no placement (same count as case 1)" 2 "$(metric_delta gang-and-plain-pod '^scheduler_generated_placements_total')"
finish
