#!/usr/bin/env bash

# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# Setup step 2: install Topograph, which labels the workers, and show the labels.
#
#   setup/topograph.sh install    pull $TOPOGRAPH_IMG_URL:$TOPOGRAPH_IMG_TAG (versions.env), load it into
#                                 kind, helm install, wait for labels
#   setup/topograph.sh generate [model] [engine-params-json]
#                                 ask the API server to regenerate and re-apply the labels,
#                                 optionally from another model in tests/models (default
#                                 tas-lab.yaml) and with k8s engine params, e.g.
#                                 '{"fabricLabels":["example.com/rack"]}'
#   setup/topograph.sh labels     print the fabric.topograph.run/* labels on every node

set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/../lib/common.sh"

image=$TOPOGRAPH_IMG_URL:$TOPOGRAPH_IMG_TAG
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
    # Pull the published image and load it into kind, so the nodes need no registry access.
    # The test provider reads models embedded in the binary, so the image must already contain
    # tests/models/tas-lab.yaml; pick a TOPOGRAPH_IMG_TAG that does.
    docker pull "$image"
    archive_dir="$(mktemp -d)"
    docker save -o "$archive_dir/topograph.tar" "$image"
    kind load image-archive "$archive_dir/topograph.tar" --name tas-lab
    rm -rf "$archive_dir"
    # install-topograph.sh takes one values file, so add the image from versions.env to a copy of the lab values.
    values_file="$(mktemp)"
    awk -v repo="$TOPOGRAPH_IMG_URL" -v tag="$TOPOGRAPH_IMG_TAG" \
        '{ print } /^image:$/ { printf "  repository: %s\n  tag: \"%s\"\n", repo, tag }' \
        "$lab_dir/setup/values.topograph.yaml" >"$values_file"
    KUBE_CONTEXT=$ctx "$repo_root/scripts/install-topograph.sh" "$values_file"
    rm -f "$values_file"
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
