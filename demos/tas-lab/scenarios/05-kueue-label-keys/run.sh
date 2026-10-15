#!/usr/bin/env bash

# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# Scenario 05: Topograph labels as Kueue Topology levels, with required and preferred topology.
# Same multi-tier model as scenario 04 (tier-0: 4 leaves, tier-1: 2 spines, accelerator domain:
# 3 domains, nvl-a filling spine-1). Each pod fills a node, so a Job of N pods needs N nodes.
# Needs setup/kueue.sh.
set -euo pipefail
here="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
source "$here/../../lib/common.sh"
source "$lab_dir/lib/kueue.sh"

result="$state_dir/kueue-label-keys.result"
fabric=label-keys-fabric
nested=label-keys-nested
nohost=label-keys-no-hostname
tier1=fabric.topograph.run/tier-1
tier0=fabric.topograph.run/tier-0
domain=accelerator.topograph.run/domain

ensure_multitier_labels
for s in "$fabric:$fabric" "$nested:$nested" "$nohost:$nohost"; do kueue_stack_down "${s%%:*}" "${s##*:}"; done
: >"$result"

step "Topology levels: tier-1 > tier-0 > hostname"
kueue_stack_up $fabric "$here/topology-fabric.yaml" $fabric
echo "-- required tier-1, 2 pods (fits one spine)"
kueue_job req-tier1 $fabric 2 required $tier1;          kueue_record req-tier1 "$result"
reset_namespace
echo "-- required tier-1, 3 pods (a spine has 2 nodes)"
kueue_job req-tier1-big $fabric 3 required $tier1;      kueue_record req-tier1-big "$result"
reset_namespace
echo "-- preferred tier-1, 3 pods (falls back to more than one spine)"
kueue_job pref-tier1-big $fabric 3 preferred $tier1;    kueue_record pref-tier1-big "$result"
reset_namespace
echo "-- required tier-0, 2 pods (a leaf has 1 node)"
kueue_job req-tier0 $fabric 2 required $tier0;          kueue_record req-tier0 "$result"
reset_namespace
kueue_stack_down $fabric $fabric

step "Topology levels: tier-1 > accelerator domain > tier-0 > hostname"
kueue_stack_up $nested "$here/topology-nested.yaml" $nested
echo "-- required accelerator domain, 2 pods (nvl-a has 2 nodes)"
kueue_job req-domain $nested 2 required $domain;        kueue_record req-domain "$result"
reset_namespace
echo "-- required accelerator domain, 3 pods (the largest domain has 2 nodes)"
kueue_job req-domain-big $nested 3 required $domain;    kueue_record req-domain-big "$result"
reset_namespace
echo "-- preferred accelerator domain, 3 pods (falls back to a wider level)"
kueue_job pref-domain-big $nested 3 preferred $domain;  kueue_record pref-domain-big "$result"
reset_namespace
kueue_stack_down $nested $nested

step "Topology levels without hostname as the last level"
kueue_stack_up $nohost "$here/topology-no-hostname.yaml" $nohost
echo "-- required tier-1, 2 pods"
kueue_job req-nohost $nohost 2 required $tier1;         kueue_record req-nohost "$result"
reset_namespace
kueue_stack_down $nohost $nohost

step "Results"
cat "$result"
