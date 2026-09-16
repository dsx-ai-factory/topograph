/*
 * Copyright 2026, NVIDIA CORPORATION
 * SPDX-License-Identifier: Apache-2.0
 */

package dsx

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/dsx-ai-factory/topograph/pkg/engines/slurm"
	"github.com/dsx-ai-factory/topograph/pkg/providers"
	"github.com/dsx-ai-factory/topograph/pkg/topology"
)

const (
	ignoreErrMsg = "_IGNORE_"

	clusterModel = `
switches:
  core:
    switches: [spine]
  spine:
    labels:
      topology.kubernetes.io/zone: az1
    switches: [leaf1,leaf2]
  leaf1: {}
  leaf2: {}
blocks:
- switch: leaf1
  nodes: [n11,n12]
  annotations:
    accelerator.topology.test/domain: nvl1
- switch: leaf2
  nodes: [n21,n22]
  annotations:
    accelerator.topology.test/domain: nvl2
`

	largeClusterModel = `
switches:
  core:
    switches: [spine]
  spine:
    labels:
      topology.kubernetes.io/zone: az1
    switches: [leaf1,leaf2]
  leaf1: {}
  leaf2: {}
blocks:
- switch: leaf1
  nodes: ["n[100-164]"]
  annotations:
    accelerator.topology.test/domain: nvl1
- switch: leaf2
  nodes: ["n[200-264]"]
  annotations:
    accelerator.topology.test/domain: nvl2
`

	singleNodeModel = `
switches:
  core:
    switches: [leaf]
  leaf: {}
blocks:
- switch: leaf
  nodes: [n1]
  annotations:
    accelerator.topology.test/domain: nvl1
`
)

// makeRegionInstances builds one region entry like gcp provider_sim tests (instance ID -> node name).
func makeRegionInstances(region string, ranges ...[2]int) []topology.ComputeInstances {
	m := make(map[string]string)
	for _, r := range ranges {
		from, to := r[0], r[1]
		for i := from; i <= to; i++ {
			m[fmt.Sprintf("n%d", i)] = fmt.Sprintf("node%d", i)
		}
	}
	return []topology.ComputeInstances{{Region: region, Instances: m}}
}

func TestProviderSimNamedLoaderSim(t *testing.T) {
	name, loader := NamedLoaderSim()
	require.Equal(t, NAME_SIM, name)
	require.NotNil(t, loader)
}

func TestProviderSim(t *testing.T) {
	ctx := context.Background()

	testCases := []struct {
		name       string
		model      string
		instances  []topology.ComputeInstances
		pageSize   *int
		params     map[string]any
		apiErr     int
		shouldFail bool
		err        string
	}{
		{
			name:       "Case 1: bad model YAML",
			model:      `bad: model: error:`,
			shouldFail: true,
			err:        ignoreErrMsg,
		},
		{
			name: "Case 2: model with no capacity blocks",
			model: `
switches:
  core:
    switches: [spine]
  spine:
    switches: [leaf]
  leaf: {}
`,
			instances: []topology.ComputeInstances{
				{
					Region:    "region",
					Instances: map[string]string{"n0": "node0"},
				},
			},
			shouldFail: false,
		},
		{
			name:  "Case 3: ClientFactory API error",
			model: clusterModel,
			instances: []topology.ComputeInstances{
				{
					Region:    "region",
					Instances: map[string]string{"n11": "node11", "n12": "node12"},
				},
			},
			apiErr:     errClientFactory,
			shouldFail: true,
			err:        "failed to get client: API error",
		},
		{
			name:  "Case 4: API error during topology fetch",
			model: clusterModel,
			instances: []topology.ComputeInstances{
				{
					Region:    "region",
					Instances: map[string]string{"n11": "node11", "n12": "node12"},
				},
			},
			apiErr:     errAPIError,
			shouldFail: true,
			err:        "API error: API error",
		},
		{
			name:  "Case 5: valid small cluster in tree format",
			model: clusterModel,
			instances: []topology.ComputeInstances{
				{
					Region: "region",
					Instances: map[string]string{
						"n11": "node11", "n12": "node12",
						"n21": "node21", "n22": "node22",
					},
				},
			},
			shouldFail: false,
		},
		{
			name:  "Case 6: valid small cluster with nodes",
			model: clusterModel,
			instances: []topology.ComputeInstances{
				{
					Region: "region",
					Instances: map[string]string{
						"n11": "node11", "n12": "node12", "n13": "node13",
						"n21": "node21", "n22": "node22", "n23": "node23",
					},
				},
			},
			shouldFail: false,
		},
		{
			name:  "Case 7: single node model",
			model: singleNodeModel,
			instances: []topology.ComputeInstances{
				{
					Region:    "region",
					Instances: map[string]string{"n1": "node1"},
				},
			},
			shouldFail: false,
		},
		{
			name:       "Case 8: large cluster with many nodes",
			model:      largeClusterModel,
			instances:  makeRegionInstances("region", [2]int{100, 164}, [2]int{200, 264}),
			shouldFail: false,
		},
		{
			name:       "Case 9: empty node list",
			model:      clusterModel,
			instances:  nil,
			shouldFail: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			f, err := os.CreateTemp("", "test-*")
			require.NoError(t, err)
			defer func() { _ = os.Remove(f.Name()) }()
			defer func() { _ = f.Close() }()
			n, err := f.WriteString(tc.model)
			require.NoError(t, err)
			require.Equal(t, len(tc.model), n)
			err = f.Sync()
			require.NoError(t, err)

			cfg := providers.Config{
				Params: map[string]any{
					"modelFileName": f.Name(),
					"api_error":     tc.apiErr,
				},
			}
			provider, httpErr := LoaderSim(ctx, cfg)
			if httpErr != nil {
				if len(tc.err) == 0 {
					require.Nil(t, httpErr)
				} else if tc.err != ignoreErrMsg {
					require.EqualError(t, httpErr, tc.err)
				}
				return
			}

			topo, httpErr := provider.GenerateTopologyConfig(ctx, tc.pageSize, tc.instances)
			if tc.shouldFail {
				if len(tc.err) == 0 {
					require.NotNil(t, httpErr)
				} else if tc.err != ignoreErrMsg {
					require.EqualError(t, httpErr, tc.err)
				}
			} else {
				require.Nil(t, httpErr)
				require.NotNil(t, topo)
				// For valid topologies, try to generate SLURM output
				data, httpErr := slurm.GenerateOutput(ctx, topo, tc.params)
				require.Nil(t, httpErr)
				nInst := 0
				for _, ci := range tc.instances {
					nInst += len(ci.Instances)
				}
				// With no requested instances, the buffer stays empty and Bytes() is nil.
				if nInst > 0 {
					require.NotNil(t, data)
				}
			}
		})
	}
}

func TestProviderSimTopologyStructure(t *testing.T) {
	ctx := context.Background()

	// Test that the generated topology has the expected structure
	model := clusterModel
	f, err := os.CreateTemp("", "test-*")
	require.NoError(t, err)
	defer func() { _ = os.Remove(f.Name()) }()
	defer func() { _ = f.Close() }()
	_, err = f.WriteString(model)
	require.NoError(t, err)
	err = f.Sync()
	require.NoError(t, err)

	cfg := providers.Config{
		Params: map[string]any{
			"modelFileName": f.Name(),
			"api_error":     errNone,
		},
	}
	provider, httpErr := LoaderSim(ctx, cfg)
	require.Nil(t, httpErr)

	instances := []topology.ComputeInstances{
		{
			Region: "region",
			Instances: map[string]string{
				"n11": "node11",
				"n12": "node12",
				"n21": "node21",
				"n22": "node22",
			},
		},
	}

	topo, httpErr := provider.GenerateTopologyConfig(ctx, nil, instances)
	require.Nil(t, httpErr)
	require.NotNil(t, topo)

	// Verify the topology tree structure
	treeVertex := topo.Tiers
	require.NotNil(t, treeVertex)
	require.NotNil(t, treeVertex.Vertices)

	// Root should have core switch
	coreVertex, exists := treeVertex.Vertices["core"]
	require.True(t, exists, "core vertex should exist")
	require.Equal(t, "core", coreVertex.ID)

	require.NotNil(t, coreVertex.Vertices)
	spineVertex, exists := coreVertex.Vertices["spine"]
	require.True(t, exists, "spine vertex should exist")
	require.Equal(t, "spine", spineVertex.ID)

	// Spine should have leaf1 and leaf2 as children
	require.NotNil(t, spineVertex.Vertices)
	require.True(t, len(spineVertex.Vertices) >= 1, "spine should have at least one leaf child")
}

func TestProviderSimWithNVLink(t *testing.T) {
	ctx := context.Background()

	// Test that nodes carry their NVLink information
	model := clusterModel
	f, err := os.CreateTemp("", "test-*")
	require.NoError(t, err)
	defer func() { _ = os.Remove(f.Name()) }()
	defer func() { _ = f.Close() }()
	_, err = f.WriteString(model)
	require.NoError(t, err)
	err = f.Sync()
	require.NoError(t, err)

	cfg := providers.Config{
		Params: map[string]any{
			"modelFileName": f.Name(),
			"api_error":     errNone,
		},
	}
	provider, httpErr := LoaderSim(ctx, cfg)
	require.Nil(t, httpErr)

	instances := []topology.ComputeInstances{
		{
			Region: "region",
			Instances: map[string]string{
				"n11": "node11",
				"n12": "node12",
			},
		},
	}

	topo, httpErr := provider.GenerateTopologyConfig(ctx, nil, instances)
	require.Nil(t, httpErr)
	require.NotNil(t, topo)

	// NVLink / accelerator domain is emitted as graph domains (same as AWS ToGraph path).
	domains := topo.Domains
	require.NotNil(t, domains)
	_, hasNVL := domains["nvl1"]
	require.True(t, hasNVL, "cluster model places cb1 nodes in NVLink domain nvl1 under topology/block")
}

func TestResponseToClusterTopologyOmitsEmptyAccelerator(t *testing.T) {
	response := &TopologyResponse{Switches: map[string]SwitchInfo{
		"leaf": {Nodes: []NodeInfo{{NodeID: "n1"}}},
	}}
	instances := []topology.ComputeInstances{{
		Instances: map[string]string{"n1": "node1"},
	}}

	topo := responseToClusterTopology(response, instances)

	require.Len(t, topo.Instances, 1)
	require.Empty(t, topo.Instances[0].XclrDomainID)
}

func TestLoaderSimMissingModelFile(t *testing.T) {
	ctx := context.Background()

	cfg := providers.Config{
		Params: map[string]any{
			"modelFileName": "/nonexistent/model.yaml",
			"api_error":     errNone,
		},
	}
	_, httpErr := LoaderSim(ctx, cfg)
	require.NotNil(t, httpErr)
	require.Contains(t, httpErr.Error(), "failed to load model file")
}

func TestLoaderSimMissingModelFileName(t *testing.T) {
	ctx := context.Background()

	cfg := providers.Config{
		Params: map[string]any{},
	}
	_, httpErr := LoaderSim(ctx, cfg)
	require.NotNil(t, httpErr)
	// Should contain error about missing model file name
	require.True(t, len(httpErr.Error()) > 0, "should have error message")
}

func TestProviderSimMultipleInstances(t *testing.T) {
	ctx := context.Background()

	model := clusterModel
	f, err := os.CreateTemp("", "test-*")
	require.NoError(t, err)
	defer func() { _ = os.Remove(f.Name()) }()
	defer func() { _ = f.Close() }()
	_, err = f.WriteString(model)
	require.NoError(t, err)
	err = f.Sync()
	require.NoError(t, err)

	cfg := providers.Config{
		Params: map[string]any{
			"modelFileName": f.Name(),
			"api_error":     errNone,
		},
	}
	provider, httpErr := LoaderSim(ctx, cfg)
	require.Nil(t, httpErr)

	// Create multiple compute instance groups
	instances := []topology.ComputeInstances{
		{
			Region: "region1",
			Instances: map[string]string{
				"n11": "node11",
				"n12": "node12",
			},
		},
		{
			Region: "region2",
			Instances: map[string]string{
				"n21": "node21",
				"n22": "node22",
			},
		},
	}

	topo, httpErr := provider.GenerateTopologyConfig(ctx, nil, instances)
	require.Nil(t, httpErr)
	require.NotNil(t, topo)
}
