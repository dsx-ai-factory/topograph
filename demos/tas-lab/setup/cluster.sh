#!/usr/bin/env bash

# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# Setup step 1: create the TAS lab kind cluster (Kubernetes version from versions.env, both scheduling
# gates on), and the RBAC that lets lib/capture-metrics.sh scrape the scheduler. The nodes carry no
# topology labels yet; Topograph adds them in step 2.

set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/../lib/common.sh"

installed_kind="$(kind version | awk '{print $2}')"
if [[ "$installed_kind" != "$KIND_VERSION" ]]; then
    echo "warning: kind $installed_kind is installed; the lab was tested with $KIND_VERSION (versions.env)" >&2
fi

kind create cluster --config "$lab_dir/setup/kind-config.yaml" --image "$KIND_NODE_IMAGE"
wait_for_api
kubectl wait --for=condition=Ready node --all --timeout=180s
kubectl label node tas-lab-worker tas-lab-worker2 tas-lab-worker3 tas-lab-worker4 tas-lab=worker --overwrite
kubectl apply -f "$lab_dir/setup/metrics-rbac.yaml"

step "Feature gates on the control plane"
for c in kube-apiserver kube-scheduler; do
    kubectl -n kube-system get pod -l component=$c \
        -o jsonpath='{.items[0].spec.containers[0].command}' | tr ',' '\n' | grep feature-gates || true
done
kubectl get nodes
