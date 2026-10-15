#!/usr/bin/env bash

# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -euo pipefail
here="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
source "$here/../../lib/common.sh"

result="$state_dir/kueue-topology-shapes.result"
val() { { grep "^$1=" "$result" || true; } | cut -d= -f2-; }

echo "Scenario 08: Kueue Topology per label shape, and changing levels"
[[ -s "$result" ]] || { echo "no result file; run run.sh first"; exit 1; }

echo " shape 1: fabric tiers only (tier-1 > tier-0 > hostname)"
check "required tier-1, 4 pods: admitted across both racks" True "$(val s1-tier1-4.admitted)"
check "...inside one spine" 1 "$(val s1-tier1-4.tier1_domains)"
check_job_running s1-tier1-4
check "required tier-0, 2 pods: admitted inside one rack" True "$(val s1-tier0-2.admitted)"
check_job_running s1-tier0-2
check "required tier-0, 3 pods: not admitted (a rack has 2 nodes)" False "$(val s1-tier0-3.admitted)"

echo " shape 2: fabric tiers + accelerator domain (tier-1 > domain > tier-0 > hostname)"
check "required domain, 2 pods: admitted inside one domain" 1 "$(val s2-domain-2.accel_domains)"
check "...which nests inside one spine" 1 "$(val s2-domain-2.tier1_domains)"
check_job_running s2-domain-2
check "required domain, 3 pods: not admitted" False "$(val s2-domain-3.admitted)"
check "a node missing a level label is ignored: 4 pods do not fit the other 3 nodes" False "$(val s2-missing-4.admitted)"
check "...and 3 pods are admitted without it" True "$(val s2-missing-3.admitted)"
check "...never on the unlabelled node" 0 "$(val s2-missing-3.nodes | tr ',' '\n' | grep -c '^tas-lab-worker3$' || true)"
check_job_running s2-missing-3

echo " shape 4: a domain that crosses spines"
check "domain below tier-1: the 3-node domain has only 2 nodes per spine, so it is not found" False "$(val s2-crossing-nested.admitted)"
check "domain above the tiers it crosses: 3 pods admitted in one domain" True "$(val s2-crossing-first.admitted)"
check "...on nodes of both spines" 2 "$(val s2-crossing-first.tier1_domains)"
check_job_running s2-crossing-first

echo " shape 3: fabric tiers + domain + sub-domain (tier-1 > domain > sub-domain > tier-0 > hostname)"
check "required sub-domain, 2 pods: admitted inside one sub-domain" 1 "$(val s3-sub-2.subdomains)"
check_job_running s3-sub-2
check "required sub-domain, 3 pods: not admitted" False "$(val s3-sub-3.admitted)"
check "required domain, 4 pods: admitted across both sub-domains" 2 "$(val s3-domain-4.subdomains)"
check_job_running s3-domain-4

echo " changing the levels of a Topology"
check "a Job requiring a label that is not a level is not admitted" False "$(val mut.before_admitted)"
check "...and Kueue says the level is missing" yes \
    "$(val mut.before_message | grep -q 'does not contain the requested level' && echo yes || echo no)"
check "adding the level admits the waiting Job without recreating it" True "$(val mut.after_add_admitted)"
check "...and its pods run inside one domain" 1 "$(val mut.after_add_domains)"
check "dropping the widest level keeps the admitted Job admitted" True "$(val mut.after_drop_admitted)"
check "...with its pods still running" 2 "$(val mut.after_drop_running)"
check "removing the hostname level is rejected" rejected "$(val mut.no_hostname)"
check "...because the hostname must stay the lowest level" yes \
    "$(val mut.no_hostname.error | grep -q 'kubernetes.io/hostname' && echo yes || echo no)"
finish
