# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# Shared helpers, sourced by the setup, teardown and scenario scripts.

lab_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
repo_root="$(cd -- "$lab_dir/../.." && pwd)"
source "$lab_dir/versions.env"
ctx="${KUBE_CONTEXT:-kind-tas-lab}"
ns=tas-lab
tier0=fabric.topograph.run/tier-0
# A private directory per user: refuse one that is a symlink or belongs to someone else, so a
# planted /tmp/tas-lab cannot redirect the result and template writes onto other files.
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

# Delete everything the scenarios create; leaves the namespace, nodes and Kueue objects alone.
reset_namespace() {
    local res served
    kubectl -n "$ns" delete job,pod --all --wait=true >/dev/null
    # PodGroups and scheduler Workloads exist only with the TAS feature gates; the others need
    # their controller. Skip a type that is not served, but report a failed delete of one that is.
    for res in podgroups.scheduling.k8s.io workloads.scheduling.k8s.io \
        leaderworkersets.leaderworkerset.x-k8s.io workloads.kueue.x-k8s.io; do
        served="$(kubectl api-resources --no-headers -o name 2>/dev/null || true)"
        grep -qx "$res" <<<"$served" || continue
        kubectl -n "$ns" delete "$res" --all --wait=true >/dev/null || echo "warning: could not delete $res" >&2
    done
}

# Scenarios rely on Topograph's labels from the default model, tas-lab.yaml: usable tier-0 values
# and no accelerator domain. Re-run generation when any worker lacks a tier-0 value (for example
# after the label edge-case scenario) or still carries a domain from another model.
ensure_labels() {
    local good stale
    good="$(kubectl get nodes -l 'tas-lab=worker,fabric.topograph.run/tier-0 in (rack-a,rack-b)' --no-headers | wc -l)"
    stale="$(kubectl get nodes -l accelerator.topograph.run/domain --no-headers | wc -l)"
    if (( good != 4 || stale != 0 )); then
        echo "Topograph labels missing or modified; regenerating"
        "$lab_dir/setup/topograph.sh" generate >/dev/null
    fi
}

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

# Print each pod of the namespace with its node, phase and the node's tier-0 value.
show_pods() {
    local pod node phase domain
    printf '%-18s %-10s %-18s %s\n' POD PHASE NODE TIER-0
    while read -r pod; do
        node="$(kubectl -n "$ns" get pod "$pod" -o jsonpath='{.spec.nodeName}')"
        phase="$(kubectl -n "$ns" get pod "$pod" -o jsonpath='{.status.phase}')"
        domain=""
        if [[ -n "$node" ]]; then
            domain="$(kubectl get node "$node" -o jsonpath='{.metadata.labels.fabric\.topograph\.run/tier-0}')"
        fi
        printf '%-18s %-10s %-18s %s\n' "$pod" "$phase" "${node:--}" "${domain:-<empty>}"
    done < <(kubectl -n "$ns" get pods -o name | sed 's|pod/||')
}

# Number of pods with the given phase, optionally limited to a name prefix.
count_pods() { # <phase> [name-prefix]
    kubectl -n "$ns" get pods --no-headers -o custom-columns=N:.metadata.name,P:.status.phase \
        | awk -v p="$1" -v pre="${2:-}" 'index($1, pre) == 1 && $2 == p' | wc -l | tr -d ' '
}

# Number of distinct values of a node label among the nodes the pods ran on.
domain_count() { # [name-prefix] [label-key, default tier-0]
    local pod node key="${2:-$tier0}"
    while read -r pod; do
        node="$(kubectl -n "$ns" get pod "$pod" -o jsonpath='{.spec.nodeName}')"
        [[ -n "$node" ]] && kubectl get node "$node" -o go-template="{{index .metadata.labels \"$key\"}}{{\"\\n\"}}"
    done < <(kubectl -n "$ns" get pods -o name | sed 's|pod/||' | grep "^${1:-}") | sort -u | wc -l | tr -d ' '
}

# Comma-separated nodes the matching pods ran on, sorted.
pod_nodes() { # [name-prefix]
    kubectl -n "$ns" get pods -o custom-columns=N:.metadata.name,NODE:.spec.nodeName --no-headers \
        | awk -v pre="${1:-}" 'index($1, pre) == 1 && $2 != "<none>" { print $2 }' | sort | paste -sd, -
}

# Run a command, record the scheduler placement metric changes into $state_dir/<name>.metrics.
capture() { # <name> <command...>
    local name="$1"; shift
    METRICS_OUT="$state_dir/$name.metrics" "$lab_dir/lib/capture-metrics.sh" "$@"
}

# Sum of the recorded change for series whose name/labels match a pattern.
metric_delta() { # <name> <series-pattern>
    awk -v pat="$2" '$1 ~ pat { s += $2 } END { print s + 0 }' "$state_dir/$1.metrics"
}

PASS=0
FAIL=0
check() { # <description> <expected> <actual>
    if [[ "$2" == "$3" ]]; then
        printf '  \033[32mPASS\033[0m %s (%s)\n' "$1" "$3"; PASS=$((PASS + 1))
    else
        printf '  \033[31mFAIL\033[0m %s: expected %s, got %s\n' "$1" "$2" "$3"; FAIL=$((FAIL + 1))
    fi
}
check_gt() { # <description> <actual> (must be > 0)
    if (( $(printf '%.0f' "$2") > 0 )); then
        printf '  \033[32mPASS\033[0m %s (%s)\n' "$1" "$2"; PASS=$((PASS + 1))
    else
        printf '  \033[31mFAIL\033[0m %s: expected > 0, got %s\n' "$1" "$2"; FAIL=$((FAIL + 1))
    fi
}
finish() { echo; echo "$PASS passed, $FAIL failed"; (( FAIL == 0 )); }
