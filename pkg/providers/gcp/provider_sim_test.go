/*
 * Copyright 2024-2026 NVIDIA CORPORATION
 * SPDX-License-Identifier: Apache-2.0
 */

package gcp

import (
	"context"
	"os"
	"testing"

	"github.com/agrea/ptr"
	"github.com/dsx-ai-factory/topograph/pkg/engines/slurm"
	"github.com/dsx-ai-factory/topograph/pkg/providers"
	"github.com/dsx-ai-factory/topograph/pkg/topology"
	"github.com/stretchr/testify/require"
)

const (
	ignoreErrMsg = "_IGNORE_"

	nodeModel = `
switches:
  core:
    switches: [spine]
  spine:
    switches: [tor]
  tor: {}
blocks:
- switch: tor
  nodes: ["11"]
  annotations:
    accelerator.topology.test/domain: nvl1
`

	clusterModel = `
switches:
  core:
    switches: [spine]
  spine:
    switches: [tor1,tor2]
  tor1: {}
  tor2: {}
blocks:
- switch: tor1
  nodes: ["11","12"]
  annotations:
    accelerator.topology.test/domain: nvl1
- switch: tor2
  nodes: ["21","22"]
  annotations:
    accelerator.topology.test/domain: nvl2
`
)

func TestProviderSim(t *testing.T) {
	ctx := context.Background()

	testCases := []struct {
		name      string
		model     string
		pageSize  *int
		instances []topology.ComputeInstances
		params    map[string]any
		apiErr    int
		topology  string
		err       string
	}{
		{
			name:  "Case 1: bad model",
			model: `bad: model: error:`,
			err:   ignoreErrMsg,
		},

		{
			name:  "Case 2: no ComputeInstances",
			model: clusterModel,
		},
		{
			name:  "Case 3.1: ClientFactory API error",
			model: nodeModel,
			instances: []topology.ComputeInstances{
				{
					Region:    "region",
					Instances: map[string]string{"11": "node11"},
				},
			},
			apiErr: errClientFactory,
			err:    "failed to get client: API error",
		},
		{
			name:  "Case 3.2: Instances API error",
			model: nodeModel,
			instances: []topology.ComputeInstances{
				{
					Region:    "region",
					Instances: map[string]string{"11": "node11"},
				},
			},
			apiErr: errInstances,
			err:    "API error",
		},
		{
			name: "Case 3.3: unsupported instance ID",
			model: `
switches:
  core:
    switches: [spine]
  spine:
    switches: [tor]
  tor: {}
blocks:
- switch: tor
  nodes: [n11]
  annotations:
    accelerator.topology.test/domain: nvl1
`,
			instances: []topology.ComputeInstances{
				{
					Region:    "region",
					Instances: map[string]string{"11": "node11"},
				},
			},
			err: `invalid instance ID "n11"; must be numerical`,
		},
		{
			name:  "Case 4: missing region",
			model: clusterModel,
			instances: []topology.ComputeInstances{
				{
					Instances: map[string]string{"11": "node11", "12": "nodeCPU"},
				},
			},
			err: "must specify region",
		},
		{
			name:  "Case 5: valid single cluster",
			model: nodeModel,
			instances: []topology.ComputeInstances{
				{
					Region:    "region",
					Instances: map[string]string{"11": "node11", "12": "nodeCPU"},
				},
			},
			topology: `SwitchName=core Switches=spine
SwitchName=no-topology Nodes=nodeCPU
SwitchName=spine Switches=tor
SwitchName=tor Nodes=node11
`,
		},
		{
			name:  "Case 6: valid cluster, no pagination",
			model: clusterModel,
			instances: []topology.ComputeInstances{
				{
					Region:    "region",
					Instances: map[string]string{"11": "node11", "12": "node12", "21": "node21", "22": "node22"},
				},
			},
			topology: `SwitchName=core Switches=spine
SwitchName=spine Switches=tor[1-2]
SwitchName=tor1 Nodes=node[11-12]
SwitchName=tor2 Nodes=node[21-22]
`,
		},
		{
			name:     "Case 7: valid cluster, pagination",
			model:    clusterModel,
			pageSize: ptr.Int(3),
			instances: []topology.ComputeInstances{
				{
					Region:    "region",
					Instances: map[string]string{"11": "node11", "12": "node12", "21": "node21", "22": "node22", "31": "node31"},
				},
			},
			topology: `SwitchName=core Switches=spine
SwitchName=no-topology Nodes=node31
SwitchName=spine Switches=tor[1-2]
SwitchName=tor1 Nodes=node[11-12]
SwitchName=tor2 Nodes=node[21-22]
`,
		},
		{
			name:   "Case 8: valid cluster in block format",
			model:  clusterModel,
			params: map[string]any{"plugin": "topology/block"},
			instances: []topology.ComputeInstances{
				{
					Region:    "region",
					Instances: map[string]string{"11": "node11", "12": "node12", "21": "node21", "22": "node22", "31": "node31"},
				},
			},
			topology: `# block001=tor1
BlockName=block001 Nodes=node[11-12]
# block002=tor2
BlockName=block002 Nodes=node[21-22]
BlockSizes=2,4
`,
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
			if len(tc.err) != 0 {
				require.EqualError(t, httpErr, tc.err)
			} else {
				require.Nil(t, httpErr)
				data, httpErr := slurm.GenerateOutput(ctx, topo, tc.params)
				require.Nil(t, httpErr)
				require.Equal(t, tc.topology, string(data))
			}
		})
	}
}

// TestProviderSimNodeFewerThanThreeNetLayers verifies that a model node with
// fewer than 3 network layers (e.g. a single-level switch, giving NetLayers
// length 1) does not cause an index-out-of-range panic in simClient.Instances.
// The instance is included in the result without PhysicalHostTopology, which
// generateRegionInstanceTopology already handles gracefully via a nil-check.
func TestProviderSimNodeFewerThanThreeNetLayers(t *testing.T) {
	const singleSwitchModel = `
switches:
  sw1: {}
blocks:
- switch: sw1
  nodes: ["11"]
`
	f, err := os.CreateTemp("", "test-single-switch-*")
	require.NoError(t, err)
	defer func() { _ = os.Remove(f.Name()) }()
	defer func() { _ = f.Close() }()
	_, err = f.WriteString(singleSwitchModel)
	require.NoError(t, err)
	require.NoError(t, f.Sync())

	cfg := providers.Config{
		Params: map[string]any{"modelFileName": f.Name()},
	}
	provider, httpErr := LoaderSim(context.Background(), cfg)
	require.Nil(t, httpErr)

	cis := []topology.ComputeInstances{
		{Region: "us-central1-a", Instances: map[string]string{"11": "node11"}},
	}
	// Must not panic; the node has only 1 NetLayer so ResourceStatus is omitted.
	_, httpErr = provider.GenerateTopologyConfig(context.Background(), nil, cis)
	require.Nil(t, httpErr)
}
