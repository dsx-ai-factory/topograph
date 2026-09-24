/*
 * Copyright 2026 NVIDIA CORPORATION
 * SPDX-License-Identifier: Apache-2.0
 */

package translate

import (
	"fmt"
	"testing"

	"github.com/dsx-ai-factory/topograph/pkg/topology"
	"github.com/stretchr/testify/require"
)

func TestSortHostsByName(t *testing.T) {
	hosts := []*topology.HostInfo{
		{HostName: "z"},
		{HostName: "a"},
		{HostName: "m"},
	}
	sortHostsByName(hosts)
	require.Equal(t, []string{"a", "m", "z"}, []string{hosts[0].HostName, hosts[1].HostName, hosts[2].HostName})
}

// TestBaseBlockFillsSlotLeftToRight verifies that hosts fill base block slots left to
// right and that slots beyond the provided hosts remain empty placeholders.
func TestBaseBlockFillsSlotLeftToRight(t *testing.T) {
	bb := newBaseBlock("B1", []*topology.HostInfo{
		{HostName: "n0", Domain: "B1"},
		{HostName: "n1", Domain: "B1"},
	}, 4)

	require.Len(t, bb.leaves, 4)
	require.NotNil(t, bb.leaves[0].host)
	require.Equal(t, "n0", bb.leaves[0].host.HostName)
	require.NotNil(t, bb.leaves[1].host)
	require.Equal(t, "n1", bb.leaves[1].host.HostName)
	require.Nil(t, bb.leaves[2].host)
	require.Nil(t, bb.leaves[3].host)
}

// TestBlockTreeGCD verifies the Euclidean GCD helper used to bound the padding loop.
func TestBlockTreeGCD(t *testing.T) {
	require.Equal(t, 4, blockTreeGCD(8, 4))
	require.Equal(t, 4, blockTreeGCD(4, 8))
	require.Equal(t, 1, blockTreeGCD(3, 7))
	require.Equal(t, 9, blockTreeGCD(9, 144))
	require.Equal(t, 6, blockTreeGCD(6, 6))
	require.Equal(t, 8, blockTreeGCD(0, 8)) // a==0: identity returns b
	require.Equal(t, 8, blockTreeGCD(8, 0)) // b==0: loop exits immediately
	require.Equal(t, 0, blockTreeGCD(0, 0)) // both zero: returns 0
}

// TestComplementPaddingBoundedForNonDivisorBlockSizes verifies that buildBlockTree
// terminates and produces a correct result when blockSizes are not in a divisor
// relationship (blockSizes=[3,7]: gcd=1, lcm=21). One real domain of 3 hosts must
// pad to 7 base blocks (21 nodes = lcm/baseBlockSize) in a bounded number of steps.
func TestComplementPaddingBoundedForNonDivisorBlockSizes(t *testing.T) {
	domains := topology.NewDomainMap()
	for i := 1; i <= 3; i++ {
		domains.AddHostInfo(&topology.HostInfo{Domain: "d1", HostName: fmt.Sprintf("h%d", i)})
	}
	nt := &NetworkTopology{
		domains: domains,
		blocks:  []*blockInfo{{name: "d1", nodes: []string{"h1", "h2", "h3"}}},
	}
	out, _ := nt.complementBlocks(nt.blocks, []int{3, 7})
	// lcm(3, 7) = 21 nodes; 21 / 3 per block = 7 base blocks (1 real + 6 empty)
	require.Len(t, out, 7)
	require.Equal(t, "d1", out[0].name)
	for _, b := range out[1:] {
		require.True(t, isEmptyBlock(b), "trailing blocks must be empty placeholders")
	}
}

// TestSplitIntoBaseBlocksChunksExcessHosts verifies that 12 hosts with a blockSize of 4
// produce exactly 3 blocks, each fully populated, filling slots left-to-right.
func TestSplitIntoBaseBlocksChunksExcessHosts(t *testing.T) {
	hosts := make([]*topology.HostInfo, 12)
	for i := range 12 {
		hosts[i] = &topology.HostInfo{
			HostName: fmt.Sprintf("n%d", i),
			Domain:   "B1",
		}
	}
	sortHostsByName(hosts)
	blocks := splitIntoBaseBlocks("B1", hosts, 4)
	require.Len(t, blocks, 3)
	require.Len(t, blocks[0].leaves, 4)
	require.Len(t, hostNamesFromLeaves(blocks[0].leaves), 4)
	require.Len(t, hostNamesFromLeaves(blocks[1].leaves), 4)
	require.Len(t, hostNamesFromLeaves(blocks[2].leaves), 4)
}
