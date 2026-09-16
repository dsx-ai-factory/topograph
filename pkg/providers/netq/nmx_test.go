/*
 * Copyright 2025 NVIDIA CORPORATION
 * SPDX-License-Identifier: Apache-2.0
 */

package netq

import (
	"os"
	"testing"

	"github.com/dsx-ai-factory/topograph/pkg/topology"
	"github.com/stretchr/testify/require"
)

func TestParseComputeNodes(t *testing.T) {
	data, err := os.ReadFile("../../../tests/output/netq/computeNodes.json")
	require.NoError(t, err)

	domains, err := parseComputeNodes(data)
	require.Nil(t, err)

	expected := topology.NewDomainMap()
	expected.AddHost("3fbfc98b-7f95-4749-ab11-bd351a2aab3e", "node1", "node1")
	expected.AddHost("3fbfc98b-7f95-4749-ab11-bd351a2aab3e", "node2", "node2")

	require.Equal(t, expected, domains)
}
