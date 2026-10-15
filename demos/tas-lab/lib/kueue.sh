# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# Helpers for the Kueue scenarios; source after lib/common.sh.

kueue_dir="$lab_dir/lib/kueue"

# kueue_stack_up <stack> <topology manifest> <topology name>
kueue_stack_up() {
    local stack="$1" topology_file="$2" topology="$3" i quota
    quota="$(kueue_cpu_quota)" || return 1
    # The Kueue webhook can need a few seconds before it accepts objects.
    for i in $(seq 1 30); do
        if kubectl apply -f "$topology_file" >/dev/null && \
            STACK="$stack" TOPOLOGY="$topology" CPU_QUOTA="$quota" envsubst <"$kueue_dir/stack.tmpl.yaml" | kubectl apply -f - >/dev/null; then
            return 0
        fi
        sleep 5
    done
    echo "Kueue objects were not accepted; is the webhook ready?" >&2
    return 1
}

# kueue_stack_down <stack> <topology name>
kueue_stack_down() {
    reset_namespace
    kubectl delete localqueue "$1" -n "$ns" --ignore-not-found >/dev/null
    kubectl delete clusterqueue "$1" --ignore-not-found >/dev/null
    kubectl delete resourceflavor "$1" --ignore-not-found >/dev/null
    kubectl delete topology "$2" --ignore-not-found >/dev/null
}

# kueue_cpu_stats <min|sum>: the smallest or the total allocatable CPU of the workers, in millicores.
kueue_cpu_stats() {
    kubectl get nodes -l tas-lab=worker -o jsonpath='{range .items[*]}{.status.allocatable.cpu}{"\n"}{end}' \
        | awk -v mode="$1" '/m$/ { v = $1 + 0 } !/m$/ { v = $1 * 1000 } { s += v } NR == 1 || v < m { m = v } END { print (mode == "sum" ? s : m) + 0 }'
}

# kueue_cpu_quota: the ClusterQueue CPU quota, the workers' total allocatable CPU, so the quota
# never refuses a Job that fits the nodes.
kueue_cpu_quota() {
    local sum
    sum="$(kueue_cpu_stats sum)"
    (( sum > 0 )) || { echo "cannot read the allocatable CPU of the workers" >&2; return 1; }
    echo "${sum}m"
}

# kueue_cpu_request: just over half of the smallest worker's allocatable CPU, in millicores, so a
# node never holds two pods however many CPUs the host gave the kind nodes.
kueue_cpu_request() {
    local min
    min="$(kueue_cpu_stats min)"
    (( min > 0 )) || { echo "cannot read the allocatable CPU of the workers" >&2; return 1; }
    echo "$(( min / 2 + 1 ))m"
}

# kueue_job <name> <stack> <pods> <annotation suffix: required|preferred> <node label>
kueue_job() {
    local cpu
    cpu="$(kueue_cpu_request)" || return 1
    NAME="$1" STACK="$2" PODS="$3" CPU_REQUEST="$cpu" ANNOTATION="kueue.x-k8s.io/podset-$4-topology" LEVEL="$5" \
        envsubst <"$kueue_dir/job.tmpl.yaml" | kubectl create -f - >/dev/null
}

# Admission state of the Workload of a job: True, False (with the reason in $kueue_msg) or none.
kueue_admitted() { # <job name>
    local wl
    wl="$(kubectl -n "$ns" get workloads.kueue.x-k8s.io -o name | grep -m1 "$1" || true)"
    [[ -n "$wl" ]] || { echo none; return; }
    kubectl -n "$ns" get "$wl" -o jsonpath='{.status.conditions[?(@.type=="Admitted")].status}'
}

kueue_message() { # <job name>
    local wl
    wl="$(kubectl -n "$ns" get workloads.kueue.x-k8s.io -o name | grep -m1 "$1" || true)"
    [[ -n "$wl" ]] && kubectl -n "$ns" get "$wl" -o jsonpath='{.status.conditions[?(@.type=="QuotaReserved")].message}'
}

# kueue_record <job name> <results file>: wait a little, then store what happened.
kueue_record() {
    local name="$1" out="$2" admitted i
    for i in $(seq 1 20); do
        admitted="$(kueue_admitted "$name")"
        [[ "$admitted" == True ]] && break
        sleep 1
    done
    sleep 8
    {
        echo "$name.admitted=$(kueue_admitted "$name")"
        echo "$name.running=$(count_pods Running "$name-")"
        echo "$name.nodes=$(pod_nodes "$name-")"
        echo "$name.tier0_domains=$(domain_count "$name-" fabric.topograph.run/tier-0)"
        echo "$name.tier1_domains=$(domain_count "$name-" fabric.topograph.run/tier-1)"
        echo "$name.accel_domains=$(domain_count "$name-" accelerator.topograph.run/domain)"
        echo "$name.subdomains=$(domain_count "$name-" accelerator.topograph.run/sub-domain)"
        echo "$name.message=$(kueue_message "$name" | tr '\n' ' ')"
    } >>"$out"
}
