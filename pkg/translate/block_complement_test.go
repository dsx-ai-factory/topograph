/*
 * Copyright 2026 NVIDIA CORPORATION
 * SPDX-License-Identifier: Apache-2.0
 */

package translate

import (
	"bytes"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/topograph/pkg/models"
	"github.com/dsx-ai-factory/topograph/pkg/topology"
	"github.com/stretchr/testify/require"
)

// TestComplementMissingBaseBlock verifies that when an accelerator domain is absent
// from the graph the complement tree pads to the next valid blockSizes capacity. With
// blockSizes=[4,8,16] and 3 domains each holding ≤4 nodes, a 4th empty block is added
// to reach the 16-node lastBS boundary.
func TestComplementMissingBaseBlock(t *testing.T) {
	root, _ := getBlockWithIBTestSet()
	delete(root.Domains, "B2")

	cfg := &Config{
		Plugin:     topology.TopologyBlock,
		BlockSizes: []int{4, 8, 16},
	}
	nt, err := NewNetworkTopology(root, cfg)
	require.NoError(t, err)

	var buf bytes.Buffer
	require.Nil(t, nt.toBlockTopology(&buf, false))

	expected := strings.Join([]string{
		"# block001=B1",
		"BlockName=block001 Nodes=Node[104-106]",
		"# block002=B3",
		"BlockName=block002 Nodes=Node[304-306]",
		"# block003=B4",
		"BlockName=block003 Nodes=Node[401-403]",
		"BlockName=block004",
		"BlockSizes=4,8,16",
		"",
	}, "\n")
	require.Equal(t, expected, buf.String())
}

// TestComplementMissingLeafSegment verifies the asymmetric-spine case: one spine has
// 4 leaf switches and the other has 3. With blockSizes=[4,16,32] and 7 domains, the
// tree pads to the next 32-node boundary, adding one empty block008 placeholder.
func TestComplementMissingLeafSegment(t *testing.T) {
	root, _ := getBlockWithIBAsymmetricSpineTestSet()

	cfg := &Config{
		Plugin:     topology.TopologyBlock,
		BlockSizes: []int{4, 16, 32},
	}
	nt, err := NewNetworkTopology(root, cfg)
	require.NoError(t, err)

	var buf bytes.Buffer
	require.Nil(t, nt.toBlockTopology(&buf, false))

	expected := strings.Join([]string{
		"# block001=B1",
		"BlockName=block001 Nodes=Node[101-103]",
		"# block002=B2",
		"BlockName=block002 Nodes=Node[201-202,205]",
		"# block003=B3",
		"BlockName=block003 Nodes=Node[301-303]",
		"# block004=B4",
		"BlockName=block004 Nodes=Node[401-403]",
		"# block005=B5",
		"BlockName=block005 Nodes=Node[501-503]",
		"# block006=B6",
		"BlockName=block006 Nodes=Node[601-603]",
		"# block007=B7",
		"BlockName=block007 Nodes=Node[701-703]",
		"BlockName=block008",
		"BlockSizes=4,16,32",
		"",
	}, "\n")
	require.Equal(t, expected, buf.String())
}

// TestNoComplementWithoutTree verifies that complementBlocks produces no empty placeholder
// slots when the graph has no Tiers (no switch tree) and no host carries a SubDomain.
// With blockSizes=[4,8,16] and each domain holding 3 nodes (DesiredNodeCount=4 = 1 base
// block), the root's DesiredNodeCount=16 drives padding only at the root level, not within
// individual domains. The per-domain output contains no empty block slots.
func TestNoComplementWithoutTree(t *testing.T) {
	root, _ := getBlockTestSet()
	cfg := &Config{
		Plugin:     topology.TopologyBlock,
		BlockSizes: []int{4, 8, 16},
	}
	nt, err := NewNetworkTopology(root, cfg)
	require.NoError(t, err)

	var buf bytes.Buffer
	require.Nil(t, nt.toBlockTopology(&buf, false))
	require.NotContains(t, buf.String(), "BlockName=block002\n")
	require.Contains(t, buf.String(), "BlockSizes=4,8,16")
}

// TestNoComplementSingleBlockSize verifies that a single BlockSizes entry (no tiers)
// disables the complement path entirely.
func TestNoComplementSingleBlockSize(t *testing.T) {
	root, _ := getBlockWithIBTestSet()
	cfg := &Config{
		Plugin:     topology.TopologyBlock,
		BlockSizes: []int{3},
	}
	nt, err := NewNetworkTopology(root, cfg)
	require.NoError(t, err)

	var buf bytes.Buffer
	require.Nil(t, nt.toBlockTopology(&buf, false))
	require.Equal(t, testBlockConfig1_2, buf.String())
}

// TestComplementKeepsSeparateAccelerators verifies that two undersized accelerators are
// never merged into a single base block. maxAcceleratorSize=3 ≤ baseBlockSize=8, so
// groupSize=1 and complement is a no-op; the original 2-block list is returned with
// each accelerator in its own separate block.
func TestComplementKeepsSeparateAccelerators(t *testing.T) {
	domains := topology.NewDomainMap()
	nodesB1 := []string{"Node101", "Node102", "Node103"}
	nodesB2 := []string{"Node201", "Node202", "Node205"}
	for _, n := range nodesB1 {
		domains.AddHostInfo(&topology.HostInfo{Domain: "B1", HostName: n, InstanceID: n})
	}
	for _, n := range nodesB2 {
		domains.AddHostInfo(&topology.HostInfo{Domain: "B2", HostName: n, InstanceID: n})
	}

	nt := &NetworkTopology{
		config:  &Config{BlockSizes: []int{8, 16}},
		domains: domains,
		blocks: []*blockInfo{
			{name: "B1", nodes: nodesB1},
			{name: "B2", nodes: nodesB2},
		},
	}

	var buf bytes.Buffer
	require.Nil(t, nt.toBlockTopology(&buf, false))

	expected := strings.Join([]string{
		"# block001=B1",
		"BlockName=block001 Nodes=Node[101-103]",
		"# block002=B2",
		"BlockName=block002 Nodes=Node[201-202,205]",
		"BlockSizes=8,16",
		"",
	}, "\n")
	require.Equal(t, expected, buf.String())
}

// TestComplementExcessHostsPerAccelerator verifies the split path: when a single
// accelerator has more hosts than baseBlockSize it is split into multiple base blocks,
// each carrying the same accelerator name, and every host appears exactly once.
// maxAcceleratorSize=12, baseBlockSize=4 → groupSize=4 (2^2*4=16 ≥ 12); 3 real blocks
// padded to 4 (ceil(3/4)*4).
func TestComplementExcessHostsPerAccelerator(t *testing.T) {
	domains := topology.NewDomainMap()
	nodes := make([]string, 0, 12)
	for i := 0; i < 12; i++ {
		name := fmt.Sprintf("Node%03d", 100+i)
		nodes = append(nodes, name)
		domains.AddHostInfo(&topology.HostInfo{
			Domain:     "B1",
			HostName:   name,
			InstanceID: name,
		})
	}

	nt := &NetworkTopology{
		config:  &Config{BlockSizes: []int{4, 8, 16}},
		domains: domains,
		blocks: []*blockInfo{{
			id:    "block001",
			name:  "B1",
			nodes: nodes,
		}},
	}

	var buf bytes.Buffer
	require.Nil(t, nt.toBlockTopology(&buf, false))

	// 3 real base blocks (ceil(12/4)) padded to 4 (groupSize=4, ceil(3/4)*4=4).
	expected := strings.Join([]string{
		"# block001=B1",
		"BlockName=block001 Nodes=Node[100-103]",
		"# block002=B1",
		"BlockName=block002 Nodes=Node[104-107]",
		"# block003=B1",
		"BlockName=block003 Nodes=Node[108-111]",
		"BlockName=block004",
		"BlockSizes=4,8,16",
		"",
	}, "\n")
	require.Equal(t, expected, buf.String())
}

// TestComplementPartitionLocalDomainsOnly verifies that complementBlocks scopes domain
// lookup to the partition's own blocks. B2 exists in nt.domains but is excluded from
// partitionBlocks, so the complement result contains only B1, B3, and B4. With
// maxAcceleratorSize=3 ≤ baseBlockSize=4, groupSize=1 and no padding is applied.
func TestComplementPartitionLocalDomainsOnly(t *testing.T) {
	root, _ := getBlockWithIBTestSet()
	nt, err := NewNetworkTopology(root, &Config{Plugin: topology.TopologyBlock, BlockSizes: []int{4, 8, 16}})
	require.NoError(t, err)

	// Partition owns B1, B3, B4 but not B2 (B2 remains in global nt.domains).
	partitionBlocks := make([]*blockInfo, 0, 3)
	for _, b := range nt.blocks {
		if b.name == "B2" {
			continue
		}
		partitionBlocks = append(partitionBlocks, b)
	}
	nt.blocks = partitionBlocks

	var buf bytes.Buffer
	require.Nil(t, nt.toBlockTopology(&buf, false))

	// 3 real domains padded to 4 to reach the 16-node lastBS boundary.
	expected := strings.Join([]string{
		"# block001=B1",
		"BlockName=block001 Nodes=Node[104-106]",
		"# block002=B3",
		"BlockName=block002 Nodes=Node[304-306]",
		"# block003=B4",
		"BlockName=block003 Nodes=Node[401-403]",
		"BlockName=block004",
		"BlockSizes=4,8,16",
		"",
	}, "\n")
	require.Equal(t, expected, buf.String())
}

// TestDomainsForBlocksFilteredToPartitionNodes is a regression test for cross-partition
// node contamination. Domain B1 holds 4 nodes globally (n1–n4), but the partition-local
// blockInfo only lists n1, n2, n3. Without filtering, domainsForBlocks would copy all 4
// hosts and n4 would appear in the complemented output. With the fix, only n1–n3 are
// used: the split produces two base blocks ([n1,n2] and [n3]) and n4 is absent.
func TestDomainsForBlocksFilteredToPartitionNodes(t *testing.T) {
	domains := topology.NewDomainMap()
	for _, n := range []string{"n1", "n2", "n3", "n4"} {
		domains.AddHostInfo(&topology.HostInfo{Domain: "B1", HostName: n, InstanceID: n})
	}

	// Partition only owns n1, n2, n3 — n4 belongs to another partition.
	nt := &NetworkTopology{
		config:  &Config{BlockSizes: []int{2, 4}},
		domains: domains,
		blocks:  []*blockInfo{{name: "B1", nodes: []string{"n1", "n2", "n3"}}},
	}

	var buf bytes.Buffer
	require.Nil(t, nt.toBlockTopology(&buf, false))

	// groupSize=2 (maxAccelSize=3, 2^1×2=4≥3); B1 splits into 2 base blocks.
	// n4 belongs to another partition and must not appear.
	expected := strings.Join([]string{
		"# block001=B1",
		"BlockName=block001 Nodes=n[1-2]",
		"# block002=B1",
		"BlockName=block002 Nodes=n3",
		"BlockSizes=2,4",
		"",
	}, "\n")
	require.Equal(t, expected, buf.String())
}

// TestComplementMultiGroupRootExpansion6x16 verifies that when total domain capacity
// (6 × groupSize × baseBlockSize) exceeds blockSizes[last]=64, the root expands to
// the next multiple of blockSizes[last] so that empty domain padding slots are emitted.
// 6 domains × 16 hosts each, blockSizes=[8,64]:
//   - groupSize = 2×8 = 16 (pow2: 2^1 × 8 ≥ 16)
//   - total capacity = 6 × 16 = 96 > 64 → root rounds up to ceil(96/64)×64 = 128
//   - 8 domain slots needed: 6 real + 2 empty → 8 × (16/8) = 16 base blocks, 4 empty
func TestComplementMultiGroupRootExpansion6x16(t *testing.T) {
	domains := topology.NewDomainMap()
	blocks := make([]*blockInfo, 0, 6)
	for d := 0; d < 6; d++ {
		domain := fmt.Sprintf("D%d", d)
		var ns []string
		for h := 0; h < 16; h++ {
			name := fmt.Sprintf("%s-n%02d", domain, h)
			ns = append(ns, name)
			domains.AddHostInfo(&topology.HostInfo{Domain: domain, HostName: name, InstanceID: name})
		}
		blocks = append(blocks, &blockInfo{name: domain, nodes: ns})
	}

	nt := &NetworkTopology{
		config:  &Config{BlockSizes: []int{8, 64}},
		domains: domains,
		blocks:  blocks,
	}

	var buf bytes.Buffer
	require.Nil(t, nt.toBlockTopology(&buf, false))

	// 16 base blocks (8 domain slots × 2 blocks each), 4 empty (2 empty domain slots × 2 blocks).
	expected := strings.Join([]string{
		"# block001=D0",
		"BlockName=block001 Nodes=D0-n[00-07]",
		"# block002=D0",
		"BlockName=block002 Nodes=D0-n[08-15]",
		"# block003=D1",
		"BlockName=block003 Nodes=D1-n[00-07]",
		"# block004=D1",
		"BlockName=block004 Nodes=D1-n[08-15]",
		"# block005=D2",
		"BlockName=block005 Nodes=D2-n[00-07]",
		"# block006=D2",
		"BlockName=block006 Nodes=D2-n[08-15]",
		"# block007=D3",
		"BlockName=block007 Nodes=D3-n[00-07]",
		"# block008=D3",
		"BlockName=block008 Nodes=D3-n[08-15]",
		"# block009=D4",
		"BlockName=block009 Nodes=D4-n[00-07]",
		"# block010=D4",
		"BlockName=block010 Nodes=D4-n[08-15]",
		"# block011=D5",
		"BlockName=block011 Nodes=D5-n[00-07]",
		"# block012=D5",
		"BlockName=block012 Nodes=D5-n[08-15]",
		"BlockName=block013",
		"BlockName=block014",
		"BlockName=block015",
		"BlockName=block016",
		"BlockSizes=8,64",
		"",
	}, "\n")
	require.Equal(t, expected, buf.String())
}

// TestComplementMultiGroupRootExpansion3x72 verifies the same multi-group root expansion
// for 3 domains × 72 hosts each, blockSizes=[18,144]:
//   - groupSize = 4×18 = 72 (pow2: 2^2 × 18 ≥ 72)
//   - total capacity = 3 × 72 = 216 > 144 → root rounds up to ceil(216/144)×144 = 288
//   - 4 domain slots needed: 3 real + 1 empty → 4 × (72/18) = 16 base blocks, 4 empty
func TestComplementMultiGroupRootExpansion3x72(t *testing.T) {
	domains := topology.NewDomainMap()
	blocks := make([]*blockInfo, 0, 3)
	for d := 0; d < 3; d++ {
		domain := fmt.Sprintf("D%d", d)
		var ns []string
		for h := 0; h < 72; h++ {
			name := fmt.Sprintf("%s-n%02d", domain, h)
			ns = append(ns, name)
			domains.AddHostInfo(&topology.HostInfo{Domain: domain, HostName: name, InstanceID: name})
		}
		blocks = append(blocks, &blockInfo{name: domain, nodes: ns})
	}

	nt := &NetworkTopology{
		config:  &Config{BlockSizes: []int{18, 144}},
		domains: domains,
		blocks:  blocks,
	}

	var buf bytes.Buffer
	require.Nil(t, nt.toBlockTopology(&buf, false))

	// 16 base blocks (4 domain slots × 4 blocks each), 4 empty (1 empty domain slot × 4 blocks).
	expected := strings.Join([]string{
		"# block001=D0",
		"BlockName=block001 Nodes=D0-n[00-17]",
		"# block002=D0",
		"BlockName=block002 Nodes=D0-n[18-35]",
		"# block003=D0",
		"BlockName=block003 Nodes=D0-n[36-53]",
		"# block004=D0",
		"BlockName=block004 Nodes=D0-n[54-71]",
		"# block005=D1",
		"BlockName=block005 Nodes=D1-n[00-17]",
		"# block006=D1",
		"BlockName=block006 Nodes=D1-n[18-35]",
		"# block007=D1",
		"BlockName=block007 Nodes=D1-n[36-53]",
		"# block008=D1",
		"BlockName=block008 Nodes=D1-n[54-71]",
		"# block009=D2",
		"BlockName=block009 Nodes=D2-n[00-17]",
		"# block010=D2",
		"BlockName=block010 Nodes=D2-n[18-35]",
		"# block011=D2",
		"BlockName=block011 Nodes=D2-n[36-53]",
		"# block012=D2",
		"BlockName=block012 Nodes=D2-n[54-71]",
		"BlockName=block013",
		"BlockName=block014",
		"BlockName=block015",
		"BlockName=block016",
		"BlockSizes=18,144",
		"",
	}, "\n")
	require.Equal(t, expected, buf.String())
}

// TestComplementMixedDomainSizesLCMPadding is a regression test for the LCM padding bug
// in toRootAggregate. Both domains are dual-level, so the capping branch in
// toDomainAggregate sizes each domain by its own ActualNodeCount, producing non-uniform
// child capacities. The old code set childCapacity from only the first domain; the LCM
// step was then too coarse to land on targetCount and the loop overshot, leaving
// result.nodeCount % rootDesired ≠ 0.
//
// Setup: blockSizes=[8,32], domain "a" with 64 nodes (capacity=64), domain "b" with 12
// nodes (capacity=16). result.nodeCount before padding = 80; 80%32=16≠0.
//
// Old (buggy): childCapacity=64 → step=lcm(64,32)=64 → targetCount=128 → adds 64 →
//
//	nodeCount=144, 144%32=16≠0.
//
// Fixed: childCapacity=GCD(64,16)=16 → step=lcm(16,32)=32 → targetCount=96 → adds 16 →
//
//	nodeCount=96, 96%32=0. Total: 12 base blocks (8 from "a", 2 from "b", 2 empty).
func TestComplementMixedDomainSizesLCMPadding(t *testing.T) {
	domains := topology.NewDomainMap()
	var aNodes, bNodes []string
	for i := 0; i < 64; i++ {
		name := fmt.Sprintf("a-n%02d", i)
		aNodes = append(aNodes, name)
		domains.AddHostInfo(&topology.HostInfo{Domain: "a", SubDomain: "a-rack", HostName: name, InstanceID: name})
	}
	for i := 0; i < 12; i++ {
		name := fmt.Sprintf("b-n%02d", i)
		bNodes = append(bNodes, name)
		domains.AddHostInfo(&topology.HostInfo{Domain: "b", SubDomain: "b-rack", HostName: name, InstanceID: name})
	}

	nt := &NetworkTopology{
		config:  &Config{BlockSizes: []int{8, 32}},
		domains: domains,
		blocks: []*blockInfo{
			{name: "a", nodes: aNodes},
			{name: "b", nodes: bNodes},
		},
	}

	var buf bytes.Buffer
	require.Nil(t, nt.toBlockTopology(&buf, false))

	// 8 real blocks from "a" + 2 real from "b" + 2 empty padding = 12 total; 96%32==0.
	expected := strings.Join([]string{
		"# block001=a-rack",
		"BlockName=block001 Nodes=a-n[00-07]",
		"# block002=a-rack",
		"BlockName=block002 Nodes=a-n[08-15]",
		"# block003=a-rack",
		"BlockName=block003 Nodes=a-n[16-23]",
		"# block004=a-rack",
		"BlockName=block004 Nodes=a-n[24-31]",
		"# block005=a-rack",
		"BlockName=block005 Nodes=a-n[32-39]",
		"# block006=a-rack",
		"BlockName=block006 Nodes=a-n[40-47]",
		"# block007=a-rack",
		"BlockName=block007 Nodes=a-n[48-55]",
		"# block008=a-rack",
		"BlockName=block008 Nodes=a-n[56-63]",
		"# block009=b-rack",
		"BlockName=block009 Nodes=b-n[00-07]",
		"# block010=b-rack",
		"BlockName=block010 Nodes=b-n[08-11]",
		"BlockName=block011",
		"BlockName=block012",
		"BlockSizes=8,32",
		"",
	}, "\n")
	require.Equal(t, expected, buf.String())
}

// TestComplementSingleLevelMixedOversizedDomainsPreservesUniformSlots verifies
// backward compatibility for single-level domains of different sizes when the
// largest domain exceeds blockSizes[last]. Before dual-level support, every
// domain received the same power-of-two slot derived from the largest domain.
// With blockSizes=[8,32], domain a (64 nodes) and domain b (12 nodes) therefore
// each occupy 8 base blocks; b has 2 live blocks followed by 6 placeholders.
func TestComplementSingleLevelMixedOversizedDomainsPreservesUniformSlots(t *testing.T) {
	domains := topology.NewDomainMap()
	var aNodes, bNodes []string
	for i := 0; i < 64; i++ {
		name := fmt.Sprintf("a-n%02d", i)
		aNodes = append(aNodes, name)
		domains.AddHostInfo(&topology.HostInfo{Domain: "a", HostName: name, InstanceID: name})
	}
	for i := 0; i < 12; i++ {
		name := fmt.Sprintf("b-n%02d", i)
		bNodes = append(bNodes, name)
		domains.AddHostInfo(&topology.HostInfo{Domain: "b", HostName: name, InstanceID: name})
	}

	nt := &NetworkTopology{
		config:  &Config{BlockSizes: []int{8, 32}},
		domains: domains,
		blocks: []*blockInfo{
			{name: "a", nodes: aNodes},
			{name: "b", nodes: bNodes},
		},
	}

	var buf bytes.Buffer
	require.Nil(t, nt.toBlockTopology(&buf, false))

	// 8 base blocks per domain (uniform slot from the largest domain); b has 2 live + 6 empty.
	expected := strings.Join([]string{
		"# block001=a",
		"BlockName=block001 Nodes=a-n[00-07]",
		"# block002=a",
		"BlockName=block002 Nodes=a-n[08-15]",
		"# block003=a",
		"BlockName=block003 Nodes=a-n[16-23]",
		"# block004=a",
		"BlockName=block004 Nodes=a-n[24-31]",
		"# block005=a",
		"BlockName=block005 Nodes=a-n[32-39]",
		"# block006=a",
		"BlockName=block006 Nodes=a-n[40-47]",
		"# block007=a",
		"BlockName=block007 Nodes=a-n[48-55]",
		"# block008=a",
		"BlockName=block008 Nodes=a-n[56-63]",
		"# block009=b",
		"BlockName=block009 Nodes=b-n[00-07]",
		"# block010=b",
		"BlockName=block010 Nodes=b-n[08-11]",
		"BlockName=block011",
		"BlockName=block012",
		"BlockName=block013",
		"BlockName=block014",
		"BlockName=block015",
		"BlockName=block016",
		"BlockSizes=8,32",
		"",
	}, "\n")
	require.Equal(t, expected, buf.String())
}

// TestComplementMixedLevelDomainsUseDualLevelSizing verifies that the legacy
// compatibility path is not selected when even one root domain has sub-domains.
// The single-level domain a and dual-level domain b are therefore sized
// independently, and root padding uses their GCD capacity.
func TestComplementMixedLevelDomainsUseDualLevelSizing(t *testing.T) {
	domains := topology.NewDomainMap()
	var aNodes, bNodes []string
	for i := 0; i < 64; i++ {
		name := fmt.Sprintf("a-n%02d", i)
		aNodes = append(aNodes, name)
		domains.AddHostInfo(&topology.HostInfo{Domain: "a", HostName: name, InstanceID: name})
	}
	for i := 0; i < 12; i++ {
		name := fmt.Sprintf("b-n%02d", i)
		bNodes = append(bNodes, name)
		domains.AddHostInfo(&topology.HostInfo{Domain: "b", SubDomain: "b-rack", HostName: name, InstanceID: name})
	}

	nt := &NetworkTopology{
		config:  &Config{BlockSizes: []int{8, 32}},
		domains: domains,
		blocks: []*blockInfo{
			{name: "a", nodes: aNodes},
			{name: "b", nodes: bNodes},
		},
	}

	var buf bytes.Buffer
	require.Nil(t, nt.toBlockTopology(&buf, false))

	// a: 8 blocks (oversized leaf, ceil(64/8)); b: 4 blocks (2 real + 2 empty padding); 96%32==0.
	expected := strings.Join([]string{
		"# block001=a",
		"BlockName=block001 Nodes=a-n[00-07]",
		"# block002=a",
		"BlockName=block002 Nodes=a-n[08-15]",
		"# block003=a",
		"BlockName=block003 Nodes=a-n[16-23]",
		"# block004=a",
		"BlockName=block004 Nodes=a-n[24-31]",
		"# block005=a",
		"BlockName=block005 Nodes=a-n[32-39]",
		"# block006=a",
		"BlockName=block006 Nodes=a-n[40-47]",
		"# block007=a",
		"BlockName=block007 Nodes=a-n[48-55]",
		"# block008=a",
		"BlockName=block008 Nodes=a-n[56-63]",
		"# block009=b-rack",
		"BlockName=block009 Nodes=b-n[00-07]",
		"# block010=b-rack",
		"BlockName=block010 Nodes=b-n[08-11]",
		"BlockName=block011",
		"BlockName=block012",
		"BlockSizes=8,32",
		"",
	}, "\n")
	require.Equal(t, expected, buf.String())
}

// TestComplementWithMissingDomain verifies that when B2 has no entry in the domain map,
// domainsForBlocks only sees B1. The complement tree produces block001 with B1's nodes
// and an empty block002 padding slot (root-padded to reach blockSizes[last]=4).
func TestComplementWithMissingDomain(t *testing.T) {
	domains := topology.NewDomainMap()
	// Only B1 is in the domain map; B2 is not.
	for _, n := range []string{"n1", "n2"} {
		domains.AddHostInfo(&topology.HostInfo{Domain: "B1", HostName: n, InstanceID: n})
	}
	nt := &NetworkTopology{
		config:  &Config{BlockSizes: []int{2, 4}},
		domains: domains,
		blocks: []*blockInfo{
			{id: "block001", name: "B1", nodes: []string{"n1", "n2"}},
			{id: "block002", name: "B2", nodes: []string{"n3", "n4"}}, // no domain entry
		},
	}

	var buf bytes.Buffer
	require.Nil(t, nt.toBlockTopology(&buf, false))

	expected := strings.Join([]string{
		"# block001=B1",
		"BlockName=block001 Nodes=n[1-2]",
		"BlockName=block002",
		"BlockSizes=2,4",
		"",
	}, "\n")
	require.Equal(t, expected, buf.String())
}

// TestGetBlockTopologyUnitWithMultiAcceleratorDomains verifies the YAML per-partition
// complement path end-to-end: two domains, three accelerators (a1, a2, a3), block
// sizes [2,4]. a2 is undersized (fewer nodes than groupSize=2 requires), so it gets
// an empty padding slot; tree-capacity expansion adds two more trailing empty slots.
func TestGetBlockTopologyUnitWithMultiAcceleratorDomains(t *testing.T) {
	domains := topology.NewDomainMap()
	for _, n := range []string{"n10", "n11", "n12"} {
		domains.AddHostInfo(&topology.HostInfo{Domain: "a1", HostName: n, InstanceID: n})
	}
	for _, n := range []string{"n20", "n21"} {
		domains.AddHostInfo(&topology.HostInfo{Domain: "a2", HostName: n, InstanceID: n})
	}
	for _, n := range []string{"n31", "n32", "n33"} {
		domains.AddHostInfo(&topology.HostInfo{Domain: "a3", HostName: n, InstanceID: n})
	}

	cfg := &Config{
		Topologies: map[string]*TopologySpec{
			"topo1": {
				Plugin:     topology.TopologyBlock,
				Nodes:      []string{"n[10-12]", "n[20-21]", "n[31-33]"},
				BlockSizes: []int{2, 4},
			},
		},
	}

	graph := &topology.Graph{Domains: domains}
	nt, err := NewNetworkTopology(graph, cfg)
	require.NoError(t, err)

	var buf bytes.Buffer
	require.Nil(t, nt.Generate(&buf))

	expected := strings.Join([]string{
		"- topology: topo1",
		"  cluster_default: false",
		"  block:",
		"    block_sizes:",
		"        - 2",
		"        - 4",
		"    blocks:",
		"        - block: block1",
		"          nodes: n[10-11]",
		"        - block: block2",
		"          nodes: n12",
		"        - block: block3",
		"          nodes: n[20-21]",
		"        - block: block4",
		"        - block: block5",
		"          nodes: n[31-32]",
		"        - block: block6",
		"          nodes: n33",
		"",
	}, "\n")
	require.Equal(t, expected, buf.String())
}

// TestGetBlockTopologyUnitSingleBlockSize verifies that a TopologySpec with a single
// BlockSizes entry still splits domains that exceed baseBlockSize, but applies no
// empty padding slots. With blockSizes=[2] and lastBS=2, all domain aggregates have
// totalCount >= 2 (a1:4, a2:2, a3:4), so the siblings-of-equal-size rule is skipped
// entirely — no group-alignment or root padding is added. The result is the 5-block
// split list replacing the original 3-block input.
func TestGetBlockTopologyUnitSingleBlockSize(t *testing.T) {
	domains := topology.NewDomainMap()
	for _, n := range []string{"n10", "n11", "n12"} {
		domains.AddHostInfo(&topology.HostInfo{Domain: "a1", HostName: n, InstanceID: n})
	}
	for _, n := range []string{"n20", "n21"} {
		domains.AddHostInfo(&topology.HostInfo{Domain: "a2", HostName: n, InstanceID: n})
	}
	for _, n := range []string{"n31", "n32", "n33"} {
		domains.AddHostInfo(&topology.HostInfo{Domain: "a3", HostName: n, InstanceID: n})
	}

	cfg := &Config{
		Topologies: map[string]*TopologySpec{
			"topo1": {
				Plugin:     topology.TopologyBlock,
				Nodes:      []string{"n[10-12]", "n[20-21]", "n[31-33]"},
				BlockSizes: []int{2},
			},
		},
	}

	graph := &topology.Graph{Domains: domains}
	nt, err := NewNetworkTopology(graph, cfg)
	require.NoError(t, err)

	var buf bytes.Buffer
	require.Nil(t, nt.Generate(&buf))

	// lastBS=2; all domain aggregates have totalCount >= 2, so padActualTree is a no-op.
	// Only the split of oversized domains (a1, a3 → 2 blocks each) produces new output.
	expected := strings.Join([]string{
		"- topology: topo1",
		"  cluster_default: false",
		"  block:",
		"    block_sizes:",
		"        - 2",
		"    blocks:",
		"        - block: block1",
		"          nodes: n[10-11]",
		"        - block: block2",
		"          nodes: n12",
		"        - block: block3",
		"          nodes: n[20-21]",
		"        - block: block4",
		"          nodes: n[31-32]",
		"        - block: block5",
		"          nodes: n33",
		"",
	}, "\n")
	require.Equal(t, expected, buf.String())
}

// TestComplementUnequalDomainSubDomainCounts verifies that domains with different
// sub-domain counts are sized independently; neither domain forces padding onto the other.
//
// Setup: blockSizes=[18,72,288]
//   - domain "a": 4 sub-domains × 9 hosts = 36 hosts total
//   - domain "b": 1 sub-domain "b.sd0" × 36 hosts
//   - root.MaxChildNodeCount = max(36, 36) = 36
//
// toDomainAggregate("a", maxSiblingNodes=36, [18,72,288]):
//   - nodeCount = aggregateSlotCapacity(36, 18) = 36; numBaseBlocks = 2
//   - Strategy 2 (recurse per child): each of 4 sub-domains → 1 block of 9 hosts
//   - 4 blocks > numBaseBlocks=2 (overflow); 4×18=72=blockSizes[1] → natural level
//     alignment, no extra padding added. Domain "a" nodeCount = 72.
//
// toDomainAggregate("b", maxSiblingNodes=36, [18,72,288]):
//   - nodeCount = 36; numBaseBlocks = 2
//   - Recurses into "b.sd0" (leaf, 36 hosts) → Strategy 1 → 2 full blocks
//   - b produces 2 blocks → domain "b" nodeCount = 36
//
// Root: total=108, rootDesired=288; childCapacity=GCD(72,36)=36; lcm(36,288)=288;
// targetCount=288 → 5 empty child aggregates × 2 blocks = 10 empty blocks.
//
// Expected: 16 base blocks total — block001-004 from "a", block005-006 from "b.sd0",
// block007-016 empty root padding.
func TestComplementUnequalDomainSubDomainCounts(t *testing.T) {
	domains := topology.NewDomainMap()
	for sd := 0; sd < 4; sd++ {
		for h := 0; h < 9; h++ {
			domains.AddHostInfo(&topology.HostInfo{Domain: "a", SubDomain: fmt.Sprintf("a.sd%d", sd), HostName: fmt.Sprintf("a-sd%d-h%d", sd, h)})
		}
	}
	for h := 0; h < 36; h++ {
		// Zero-padded so alphabetical sort matches numeric order (b-h00 < b-h09 < b-h10).
		domains.AddHostInfo(&topology.HostInfo{Domain: "b", SubDomain: "b.sd0", HostName: fmt.Sprintf("b-h%02d", h)})
	}

	cfg := &Config{
		Plugin:     topology.TopologyBlock,
		BlockSizes: []int{18, 72, 288},
	}
	graph := &topology.Graph{Domains: domains}
	nt, err := NewNetworkTopology(graph, cfg)
	require.NoError(t, err)

	var buf bytes.Buffer
	require.Nil(t, nt.toBlockTopology(&buf, false))

	expected := strings.Join([]string{
		"# block001=a.sd0",
		"BlockName=block001 Nodes=a-sd0-h[0-8]",
		"# block002=a.sd1",
		"BlockName=block002 Nodes=a-sd1-h[0-8]",
		"# block003=a.sd2",
		"BlockName=block003 Nodes=a-sd2-h[0-8]",
		"# block004=a.sd3",
		"BlockName=block004 Nodes=a-sd3-h[0-8]",
		"# block005=b.sd0",
		"BlockName=block005 Nodes=b-h[00-17]",
		"# block006=b.sd0",
		"BlockName=block006 Nodes=b-h[18-35]",
		"BlockName=block007",
		"BlockName=block008",
		"BlockName=block009",
		"BlockName=block010",
		"BlockName=block011",
		"BlockName=block012",
		"BlockName=block013",
		"BlockName=block014",
		"BlockName=block015",
		"BlockName=block016",
		"BlockSizes=18,72,288",
		"",
	}, "\n")
	require.Equal(t, expected, buf.String())
}

// TestComplementOverflowRoundedToAggregateBoundary is a regression test for the P1
// overflow alignment bug: when Strategy 2 produces more base blocks than numBaseBlocks,
// the excess must be rounded up to the nearest blockSizes-level boundary so that the
// next domain always starts at a clean Slurm aggregate position.
//
// Setup: blockSizes=[8,32]
//   - domain "a": 5 sub-domains × 3 hosts = 15 nodes
//     numBaseBlocks = aggregateSlotCapacity(min(32,15)=15, 8)/8 = 2
//     5 sub-domains → 5 blocks (overflow); alignSize = blockSizes fallback → 40 nodes →
//     aggregateSlotCapacity(40,8)/8 = 8 → round 5→8 (3 empty padding blocks added)
//   - domain "b": 4 sub-domains × 2 hosts = 8 nodes
//     numBaseBlocks = 2; 4 blocks (overflow); alignSize = 32 (blockSizes[1] ≥ 32) →
//     aggregateWidth = 4 → 4 already a multiple of 4, no extra padding
//   - Root: 8+4=12 blocks, 96%32=0, no root padding
//
// Without the fix, domain "a" would produce 5 blocks and domain "b" would start at
// block 6 — inside Slurm's second 4-block (32-node) aggregate — mixing both domains.
func TestComplementOverflowRoundedToAggregateBoundary(t *testing.T) {
	domains := topology.NewDomainMap()
	for sd := range 5 {
		for h := range 3 {
			domains.AddHostInfo(&topology.HostInfo{
				Domain:    "a",
				SubDomain: fmt.Sprintf("a.sd%d", sd),
				HostName:  fmt.Sprintf("a-sd%d-h%d", sd, h),
			})
		}
	}
	for sd := range 4 {
		for h := range 2 {
			domains.AddHostInfo(&topology.HostInfo{
				Domain:    "b",
				SubDomain: fmt.Sprintf("b.sd%d", sd),
				HostName:  fmt.Sprintf("b-sd%d-h%d", sd, h),
			})
		}
	}

	cfg := &Config{
		Plugin:     topology.TopologyBlock,
		BlockSizes: []int{8, 32},
	}
	graph := &topology.Graph{Domains: domains}
	nt, err := NewNetworkTopology(graph, cfg)
	require.NoError(t, err)

	var buf bytes.Buffer
	require.Nil(t, nt.toBlockTopology(&buf, false))

	expected := strings.Join([]string{
		"# block001=a.sd0",
		"BlockName=block001 Nodes=a-sd0-h[0-2]",
		"# block002=a.sd1",
		"BlockName=block002 Nodes=a-sd1-h[0-2]",
		"# block003=a.sd2",
		"BlockName=block003 Nodes=a-sd2-h[0-2]",
		"# block004=a.sd3",
		"BlockName=block004 Nodes=a-sd3-h[0-2]",
		"# block005=a.sd4",
		"BlockName=block005 Nodes=a-sd4-h[0-2]",
		"BlockName=block006",
		"BlockName=block007",
		"BlockName=block008",
		"# block009=b.sd0",
		"BlockName=block009 Nodes=b-sd0-h[0-1]",
		"# block010=b.sd1",
		"BlockName=block010 Nodes=b-sd1-h[0-1]",
		"# block011=b.sd2",
		"BlockName=block011 Nodes=b-sd2-h[0-1]",
		"# block012=b.sd3",
		"BlockName=block012 Nodes=b-sd3-h[0-1]",
		"BlockSizes=8,32",
		"",
	}, "\n")
	require.Equal(t, expected, buf.String())
}

// TestComplementSmallSubDomainsRecursePerChild verifies that when sub-domains are
// smaller than a base block, Strategy 2 (recurse per child) gives each sub-domain
// its own base block rather than merging across boundaries.
//
// Setup: blockSizes=[8, 32], domain "d" with 4 sub-domains of 3 hosts each (12 total).
//   - baseBlockSize=8, Strategy 2: recurse per child → 4 separate blocks of 3 hosts
//   - Root: 4 blocks × 8 = 32 nodes = rootDesired → no padding
func TestComplementSmallSubDomainsRecursePerChild(t *testing.T) {
	domains := topology.NewDomainMap()
	for sd := 0; sd < 4; sd++ {
		for h := 0; h < 3; h++ {
			domains.AddHostInfo(&topology.HostInfo{
				Domain:    "d",
				SubDomain: fmt.Sprintf("d.sd%d", sd),
				HostName:  fmt.Sprintf("d-sd%d-h%d", sd, h),
			})
		}
	}

	cfg := &Config{
		Plugin:     topology.TopologyBlock,
		BlockSizes: []int{8, 32},
	}
	graph := &topology.Graph{Domains: domains}
	nt, err := NewNetworkTopology(graph, cfg)
	require.NoError(t, err)

	var buf bytes.Buffer
	require.Nil(t, nt.toBlockTopology(&buf, false))

	expected := strings.Join([]string{
		"# block001=d.sd0",
		"BlockName=block001 Nodes=d-sd0-h[0-2]",
		"# block002=d.sd1",
		"BlockName=block002 Nodes=d-sd1-h[0-2]",
		"# block003=d.sd2",
		"BlockName=block003 Nodes=d-sd2-h[0-2]",
		"# block004=d.sd3",
		"BlockName=block004 Nodes=d-sd3-h[0-2]",
		"BlockSizes=8,32",
		"",
	}, "\n")
	require.Equal(t, expected, buf.String())
}

// TestComplementOddSubDomainCount verifies that an odd number of sub-domains each
// smaller than a base block all receive their own block with no spurious padding.
//
// Setup: blockSizes=[8], domain "d" with 5 sub-domains of 3 hosts each (15 total).
//   - Strategy 2 (recurse per child): 5 sub-domains → 5 separate blocks of 3 hosts
//   - Root: 5×8=40, rootDesired=8, 40%8=0 → no padding
func TestComplementOddSubDomainCount(t *testing.T) {
	domains := topology.NewDomainMap()
	for sd := range 5 {
		for h := range 3 {
			domains.AddHostInfo(&topology.HostInfo{
				Domain:    "d",
				SubDomain: fmt.Sprintf("d.sd%d", sd),
				HostName:  fmt.Sprintf("d-sd%d-h%d", sd, h),
			})
		}
	}

	cfg := &Config{
		Plugin:     topology.TopologyBlock,
		BlockSizes: []int{8},
	}
	graph := &topology.Graph{Domains: domains}
	nt, err := NewNetworkTopology(graph, cfg)
	require.NoError(t, err)

	var buf bytes.Buffer
	require.Nil(t, nt.toBlockTopology(&buf, false))

	expected := strings.Join([]string{
		"# block001=d.sd0",
		"BlockName=block001 Nodes=d-sd0-h[0-2]",
		"# block002=d.sd1",
		"BlockName=block002 Nodes=d-sd1-h[0-2]",
		"# block003=d.sd2",
		"BlockName=block003 Nodes=d-sd2-h[0-2]",
		"# block004=d.sd3",
		"BlockName=block004 Nodes=d-sd3-h[0-2]",
		"# block005=d.sd4",
		"BlockName=block005 Nodes=d-sd4-h[0-2]",
		"BlockSizes=8",
		"",
	}, "\n")
	require.Equal(t, expected, buf.String())
}

// TestComplementFormatterProducesPerSubDomainBlocks verifies that a blockName formatter
// derives consistent block names when sub-domains are smaller than a base block.
// Each sub-domain keeps its own base block so the formatter sees a homogeneous host list.
//
// Setup: 1 domain "d", 4 sub-domains (r1–r4) × 2 hosts each, blockSizes=[8,16].
//   - Strategy 2 (recurse per child): 4 separate blocks, one per sub-domain
//   - Each block's hosts share the same sub-domain prefix → formatter succeeds
//   - Block IDs become "r1"…"r4" as derived by the formatter
func TestComplementFormatterProducesPerSubDomainBlocks(t *testing.T) {
	domains := topology.NewDomainMap()
	for _, rack := range []string{"r1", "r2", "r3", "r4"} {
		for h := 0; h < 2; h++ {
			name := fmt.Sprintf("%s-h%d", rack, h)
			domains.AddHostInfo(&topology.HostInfo{
				Domain:     "d",
				SubDomain:  rack,
				HostName:   name,
				InstanceID: name,
			})
		}
	}

	cfg := &Config{
		Plugin:     topology.TopologyBlock,
		BlockSizes: []int{8, 16},
		BlockName: &BlockNameConfig{
			NodeNameRegexp: `^(r[0-9]+)-`,
			Format:         "${1}",
			compiledRegexp: regexp.MustCompile(`^(r[0-9]+)-`),
		},
	}
	graph := &topology.Graph{Domains: domains}
	nt, err := NewNetworkTopology(graph, cfg)
	require.NoError(t, err)

	var buf bytes.Buffer
	require.Nil(t, nt.toBlockTopology(&buf, false), "formatBlockNames must not error when Strategy 2 is skipped")

	expected := strings.Join([]string{
		"# r1=r1",
		"BlockName=r1 Nodes=r1-h[0-1]",
		"# r2=r2",
		"BlockName=r2 Nodes=r2-h[0-1]",
		"# r3=r3",
		"BlockName=r3 Nodes=r3-h[0-1]",
		"# r4=r4",
		"BlockName=r4 Nodes=r4-h[0-1]",
		"BlockSizes=8,16",
		"",
	}, "\n")
	require.Equal(t, expected, buf.String())
}

// TestComplementSingleLevelOversizedDomain verifies that a single-level domain whose
// node count exceeds blockSizes[last] produces exactly ceil(ActualNodeCount/blockSizes[0])
// base blocks with no empty placeholder. Setup: 1 domain, 5 hosts, BlockSizes=[2]
// (lastBlockSize=2 < maxSiblingNodes=5) → 3 blocks.
func TestComplementSingleLevelOversizedDomain(t *testing.T) {
	domains := topology.NewDomainMap()
	for i := 0; i < 5; i++ {
		domains.AddHostInfo(&topology.HostInfo{
			Domain:   "d",
			HostName: fmt.Sprintf("h%d", i),
		})
	}

	cfg := &Config{
		Plugin:     topology.TopologyBlock,
		BlockSizes: []int{2},
	}
	graph := &topology.Graph{Domains: domains}
	nt, err := NewNetworkTopology(graph, cfg)
	require.NoError(t, err)

	var buf bytes.Buffer
	require.Nil(t, nt.toBlockTopology(&buf, false))

	expected := strings.Join([]string{
		"# block001=d",
		"BlockName=block001 Nodes=h[0-1]",
		"# block002=d",
		"BlockName=block002 Nodes=h[2-3]",
		"# block003=d",
		"BlockName=block003 Nodes=h4",
		"BlockSizes=2",
		"",
	}, "\n")
	require.Equal(t, expected, buf.String())
}

// TestComplementDualLevel validates dual-level block tree construction using the
// dual-level simulation model. Two accelerator domains (domain-01, domain-02) each
// contain sub-domains identified by SubDomain (rack-1-01 … rack-1-16 and
// rack-2-01 … rack-2-16). rack-1-03 and rack-1-13 have no nodes; rack-2-11 is absent.
//
// With BlockSizes=[9,144]:
//
//   - Group level (depth 2): max ActualNodeCount = 9 → DesiredNodeCount = 9 per group.
//     Each group leaf emits exactly 1 base block (groupSize = 9/9 = 1).
//
//   - Domain level (depth 1): max ActualNodeCount = 131 (domain-02) → DesiredNodeCount = 144.
//     domain-01 has 14 active groups → 14 real + 2 empty = 16 slots (blocks 001–016).
//     domain-02 has 15 active groups → 15 real + 1 empty = 16 slots (blocks 017–032).
//
//   - Root (depth 0): DesiredNodeCount = 144; 2 domain children total 288 > 144 → no padding.
//
//   - Total output: 32 blocks (16 per domain) followed by BlockSizes=9,144.
func TestComplementDualLevel(t *testing.T) {
	model, err := models.NewModelFromFile("dual-xclr-irregular.yaml")
	require.NoError(t, err)

	graph, _ := model.ToGraph(nil)
	require.NotNil(t, graph.Domains)

	cfg := &Config{
		Plugin:     topology.TopologyBlock,
		BlockSizes: []int{9, 144},
	}
	nt, err := NewNetworkTopology(graph, cfg)
	require.NoError(t, err)

	var buf bytes.Buffer
	require.Nil(t, nt.toBlockTopology(&buf, false))

	expected := strings.Join([]string{
		"# block001=rack-1-01",
		"BlockName=block001 Nodes=node[0001-0009]",
		"# block002=rack-1-02",
		"BlockName=block002 Nodes=node[0010-0018]",
		"# block003=rack-1-04",
		"BlockName=block003 Nodes=node[0028-0030,0032,0034-0036]",
		"# block004=rack-1-05",
		"BlockName=block004 Nodes=node[0037-0045]",
		"# block005=rack-1-06",
		"BlockName=block005 Nodes=node[0046-0054]",
		"# block006=rack-1-07",
		"BlockName=block006 Nodes=node[0055-0063]",
		"# block007=rack-1-08",
		"BlockName=block007 Nodes=node[0064-0072]",
		"# block008=rack-1-09",
		"BlockName=block008 Nodes=node[0073-0081]",
		"# block009=rack-1-10",
		"BlockName=block009 Nodes=node[0082-0090]",
		"# block010=rack-1-11",
		"BlockName=block010 Nodes=node[0091-0099]",
		"# block011=rack-1-12",
		"BlockName=block011 Nodes=node[0100-0108]",
		"# block012=rack-1-14",
		"BlockName=block012 Nodes=node[0118-0126]",
		"# block013=rack-1-15",
		"BlockName=block013 Nodes=node[0127-0135]",
		"# block014=rack-1-16",
		"BlockName=block014 Nodes=node[0136-0144]",
		"BlockName=block015",
		"BlockName=block016",
		"# block017=rack-2-01",
		"BlockName=block017 Nodes=node[0145-0153]",
		"# block018=rack-2-02",
		"BlockName=block018 Nodes=node[0154-0162]",
		"# block019=rack-2-03",
		"BlockName=block019 Nodes=node[0163-0171]",
		"# block020=rack-2-04",
		"BlockName=block020 Nodes=node[0172-0180]",
		"# block021=rack-2-05",
		"BlockName=block021 Nodes=node[0181-0189]",
		"# block022=rack-2-06",
		"BlockName=block022 Nodes=node[0190-0198]",
		"# block023=rack-2-07",
		"BlockName=block023 Nodes=node[0199-0207]",
		"# block024=rack-2-08",
		"BlockName=block024 Nodes=node[0208-0216]",
		"# block025=rack-2-09",
		"BlockName=block025 Nodes=node[0217-0225]",
		"# block026=rack-2-10",
		"BlockName=block026 Nodes=node[0226-0234]",
		"# block027=rack-2-12",
		"BlockName=block027 Nodes=node[0244-0252]",
		"# block028=rack-2-13",
		"BlockName=block028 Nodes=node[0253-0261]",
		"# block029=rack-2-14",
		"BlockName=block029 Nodes=node[0262-0270]",
		"# block030=rack-2-15",
		"BlockName=block030 Nodes=node[0271-0275]",
		"# block031=rack-2-16",
		"BlockName=block031 Nodes=node[0280-0288]",
		"BlockName=block032",
		"BlockSizes=9,144",
		"",
	}, "\n")
	require.Equal(t, expected, buf.String())
}

// getBlockWithIBAsymmetricSpineTestSet models two spines with four leaf switches on the
// left spine and three on the right, each leaf switch hosting one accelerator domain.
func getBlockWithIBAsymmetricSpineTestSet() (*topology.Graph, map[string]string) {
	n := func(id, name string) *topology.Vertex {
		return &topology.Vertex{ID: id, Name: name}
	}

	leaf := func(id string, nodes map[string]*topology.Vertex) *topology.Vertex {
		return &topology.Vertex{ID: id, Vertices: nodes}
	}

	l11 := leaf("L11", map[string]*topology.Vertex{"I11a": n("I11a", "Node101"), "I11b": n("I11b", "Node102"), "I11c": n("I11c", "Node103")})
	l12 := leaf("L12", map[string]*topology.Vertex{"I12a": n("I12a", "Node201"), "I12b": n("I12b", "Node202"), "I12c": n("I12c", "Node205")})
	l13 := leaf("L13", map[string]*topology.Vertex{"I13a": n("I13a", "Node301"), "I13b": n("I13b", "Node302"), "I13c": n("I13c", "Node303")})
	l14 := leaf("L14", map[string]*topology.Vertex{"I14a": n("I14a", "Node401"), "I14b": n("I14b", "Node402"), "I14c": n("I14c", "Node403")})
	l21 := leaf("L21", map[string]*topology.Vertex{"I21a": n("I21a", "Node501"), "I21b": n("I21b", "Node502"), "I21c": n("I21c", "Node503")})
	l22 := leaf("L22", map[string]*topology.Vertex{"I22a": n("I22a", "Node601"), "I22b": n("I22b", "Node602"), "I22c": n("I22c", "Node603")})
	l23 := leaf("L23", map[string]*topology.Vertex{"I23a": n("I23a", "Node701"), "I23b": n("I23b", "Node702"), "I23c": n("I23c", "Node703")})

	spine1 := &topology.Vertex{ID: "SP1", Vertices: map[string]*topology.Vertex{"L11": l11, "L12": l12, "L13": l13, "L14": l14}}
	spine2 := &topology.Vertex{ID: "SP2", Vertices: map[string]*topology.Vertex{"L21": l21, "L22": l22, "L23": l23}}
	core := &topology.Vertex{Vertices: map[string]*topology.Vertex{"SP1": spine1, "SP2": spine2}}

	domains := testDomainMap(map[string]map[string]string{
		"B1": {"Node101": "I11a", "Node102": "I11b", "Node103": "I11c"},
		"B2": {"Node201": "I12a", "Node202": "I12b", "Node205": "I12c"},
		"B3": {"Node301": "I13a", "Node302": "I13b", "Node303": "I13c"},
		"B4": {"Node401": "I14a", "Node402": "I14b", "Node403": "I14c"},
		"B5": {"Node501": "I21a", "Node502": "I21b", "Node503": "I21c"},
		"B6": {"Node601": "I22a", "Node602": "I22b", "Node603": "I22c"},
		"B7": {"Node701": "I23a", "Node702": "I23b", "Node703": "I23c"},
	})

	return &topology.Graph{Tiers: core, Domains: domains}, nil
}

// TestComplementSubDomainGranularityWithoutBlockSizes verifies the OCI two-rack scenario:
// one fabric domain with hosts split across two sub-domains (racks), BlockSizes unset.
// complementBlocks must infer [8,16] from the sub-domain structure and produce two
// rack-level blocks rather than collapsing all nodes into one coarse block.
func TestComplementSubDomainGranularityWithoutBlockSizes(t *testing.T) {
	const domain = "fabric-1"
	const subA = "fabric-1.rack-a"
	const subB = "fabric-1.rack-b"

	domains := topology.NewDomainMap()
	var nodesA, nodesB []string
	for i := 1; i <= 8; i++ {
		name := fmt.Sprintf("NodeA%03d", i)
		nodesA = append(nodesA, name)
		domains.AddHostInfo(&topology.HostInfo{Domain: domain, SubDomain: subA, HostName: name, InstanceID: name})
	}
	for i := 1; i <= 8; i++ {
		name := fmt.Sprintf("NodeB%03d", i)
		nodesB = append(nodesB, name)
		domains.AddHostInfo(&topology.HostInfo{Domain: domain, SubDomain: subB, HostName: name, InstanceID: name})
	}

	allNodes := append(append([]string(nil), nodesA...), nodesB...)
	nt := &NetworkTopology{
		config:  &Config{}, // BlockSizes intentionally unset — must be inferred from sub-domain structure
		domains: domains,
		// toBlockInfos produces one coarse block per DomainMap key (no sub-domain expansion)
		blocks: []*blockInfo{{id: "block001", name: domain, nodes: allNodes}},
	}

	var buf bytes.Buffer
	require.Nil(t, nt.toBlockTopology(&buf, false))

	// BlockSizes=[8,16] inferred from sub-domain structure; one block per rack sub-domain.
	expected := strings.Join([]string{
		"# block001=fabric-1.rack-a",
		"BlockName=block001 Nodes=NodeA[001-008]",
		"# block002=fabric-1.rack-b",
		"BlockName=block002 Nodes=NodeB[001-008]",
		"BlockSizes=8,16",
		"",
	}, "\n")
	require.Equal(t, expected, buf.String())
}
