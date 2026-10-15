#!/usr/bin/env bash

# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# Scenario 03: the enable-after-the-fact trap. Create the gang with the scheduler's
# TopologyAwareWorkloadScheduling gate OFF, turn it on, then recreate the pods.
# Needs docker exec access to the control-plane node. Always leaves the gate ON.
set -euo pipefail
here="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
source "$here/../../lib/common.sh"

cp_node=tas-lab-control-plane
manifest=/etc/kubernetes/manifests/kube-scheduler.yaml
result="$state_dir/gate-enabled-after.result"
docker_cli="${DOCKER:-docker}"

set_gate() { # true|false
    "$docker_cli" exec "$cp_node" sed -i -E "s/TopologyAwareWorkloadScheduling=(true|false)/TopologyAwareWorkloadScheduling=$1/" "$manifest"
    # The kubelet recreates the static pod. Wait for Ready, then for it to win the leader
    # lease; pods submitted earlier than that sit Pending and look like a result, but are not.
    sleep 5
    kubectl -n kube-system wait --for=condition=Ready pod -l component=kube-scheduler --timeout=120s
    sleep 30
}
trap 'set_gate true >/dev/null 2>&1 || true' EXIT

reset_namespace
ensure_labels
: >"$result"

step "Gate OFF on the scheduler, then submit the gang"
set_gate false
kubectl apply -f "$here/gang.yaml"
sleep 10
show_pods
echo "off_running=$(count_pods Running gang-spread-)" >>"$result"
echo "off_domains=$(domain_count gang-spread-)" >>"$result"

step "Gate ON again; the existing pods are untouched"
set_gate true
show_pods
echo "on_existing_running=$(count_pods Running gang-spread-)" >>"$result"

step "Delete the pods and recreate them under the same PodGroup"
kubectl -n "$ns" delete pod --all --wait=true >/dev/null
kubectl apply -f "$here/gang.yaml" >/dev/null
sleep 10
show_pods
echo "recreated_pending=$(count_pods Pending gang-spread-)" >>"$result"
