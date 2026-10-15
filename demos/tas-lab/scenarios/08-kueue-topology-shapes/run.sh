#!/usr/bin/env bash

# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# Scenario 08: the Kueue Topology for each shape of Topograph labels (topologies.yaml, the
# manifests published in docs/engines/k8s.md), and changing
# a Topology's levels while workloads are waiting or admitted. Each pod fills a node (just
# over half of its CPUs), so a Job of N pods needs N nodes. Needs setup/kueue.sh.
set -euo pipefail
here="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
source "$here/../../lib/common.sh"
source "$lab_dir/lib/kueue.sh"

result="$state_dir/kueue-topology-shapes.result"
tier1=fabric.topograph.run/tier-1
tier0=fabric.topograph.run/tier-0
domain=accelerator.topograph.run/domain
subdomain=accelerator.topograph.run/sub-domain

# shape_up <name>: queues and a flavor over the Topology <name> from topologies.yaml.
shape_up() {
    local i quota
    quota="$(kueue_cpu_quota)" || return 1
    for i in $(seq 1 30); do
        if STACK="$1" CPU_QUOTA="$quota" envsubst <"$here/stack.tmpl.yaml" | kubectl apply -f - >/dev/null; then
            return 0
        fi
        sleep 5
    done
    echo "Kueue objects were not accepted; is the webhook ready?" >&2
    return 1
}
# The Topologies stay in place for the whole scenario; only the queues and the flavor go.
shape_down() {
    reset_namespace
    kubectl delete localqueue "$1" -n "$ns" --ignore-not-found >/dev/null
    kubectl delete clusterqueue "$1" --ignore-not-found >/dev/null
    kubectl delete resourceflavor "$1" --ignore-not-found >/dev/null
}

# job <name> <stack> <pods> <required|preferred> <label>
job() { kueue_job "$@"; kueue_record "$1" "$result"; reset_namespace; }

# Remove the queues, flavors and Topologies of every shape; one failing delete must not stop the rest.
cleanup_objects() {
    local s
    for s in topograph-fabric topograph-fabric-domain topograph-fabric-domain-subdomain topograph-domain-first topograph-mutable; do
        shape_down "$s" || true
    done
    kubectl delete -f "$here/topologies.yaml" --ignore-not-found >/dev/null || true
    kubectl delete topology topograph-mutable --ignore-not-found >/dev/null || true
}
# The shapes switch Topograph models; however the scenario ends, remove its objects and leave the
# default labels.
cleanup() {
    trap - EXIT
    cleanup_objects
    "$lab_dir/setup/topograph.sh" generate >/dev/null 2>&1 || echo "warning: could not restore the default labels" >&2
}
trap cleanup EXIT

cleanup_objects
: >"$result"
for i in $(seq 1 30); do kubectl apply -f "$here/topologies.yaml" >/dev/null && break; sleep 5; done

step "Shape 1: fabric tiers only (tas-lab.yaml: two racks under one spine)"
ensure_labels
kubectl get nodes -L "$tier1,$tier0"
shape_up topograph-fabric
job s1-tier1-4 topograph-fabric 4 required "$tier1"
job s1-tier0-2 topograph-fabric 2 required "$tier0"
job s1-tier0-3 topograph-fabric 3 required "$tier0"
shape_down topograph-fabric

step "Shape 2: fabric tiers plus an accelerator domain (tas-lab-multitier.yaml)"
ensure_multitier_labels
kubectl get nodes -L "$tier1,$domain,$tier0"
shape_up topograph-fabric-domain
job s2-domain-2 topograph-fabric-domain 2 required "$domain"
job s2-domain-3 topograph-fabric-domain 3 required "$domain"
echo "-- a node without the domain label (tas-lab-nodomain.yaml: a provider that reports no domain for it)"
"$lab_dir/setup/topograph.sh" generate tas-lab-nodomain.yaml >/dev/null
kubectl get nodes -L "$tier1,$domain,$tier0"
job s2-missing-4 topograph-fabric-domain 4 preferred "$tier1"
job s2-missing-3 topograph-fabric-domain 3 preferred "$tier1"
echo "-- a domain that crosses spines (tas-lab-crossing.yaml: nvl-a on spine-1 and on tas-lab-worker3 in spine-2)"
"$lab_dir/setup/topograph.sh" generate tas-lab-crossing.yaml >/dev/null
kubectl get nodes -L "$tier1,$domain,$tier0"
job s2-crossing-nested topograph-fabric-domain 3 required "$domain"
shape_down topograph-fabric-domain
shape_up topograph-domain-first
job s2-crossing-first topograph-domain-first 3 required "$domain"
shape_down topograph-domain-first

step "Shape 3: fabric tiers, an accelerator domain and a sub-domain (tas-lab-subdomain.yaml)"
"$lab_dir/setup/topograph.sh" generate tas-lab-subdomain.yaml >/dev/null
kubectl get nodes -L "$tier1,$domain,$subdomain,$tier0"
shape_up topograph-fabric-domain-subdomain
job s3-sub-2 topograph-fabric-domain-subdomain 2 required "$subdomain"
job s3-sub-3 topograph-fabric-domain-subdomain 3 required "$subdomain"
job s3-domain-4 topograph-fabric-domain-subdomain 4 required "$domain"
shape_down topograph-fabric-domain-subdomain

step "Changing a Topology's levels at run time (multi-tier labels)"
ensure_multitier_labels
kubectl apply -f "$here/mutable-fabric.yaml" >/dev/null
shape_up topograph-mutable
echo "-- 1. a Job requires the accelerator domain, which is not a level yet"
kueue_job mut-job topograph-mutable 2 required "$domain"
sleep 12
echo "mut.before_admitted=$(kueue_admitted mut-job)" >>"$result"
echo "mut.before_message=$(kueue_message mut-job | tr '\n' ' ')" >>"$result"
echo "-- 2. add the accelerator domain as a level; the waiting Job should be retried"
kubectl apply -f "$here/mutable-with-domain.yaml"
for i in $(seq 1 40); do [[ "$(kueue_admitted mut-job)" == True ]] && break; sleep 2; done
sleep 8
echo "mut.after_add_admitted=$(kueue_admitted mut-job)" >>"$result"
echo "mut.after_add_running=$(count_pods Running mut-job-)" >>"$result"
echo "mut.after_add_domains=$(domain_count mut-job- "$domain")" >>"$result"
echo "-- 3. drop the widest level while the Job is admitted"
kubectl apply -f "$here/mutable-dropped-spine.yaml"
sleep 12
echo "mut.after_drop_admitted=$(kueue_admitted mut-job)" >>"$result"
echo "mut.after_drop_running=$(count_pods Running mut-job-)" >>"$result"
echo "-- 4. remove the hostname level"
if out="$(kubectl apply -f "$here/mutable-no-hostname.yaml" 2>&1)"; then
    echo "mut.no_hostname=accepted" >>"$result"
else
    echo "mut.no_hostname=rejected" >>"$result"
fi
echo "$out"

step "Clean up and restore the default labels"
cleanup

step "Results"
cat "$result"
