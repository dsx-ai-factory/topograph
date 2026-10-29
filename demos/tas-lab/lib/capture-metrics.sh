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

# Reads the kube-scheduler's placement metrics for the scenario tests, which take a baseline before
# a workload is submitted and compare after it (scenarios/templates/scheduler-metrics-*.yaml):
#
#   lib/capture-metrics.sh --snapshot <file>              write the current series to <file>
#   lib/capture-metrics.sh --delta <file> <series-regex>  print how much the matching series changed since <file>
#
# --delta reads counters, so it prints whole numbers and fails if a series went down (a scheduler restart).
# The series are scheduler_generated_placements_total, scheduler_placement_evaluations_total and
# scheduler_placement_evaluation_duration_seconds. `kubectl` here targets the lab context
# (KUBE_CONTEXT, default kind-tas-lab). The scheduler pod is scraped over its secure port (10259)
# through a port-forward on a free local port, which is checked before the token is sent.

set -euo pipefail

ctx="${KUBE_CONTEXT:-kind-tas-lab}"
kubectl() { command kubectl --context "$ctx" "$@"; }
pattern='^(scheduler_generated_placements_total|scheduler_placement_evaluations_total|scheduler_placement_evaluation_duration_seconds)'
out="$(mktemp -d)"
trap '[[ -z "${pf:-}" ]] || kill "$pf" 2>/dev/null || true; rm -rf "$out"' EXIT

usage() { echo "usage: $0 --snapshot <file> | --delta <file> <series-regex>" >&2; exit 2; }
case "${1:-}" in
--snapshot) [[ $# -eq 2 ]] || usage ;;
--delta) [[ $# -eq 3 ]] || usage ;;
*) usage ;;
esac

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

snap() { # <file>
    # A series only exists after its first observation, so an empty snapshot is normal; an HTTP error
    # (-f) is not, and fails the pipeline instead of leaving an empty file that reads as valid metrics.
    curl -sfk -H "Authorization: Bearer $token" "https://127.0.0.1:$port/metrics" | { grep -E "$pattern" || true; } | sort >"$1"
}

if [[ "$1" == --snapshot ]]; then
    snap "$2"
else
    [[ -f "$2" ]] || { echo "no snapshot $2; take one with --snapshot first" >&2; exit 1; }
    snap "$out/after"
    # The series name and labels are everything before the last field, so a label value with a space
    # stays in the key. The regex comes in through the environment, so awk does not process its
    # backslash escapes. Histogram buckets and _sum series (elapsed seconds, not counts) are left out. Counters only go up: a series that
    # fell or vanished means the scheduler restarted since the snapshot, and the change is unreadable.
    SERIES="$3" awk '
        function key(line, v) { return substr(line, 1, length(line) - length(v) - 1) }
        FILENAME == ARGV[1] { b[key($0, $NF)] = $NF; next }
        { k = key($0, $NF); seen[k] = 1 }
        k !~ /_bucket/ && k !~ /_sum[{]/ && k !~ /_sum$/ && k ~ ENVIRON["SERIES"] { d = $NF - (b[k] + 0); if (d < 0) reset = 1; s += d }
        END {
            for (k in b) if (!(k in seen) && k !~ /_bucket/ && k !~ /_sum[{]/ && k !~ /_sum$/ && k ~ ENVIRON["SERIES"]) reset = 1
            if (reset) { print "a matching series went down or vanished: the scheduler restarted since the snapshot" > "/dev/stderr"; exit 1 }
            printf "%.0f\n", s
        }' "$2" "$out/after"
fi
