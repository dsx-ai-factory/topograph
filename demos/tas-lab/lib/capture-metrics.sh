#!/usr/bin/env bash

# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# Snapshots the scheduler placement metrics, runs a command, snapshots again and prints the diff.
#
#   lib/capture-metrics.sh kubectl apply -f gang.yaml
# `kubectl` here targets the lab context, as in the other scripts.
#
# The per-series change (one "series delta" line each, histogram buckets left out) is printed and,
# with METRICS_OUT set, also written to that file for validate.sh scripts.
# The scheduler pod is scraped over its secure port (10259) through a port-forward on a free local
# port, which is checked before the token is sent.
# With WAIT_FOR_SCRIPT set, that executable is polled (up to WAIT_TIMEOUT seconds, default 60)
# after the command, so the second snapshot follows the scheduling attempt and not a fixed delay.

set -euo pipefail

ctx="${KUBE_CONTEXT:-kind-tas-lab}"
kubectl() { command kubectl --context "$ctx" "$@"; }
pattern='^(scheduler_generated_placements_total|scheduler_placement_evaluations_total|scheduler_placement_evaluation_duration_seconds)'
out="$(mktemp -d)"
trap '[[ -z "${pf:-}" ]] || kill "$pf" 2>/dev/null || true; rm -rf "$out"' EXIT

pod="$(kubectl -n kube-system get pod -l component=kube-scheduler -o name | head -1)"
# A free local port (":10259" lets kubectl pick), so another listener can never receive the token.
pf_log="$out/pf.log"
command kubectl --context "$ctx" -n kube-system port-forward "$pod" :10259 >"$pf_log" 2>&1 &
pf=$!
port=""
for _ in $(seq 1 30); do
    port="$(sed -n 's/^Forwarding from 127.0.0.1:\([0-9]*\) .*/\1/p' "$pf_log" | head -1)"
    [[ -n "$port" ]] && break
    kill -0 "$pf" 2>/dev/null || break
    sleep 1
done
[[ -n "$port" ]] || { echo "port-forward to $pod failed:" >&2; cat "$pf_log" >&2; exit 1; }
token="$(kubectl -n kube-system create token metrics-reader)"

snap() {
    # A series only exists after its first observation, so an empty snapshot is normal.
    curl -sk -H "Authorization: Bearer $token" "https://127.0.0.1:$port/metrics" | { grep -E "$pattern" || true; } | sort >"$out/$1"
}

snap before
"$@"
if [[ -n "${WAIT_FOR_SCRIPT:-}" ]]; then
    deadline=$((SECONDS + ${WAIT_TIMEOUT:-60}))
    until "$WAIT_FOR_SCRIPT" 2>/dev/null; do
        (( SECONDS < deadline )) || { echo "warning: $WAIT_FOR_SCRIPT did not succeed in time" >&2; break; }
        sleep 1
    done
fi
sleep "${SETTLE_SECONDS:-5}"
snap after

# Changed series only; histogram buckets are left out as noise.
summary="$out/summary"
awk 'FILENAME == ARGV[1] { b[$1] = $2; next } { d = $2 - (b[$1] + 0); if (d != 0 && $1 !~ /_bucket/) print $1, d }' \
    "$out/before" "$out/after" >"$summary"
echo "--- scheduler metric changes ---"
if [[ -s "$summary" ]]; then sed 's/{profile="default-scheduler"/{/;s/^/  /' "$summary"; else echo "  (none)"; fi
if [[ -n "${METRICS_OUT:-}" ]]; then cp "$summary" "$METRICS_OUT"; fi
