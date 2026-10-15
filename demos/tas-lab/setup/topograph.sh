#!/usr/bin/env bash

# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# Setup step 2: install Topograph, which labels the workers, and show the labels.
#
#   setup/topograph.sh install    build the image, load it into kind, helm install, wait for labels
#   setup/topograph.sh generate [model] [engine-params-json]
#                                 ask the API server to regenerate and re-apply the labels,
#                                 optionally from another model in tests/models (default
#                                 tas-lab.yaml) and with k8s engine params, e.g.
#                                 '{"fabricLabels":["example.com/rack"]}'
#   setup/topograph.sh labels     print the fabric.topograph.run/* labels on every node

set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/../lib/common.sh"

image=ghcr.io/dsx-ai-factory/topograph:tas-lab
topograph_ns=topograph

labels() {
    kubectl get nodes -L fabric.topograph.run/tier-0,fabric.topograph.run/tier-1,accelerator.topograph.run/domain,accelerator.topograph.run/sub-domain
}

workers() { kubectl get nodes -l tas-lab=worker --no-headers 2>/dev/null | wc -l | tr -d ' '; }

wait_for_labels() {
    local i want
    want="$(workers)"
    for i in $(seq 1 60); do
        if (( $(kubectl get nodes -l tas-lab=worker,fabric.topograph.run/tier-0 --no-headers 2>/dev/null | wc -l) == want )); then
            return 0
        fi
        sleep 2
    done
    echo "timed out waiting for fabric.topograph.run/tier-0 on the workers" >&2
    return 1
}

wait_for_api 30

case "${1:-}" in
install)
    # The test provider only reads models embedded in the binary, so build from this checkout.
    # The image runs in kind nodes, so build for the container engine's architecture, not the host's.
    arch="${GOARCH:-$(docker info --format '{{.Architecture}}' | sed 's/aarch64/arm64/; s/x86_64/amd64/')}"
    (cd "$repo_root" && make image-build GOOS=linux GOARCH="$arch" \
        IMAGE_REPO=ghcr.io/dsx-ai-factory/topograph IMAGE_TAG=tas-lab)
    archive_dir="$(mktemp -d)"
    docker save -o "$archive_dir/topograph.tar" "$image"
    kind load image-archive "$archive_dir/topograph.tar" --name tas-lab
    rm -rf "$archive_dir"
    KUBE_CONTEXT=$ctx "$repo_root/scripts/install-topograph.sh" "$lab_dir/setup/values.topograph.yaml"
    # The image tag is fixed, so a re-install with a rebuilt image needs the pods restarted.
    kubectl -n $topograph_ns rollout restart deploy
    kubectl -n $topograph_ns rollout status deploy --timeout=120s
    wait_for_labels
    labels
    ;;
generate)
    # A free local port, so a leftover port-forward can never be the one that answers.
    pf_log="$(mktemp)"
    # `command kubectl` so that $! is the port-forward itself, not a function subshell.
    command kubectl --context "$ctx" -n $topograph_ns port-forward svc/topograph :49021 >"$pf_log" 2>&1 &
    pf=$!
    trap 'kill $pf 2>/dev/null || true; rm -f "$pf_log"' EXIT
    port=""
    for _ in $(seq 1 30); do
        port="$(sed -n 's/^Forwarding from 127.0.0.1:\([0-9]*\) .*/\1/p' "$pf_log" | head -1)"
        [[ -n "$port" ]] && break
        sleep 1
    done
    [[ -n "$port" ]] || { echo "port-forward to svc/topograph failed:" >&2; cat "$pf_log" >&2; exit 1; }
    model="${2:-tas-lab.yaml}"
    # Assigned separately: "${3:-{\}}" expands to {\} in Bash 3.2 (macOS system Bash).
    engine_params="${3:-}"
    [[ -n "$engine_params" ]] || engine_params='{}'
    id="$(curl -fsS -X POST -H 'Content-Type: application/json' \
        -d "{\"provider\":{\"name\":\"test\",\"params\":{\"modelFileName\":\"$model\"}},\"engine\":{\"name\":\"k8s\",\"params\":$engine_params}}" \
        "http://localhost:$port/v1/generate")"
    echo "request id: $id"
    # Poll the request until the server has finished it (202 while pending, 200 when done).
    code=""
    for _ in $(seq 1 60); do
        code="$(curl -s -o /dev/null -w '%{http_code}' "http://localhost:$port/v1/topology?uid=$id" || true)"
        [[ "$code" == 200 ]] && break
        sleep 1
    done
    [[ "$code" == 200 ]] || { echo "request $id did not complete (last HTTP status: $code)" >&2; exit 1; }
    sleep "${SETTLE_SECONDS:-2}"
    labels
    ;;
labels)
    labels
    ;;
*)
    echo "usage: $0 install|generate|labels" >&2
    exit 2
    ;;
esac
