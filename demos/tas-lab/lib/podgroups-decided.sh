#!/usr/bin/env bash

# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# Exits 0 once the namespace has PodGroups and every one carries a PodGroupInitiallyScheduled
# condition (True or False), that is, the scheduler has tried the gang and the outcome is final
# enough to read. Used by lib/capture-metrics.sh (WAIT_FOR_SCRIPT) to snapshot the metrics only
# after the scheduling attempt, a failed one included.
set -euo pipefail
ctx="${KUBE_CONTEXT:-kind-tas-lab}"
command kubectl --context "$ctx" -n tas-lab get podgroups.scheduling.k8s.io --no-headers \
    -o 'custom-columns=N:.metadata.name,S:.status.conditions[?(@.type=="PodGroupInitiallyScheduled")].status' \
    | awk 'NF { n++; if ($2 == "<none>") bad++ } END { exit !(n > 0 && bad == 0) }'
