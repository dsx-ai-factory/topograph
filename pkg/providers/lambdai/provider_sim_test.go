/*
 * Copyright 2026, NVIDIA CORPORATION
 * SPDX-License-Identifier: Apache-2.0
 */

package lambdai

import (
	"context"
	"os"
	"testing"

	"github.com/agrea/ptr"
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
    switches: [tor1,tor2]
  tor1: {}
  tor2: {}
blocks:
- switch: tor1
  nodes: [n11,n12]
  annotations:
    accelerator.topology.test/domain: nvl1
- switch: tor2
  nodes: [n21,n22]
  annotations:
    accelerator.topology.test/domain: nvl2
`
)

func TestProviderSim(t *testing.T) {
	ctx := context.Background()

	testCases := []struct {
		name      string
		model     string
		region    string
		instances []topology.ComputeInstances
		pageSize  *int
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
			name:  "Case 3: missing region",
			model: clusterModel,
			instances: []topology.ComputeInstances{
				{
					Instances: map[string]string{"n11": "node11"},
				},
			},
			err: `must specify region`,
		},
		{
			name:  "Case 4.1: ClientFactory API error",
			model: clusterModel,
			instances: []topology.ComputeInstances{
				{
					Region:    "region",
					Instances: map[string]string{"n11": "node11"},
				},
			},
			apiErr: errClientFactory,
			err:    `failed to create API client: API error`,
		},
		{
			name:  "Case 4.2: InstanceList API error",
			model: clusterModel,
			instances: []topology.ComputeInstances{
				{
					Region:    "region",
					Instances: map[string]string{"n11": "node11"},
				},
			},
			apiErr: errInstanceList,
			err:    `failed to get instance list: API error`,
		},
		{
			name:   "Case 5: valid cluster in tree format without paging",
			model:  clusterModel,
			region: "region",
			instances: []topology.ComputeInstances{
				{
					Region:    "region",
					Instances: map[string]string{"n11": "node11", "n12": "node12", "n21": "node21", "n22": "node22", "n31": "node31"},
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
			name:   "Case 6: valid cluster in block format with paging",
			model:  clusterModel,
			region: "region",
			instances: []topology.ComputeInstances{
				{
					Region:    "region",
					Instances: map[string]string{"n11": "node11", "n12": "node12", "n21": "node21", "n22": "node22", "n31": "node31"},
				},
			},
			pageSize: ptr.Int(2),
			params:   map[string]any{"plugin": "topology/block"},
			topology: `# block001=nvl1.simulation
BlockName=block001 Nodes=node[11-12]
# block002=nvl2.simulation
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
