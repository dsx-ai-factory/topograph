/*
 * Copyright 2025 NVIDIA CORPORATION
 * SPDX-License-Identifier: Apache-2.0
 */

package translate

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/topograph/pkg/topology"
	"github.com/stretchr/testify/require"
)

func TestBlockTopology(t *testing.T) {
	testCases := []struct {
		name   string
		nt     *NetworkTopology
		output string
		err    string
	}{
		{
			name: "Case 1: a block without name",
			nt: &NetworkTopology{
				config: &Config{
					BlockSizes: []int{2},
				},
				blocks: []*blockInfo{
					{
						id:    "b1",
						nodes: []string{"n1", "n2"},
					},
				},
			},
			output: strings.Join([]string{
				"BlockName=b1 Nodes=n[1-2]",
				"BlockSizes=2",
				"",
			}, "\n"),
		},
		{
			name: "Case 2: a block with name",
			nt: &NetworkTopology{
				config: &Config{
					BlockSizes: []int{2},
				},
				blocks: []*blockInfo{
					{
						id:    "block001",
						name:  "nvl1",
						nodes: []string{"n1", "n2"},
					},
				},
			},
			output: `# block001=nvl1
BlockName=block001 Nodes=n[1-2]
BlockSizes=2
`,
		},
		{
			name: "Case 3: multiple blocks with mixed settings with blockSizes",
			nt: &NetworkTopology{
				config: &Config{
					BlockSizes: []int{2, 4},
				},
				blocks: []*blockInfo{
					{
						id:    "b1",
						nodes: []string{"n1", "n2"},
					},
					{
						id:    "b2",
						name:  "block2",
						nodes: []string{"n3"},
					},
				},
			},
			output: `BlockName=b1 Nodes=n[1-2]
# b2=block2
BlockName=b2 Nodes=n3
BlockSizes=2,4
`,
		},
		{
			name: "Case 4: multiple blocks with mixed settings without blockSizes",
			nt: &NetworkTopology{
				config: &Config{},
				blocks: []*blockInfo{
					{
						id:    "b1",
						nodes: []string{"n1", "n2"},
					},
					{
						id:    "b2",
						name:  "block2",
						nodes: []string{"n3"},
					},
				},
			},
			output: `BlockName=b1 Nodes=n[1-2]
# b2=block2
BlockName=b2 Nodes=n3
BlockSizes=1,2
`,
		},
		{
			name: "Case 5: no blocks",
			nt: &NetworkTopology{
				config: &Config{},
			},
			err: "cannot determine blockSizes: topology contains no blocks",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			err := tc.nt.toBlockTopology(&buf, false)
			if len(tc.err) != 0 {
				require.EqualError(t, err, tc.err)
			} else {
				require.Nil(t, err)
				require.Equal(t, tc.output, buf.String())
			}
		})
	}
}

func TestGetBlockSizes(t *testing.T) {
	testCases := []struct {
		name           string
		blocks         map[string]int
		blockSize      []int
		expectedOutput []int
	}{
		{
			name: "Case 1: #nodes/block same, #blocks power of 2, blockSizes not requested",
			blocks: map[string]int{
				"nvl1": 2,
				"nvl2": 2,
			},
			expectedOutput: []int{2, 4},
		},
		{
			name: "Case 2: #nodes/block different, #blocks power of 2, blockSizes not requested",
			blocks: map[string]int{
				"nvl1": 2,
				"nvl2": 3,
			},
			expectedOutput: []int{2, 4},
		},
		{
			name: "Case 3: #nodes/block same, #blocks !power of 2, blockSizes not requested",
			blocks: map[string]int{
				"nvl1": 2,
				"nvl2": 2,
				"nvl3": 2,
			},
			expectedOutput: []int{2, 4},
		},
		{
			name: "Case 4: #nodes/block same, #blocks power of 2, blockSizes requested",
			blocks: map[string]int{
				"nvl1": 2,
				"nvl2": 2,
			},
			blockSize:      []int{2},
			expectedOutput: []int{2},
		},
		{
			name: "Case 5: #nodes/block different, #blocks power of 2, blockSizes requested",
			blocks: map[string]int{
				"nvl1": 2,
				"nvl2": 3,
			},
			blockSize:      []int{2},
			expectedOutput: []int{2},
		},
		{
			name: "Case 6: #nodes/block same, #blocks !power of 2, blockSizes requested",
			blocks: map[string]int{
				"nvl1": 2,
				"nvl2": 2,
				"nvl3": 2,
			},
			blockSize:      []int{2},
			expectedOutput: []int{2},
		},
		{
			name: "Case 7: #nodes/block same, #blocks power of 2, blockSizes requested",
			blocks: map[string]int{
				"nvl1": 3,
				"nvl2": 3,
				"nvl3": 3,
				"nvl4": 3,
			},
			blockSize:      []int{3, 6, 12},
			expectedOutput: []int{3, 6, 12},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			blockSize, err := getBlockSizes(populateBlockInfo(tc.blocks), tc.blockSize)
			require.NoError(t, err)
			require.Equal(t, tc.expectedOutput, blockSize)
		})
	}

	blockSizes, err := getBlockSizes(nil, nil)
	require.EqualError(t, err, "cannot determine blockSizes: topology contains no blocks")
	require.Nil(t, blockSizes)

	requestedBlockSizes := []int{8, 16}
	blockSizes, err = getBlockSizes(nil, requestedBlockSizes)
	require.NoError(t, err)
	require.Equal(t, requestedBlockSizes, blockSizes)
}

func TestFindMinDomainSize(t *testing.T) {
	minDomainSize, err := findMinDomainSize(nil)
	require.EqualError(t, err, "cannot determine blockSizes: topology contains no blocks")
	require.Zero(t, minDomainSize)

	minDomainSize, err = findMinDomainSize([]*blockInfo{
		{nodes: []string{"node-1", "node-2", "node-3"}},
		{nodes: []string{"node-4"}},
		{nodes: []string{"node-5", "node-6"}},
	})
	require.NoError(t, err)
	require.Equal(t, 1, minDomainSize)
}

// TestGenerateTopologyConfig exercises GenerateTopologyConfig directly for a
// cluster-wide block topology. For cluster-wide mode (no per-partition Topologies
// map), the returned []*TopologyUnit slice is always empty — the topology is written
// to the writer rather than returned.
//
// BlockSizes={2,4,8} triggers node complementing: baseBlockSize=2,
// maxAcceleratorSize=3, so groupSize=2 (2^1×2=4≥3). Each 3-node accelerator is
// split into 2 base blocks (no empty padding needed since 2 is already a multiple of
// groupSize). The 4 accelerators × 2 blocks each = 8 output blocks total.
//
// The test covers both full output (with node lists compacted to ranges) and
// skeleton-only output (block structure without node lists).
func TestGenerateTopologyConfig(t *testing.T) {
	root, _ := getBlockWithIBTestSet()
	cfg := &Config{
		Plugin:     topology.TopologyBlock,
		BlockSizes: []int{2, 4, 8},
	}
	nt, err := NewNetworkTopology(root, cfg)
	require.NoError(t, err)

	t.Run("full output", func(t *testing.T) {
		var buf bytes.Buffer
		topologies, httpErr := nt.GenerateTopologyConfig(&buf, false)
		require.Nil(t, httpErr)
		// Cluster-wide block topology: topology is written to the writer, not
		// returned in the TopologyUnit slice.
		require.Empty(t, topologies)
		// Each accelerator is split into 2 base blocks (nodes sorted alphabetically,
		// then chunked at baseBlockSize=2): first chunk gets the lower 2, second
		// gets the remaining 1.
		expected := strings.Join([]string{
			"# block001=B1",
			"BlockName=block001 Nodes=Node[104-105]",
			"# block002=B1",
			"BlockName=block002 Nodes=Node106",
			"# block003=B2",
			"BlockName=block003 Nodes=Node[201-202]",
			"# block004=B2",
			"BlockName=block004 Nodes=Node205",
			"# block005=B3",
			"BlockName=block005 Nodes=Node[304-305]",
			"# block006=B3",
			"BlockName=block006 Nodes=Node306",
			"# block007=B4",
			"BlockName=block007 Nodes=Node[401-402]",
			"# block008=B4",
			"BlockName=block008 Nodes=Node403",
			"BlockSizes=2,4,8",
			"",
		}, "\n")
		require.Equal(t, expected, buf.String())
	})

	t.Run("skeleton only", func(t *testing.T) {
		var buf bytes.Buffer
		topologies, httpErr := nt.GenerateTopologyConfig(&buf, true)
		require.Nil(t, httpErr)
		require.Empty(t, topologies)
		// Same 8-block structure but Nodes= is omitted from every line.
		expected := strings.Join([]string{
			"# block001=B1",
			"BlockName=block001",
			"# block002=B1",
			"BlockName=block002",
			"# block003=B2",
			"BlockName=block003",
			"# block004=B2",
			"BlockName=block004",
			"# block005=B3",
			"BlockName=block005",
			"# block006=B3",
			"BlockName=block006",
			"# block007=B4",
			"BlockName=block007",
			"# block008=B4",
			"BlockName=block008",
			"BlockSizes=2,4,8",
			"",
		}, "\n")
		require.Equal(t, expected, buf.String())
	})
}

// TestGetNodeTopologySpecAfterComplement verifies that GetNodeTopologySpec returns
// block IDs that match the emitted topology file after complement splits domains
// across multiple base blocks.
func TestGetNodeTopologySpecAfterComplement(t *testing.T) {
	root, _ := getBlockWithIBTestSet()
	cfg := &Config{
		Plugin:     topology.TopologyBlock,
		BlockSizes: []int{2, 4, 8},
	}
	nt, err := NewNetworkTopology(root, cfg)
	require.NoError(t, err)

	var buf bytes.Buffer
	_, httpErr := nt.GenerateTopologyConfig(&buf, false)
	require.Nil(t, httpErr)

	// Expected mapping: first two nodes of each domain go to the first base block,
	// the third node goes to the second (sorted alphabetically within the domain).
	cases := []struct {
		node    string
		blockID string
	}{
		{"Node104", "block001"}, // B1 first chunk
		{"Node105", "block001"}, // B1 first chunk
		{"Node106", "block002"}, // B1 second chunk — stale before fix
		{"Node201", "block003"}, // B2 first chunk — stale before fix
		{"Node202", "block003"}, // B2 first chunk — stale before fix
		{"Node205", "block004"}, // B2 second chunk — stale before fix
		{"Node304", "block005"}, // B3 first chunk
		{"Node305", "block005"}, // B3 first chunk
		{"Node306", "block006"}, // B3 second chunk
		{"Node401", "block007"}, // B4 first chunk
		{"Node402", "block007"}, // B4 first chunk
		{"Node403", "block008"}, // B4 second chunk
	}
	for _, tc := range cases {
		spec, specErr := nt.GetNodeTopologySpec(tc.node, nil)
		require.Nil(t, specErr, "node %s", tc.node)
		require.Equal(t, "default:"+tc.blockID, spec, "node %s", tc.node)
	}
}

func populateBlockInfo(blocks map[string]int) []*blockInfo {
	result := make([]*blockInfo, 0, len(blocks))

	for name, numNodes := range blocks {
		info := &blockInfo{
			id:    fmt.Sprintf("block-%s", name),
			name:  name,
			nodes: make([]string, 0, numNodes),
		}

		for node := range numNodes {
			info.nodes = append(info.nodes, fmt.Sprintf("%d", node))
		}

		result = append(result, info)
	}
	return result
}
