// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package akamai

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/dsx-ai-factory/topograph/pkg/engines/slurm"
	"github.com/dsx-ai-factory/topograph/pkg/models"
	"github.com/dsx-ai-factory/topograph/pkg/providers"
	"github.com/dsx-ai-factory/topograph/pkg/topology"
)

const simulationModel = `switches:
  root:
    switches: [leaf1, leaf2]
blocks:
  - switch: leaf1
    nodes: [worker1, worker2]
    labels:
      topology.kubernetes.io/region: r1
      hardware: gpu
  - switch: leaf2
    nodes: [worker3]
    labels:
      topology.kubernetes.io/region: r2
`

func writeTestFile(t *testing.T, data string) string {
	t.Helper()
	name := filepath.Join(t.TempDir(), "input.yaml")
	require.NoError(t, os.WriteFile(name, []byte(data), 0600))
	return name
}

func TestModelSimulation(t *testing.T) {
	modelFile := writeTestFile(t, simulationModel)
	name, loader := NamedLoaderSim()
	require.Equal(t, "akamai-sim", name)
	for _, trim := range []int{0, 1, 2} {
		p, err := loader(context.Background(), providers.Config{Params: map[string]any{"modelFileName": modelFile, "trimTiers": trim}})
		require.Nil(t, err)
		instances, err := p.(*simProvider).GetComputeInstances(context.Background())
		require.Nil(t, err)
		require.Len(t, instances, 2)
		// One switch per page forces parent and host records onto different pages.
		size := 1
		g, err := p.GenerateTopologyConfig(context.Background(), &size, instances)
		require.Nil(t, err)
		require.Len(t, g.Instances, 3)
		layers := []string{"leaf1", "root"}
		require.Equal(t, layers[:len(layers)-trim], g.Instances["worker1"].NetworkLayers)
		require.Equal(t, "gpu", g.Instances["worker1"].Labels["hardware"])
		require.Nil(t, g.Domains)
	}
	p, err := loader(context.Background(), providers.Config{Params: map[string]any{"modelFileName": modelFile}})
	require.Nil(t, err)
	g, err := p.GenerateTopologyConfig(context.Background(), nil, []topology.ComputeInstances{{Region: "r1", Instances: map[string]string{"worker1": "node1", "unknown": "node2"}}})
	require.Nil(t, err)
	require.Len(t, g.Instances, 1)
	require.Equal(t, "node2", g.Tiers.Vertices[topology.NoTopology].Vertices["unknown"].Name)
	output, err := slurm.GenerateOutput(context.Background(), g, nil)
	require.Nil(t, err)
	require.Contains(t, string(output), "SwitchName=leaf1 Nodes=node1")
}

func TestSnapshotSimulation(t *testing.T) {
	p, err := LoaderSim(context.Background(), providers.Config{Params: map[string]any{"topologyFileName": "testdata/topology.json"}})
	require.Nil(t, err)
	size := 1
	g, err := p.GenerateTopologyConfig(context.Background(), &size, requestedFixture(t))
	require.Nil(t, err)
	assertFixtureGraph(t, g)
	instances, err := p.(*simProvider).GetComputeInstances(context.Background())
	require.Nil(t, err)
	require.Len(t, instances, 1)
	require.Equal(t, "fake1", instances[0].Region)
	require.Len(t, instances[0].Instances, 3)
	g, err = p.GenerateTopologyConfig(context.Background(), nil, instances)
	require.Nil(t, err)
	require.Len(t, g.Instances, 3)
	require.NotContains(t, g.Tiers.Vertices, topology.NoTopology)
}

func TestYAMLSnapshotSimulation(t *testing.T) {
	snapshot := writeTestFile(t, `# SPDX-License-Identifier: Apache-2.0
region: r
page: 1
pages: 1
results: 2
data:
  - name: leaf
    uplink:
      device: root
    hosts:
      - host_uuid: n1
        port: eth1
  - name: root
    uplink: null
    hosts: []
`)
	p, err := LoaderSim(context.Background(), providers.Config{Params: map[string]any{"topologyFileName": snapshot}})
	require.Nil(t, err)
	g, err := p.GenerateTopologyConfig(context.Background(), nil, []topology.ComputeInstances{{Region: "r", Instances: map[string]string{"n1": "worker"}}})
	require.Nil(t, err)
	require.Equal(t, []string{"leaf", "root"}, g.Instances["n1"].NetworkLayers)
}

func TestSimulationFailures(t *testing.T) {
	model := writeTestFile(t, simulationModel)
	badSnapshot := writeTestFile(t, `{"region":"r","data":[],"page":1,"pages":2,"results":0}`)
	for _, params := range []map[string]any{
		nil, {"modelFileName": "does-not-exist.yaml"}, {"topologyFileName": "does-not-exist.json"},
		{"modelFileName": model, "topologyFileName": "testdata/topology.json"},
		{"modelFileName": writeTestFile(t, "bad: yaml: input:")}, {"topologyFileName": writeTestFile(t, "bad")},
		{"topologyFileName": badSnapshot}, {"topologyFileName": writeTestFile(t, `{"region":"r","data":[],"page":1,"pages":1,"results":1}`)},
		{"modelFileName": model, "trimTiers": 3}, {"modelFileName": model, "api_error": []int{1}},
	} {
		_, err := LoaderSim(context.Background(), providers.Config{Params: params})
		require.NotNil(t, err, "%v", params)
		require.Equal(t, 400, err.Code())
	}
	for _, apiErr := range []int{errTopology, 401, 429, 503} {
		p, err := LoaderSim(context.Background(), providers.Config{Params: map[string]any{"modelFileName": model, "api_error": apiErr}})
		require.Nil(t, err)
		g, err := p.GenerateTopologyConfig(context.Background(), nil, []topology.ComputeInstances{{Region: "r1", Instances: map[string]string{"worker1": "node"}}})
		require.Nil(t, g)
		require.NotNil(t, err)
		code := 502
		if apiErr >= 400 {
			code = apiErr
		}
		require.Equal(t, code, err.Code())
	}
}

func TestSimulationPaging(t *testing.T) {
	model, err := models.NewModelFromData([]byte(simulationModel), "test model")
	require.NoError(t, err)
	c := newSimClient(model, 0)
	first, httpErr := c.Topology(context.Background(), "r1", 1, 1)
	require.Nil(t, httpErr)
	require.Equal(t, 2, first.Pages)
	require.Equal(t, 2, first.Results)
	require.Equal(t, "leaf1", first.Data[0].Name)
	second, httpErr := c.Topology(context.Background(), "r1", 1, 2)
	require.Nil(t, httpErr)
	require.Equal(t, "root", second.Data[0].Name)
	require.Equal(t, "leaf1", second.Data[0].Connections[0].Device)
	empty, httpErr := c.Topology(context.Background(), "absent", 1, 1)
	require.Nil(t, httpErr)
	require.NotNil(t, empty.Data)
	require.Empty(t, empty.Data)
	for _, args := range [][2]int{{0, 1}, {1, 0}, {1, 3}} {
		_, err := c.Topology(context.Background(), "r1", args[0], args[1])
		require.Equal(t, 400, err.Code())
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, httpErr = c.Topology(ctx, "r1", 1, 1)
	require.Equal(t, 502, httpErr.Code())
}

func TestIgnoredMetadata(t *testing.T) {
	records := simpleRecords()
	unhealthy := false
	records[0].Fabric = "future_fabric"
	records[0].IsHealthy = &unhealthy
	// Connections intentionally disagree with uplink; uplink remains authoritative.
	records[0].Connections = []Link{{Device: "unknown"}}
	g, err := testProvider(records, "r").GenerateTopologyConfig(context.Background(), nil, []topology.ComputeInstances{{Region: "r", Instances: map[string]string{"n1": "node"}}})
	require.Nil(t, err)
	require.Equal(t, []string{"leaf", "root"}, g.Instances["n1"].NetworkLayers)
}
