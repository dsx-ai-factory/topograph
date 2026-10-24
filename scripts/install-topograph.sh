#!/usr/bin/env bash

# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -euo pipefail

if (( $# < 1 || $# > 2 )); then
    echo "usage: $0 <values-file> [version]" >&2
    exit 2
fi

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd -- "$script_dir/.." && pwd)"
values_file="$1"
version="${2:-}"

if [[ "$values_file" != /* ]]; then
    values_file="$PWD/$values_file"
fi

if [[ ! -f "$values_file" ]]; then
    echo "values file does not exist: $values_file" >&2
    exit 2
fi

kube_context="${KUBE_CONTEXT:-kind-topograph}"
release="${TOPOGRAPH_RELEASE:-topograph}"
namespace="${TOPOGRAPH_NAMESPACE:-topograph}"
timeout="${TOPOGRAPH_TIMEOUT:-5m}"

helm_command=(helm upgrade --install "$release")
if [[ -n "$version" ]]; then
    helm_command+=(topograph
        --repo https://helm.ngc.nvidia.com/nvidia/topograph --version "$version")
else
    helm_command+=("$repo_root/charts/topograph")
fi

"${helm_command[@]}" \
    --kube-context "$kube_context" \
    --namespace "$namespace" \
    --create-namespace \
    --values "$values_file" \
    --wait \
    --timeout "$timeout"
