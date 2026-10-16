#!/usr/bin/env bash

# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# Deletes the TAS lab cluster (Topograph and Kueue go with it) and the scratch files.
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/../lib/common.sh"

kind delete cluster --name tas-lab
rm -rf "$state_dir"
# The Chainsaw CLI in .bin/ (setup/chainsaw.sh) stays; it is a tool, not cluster state.
# The image built by setup/topograph.sh stays in the local image store; remove it with:
#   docker rmi ghcr.io/dsx-ai-factory/topograph:tas-lab
