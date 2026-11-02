// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/dsx-ai-factory/topograph/internal/kwok"
)

func TestMainInternalWritesManifest(t *testing.T) {
	output := filepath.Join(t.TempDir(), "nodes.yaml")

	err := mainInternal(options{
		modelFile:  "../../tests/models/small-tree.yaml",
		outputFile: output,
		capacity:   kwok.DefaultCapacity(),
	})
	require.NoError(t, err)

	data, err := os.ReadFile(output)
	require.NoError(t, err)
	require.Contains(t, string(data), "kind: List")
	require.Contains(t, string(data), "name: i21")
	require.Contains(t, string(data), "accelerator.topology.test/domain: nvl2")
}
