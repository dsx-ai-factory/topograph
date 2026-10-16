# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# Shared helpers, sourced by the setup and teardown scripts and by the scenario tests' scripts.

lab_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
repo_root="$(cd -- "$lab_dir/../.." && pwd)"
source "$lab_dir/versions.env"
ctx="${KUBE_CONTEXT:-kind-tas-lab}"
# A private directory per user for scratch files (the record of the labels Topograph last applied): refuse one that is
# a symlink or belongs to someone else, so a planted /tmp/tas-lab cannot redirect writes onto other files.
state_dir="${TMPDIR:-/tmp}/tas-lab-$(id -u)"
if [[ -L "$state_dir" ]] || { [[ -e "$state_dir" ]] && { [[ ! -d "$state_dir" ]] || [[ ! -O "$state_dir" ]]; }; }; then
    echo "error: $state_dir is not a directory owned by you; remove it and retry" >&2
    return 1 2>/dev/null || exit 1
fi
mkdir -p "$state_dir"
chmod 700 "$state_dir"

# Every kubectl call in these scripts targets the lab cluster; `command kubectl` is the real binary.
kubectl() { command kubectl --context "$ctx" "$@"; }

# Wait until the API server answers. kind returns as soon as the control plane is up, and the
# first kubectl call can still hit a restarting or not yet published API server ("connection
# refused"), so poll instead of failing on the first call. Prints diagnostics on timeout.
wait_for_api() { # [timeout-seconds]
    local deadline=$((SECONDS + ${1:-120}))
    until kubectl get --raw=/readyz >/dev/null 2>&1; do
        if (( SECONDS >= deadline )); then
            echo "error: the API server of context '$ctx' did not become ready" >&2
            echo "  server in kubeconfig : $(kubectl config view --minify -o jsonpath='{.clusters[0].cluster.server}' 2>/dev/null)" >&2
            echo "  published by the node: $(docker port tas-lab-control-plane 6443 2>/dev/null || echo 'node container not found')" >&2
            echo "  node containers      : $(docker ps -a --filter name=tas-lab --format '{{.Names}}={{.Status}}' 2>/dev/null | tr '\n' ' ')" >&2
            echo "  KUBECONFIG           : ${KUBECONFIG:-unset}" >&2
            echo "If the two addresses differ, a stale kind-tas-lab entry in another kubeconfig file shadows" >&2
            echo "the new one: run 'kubectl config delete-context kind-tas-lab' for the old file and retry." >&2
            return 1
        fi
        sleep 2
    done
}

step() { printf '\n\033[33m== %s\033[0m\n' "$*"; }

# The multi-tier scenarios key on tier-1 and the accelerator domain, which the default model does
# not provide. Switch the nodes to tests/models/tas-lab-multitier.yaml.
ensure_multitier_labels() {
    local good
    good="$(kubectl get nodes -l 'tas-lab=worker,fabric.topograph.run/tier-0 in (leaf-1,leaf-2,leaf-3,leaf-4),fabric.topograph.run/tier-1 in (spine-1,spine-2),accelerator.topograph.run/domain in (nvl-a,nvl-b,nvl-c)' --no-headers | wc -l)"
    # nvl-b and nvl-c are single nodes only in this model; they tell it apart from the variants
    # (tas-lab-nodomain.yaml, tas-lab-crossing.yaml) that differ in one node's domain.
    if (( good != 4 )) \
        || (( $(kubectl get nodes -l accelerator.topograph.run/domain=nvl-b --no-headers | wc -l) != 1 )) \
        || (( $(kubectl get nodes -l accelerator.topograph.run/domain=nvl-c --no-headers | wc -l) != 1 )); then
        echo "Switching Topograph labels to the multi-tier model"
        "$lab_dir/setup/topograph.sh" generate tas-lab-multitier.yaml >/dev/null
    fi
}

# The workers' labels, as one checksum, to tell whether they changed since Topograph wrote them.
label_fingerprint() {
    kubectl get nodes -l tas-lab=worker -o go-template='{{range .items}}{{.metadata.name}} {{.metadata.labels}}{{"\n"}}{{end}}' | cksum
}

# generate_labels [model] [engine-params-json]: like `setup/topograph.sh generate`, but skipped when
# the workers still carry the labels that the same request wrote last time (the request and the
# labels' checksum are kept in $state_dir). A test that starts from the model the previous test
# restored then does not pay for a second generation.
generate_labels() {
    local request="${1:-tas-lab.yaml}|${2:-}" applied="$state_dir/labels-applied"
    [[ "$request" == *"|" ]] && request="${request}{}"
    if [[ -f "$applied" && "$(cat "$applied")" == "$request|$(label_fingerprint)" ]]; then
        return 0
    fi
    # A failed generation is not recorded, so the next call retries it.
    "$lab_dir/setup/topograph.sh" generate "${1:-tas-lab.yaml}" "${request#*|}" >/dev/null || return 1
    echo "$request|$(label_fingerprint)" >"$applied"
}
