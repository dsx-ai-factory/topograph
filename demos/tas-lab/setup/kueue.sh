#!/usr/bin/env bash

# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# Setup step 3: install Kueue (KUEUE_VERSION in versions.env) with TAS and
# TASTopologySpreading enabled. The Kueue objects (Topology, ResourceFlavor, queues)
# belong to the Kueue scenarios (05-08).

set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/../lib/common.sh"

wait_for_api 30

kubectl apply --server-side --force-conflicts -f \
    "https://github.com/kubernetes-sigs/kueue/releases/download/$KUEUE_VERSION/manifests.yaml"
kubectl -n kueue-system rollout status deploy/kueue-controller-manager --timeout=180s

gates="--feature-gates=TopologyAwareScheduling=true,TASTopologySpreading=true"
if ! kubectl -n kueue-system get deploy kueue-controller-manager \
    -o jsonpath='{.spec.template.spec.containers[0].args}' | grep -q -- "$gates"; then
    kubectl -n kueue-system patch deploy kueue-controller-manager --type=json \
        -p="[{\"op\":\"add\",\"path\":\"/spec/template/spec/containers/0/args/-\",\"value\":\"$gates\"}]"
    kubectl -n kueue-system rollout status deploy/kueue-controller-manager --timeout=180s
fi

# Topology spreading selects "the other workloads of the same service" by Workload labels. By
# default only the parent job's UID is copied, so copy the "app" label too (scenario 06).
cfg="$(kubectl -n kueue-system get cm kueue-manager-config -o jsonpath='{.data.controller_manager_config\.yaml}')"
if ! grep -q "labelKeysToCopy" <<<"$cfg"; then
    new="$(python3 -c '
import sys
cfg = sys.stdin.read()
cfg = cfg.replace("integrations:\n", "integrations:\n  labelKeysToCopy:\n  - app\n", 1)
sys.stdout.write(cfg)' <<<"$cfg")"
    kubectl -n kueue-system create cm kueue-manager-config --dry-run=client -o yaml \
        --from-literal=controller_manager_config.yaml="$new" | kubectl apply --server-side --force-conflicts -f -
    kubectl -n kueue-system rollout restart deploy/kueue-controller-manager
    kubectl -n kueue-system rollout status deploy/kueue-controller-manager --timeout=180s
fi
