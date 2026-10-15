#!/usr/bin/env bash

# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# Scenario 07: the same Kueue runs as scenarios 05 and 06, but written against "rack" and "block"
# aliases instead of fabric.topograph.run/tier-N. Topograph's k8s engine writes the aliases
# through its fabricLabels parameter (closest tier first), so which tier is the rack is decided
# once, in the Topograph configuration. Two mappings of the same fabric show what the workload
# manifests still depend on. Needs setup/kueue.sh and setup/lws.sh.
set -euo pipefail
here="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
source "$here/../../lib/common.sh"
source "$lab_dir/lib/kueue.sh"

# The alias keys; change them here if your clusters use other names.
export RACK_KEY="${RACK_KEY:-cloud.provider.com/topology-rack}"
export BLOCK_KEY="${BLOCK_KEY:-cloud.provider.com/topology-block}"
export LEAF_KEY="${LEAF_KEY:-example.com/leaf}"

result="$state_dir/label-aliases.result"

# Remove the alias stack and labels and restore the default model, however the scenario ends.
# Each step is allowed to fail so that the others still run.
cleanup() {
    trap - EXIT
    echo "Clean up: remove the alias labels and restore the default model"
    kueue_stack_down aliases aliases || true
    local key
    for key in "$RACK_KEY" "$BLOCK_KEY" "$LEAF_KEY"; do
        kubectl label nodes -l tas-lab=worker "$key-" >/dev/null 2>&1 || true
    done
    "$lab_dir/setup/topograph.sh" generate >/dev/null 2>&1 || true
}
trap cleanup EXIT

# apply_mapping <fabricLabels json> <Topology levels yaml>
apply_mapping() {
    "$lab_dir/setup/topograph.sh" generate tas-lab-multitier.yaml "{\"fabricLabels\":$1}" >/dev/null
    kueue_stack_down aliases aliases
    LEVELS="$2" envsubst <"$here/topology.tmpl.yaml" >"$state_dir/aliases-topology.yaml"
    kueue_stack_up aliases "$state_dir/aliases-topology.yaml" aliases
}

lws_case() { # <label> <template>
    local label="$1" g p n
    step "LeaderWorkerSet, $label"
    reset_namespace
    envsubst <"$here/$2" | kubectl apply -f - >/dev/null
    kubectl -n "$ns" wait --for=condition=Ready pod --all --timeout=90s >/dev/null 2>&1 || true
    sleep 5
    show_pods
    for g in 0 1; do
        echo "$label.group$g.running=$(count_pods Running "spread-demo-$g")" >>"$result"
        echo "$label.group$g.nodes=$(pod_nodes "spread-demo-$g")" >>"$result"
        echo "$label.group$g.racks=$(domain_count "spread-demo-$g" "$RACK_KEY")" >>"$result"
        echo "$label.group$g.rack=$(for p in $(kubectl -n "$ns" get pods -o name | sed 's|pod/||' | grep "^spread-demo-$g"); do
            n="$(kubectl -n "$ns" get pod "$p" -o jsonpath='{.spec.nodeName}')"
            [[ -n "$n" ]] && kubectl get node "$n" -o go-template="{{index .metadata.labels \"$RACK_KEY\"}}{{\"\\n\"}}"
        done | sort -u | paste -sd, -)" >>"$result"
    done
}

: >"$result"
ensure_multitier_labels

step "Mapping A: rack = tier-0 (leaf switch), block = tier-1 (spine)"
apply_mapping "[\"$RACK_KEY\",\"$BLOCK_KEY\"]" "    - nodeLabel: $BLOCK_KEY
    - nodeLabel: $RACK_KEY"
kubectl get nodes -L "$RACK_KEY,$BLOCK_KEY,fabric.topograph.run/tier-0,fabric.topograph.run/tier-1"
echo "A.default_tier_labels_left=$(kubectl get nodes -l fabric.topograph.run/tier-0 --no-headers | wc -l | tr -d ' ')" >>"$result"
echo "A.rack_labels=$(kubectl get nodes -l "$RACK_KEY" --no-headers | wc -l | tr -d ' ')" >>"$result"
echo "-- required rack, 2 pods (a rack is one node)"
kueue_job a-req-rack aliases 2 required "$RACK_KEY";   kueue_record a-req-rack "$result"
reset_namespace
echo "-- required block, 2 pods"
kueue_job a-req-block aliases 2 required "$BLOCK_KEY"; kueue_record a-req-block "$result"
reset_namespace
lws_case A-baseline lws-baseline.tmpl.yaml
lws_case A-spread lws-spread.tmpl.yaml
reset_namespace

step "Mapping B: the same fabric, but rack = tier-1 (spine); tier-0 goes to $LEAF_KEY"
apply_mapping "[\"$LEAF_KEY\",\"$RACK_KEY\"]" "    - nodeLabel: $RACK_KEY
    - nodeLabel: $LEAF_KEY"
kubectl get nodes -L "$RACK_KEY,$LEAF_KEY"
echo "-- the SAME manifest as in mapping A: required rack, 2 pods"
kueue_job b-req-rack aliases 2 required "$RACK_KEY";   kueue_record b-req-rack "$result"
reset_namespace

step "Clean up: remove the alias labels and restore the default model"
cleanup

step "Results"
cat "$result"
