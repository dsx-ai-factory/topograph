#!/usr/bin/env bash

# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -euo pipefail
here="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
source "$here/../../lib/common.sh"

result="$state_dir/kueue-label-keys.result"
val() { { grep "^$1=" "$result" || true; } | cut -d= -f2-; }

echo "Scenario 05: Topograph labels as Kueue Topology levels"
[[ -s "$result" ]] || { echo "no result file; run run.sh first"; exit 1; }

echo " required tier-1"
check "2 pods admitted" True "$(val req-tier1.admitted)"
check_job_running req-tier1
check "both pods in one spine" 1 "$(val req-tier1.tier1_domains)"
check "3 pods are not admitted" False "$(val req-tier1-big.admitted)"
check "...because only 2 of 3 fit a spine" yes "$(val req-tier1-big.message | grep -q 'fit only 2 out of 3' && echo yes || echo no)"

echo " preferred tier-1"
check "3 pods admitted anyway" True "$(val pref-tier1-big.admitted)"
check_job_running pref-tier1-big
check "...across both spines" 2 "$(val pref-tier1-big.tier1_domains)"

echo " required tier-0 (one node per leaf)"
check "2 pods are not admitted" False "$(val req-tier0.admitted)"

echo " accelerator domain as a level of the same hierarchy"
check "required domain, 2 pods: admitted" True "$(val req-domain.admitted)"
check_job_running req-domain
check "both pods in one accelerator domain" 1 "$(val req-domain.accel_domains)"
check "the domain nests inside one spine" 1 "$(val req-domain.tier1_domains)"
check "required domain, 3 pods: not admitted" False "$(val req-domain-big.admitted)"
check "preferred domain, 3 pods: admitted at a wider level" True "$(val pref-domain-big.admitted)"
check_job_running pref-domain-big
check "...across both spines" 2 "$(val pref-domain-big.tier1_domains)"

echo " Topology without hostname as the last level"
check "required tier-1 still admitted" True "$(val req-nohost.admitted)"
check_job_running req-nohost
finish
