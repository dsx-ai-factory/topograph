#!/usr/bin/env bash

# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# Runs every scenario's run.sh then validate.sh and prints a summary. Scenario 03 takes about
# two minutes because the scheduler is restarted twice.
set -uo pipefail
d="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
failed=()
for s in "$d"/[0-9]*/; do
    name="$(basename "$s")"
    printf '\n\033[36m######## %s ########\033[0m\n' "$name"
    "$s/run.sh" || failed+=("$name (run)")
    "$s/validate.sh" || failed+=("$name (validate)")
done
echo
if (( ${#failed[@]} )); then
    printf 'FAILED: %s\n' "${failed[@]}"
    exit 1
fi
echo "All scenarios passed."
