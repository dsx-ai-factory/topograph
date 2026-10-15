#!/usr/bin/env bash

# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# Scenario 02: how the scheduler treats a node that lacks the constraint key. Topograph labels the
# workers from tests/models/tas-lab-nodomain.yaml, where tas-lab-worker3 has no accelerator domain
# and so gets no accelerator.topograph.run/domain label. The other workers are cordoned, which
# leaves that node as the only free one.
set -euo pipefail
here="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
source "$here/../../lib/common.sh"

result="$state_dir/missing-label.result"
trap 'kubectl uncordon tas-lab-worker tas-lab-worker2 tas-lab-worker4 >/dev/null 2>&1 || true; "$lab_dir/setup/topograph.sh" generate >/dev/null 2>&1 || true' EXIT

reset_namespace
step "Label the workers from tas-lab-nodomain.yaml"
"$lab_dir/setup/topograph.sh" generate tas-lab-nodomain.yaml
: >"$result"

step "Gang of 1 on the domain key; only tas-lab-worker3, which has no domain label, is free"
kubectl cordon tas-lab-worker tas-lab-worker2 tas-lab-worker4 >/dev/null
WAIT_FOR_SCRIPT="$lab_dir/lib/podgroups-decided.sh" capture missing-label kubectl apply -f "$here/gang.yaml"
sleep 5
show_pods
{
    echo "missing.pending=$(count_pods Pending gang-domain-)"
    echo "missing.on_unlabelled=$(kubectl -n "$ns" get pods -o jsonpath='{range .items[*]}{.spec.nodeName}{"\n"}{end}' | grep -c '^tas-lab-worker3$' || true)"
} >>"$result"
kubectl uncordon tas-lab-worker tas-lab-worker2 tas-lab-worker4 >/dev/null
reset_namespace
