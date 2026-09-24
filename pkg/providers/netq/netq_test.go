/*
 * Copyright 2025-2026 NVIDIA CORPORATION
 * SPDX-License-Identifier: Apache-2.0
 */

package netq

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/dsx-ai-factory/topograph/pkg/topology"
)

func TestParseNetq(t *testing.T) {
	data, err := os.ReadFile("../../../tests/output/netq/topologyGraph.json")
	require.NoError(t, err)

	treeRoot := &topology.Vertex{Vertices: make(map[string]*topology.Vertex)}
	err = parseNetq(treeRoot, data, map[string]bool{
		"dgx-01": true,
		"dgx-02": true,
		"dgx-03": true,
		"dgx-04": true,
		"dgx-05": true,
		"dgx-06": true,
		"dgx-07": true,
		"dgx-08": true,
	})
	require.Nil(t, err)

	dgx01 := &topology.Vertex{ID: "node21", Name: "dgx-01"}
	dgx02 := &topology.Vertex{ID: "node24", Name: "dgx-02"}
	dgx03 := &topology.Vertex{ID: "node22", Name: "dgx-03"}
	dgx04 := &topology.Vertex{ID: "node2", Name: "dgx-04"}
	dgx05 := &topology.Vertex{ID: "node12", Name: "dgx-05"}
	dgx06 := &topology.Vertex{ID: "node19", Name: "dgx-06"}
	dgx07 := &topology.Vertex{ID: "node25", Name: "dgx-07"}
	dgx08 := &topology.Vertex{ID: "node7", Name: "dgx-08"}

	node0 := &topology.Vertex{ID: "node0", Name: "net-unit-l1", Vertices: map[string]*topology.Vertex{
		"node21": dgx01, "node24": dgx02, "node22": dgx03, "node2": dgx04,
	}}
	node9 := &topology.Vertex{ID: "node9", Name: "net-unit-l2", Vertices: map[string]*topology.Vertex{
		"node12": dgx05, "node19": dgx06, "node25": dgx07, "node7": dgx08,
	}}
	node3 := &topology.Vertex{ID: "node3", Name: "net-unit-s1", Vertices: map[string]*topology.Vertex{"node0": node0, "node9": node9}}

	expected := &topology.Vertex{Vertices: map[string]*topology.Vertex{"node3": node3}}

	require.Equal(t, expected, treeRoot)

	// empty input
	treeRoot = &topology.Vertex{Vertices: make(map[string]*topology.Vertex)}
	data = []byte{}
	err = parseNetq(treeRoot, data, map[string]bool{})
	require.EqualError(t, err, "netq output read failed: unexpected end of JSON input")

	// invalid input
	str := `[{},{}]`
	err = parseNetq(treeRoot, []byte(str), map[string]bool{})
	require.EqualError(t, err, "invalid NetQ response: multiple entries")

	// link referencing an undeclared switch must not panic; the orphaned leaf
	// has no valid parent so it surfaces at the top tier of the tree.
	treeRoot = &topology.Vertex{Vertices: make(map[string]*topology.Vertex)}
	crasher := []byte(`[{"nodes":[{"compounded_nodes":[{"id":"leaf1","name":"node-a","tier":-1}]}],"links":[{"id":"leaf1-*-sw_undeclared"}]}]`)
	err = parseNetq(treeRoot, crasher, map[string]bool{"node-a": true})
	require.Nil(t, err)
}
