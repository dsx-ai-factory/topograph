#!/usr/bin/env bash

# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# Scenario 04: which Topograph labels work as the kube-scheduler's TopologyConstraint key.
# The multi-tier model groups the same four nodes three ways (see the model's header):
# tier-0 = 4 leaf switches, tier-1 = 2 spines, accelerator domain = 3 NVLink domains, one of
# which (nvl-a) fills the whole of spine-1.
set -euo pipefail
here="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
source "$here/../../lib/common.sh"

result="$state_dir/scheduler-label-keys.result"

# run_case <name> <label key> <minCount>
run_case() {
    local name="$1" key="$2" min="$3"
    step "Case $name: gang of $min, one pod per node, constraint key $key"
    reset_namespace
    NAME="$name" KEY="$key" MIN="$min" envsubst <"$here/gang.tmpl.yaml" >"$state_dir/$name.yaml"
    WAIT_FOR_SCRIPT="$lab_dir/lib/podgroups-decided.sh" capture "$name" kubectl apply -f "$state_dir/$name.yaml"
    sleep 8
    show_pods
    {
        echo "$name.running=$(count_pods Running "$name-")"
        echo "$name.pending=$(count_pods Pending "$name-")"
        echo "$name.nodes=$(pod_nodes "$name-")"
        echo "$name.tier1_domains=$(domain_count "$name-" fabric.topograph.run/tier-1)"
        echo "$name.accel_domains=$(domain_count "$name-" accelerator.topograph.run/domain)"
        echo "$name.subdomains=$(domain_count "$name-" accelerator.topograph.run/sub-domain)"
        echo "$name.generated=$(metric_delta "$name" '^scheduler_generated_placements_total')"
        echo "$name.feasible=$(metric_delta "$name" 'evaluations_total.*result="feasible"')"
        echo "$name.infeasible=$(metric_delta "$name" 'evaluations_total.*result="infeasible"')"
        echo "$name.condition=$(kubectl -n "$ns" get podgroups.scheduling.k8s.io "$name" -o jsonpath='{.status.conditions[?(@.type=="PodGroupInitiallyScheduled")].status}')"
    } >>"$result"
}

ensure_multitier_labels
: >"$result"
kubectl get nodes -L fabric.topograph.run/tier-0,fabric.topograph.run/tier-1,accelerator.topograph.run/domain

run_case on-tier1 fabric.topograph.run/tier-1 2
run_case on-tier0 fabric.topograph.run/tier-0 2
run_case on-domain accelerator.topograph.run/domain 2
run_case tier1-too-big fabric.topograph.run/tier-1 3
run_case domain-too-big accelerator.topograph.run/domain 3

step "Two topology constraints on one PodGroup"
reset_namespace
if out="$(kubectl apply -f "$here/two-constraints.yaml" 2>&1)"; then
    echo "two_constraints=accepted" >>"$result"
else
    echo "two_constraints=rejected" >>"$result"
    echo "two_constraints.error=$(tr '\n' ' ' <<<"$out")" >>"$result"
fi
echo "$out"
reset_namespace

step "Sub-domain as the key (tas-lab-subdomain.yaml: one domain, a sub-domain per rack)"
"$lab_dir/setup/topograph.sh" generate tas-lab-subdomain.yaml >/dev/null
kubectl get nodes -L accelerator.topograph.run/domain,accelerator.topograph.run/sub-domain
run_case on-subdomain accelerator.topograph.run/sub-domain 2
run_case subdomain-too-big accelerator.topograph.run/sub-domain 3
reset_namespace
