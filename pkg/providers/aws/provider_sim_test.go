/*
 * Copyright (c) 2024, NVIDIA CORPORATION.  All rights reserved.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package aws

import (
	"context"
	"fmt"
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
    labels:
      topology.kubernetes.io/zone: az1
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

	largeClusterModel = `
switches:
  core:
    switches: [spine]
  spine:
    labels:
      topology.kubernetes.io/zone: az1
    switches: [tor1,tor2]
  tor1: {}
  tor2: {}
blocks:
- switch: tor1
  nodes: ["n[100-199]"]
  annotations:
    accelerator.topology.test/domain: nvl1
- switch: tor2
  nodes: ["n[200-299]"]
  annotations:
    accelerator.topology.test/domain: nvl2
`
)

func TestProviderSim(t *testing.T) {
	ctx := context.Background()

	type interval struct{ from, to int }

	testCases := []struct {
		name      string
		model     string
		region    string
		intervals []interval
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
			name: "Case 2: no ComputeInstances",
			model: `
switches:
  core:
    switches: [spine]
  spine:
    switches: [tor]
  tor: {}
blocks:
- switch: tor
  nodes: [n11,n12]
  annotations:
    accelerator.topology.test/domain: nvl1
`,
		},
		{
			name:      "Case 3.1: ClientFactory API error",
			model:     clusterModel,
			region:    "region",
			intervals: []interval{{11, 12}},
			apiErr:    errClientFactory,
			err:       "failed to get client: API error",
		},
		{
			name:      "Case 3.2: DescribeInstanceTopology API error",
			model:     clusterModel,
			region:    "region",
			intervals: []interval{{11, 12}},
			apiErr:    errDescribeInstanceTopology,
			err:       "failed to describe instance topology: API error",
		},
		{
			name:      "Case 4: missing region",
			model:     clusterModel,
			intervals: []interval{{11, 12}},
			err:       `must specify region`,
		},
		{
			name: "Case 5.1: missing availability zone",
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
			region:    "region",
			intervals: []interval{{11, 11}},
			err:       `failed to describe instance topology: availability zone not found for instance "n11" in AWS simulation`,
		},
		{
			name:      "Case 6: valid cluster in tree format",
			model:     clusterModel,
			region:    "region",
			intervals: []interval{{11, 13}, {21, 23}},
			topology: `SwitchName=core Switches=spine
SwitchName=no-topology Nodes=node[13,23]
SwitchName=spine Switches=tor[1-2]
SwitchName=tor1 Nodes=node[11-12]
SwitchName=tor2 Nodes=node[21-22]
`,
		},
		{
			name:      "Case 7: valid cluster in block format",
			model:     clusterModel,
			region:    "region",
			intervals: []interval{{11, 12}, {21, 22}, {31, 32}},
			params:    map[string]any{"plugin": "topology/block"},
			topology: `# block001=nvl1
BlockName=block001 Nodes=node[11-12]
# block002=nvl2
BlockName=block002 Nodes=node[21-22]
BlockSizes=2,4
`,
		},
		{
			name:      "Case 8: valid large cluster in block format",
			model:     largeClusterModel,
			region:    "region",
			intervals: []interval{{101, 164}, {201, 264}},
			pageSize:  ptr.Int(25),
			params:    map[string]any{"plugin": "topology/block"},
			topology: `# block001=nvl1
BlockName=block001 Nodes=node[101-164]
# block002=nvl2
BlockName=block002 Nodes=node[201-264]
BlockSizes=64,128
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

			var instances []topology.ComputeInstances
			if len(tc.intervals) != 0 {
				instances = []topology.ComputeInstances{
					{
						Region:    tc.region,
						Instances: make(map[string]string),
					},
				}
				for _, item := range tc.intervals {
					for i := item.from; i <= item.to; i++ {
						instances[0].Instances[fmt.Sprintf("n%d", i)] = fmt.Sprintf("node%d", i)
					}
				}
			}
			topo, httpErr := provider.GenerateTopologyConfig(ctx, tc.pageSize, instances)
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
