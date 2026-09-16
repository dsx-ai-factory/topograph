/*
 * Copyright 2026 NVIDIA CORPORATION
 * SPDX-License-Identifier: Apache-2.0
 */

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
