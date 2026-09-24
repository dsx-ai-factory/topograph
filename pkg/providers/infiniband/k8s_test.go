/*
 * Copyright 2025-2026 NVIDIA CORPORATION
 * SPDX-License-Identifier: Apache-2.0
 */

package infiniband

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/dsx-ai-factory/topograph/pkg/accelerator"
	"github.com/dsx-ai-factory/topograph/pkg/topology"
)

func TestGetNodeAnnotationsWithoutCollection(t *testing.T) {
	ctx := context.TODO()

	sections := []string{
		"",
		`{"source":"kubernetes-label","kubernetesLabel":{"key":"example.com/domain"}}`,
		`{"source":"none"}`,
	}
	for _, encodedSection := range sections {
		section, err := accelerator.DecodeSection(encodedSection)
		require.NoError(t, err)
		annotations, err := GetNodeAnnotations(ctx, nil, nil, "node-1", section)
		require.NoError(t, err)
		require.Equal(t, map[string]string{
			topology.KeyNodeInstance: "node-1",
			topology.KeyNodeRegion:   "local",
		}, annotations)
	}

	section, err := accelerator.DecodeSection(`{"source":"invalid"}`)
	require.NoError(t, err)
	_, err = GetNodeAnnotations(ctx, nil, nil, "node-1", section)
	require.EqualError(t, err, `unsupported accelerator source "invalid"`)
}
