/*
 * Copyright 2025 NVIDIA CORPORATION
 * SPDX-License-Identifier: Apache-2.0
 */

package translate

import (
	"bytes"
	"testing"

	"github.com/dsx-ai-factory/topograph/pkg/topology"
	"github.com/stretchr/testify/require"
)

func TestTreeYamlTopology(t *testing.T) {
	expected := `- topology: topo1
  cluster_default: false
  tree:
    switches:
        - switch: S1
          children: S2
        - switch: S2
          nodes: Node[201,205]
- topology: topo2
  cluster_default: true
  tree:
    switches:
        - switch: S1
          children: S3
        - switch: S3
          nodes: Node[304-305]
`
	expectedSkeleton := `- topology: topo1
  cluster_default: false
  tree:
    switches:
        - switch: S1
          children: S2
        - switch: S2
- topology: topo2
  cluster_default: true
  tree:
    switches:
        - switch: S1
          children: S3
        - switch: S3
`
	v, _ := GetTreeTestSet(false)
	cfg := &Config{
		Topologies: map[string]*TopologySpec{
			"topo1": {
				Plugin: topology.TopologyTree,
				Nodes:  []string{"Node[201,205]"},
			},
			"topo2": {
				Plugin:         topology.TopologyTree,
				Nodes:          []string{"Node[304,305]"},
				ClusterDefault: true,
			},
		},
	}
	nt, _ := NewNetworkTopology(v, cfg)
	buf := &bytes.Buffer{}
	err := nt.Generate(buf)
	require.Nil(t, err)
	require.Equal(t, expected, buf.String())

	buf = &bytes.Buffer{}
	_, err = nt.GenerateTopologyConfig(buf, true)
	require.Nil(t, err)
	require.Equal(t, expectedSkeleton, buf.String())
}

func TestBlockYamlTopology(t *testing.T) {
	expected := `- topology: topo1
  cluster_default: true
  block:
    block_sizes:
        - 2
    blocks:
        - block: block1
          nodes: Node[104-105]
- topology: topo2
  cluster_default: false
  block:
    block_sizes:
        - 2
    blocks:
        - block: block1
          nodes: Node[301,303]
`
	expectedSkeleton := `- topology: topo1
  cluster_default: true
  block:
    block_sizes:
        - 2
    blocks:
        - block: block1
- topology: topo2
  cluster_default: false
  block:
    block_sizes:
        - 2
    blocks:
        - block: block1
`

	v, _ := GetBlockWithMultiIBTestSet()
	cfg := &Config{
		Topologies: map[string]*TopologySpec{
			"topo1": {
				Plugin:         topology.TopologyBlock,
				Nodes:          []string{"Node[104,105]"},
				ClusterDefault: true,
			},
			"topo2": {
				Plugin: topology.TopologyBlock,
				Nodes:  []string{"Node[301,303]"},
			},
		},
	}
	nt, _ := NewNetworkTopology(v, cfg)
	buf := &bytes.Buffer{}
	err := nt.Generate(buf)
	require.Nil(t, err)
	require.Equal(t, expected, buf.String())

	buf = &bytes.Buffer{}
	_, err = nt.GenerateTopologyConfig(buf, true)
	require.Nil(t, err)
	require.Equal(t, expectedSkeleton, buf.String())
}

func TestMixedYamlTopology(t *testing.T) {
	expected := `- topology: topo1
  cluster_default: false
  tree:
    switches:
        - switch: IB2
          children: S1
        - switch: S1
          children: S3
        - switch: S3
          nodes: Node[201,205]
- topology: topo2
  cluster_default: false
  tree:
    switches:
        - switch: IB1
          children: S4
        - switch: IB2
          children: S1
        - switch: S4
          children: S6
        - switch: S1
          children: S[2-3]
        - switch: S6
          nodes: Node[401-403]
        - switch: S2
          nodes: Node[104-105]
        - switch: S3
          nodes: Node[201,205]
- topology: topo3
  cluster_default: false
  block:
    block_sizes:
        - 2
    blocks:
        - block: block1
          nodes: Node[104-105]
- topology: topo4
  cluster_default: false
  block:
    block_sizes:
        - 2
    blocks:
        - block: block1
          nodes: Node[301-302]
        - block: block2
          nodes: Node303
- topology: topo5
  cluster_default: true
  flat: true
`
	v, _ := GetBlockWithMultiIBTestSet()
	cfg := &Config{
		Topologies: map[string]*TopologySpec{
			"topo1": {
				Plugin: topology.TopologyTree,
				Nodes:  []string{"Node[201,205]"},
			},
			"topo2": {
				Plugin: topology.TopologyTree,
				Nodes:  []string{"Node[104,105]", "Node[201,205-207]", "Node[401-403]"},
			},
			"topo3": {
				Plugin: topology.TopologyBlock,
				Nodes:  []string{"Node[104-105,107-108]"},
			},
			"topo4": {
				Plugin:     topology.TopologyBlock,
				Nodes:      []string{"Node[301,302,303]"},
				BlockSizes: []int{2},
			},
			"topo5": {
				Plugin:         topology.TopologyFlat,
				ClusterDefault: true,
			},
		},
	}
	nt, _ := NewNetworkTopology(v, cfg)
	buf := &bytes.Buffer{}
	err := nt.Generate(buf)
	require.Nil(t, err)
	require.Equal(t, expected, buf.String())
}

func TestBlockOnlyYamlTopology(t *testing.T) {
	expected := `- topology: topo1
  cluster_default: true
  block:
    block_sizes:
        - 2
    blocks:
        - block: block1
          nodes: Node[104-105]
- topology: topo2
  cluster_default: false
  block:
    block_sizes:
        - 2
    blocks:
        - block: block1
          nodes: Node[301,303]
`
	v, _ := GetBlockWithMultiIBTestSet()
	v.Tiers = nil

	cfg := &Config{
		Topologies: map[string]*TopologySpec{
			"topo1": {
				Plugin:         topology.TopologyBlock,
				Nodes:          []string{"Node[104,105]"},
				ClusterDefault: true,
			},
			"topo2": {
				Plugin: topology.TopologyBlock,
				Nodes:  []string{"Node[301,303]"},
			},
		},
	}
	nt, _ := NewNetworkTopology(v, cfg)
	buf := &bytes.Buffer{}
	err := nt.Generate(buf)
	require.Nil(t, err)
	require.Equal(t, expected, buf.String())
}

func TestEmptyPartitionTopology(t *testing.T) {
	expected := `- topology: topo1
  cluster_default: false
  tree:
    switches:
        - switch: IB2
          children: S1
        - switch: S1
          children: S3
        - switch: S3
          nodes: Node[201,205]
- topology: topo2
  cluster_default: false
  flat: true
- topology: topo3
  cluster_default: false
  block:
    block_sizes:
        - 2
    blocks:
        - block: block1
          nodes: Node[104-105]
- topology: topo4
  cluster_default: false
  flat: true
- topology: topo5
  cluster_default: true
  flat: true
`
	expectedSkeleton := `- topology: topo1
  cluster_default: false
  tree:
    switches:
        - switch: IB2
          children: S1
        - switch: S1
          children: S3
        - switch: S3
- topology: topo2
  cluster_default: false
  flat: true
- topology: topo3
  cluster_default: false
  block:
    block_sizes:
        - 2
    blocks:
        - block: block1
- topology: topo4
  cluster_default: false
  flat: true
- topology: topo5
  cluster_default: true
  flat: true
`

	v, _ := GetBlockWithMultiIBTestSet()
	cfg := &Config{
		Topologies: map[string]*TopologySpec{
			"topo1": {
				Plugin: topology.TopologyTree,
				Nodes:  []string{"Node[201,205]"},
			},
			"topo2": {
				Plugin: topology.TopologyTree,
				Nodes:  []string{"NodeIsDown"},
			},
			"topo3": {
				Plugin: topology.TopologyBlock,
				Nodes:  []string{"Node[104-105,107-108]"},
			},
			"topo4": {
				Plugin:     topology.TopologyBlock,
				Nodes:      []string{"NodeIsDown"},
				BlockSizes: []int{2},
			},
			"topo5": {
				Plugin:         topology.TopologyFlat,
				ClusterDefault: true,
			},
		},
	}
	nt, err := NewNetworkTopology(v, cfg)
	require.Nil(t, err)
	buf := &bytes.Buffer{}
	err = nt.Generate(buf)
	require.Nil(t, err)
	require.Equal(t, expected, buf.String())

	buf = &bytes.Buffer{}
	_, err = nt.GenerateTopologyConfig(buf, true)
	require.Nil(t, err)
	require.Equal(t, expectedSkeleton, buf.String())
}

// TestGetBlockTopologyUnitComplementEmptyNodes verifies that per-partition block
// topology pads each accelerator's base blocks to a complete aggregate group. a2 has
// only 2 nodes (1 base block) but groupSize=2 requires 2 slots, so an empty block4 is
// inserted. Tree-capacity expansion rounds 3 groups to 4, adding block7 and block8.
func TestGetBlockTopologyUnitComplementEmptyNodes(t *testing.T) {
	expected := `- topology: topo1
  cluster_default: false
  block:
    block_sizes:
        - 2
        - 4
    blocks:
        - block: block1
          nodes: n[10-11]
        - block: block2
          nodes: n12
        - block: block3
          nodes: n[20-21]
        - block: block4
        - block: block5
          nodes: n[31-32]
        - block: block6
          nodes: n33
`
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

	buf := &bytes.Buffer{}
	require.Nil(t, nt.Generate(buf))
	require.Equal(t, expected, buf.String())
}

// TestExpandDoSNodeRange verifies that an attacker-supplied node range that
// would expand to billions of entries (decompression bomb) is rejected with an
// error — not an OOM kill — for both the block and tree topology plugins.
func TestExpandDoSNodeRange(t *testing.T) {
	t.Run(topology.TopologyBlock, func(t *testing.T) {
		domains := topology.NewDomainMap()
		domains.AddHost("domain-1", "instance-1", "node-1")
		graph := &topology.Graph{Domains: domains}
		cfg := &Config{
			Topologies: map[string]*TopologySpec{
				"evil": {
					Plugin: topology.TopologyBlock,
					Nodes:  []string{"n[0-2000000000]"},
				},
			},
		}
		nt, err := NewNetworkTopology(graph, cfg)
		require.NoError(t, err)

		_, httpErr := nt.GetTopologies()
		require.NotNil(t, httpErr, "expected error from oversized range, got nil")
		require.Equal(t, 400, httpErr.Code())
		require.Contains(t, httpErr.Error(), "exceeds")
	})

	t.Run(topology.TopologyTree, func(t *testing.T) {
		// Tree topology requires a non-nil graph.Tiers to pass validation.
		graph := &topology.Graph{
			Tiers: &topology.Vertex{
				Vertices: map[string]*topology.Vertex{},
			},
		}
		cfg := &Config{
			Topologies: map[string]*TopologySpec{
				"evil": {
					Plugin: topology.TopologyTree,
					Nodes:  []string{"n[0-2000000000]"},
				},
			},
		}
		nt, err := NewNetworkTopology(graph, cfg)
		require.NoError(t, err)

		_, httpErr := nt.GetTopologies()
		require.NotNil(t, httpErr, "expected error from oversized range, got nil")
		require.Equal(t, 400, httpErr.Code())
		require.Contains(t, httpErr.Error(), "exceeds")
	})
}
