#!/usr/bin/env bash

# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# Runs all five setup steps in order.
set -euo pipefail
d="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"

"$d/cluster.sh"
"$d/topograph.sh" install
"$d/kueue.sh"
"$d/lws.sh"
"$d/chainsaw.sh"
