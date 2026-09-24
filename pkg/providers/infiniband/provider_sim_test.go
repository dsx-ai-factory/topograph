/*
 * Copyright 2026 NVIDIA CORPORATION
 * SPDX-License-Identifier: Apache-2.0
 */

package infiniband

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/dsx-ai-factory/topograph/pkg/providers"
	"github.com/dsx-ai-factory/topograph/pkg/topology"
)

const exampleIBOutput = "../../../tests/output/ibnetdiscover/example.out"
const computeStorageIBOutput = "../../../tests/output/ibnetdiscover/compute-storage.out"

func TestLoaderSim(t *testing.T) {
	name, loader := NamedLoaderSim()
	require.Equal(t, NAME_SIM, name)
	require.NotNil(t, loader)

	for _, tc := range []struct {
		name   string
		params map[string]any
	}{
		{"missing path", nil},
		{"wrong path type", map[string]any{"ibnetdiscoverFileName": 42}},
		{"missing file", map[string]any{"ibnetdiscoverFileName": "not-a-file.out"}},
		{"model file", map[string]any{"modelFileName": "small-tree.yaml"}},
		{"model and capture", map[string]any{"modelFileName": "small-tree.yaml", "ibnetdiscoverFileName": exampleIBOutput}},
		{"invalid switch selector", map[string]any{"ibnetdiscoverFileName": computeStorageIBOutput, "switchSelector": map[string]any{"exclude": []any{"["}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			provider, httpErr := loader(context.Background(), providers.Config{Params: tc.params})
			require.Nil(t, provider)
			require.Equal(t, http.StatusBadRequest, httpErr.Code())
			if _, hasModel := tc.params["modelFileName"]; hasModel {
				require.ErrorContains(t, httpErr, "use ibnetdiscoverFileName")
			}
		})
	}

	invalid := filepath.Join(t.TempDir(), "invalid.out")
	require.NoError(t, os.WriteFile(invalid, []byte("not ibnetdiscover output"), 0600))
	provider, httpErr := loader(context.Background(), providers.Config{Params: map[string]any{"ibnetdiscoverFileName": invalid}})
	require.Nil(t, provider)
	require.Equal(t, http.StatusBadRequest, httpErr.Code())

	disconnected := filepath.Join(t.TempDir(), "disconnected.out")
	output := "# Topology file:\nSwitch 2 \"S-leaf\" # \"leaf\"\n[1] \"H-connected\"[1] # \"node-a mlx5_0\"\n\n" +
		"Ca 1 \"H-connected\" # \"node-a mlx5_0\"\nCa 1 \"H-disconnected\" # \"node-b mlx5_0\"\n"
	require.NoError(t, os.WriteFile(disconnected, []byte(output), 0600))
	provider, httpErr = loader(context.Background(), providers.Config{Params: map[string]any{"ibnetdiscoverFileName": disconnected}})
	require.Nil(t, httpErr)
	instances, httpErr := provider.(*ProviderSim).GetComputeInstances(context.Background())
	require.Nil(t, httpErr)
	require.Equal(t, map[string]string{"node-a": "node-a"}, instances[0].Instances)
}

func TestProviderSimSwitchSelector(t *testing.T) {
	const spine = "S-0000000000000021"
	const computeLeaf1 = "S-0000000000000011"
	const computeLeaf2 = "S-0000000000000012"
	const storageLeaf = "S-0000000000000031"

	for _, tc := range []struct {
		name           string
		selector       map[string]any
		expectedLeaves []string
	}{
		{"unfiltered", nil, []string{computeLeaf1, computeLeaf2, storageLeaf}},
		{"include compute", map[string]any{"include": []any{"^IB-ComputeLeaf"}}, []string{computeLeaf1, computeLeaf2}},
		{"exclude storage", map[string]any{"exclude": []any{"^IB-Storage"}}, []string{computeLeaf1, computeLeaf2}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			params := map[string]any{"ibnetdiscoverFileName": computeStorageIBOutput}
			if tc.selector != nil {
				params["switchSelector"] = tc.selector
			}
			provider, httpErr := LoaderSim(context.Background(), providers.Config{Params: params})
			require.Nil(t, httpErr)
			instances, httpErr := provider.(*ProviderSim).GetComputeInstances(context.Background())
			require.Nil(t, httpErr)
			require.Len(t, instances[0].Instances, 6)

			graph, httpErr := provider.GenerateTopologyConfig(context.Background(), nil, instances)
			require.Nil(t, httpErr)
			require.Contains(t, graph.Tiers.Vertices, spine)
			leaves := make([]string, 0, len(graph.Tiers.Vertices[spine].Vertices))
			for id := range graph.Tiers.Vertices[spine].Vertices {
				leaves = append(leaves, id)
			}
			require.ElementsMatch(t, tc.expectedLeaves, leaves)
			expectedHosts := []string{"node-a", "node-b", "node-c", "node-d", "node-e", "node-f"}
			if tc.selector != nil {
				require.ElementsMatch(t, expectedHosts, leafNames(graph.Tiers))
				require.ElementsMatch(t, []string{"node-a", "node-b", "node-c"}, leafNames(graph.Tiers.Vertices[spine].Vertices[computeLeaf1]))
				require.ElementsMatch(t, []string{"node-d", "node-e", "node-f"}, leafNames(graph.Tiers.Vertices[spine].Vertices[computeLeaf2]))
			} else {
				require.ElementsMatch(t, expectedHosts, leafNames(graph.Tiers.Vertices[spine].Vertices[storageLeaf]))
			}
		})
	}

	provider, httpErr := LoaderSim(context.Background(), providers.Config{Params: map[string]any{
		"ibnetdiscoverFileName": computeStorageIBOutput,
		"switchSelector":        map[string]any{"include": []any{"^not-present"}},
	}})
	require.Nil(t, httpErr)
	instances, httpErr := provider.(*ProviderSim).GetComputeInstances(context.Background())
	require.Nil(t, httpErr)
	_, httpErr = provider.GenerateTopologyConfig(context.Background(), nil, instances)
	require.Equal(t, http.StatusInternalServerError, httpErr.Code())
	require.ErrorContains(t, httpErr, "switchSelector matched no usable InfiniBand topology")
}

func TestProviderSimGenerateTopologyConfig(t *testing.T) {
	p, httpErr := LoaderSim(context.Background(), providers.Config{Params: map[string]any{"ibnetdiscoverFileName": exampleIBOutput}})
	require.Nil(t, httpErr)
	sim := p.(*ProviderSim)

	all, httpErr := sim.GetComputeInstances(context.Background())
	require.Nil(t, httpErr)
	require.Len(t, all, 1)
	require.Equal(t, "b07-p1-dgx-07-c01", all[0].Instances["b07-p1-dgx-07-c01"])
	graph, httpErr := sim.GenerateTopologyConfig(context.Background(), nil, all)
	require.Nil(t, httpErr)
	require.NotEmpty(t, graph.Tiers.Vertices)
	expectedHosts := make(map[string]bool, len(all[0].Instances))
	for _, hostname := range all[0].Instances {
		expectedHosts[hostname] = true
	}
	actualHosts := make(map[string]bool)
	for _, hostname := range leafNames(graph.Tiers) {
		actualHosts[hostname] = true
	}
	require.Equal(t, expectedHosts, actualHosts)
	require.Empty(t, graph.Domains)

	selected := []topology.ComputeInstances{{Region: "local", Instances: map[string]string{
		"b07-p1-dgx-07-c01": "b07-p1-dgx-07-c01",
	}}}
	graph, httpErr = sim.GenerateTopologyConfig(context.Background(), nil, selected)
	require.Nil(t, httpErr)
	require.Equal(t, []string{"b07-p1-dgx-07-c01"}, leafNames(graph.Tiers))

	_, httpErr = sim.GenerateTopologyConfig(context.Background(), nil, []topology.ComputeInstances{{Region: "one"}, {Region: "two"}})
	require.Equal(t, http.StatusBadRequest, httpErr.Code())

	instanceMap, err := sim.Instances2NodeMap(context.Background(), []string{"node-a"})
	require.NoError(t, err)
	require.Equal(t, map[string]string{"node-a": "node-a"}, instanceMap)
	regions, err := sim.GetInstancesRegions(context.Background(), []string{"node-a"})
	require.NoError(t, err)
	require.Equal(t, map[string]string{"node-a": "local"}, regions)
}

func leafNames(root *topology.Vertex) []string {
	if len(root.Vertices) == 0 {
		return []string{root.Name}
	}
	var names []string
	for _, child := range root.Vertices {
		names = append(names, leafNames(child)...)
	}
	return names
}
