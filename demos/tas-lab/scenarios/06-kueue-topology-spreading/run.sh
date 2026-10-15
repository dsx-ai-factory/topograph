#!/usr/bin/env bash

# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# Scenario 06: LeaderWorkerSet groups spread across spine domains (fabric.topograph.run/tier-1)
# by Kueue's TASTopologySpreading, while each group stays inside one spine. Run twice, without
# and with the spreading annotation. Needs setup/kueue.sh and setup/lws.sh.
set -euo pipefail
here="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
source "$here/../../lib/common.sh"
source "$lab_dir/lib/kueue.sh"

result="$state_dir/kueue-topology-spreading.result"
tier1=fabric.topograph.run/tier-1

# lws_case <label> <manifest>
lws_case() {
    local label="$1" manifest="$2" g
    step "LeaderWorkerSet, $label"
    reset_namespace
    kubectl apply -f "$manifest" >/dev/null
    kubectl -n "$ns" wait --for=condition=Ready pod --all --timeout=90s >/dev/null 2>&1 || true
    sleep 5
    show_pods
    kubectl get nodes -L "$tier1" --no-headers | awk '$NF != "" && $1 ~ /worker/ {print "  " $1 " -> " $NF}' || true
    for g in 0 1; do
        echo "$label.group$g.running=$(count_pods Running "spread-demo-$g")"
        echo "$label.group$g.spines=$(domain_count "spread-demo-$g" "$tier1")"
        echo "$label.group$g.spine=$(for p in $(kubectl -n "$ns" get pods -o name | sed 's|pod/||' | grep "^spread-demo-$g"); do
            n="$(kubectl -n "$ns" get pod "$p" -o jsonpath='{.spec.nodeName}')"; [[ -n "$n" ]] && kubectl get node "$n" -o go-template='{{index .metadata.labels "fabric.topograph.run/tier-1"}}{{"\n"}}'
        done | sort -u | paste -sd, -)"
    done >>"$result"
    echo "$label.workloads_admitted=$(kubectl -n "$ns" get workloads.kueue.x-k8s.io \
        -o jsonpath='{range .items[*]}{.status.conditions[?(@.type=="Admitted")].status}{"\n"}{end}' | grep -c True || true)" >>"$result"
    echo "$label.total_pods=$(kubectl -n "$ns" get pods --no-headers | wc -l | tr -d ' ')" >>"$result"
}

ensure_multitier_labels
kueue_stack_down spreading spreading
: >"$result"
kueue_stack_up spreading "$here/topology.yaml" spreading

lws_case baseline "$here/lws-baseline.yaml"
lws_case spread "$here/lws-spread.yaml"

kueue_stack_down spreading spreading
step "Results"
cat "$result"
