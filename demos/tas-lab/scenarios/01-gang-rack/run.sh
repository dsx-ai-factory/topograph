#!/usr/bin/env bash

# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# Scenario 01: a 4-pod gang with a tier-0 topology constraint lands inside a single rack, and an
# ordinary pod submitted next to it is not part of the gang evaluation.
set -euo pipefail
here="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
source "$here/../../lib/common.sh"

result="$state_dir/gang-rack.result"

reset_namespace
ensure_labels
: >"$result"

step "Case 1: submit the gang and capture the scheduler placement metrics"
WAIT_FOR_SCRIPT="$lab_dir/lib/podgroups-decided.sh" capture gang-rack kubectl apply -f "$here/gang.yaml"
kubectl -n "$ns" wait --for=condition=Ready pod --all --timeout=60s
show_pods
{
    echo "gang.running=$(count_pods Running gang-rack-)"
    echo "gang.domains=$(domain_count gang-rack-)"
} >>"$result"

step "Case 2: the same gang next to a pod without schedulingGroup"
reset_namespace
WAIT_FOR_SCRIPT="$lab_dir/lib/podgroups-decided.sh" capture gang-and-plain-pod kubectl apply -f "$here/gang.yaml" -f "$here/plain-pod.yaml"
kubectl -n "$ns" wait --for=condition=Ready pod --all --timeout=60s
show_pods
{
    echo "mixed.running=$(count_pods Running)"
    echo "mixed.gang_domains=$(domain_count gang-rack-)"
} >>"$result"
