/*
 * Copyright 2026 NVIDIA CORPORATION
 * SPDX-License-Identifier: Apache-2.0
 */

package firebird

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/dsx-ai-factory/topograph/pkg/engines/slurm"
	"github.com/dsx-ai-factory/topograph/pkg/providers"
	"github.com/dsx-ai-factory/topograph/pkg/topology"
	"github.com/stretchr/testify/require"
)

const simulationModel = `
switches:
  core:
    switches: [spine]
  spine:
    switches: [leaf1, leaf2]
blocks:
- switch: leaf1
  nodes: [n1, n2, n3]
  labels:
    hardware: gpu
  annotations:
    accelerator.topology.test/domain: ignored-domain
- switch: leaf2
  nodes: [n4, n5]
`

func writeSimulationModel(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "firebird.yaml")
	require.NoError(t, os.WriteFile(path, []byte(content), 0600))
	return path
}

func TestSimulation(t *testing.T) {
	model := writeSimulationModel(t, simulationModel)
	name, loader := NamedLoaderSim()
	require.Equal(t, "firebird-sim", name)
	for _, trim := range []int{0, 1, 2} {
		p, err := loader(context.Background(), providers.Config{Params: map[string]any{"modelFileName": model, "trimTiers": trim}})
		require.Nil(t, err)
		sim := p.(*simProvider)
		cis, err := sim.GetComputeInstances(context.Background())
		require.Nil(t, err)
		require.Len(t, topology.GetNodeNameList(cis), 5)
		size := 2 // Five records produce a partial final page.
		graph, err := p.GenerateTopologyConfig(context.Background(), &size, cis)
		require.Nil(t, err)
		require.Len(t, graph.Instances, 5)
		require.Nil(t, graph.Domains) // Firebird has no accelerator discovery.
		layers := []string{"leaf1", "spine", "core"}
		require.Equal(t, layers[:len(layers)-trim], graph.Instances["n1"].NetworkLayers)
		require.Equal(t, "gpu", graph.Instances["n1"].Labels["hardware"])
	}
	// Production filtering and missing-topology handling also apply in simulation.
	p, err := loader(context.Background(), providers.Config{Params: map[string]any{"modelFileName": model}})
	require.Nil(t, err)
	graph, err := p.GenerateTopologyConfig(context.Background(), nil, []topology.ComputeInstances{{Instances: map[string]string{"n1": "worker1", "missing": "worker2"}}})
	require.Nil(t, err)
	require.Len(t, graph.Instances, 1)
	require.Equal(t, "worker2", graph.Tiers.Vertices[topology.NoTopology].Vertices["missing"].Name)
	output, err := slurm.GenerateOutput(context.Background(), graph, nil)
	require.Nil(t, err)
	require.Equal(t, "SwitchName=core Switches=spine\nSwitchName=no-topology Nodes=worker2\nSwitchName=spine Switches=leaf1\nSwitchName=leaf1 Nodes=worker1\n", string(output))
}

func TestSimulationPages(t *testing.T) {
	model := writeSimulationModel(t, simulationModel)
	p, err := LoaderSim(context.Background(), providers.Config{Params: map[string]any{"modelFileName": model}})
	require.Nil(t, err)
	client := p.(*simProvider).client
	token := ""
	for _, ids := range [][]string{{"n1", "n2"}, {"n3", "n4"}, {"n5"}} {
		page, err := client.Topology(context.Background(), 2, token)
		require.Nil(t, err)
		got := make([]string, 0, len(page.Nodes))
		for _, node := range page.Nodes {
			got = append(got, node.NodeID)
			require.Equal(t, "bm", node.NodeKind)
			require.Equal(t, "complete", node.TopologyStatus)
		}
		require.Equal(t, ids, got)
		token = page.NextPageToken
	}
	require.Empty(t, token)
	page, err := client.Topology(context.Background(), 2, "5")
	require.Nil(t, err)
	require.NotNil(t, page.Nodes)
	require.Empty(t, page.Nodes)
	for _, token := range []string{"invalid", "-1", "6"} {
		_, err := client.Topology(context.Background(), 2, token)
		require.Equal(t, http.StatusBadRequest, err.Code())
	}
	_, err = client.Topology(context.Background(), 0, "")
	require.Equal(t, http.StatusBadRequest, err.Code())
}

func TestSimulationFailures(t *testing.T) {
	model := writeSimulationModel(t, simulationModel)
	badModel := writeSimulationModel(t, "bad: model: error:")
	for _, params := range []map[string]any{
		nil,
		{"modelFileName": "/nonexistent/firebird-model.yaml"},
		{"modelFileName": badModel},
		{"modelFileName": model, "trimTiers": 3},
		{"modelFileName": model, "trimTiers": "invalid"},
	} {
		_, err := LoaderSim(context.Background(), providers.Config{Params: params})
		require.NotNil(t, err)
		require.Equal(t, http.StatusBadRequest, err.Code())
	}
	requested := []topology.ComputeInstances{{Instances: map[string]string{"n1": "worker"}}}
	p, err := LoaderSim(context.Background(), providers.Config{Params: map[string]any{"modelFileName": model, "api_error": errTopology}})
	require.Nil(t, err)
	_, err = p.GenerateTopologyConfig(context.Background(), nil, requested)
	require.Equal(t, http.StatusBadGateway, err.Code())
	require.ErrorContains(t, err, "API error")
	p, err = LoaderSim(context.Background(), providers.Config{Params: map[string]any{"modelFileName": model}})
	require.Nil(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = p.GenerateTopologyConfig(ctx, nil, requested)
	require.Equal(t, http.StatusBadGateway, err.Code())
	for _, size := range []int{0, -1} {
		graph, err := p.GenerateTopologyConfig(context.Background(), &size, requested)
		require.Nil(t, err)
		require.Len(t, graph.Instances, 1)
	}
	graph, err := p.GenerateTopologyConfig(context.Background(), nil, nil)
	require.Nil(t, err)
	require.Empty(t, graph.Tiers.Vertices)
}

func TestSimulationAPIErrors(t *testing.T) {
	model := writeSimulationModel(t, simulationModel)
	for _, status := range []int{http.StatusBadRequest, http.StatusNotFound, http.StatusTooManyRequests} {
		p, err := LoaderSim(context.Background(), providers.Config{Params: map[string]any{"modelFileName": model, "api_error": status}})
		require.Nil(t, err)
		_, err = p.GenerateTopologyConfig(context.Background(), nil, []topology.ComputeInstances{{Instances: map[string]string{"n1": "worker"}}})
		require.NotNil(t, err)
		require.Equal(t, status, err.Code())
		require.Contains(t, err.Error(), "Firebird topology request failed:")
	}
}
