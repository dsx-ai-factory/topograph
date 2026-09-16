/*
 * Copyright 2025-2026 NVIDIA CORPORATION
 * SPDX-License-Identifier: Apache-2.0
 */

package infiniband

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/dsx-ai-factory/topograph/pkg/topology"
)

func TestProviderK8SRejectsMultiRegionRequest(t *testing.T) {
	provider := &ProviderK8S{}
	graph, httpErr := provider.GenerateTopologyConfig(context.Background(), nil, []topology.ComputeInstances{
		{Region: "region-1"},
		{Region: "region-2"},
	})

	require.Nil(t, graph)
	require.EqualError(t, httpErr, "on-prem does not support multi-region topology requests")
}
