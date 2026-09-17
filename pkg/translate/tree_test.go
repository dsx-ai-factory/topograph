/*
 * Copyright 2026 NVIDIA CORPORATION
 * SPDX-License-Identifier: Apache-2.0
 */

package translate

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/dsx-ai-factory/topograph/pkg/topology"
)

// TestToTreeTopologySkeletonOnly verifies that skeleton-only output for a
// "topology/tree" config keeps only the top-level (root) switches, declared
// by name alone: intermediate and leaf switches are omitted entirely, and the
// top-level switch's own Switches= list is dropped too, since either would
// change (and so trigger a reconfigure) whenever a switch is added or removed
// below the top level.
func TestToTreeTopologySkeletonOnly(t *testing.T) {
	v, _ := GetTreeTestSet(false)
	cfg := &Config{Plugin: topology.TopologyTree}
	nt, err := NewNetworkTopology(v, cfg)
	require.NoError(t, err)

	buf := &bytes.Buffer{}
	_, httpErr := nt.GenerateTopologyConfig(buf, true)
	require.Nil(t, httpErr)
	require.Equal(t, "SwitchName=S1\n", buf.String())
}

// TestToTreeTopologySkeletonOnlyMultipleTopLevelSwitches verifies that
// skeleton-only output declares every sibling top-level switch by name alone,
// each independently of the others' descendants.
func TestToTreeTopologySkeletonOnlyMultipleTopLevelSwitches(t *testing.T) {
	nA1 := &topology.Vertex{ID: "IA1", Name: "NodeA1"}
	swA := &topology.Vertex{ID: "SA", Vertices: map[string]*topology.Vertex{"IA1": nA1}}

	nB1 := &topology.Vertex{ID: "IB1", Name: "NodeB1"}
	swB2 := &topology.Vertex{ID: "SB2", Vertices: map[string]*topology.Vertex{"IB1": nB1}}
	swB := &topology.Vertex{ID: "SB", Vertices: map[string]*topology.Vertex{"SB2": swB2}}

	treeRoot := &topology.Vertex{Vertices: map[string]*topology.Vertex{"SA": swA, "SB": swB}}

	cfg := &Config{Plugin: topology.TopologyTree}
	nt, err := NewNetworkTopology(&topology.Graph{Tiers: treeRoot}, cfg)
	require.NoError(t, err)

	buf := &bytes.Buffer{}
	_, httpErr := nt.GenerateTopologyConfig(buf, true)
	require.Nil(t, httpErr)
	require.Equal(t, "SwitchName=SA\nSwitchName=SB\n", buf.String())
}

// TestToTreeTopologySkeletonOnlyLeafTopLevel verifies that a top-level switch
// whose only children are compute nodes (no intermediate switch tier below
// it) still gets declared under skeleton-only, instead of being silently
// dropped because it has neither a Switches= nor a (skeleton-suppressed)
// Nodes= line to emit.
func TestToTreeTopologySkeletonOnlyLeafTopLevel(t *testing.T) {
	n1 := &topology.Vertex{ID: "I1", Name: "Node001"}
	n2 := &topology.Vertex{ID: "I2", Name: "Node002"}
	sw := &topology.Vertex{ID: "S1", Vertices: map[string]*topology.Vertex{"I1": n1, "I2": n2}}
	treeRoot := &topology.Vertex{Vertices: map[string]*topology.Vertex{"S1": sw}}

	cfg := &Config{Plugin: topology.TopologyTree}
	nt, err := NewNetworkTopology(&topology.Graph{Tiers: treeRoot}, cfg)
	require.NoError(t, err)

	buf := &bytes.Buffer{}
	_, httpErr := nt.GenerateTopologyConfig(buf, true)
	require.Nil(t, httpErr)
	require.Equal(t, "SwitchName=S1\n", buf.String())
}

// TestToTreeTopologySkeletonOnlyFullyTrimmedTiers verifies that when
// trimTiers removes every fabric tier for an instance, the resulting bare
// compute-node vertex placed directly under the tree root is not emitted as
// a switch under skeleton-only.
func TestToTreeTopologySkeletonOnlyFullyTrimmedTiers(t *testing.T) {
	topo := topology.NewClusterTopology()
	topo.Append(&topology.InstanceTopology{
		InstanceID:  "instance-1",
		FabricTiers: topology.ClosestFirstFabricTiers("fabric-0", "fabric-1"),
	})

	// trimTiers=2 clips every fabric tier; the instance itself becomes a
	// direct child of the tree root, with no switch wrapping it.
	graph := topo.ToGraph("test", []topology.ComputeInstances{{
		Instances: map[string]string{"instance-1": "node-1"},
	}}, 2, false)

	cfg := &Config{Plugin: topology.TopologyTree}
	nt, err := NewNetworkTopology(graph, cfg)
	require.NoError(t, err)

	buf := &bytes.Buffer{}
	_, httpErr := nt.GenerateTopologyConfig(buf, true)
	require.Nil(t, httpErr)
	require.Equal(t, "", buf.String())
}

// TestToTreeTopologySkeletonOnlyComposesWithTrimTiers verifies that
// skeleton-only "top-level" means the highest surviving tier after
// trimTiers has already clipped the fabric path at graph-construction time,
// not necessarily the physical root of the discovered fabric.
func TestToTreeTopologySkeletonOnlyComposesWithTrimTiers(t *testing.T) {
	topo := topology.NewClusterTopology()
	topo.Append(&topology.InstanceTopology{
		InstanceID:  "instance-1",
		FabricTiers: topology.ClosestFirstFabricTiers("fabric-0", "fabric-1", "fabric-2"),
	})

	// trimTiers=1 clips the physical root (fabric-2); fabric-1 becomes the
	// top-level tier the tree translator ever sees.
	graph := topo.ToGraph("test", []topology.ComputeInstances{{
		Instances: map[string]string{"instance-1": "node-1"},
	}}, 1, false)

	cfg := &Config{Plugin: topology.TopologyTree}
	nt, err := NewNetworkTopology(graph, cfg)
	require.NoError(t, err)

	buf := &bytes.Buffer{}
	_, httpErr := nt.GenerateTopologyConfig(buf, true)
	require.Nil(t, httpErr)
	require.Equal(t, "SwitchName=fabric-1\n", buf.String())
}

// TestToTreeTopologySkeletonOnlyDeepTree verifies the cutoff holds for a tree
// with more than two switch tiers below the top level: only the top-level
// switch is emitted, regardless of how many tiers exist beneath it.
func TestToTreeTopologySkeletonOnlyDeepTree(t *testing.T) {
	leaf := &topology.Vertex{ID: "I1", Name: "Node001"}
	tier3 := &topology.Vertex{ID: "T3", Vertices: map[string]*topology.Vertex{"I1": leaf}}
	tier2 := &topology.Vertex{ID: "T2", Vertices: map[string]*topology.Vertex{"T3": tier3}}
	tier1 := &topology.Vertex{ID: "T1", Vertices: map[string]*topology.Vertex{"T2": tier2}}
	treeRoot := &topology.Vertex{Vertices: map[string]*topology.Vertex{"T1": tier1}}

	cfg := &Config{Plugin: topology.TopologyTree}
	nt, err := NewNetworkTopology(&topology.Graph{Tiers: treeRoot}, cfg)
	require.NoError(t, err)

	buf := &bytes.Buffer{}
	_, httpErr := nt.GenerateTopologyConfig(buf, true)
	require.Nil(t, httpErr)
	require.Equal(t, "SwitchName=T1\n", buf.String())

	// Sanity check the non-skeleton path still emits every tier.
	buf.Reset()
	_, httpErr = nt.GenerateTopologyConfig(buf, false)
	require.Nil(t, httpErr)
	require.Equal(t, "SwitchName=T1 Switches=T2\nSwitchName=T2 Switches=T3\nSwitchName=T3 Nodes=Node001\n", buf.String())
}
