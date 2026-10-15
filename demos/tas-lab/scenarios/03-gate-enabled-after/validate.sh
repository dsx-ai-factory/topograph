#!/usr/bin/env bash

# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -euo pipefail
here="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
source "$here/../../lib/common.sh"

result="$state_dir/gate-enabled-after.result"
val() { { grep "^$1=" "$result" || true; } | cut -d= -f2; }

echo "Scenario 03: gate enabled after the fact"
[[ -s "$result" ]] || { echo "no result file; run run.sh first"; exit 1; }
check "gate OFF: gang ran (constraint ignored)" 4 "$(val off_running)"
check "gate OFF: pods spread over both racks" 2 "$(val off_domains)"
check "gate turned ON: existing pods keep running" 4 "$(val on_existing_running)"
check "recreated under gate ON: all pods Pending" 4 "$(val recreated_pending)"
check "scheduler gate is back ON" "TopologyAwareWorkloadScheduling=true" \
    "$(${DOCKER:-docker} exec tas-lab-control-plane grep -o 'TopologyAwareWorkloadScheduling=[a-z]*' /etc/kubernetes/manifests/kube-scheduler.yaml)"
finish
