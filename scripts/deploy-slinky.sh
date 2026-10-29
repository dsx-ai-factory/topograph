#!/usr/bin/env bash

# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
# http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

set -euo pipefail

kube_context="${KUBE_CONTEXT:-kind-topograph}"
slinky_version="${SLINKY_VERSION:-1.2.0}"
compute_mode=""

control_plane_selector='{"node-role.kubernetes.io/control-plane":""}'
control_plane_tolerations='[{"key":"node-role.kubernetes.io/control-plane","operator":"Exists","effect":"NoSchedule"}]'
kwok_selector='{"kwok.x-k8s.io/node":"fake"}'

usage() {
    cat <<EOF
Usage: $0 --compute-mode <none|kwok>

Deploy Slinky to a kind cluster with exactly one control-plane node.

Options:
  --compute-mode <none|kwok>  Deploy without compute nodes or with a KWOK
                              worker NodeSet.
  -h, --help                  Show this help.

Environment:
  KUBE_CONTEXT    Kubernetes context. Default: ${kube_context}
  SLINKY_VERSION  Slinky chart version. Default: ${slinky_version}
EOF
}

while (( $# > 0 )); do
    case "$1" in
        --compute-mode)
            if (( $# < 2 )); then
                echo "missing value for --compute-mode" >&2
                usage >&2
                exit 2
            fi
            compute_mode="$2"
            shift 2
            ;;
        -h|--help)
            usage
            exit 0
            ;;
        *)
            echo "unknown argument: $1" >&2
            usage >&2
            exit 2
            ;;
    esac
done

case "$compute_mode" in
    none|kwok)
        ;;
    "")
        echo "missing required --compute-mode" >&2
        usage >&2
        exit 2
        ;;
    *)
        echo "invalid compute mode: $compute_mode" >&2
        usage >&2
        exit 2
        ;;
esac

control_plane_count="$(kubectl --context "$kube_context" get nodes \
    -l node-role.kubernetes.io/control-plane -o name | wc -l | tr -d '[:space:]')"
if [[ "$control_plane_count" != "1" ]]; then
    echo "expected exactly one control-plane node, found $control_plane_count" >&2
    exit 1
fi

kwok_node_count=0
if [[ "$compute_mode" == "kwok" ]]; then
    kwok_node_count="$(kubectl --context "$kube_context" get nodes \
        -l kwok.x-k8s.io/node=fake -o name | wc -l | tr -d '[:space:]')"
    if [[ "$kwok_node_count" == "0" ]]; then
        echo "expected at least one KWOK node with kwok.x-k8s.io/node=fake" >&2
        exit 1
    fi
fi

helm upgrade --install slurm-operator \
    oci://ghcr.io/slinkyproject/charts/slurm-operator \
    --version "$slinky_version" \
    --kube-context "$kube_context" \
    --namespace slinky --create-namespace \
    --set crds.enabled=true \
    --set certManager.enabled=false \
    --set-json "operator.nodeSelector=$control_plane_selector" \
    --set-json "operator.tolerations=$control_plane_tolerations" \
    --set-json "webhook.nodeSelector=$control_plane_selector" \
    --set-json "webhook.tolerations=$control_plane_tolerations" \
    --wait --timeout 5m

webhook_local_port=19443
webhook_url="https://127.0.0.1:${webhook_local_port}/validate-slinky-slurm-net-v1beta1-controller"
webhook_port_forward_log="$(mktemp)"
webhook_ready=false

# Slinky 1.2.0 implements /readyz as a standalone ping that can succeed before
# the TLS admission server starts. Probe an actual validation route instead.
kubectl --context "$kube_context" --namespace slinky port-forward \
    service/slurm-operator-webhook "${webhook_local_port}:https" \
    >"$webhook_port_forward_log" 2>&1 &
webhook_port_forward_pid=$!

cleanup_webhook_port_forward() {
    kill "$webhook_port_forward_pid" >/dev/null 2>&1 || true
    wait "$webhook_port_forward_pid" >/dev/null 2>&1 || true
    rm -f "$webhook_port_forward_log"
}
trap cleanup_webhook_port_forward EXIT

echo "Waiting for the Slinky admission webhook TLS listener"
for _ in {1..60}; do
    if ! kill -0 "$webhook_port_forward_pid" >/dev/null 2>&1; then
        echo "Failed to establish port-forward to the Slinky admission webhook" >&2
        cat "$webhook_port_forward_log" >&2
        exit 1
    fi
    # Any HTTP response proves that the TLS admission server is accepting
    # connections; this validation endpoint does not need to accept GET.
    if curl --insecure --silent --output /dev/null \
        --connect-timeout 1 --max-time 2 "$webhook_url"; then
        webhook_ready=true
        break
    fi
    sleep 1
done

if [[ "$webhook_ready" != "true" ]]; then
    echo "Slinky admission webhook TLS listener did not become reachable" >&2
    cat "$webhook_port_forward_log" >&2
    exit 1
fi

cleanup_webhook_port_forward
trap - EXIT

slurm_args=(
    upgrade --install slurm
    oci://ghcr.io/slinkyproject/charts/slurm
    --version "$slinky_version"
    --kube-context "$kube_context"
    --namespace slurm --create-namespace
    --set controller.persistence.enabled=false
    --set-json "controller.podSpec.nodeSelector=$control_plane_selector"
    --set-json "controller.podSpec.tolerations=$control_plane_tolerations"
    --set-json "restapi.podSpec.nodeSelector=$control_plane_selector"
    --set-json "restapi.podSpec.tolerations=$control_plane_tolerations"
    --set-string 'configFiles.topology\.conf='
    --wait --timeout 10m
)

if [[ "$compute_mode" == "kwok" ]]; then
    slurm_args+=(
        --set nodesets.kwok.scalingMode=DaemonSet
        --set-json "nodesets.kwok.podSpec.nodeSelector=$kwok_selector"
        --set partitions.all.enabled=true
    )
fi

helm "${slurm_args[@]}"

if [[ "$compute_mode" == "kwok" ]]; then
    # KWOK marks pods Ready without running their containers. Keep the
    # simulated slurmd pods from being deleted for failing to register.
    kubectl --context "$kube_context" patch nodeset slurm-worker-kwok \
        --namespace slurm \
        --type merge \
        --patch '{"spec":{"minReadySeconds":2147483647}}'

    min_ready_seconds="$(kubectl --context "$kube_context" get nodeset slurm-worker-kwok \
        --namespace slurm \
        -o jsonpath='{.spec.minReadySeconds}')"
    if [[ "$min_ready_seconds" != "2147483647" ]]; then
        echo "failed to configure simulated slurmd pod stability" >&2
        exit 1
    fi

    echo "Waiting for $kwok_node_count KWOK worker pods to become Ready"
    kubectl --context "$kube_context" wait \
        --namespace slurm \
        --for="jsonpath={.status.readyReplicas}=${kwok_node_count}" \
        nodeset/slurm-worker-kwok \
        --timeout 10m

    echo "Slinky $slinky_version deployed to $kube_context with $kwok_node_count KWOK compute nodes"
else
    echo "Slinky $slinky_version deployed to $kube_context without compute nodes"
fi
