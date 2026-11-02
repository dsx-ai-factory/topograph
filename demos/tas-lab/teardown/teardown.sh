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

# Deletes the TAS lab cluster (Topograph and Kueue go with it) and the scratch files.
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/../lib/common.sh"

kind delete cluster --name tas-lab
rm -rf "$state_dir"
# The Chainsaw CLI in .bin/ (setup/chainsaw.sh) stays; it is a tool, not cluster state.
# The image pulled by setup/topograph.sh stays in the local image store; remove it with
# (values in versions.env):
#   docker rmi "$TOPOGRAPH_IMG_URL:$TOPOGRAPH_IMG_TAG"
