#!/usr/bin/env bash

# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -euo pipefail
here="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
source "$here/../../lib/common.sh"

result="$state_dir/scheduler-label-keys.result"
val() { { grep "^$1=" "$result" || true; } | cut -d= -f2-; }

echo "Scenario 04: Topograph labels as TopologyConstraint keys"
[[ -s "$result" ]] || { echo "no result file; run run.sh first"; exit 1; }

echo " key = tier-1 (2 spines, 2 nodes each)"
check "gang runs" 2 "$(val on-tier1.running)"
check "both pods in one spine" 1 "$(val on-tier1.tier1_domains)"
check "placements generated = distinct tier-1 values" 2 "$(val on-tier1.generated)"
check "PodGroupInitiallyScheduled" True "$(val on-tier1.condition)"

echo " key = tier-0 (4 leaves, 1 node each): a 2-pod, one-pod-per-node gang cannot fit"
check "gang stays Pending" 2 "$(val on-tier0.pending)"
check "no pod Running" 0 "$(val on-tier0.running)"
check "PodGroupInitiallyScheduled" False "$(val on-tier0.condition)"
check_gt "infeasible evaluations" "$(val on-tier0.infeasible)"
check "every attempt generates one placement per leaf (multiple of 4)" 0 "$(( $(val on-tier0.generated) % 4 ))"

echo " key = accelerator.topograph.run/domain (nvl-a fills spine-1; nvl-b and nvl-c are single nodes)"
check "gang runs" 2 "$(val on-domain.running)"
check "both pods in one accelerator domain" 1 "$(val on-domain.accel_domains)"
check "the domain nests inside one spine" 1 "$(val on-domain.tier1_domains)"
check "placements generated = distinct domain values" 3 "$(val on-domain.generated)"
check "only nvl-a is feasible" 1 "$(val on-domain.feasible)"

echo " too big for any domain"
check "tier-1 gang of 3 stays Pending (a spine has 2 nodes)" 3 "$(val tier1-too-big.pending)"
check "domain gang of 3 stays Pending (the largest domain has 2 nodes)" 3 "$(val domain-too-big.pending)"
check "no pod Running in either case" 0 "$(( $(val tier1-too-big.running) + $(val domain-too-big.running) ))"

echo " key = accelerator.topograph.run/sub-domain (a sub-domain per rack inside one domain)"
check "2-pod gang runs" 2 "$(val on-subdomain.running)"
check "both pods in one sub-domain" 1 "$(val on-subdomain.subdomains)"
check "placements generated = distinct sub-domain values" 2 "$(val on-subdomain.generated)"
check "3-pod gang stays Pending (a sub-domain has 2 nodes)" 3 "$(val subdomain-too-big.pending)"

echo " two constraints on one PodGroup"
check "API rejects more than one topology constraint" rejected "$(val two_constraints)"
check "...because of the item limit, not another apply failure" yes \
    "$(val two_constraints.error | grep -q 'at most 1 item' && echo yes || echo no)"
finish
