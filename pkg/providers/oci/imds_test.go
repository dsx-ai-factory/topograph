// SPDX-FileCopyrightText: Copyright (c) 2024 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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

package oci

import (
	"bytes"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseTopologyOutput(t *testing.T) {
	testCases := []struct {
		name   string
		input  string
		output map[string]*topologyData
		err    string
	}{
		{
			name: "Case 1: with fabric",
			input: `node1: { "customerGpuMemoryFabric": "fab1", "customerHPCIslandId": "hpc1", "customerNetworkBlock": "net1", "customerLocalBlock": "loc1" }
node2: { "customerGpuMemoryFabric": "fab2", "customerHPCIslandId": "hpc2", "customerNetworkBlock": "net2", "customerLocalBlock": "loc2" }
node3: { "customerGpuMemoryFabric": "fab3", "customerHPCIslandId": "hpc3", "customerNetworkBlock": "net3", "customerLocalBlock": "loc3" }
node4: { "customerGpuMemoryFabric": "fab4", "customerHPCIslandId": "hpc4", "customerNetworkBlock": "net4", "customerLocalBlock": "loc4" }
`,
			output: map[string]*topologyData{
				"node1": {GpuMemoryFabric: "fab1", HPCIslandId: "hpc1", NetworkBlock: "net1", LocalBlock: "loc1"},
				"node2": {GpuMemoryFabric: "fab2", HPCIslandId: "hpc2", NetworkBlock: "net2", LocalBlock: "loc2"},
				"node3": {GpuMemoryFabric: "fab3", HPCIslandId: "hpc3", NetworkBlock: "net3", LocalBlock: "loc3"},
				"node4": {GpuMemoryFabric: "fab4", HPCIslandId: "hpc4", NetworkBlock: "net4", LocalBlock: "loc4"},
			},
		},
		{
			name: "Case 2: without fabric",
			input: `node1: { "customerHPCIslandId": "hpc1", "customerNetworkBlock": "net1", "customerLocalBlock": "loc1" }
node2: { "customerHPCIslandId": "hpc2", "customerNetworkBlock": "net2", "customerLocalBlock": "loc2" }
node3: { "customerHPCIslandId": "hpc3", "customerNetworkBlock": "net3", "customerLocalBlock": "loc3" }
node4: { "customerHPCIslandId": "hpc4", "customerNetworkBlock": "net4", "customerLocalBlock": "loc4" }
`,
			output: map[string]*topologyData{
				"node1": {GpuMemoryFabric: "", HPCIslandId: "hpc1", NetworkBlock: "net1", LocalBlock: "loc1"},
				"node2": {GpuMemoryFabric: "", HPCIslandId: "hpc2", NetworkBlock: "net2", LocalBlock: "loc2"},
				"node3": {GpuMemoryFabric: "", HPCIslandId: "hpc3", NetworkBlock: "net3", LocalBlock: "loc3"},
				"node4": {GpuMemoryFabric: "", HPCIslandId: "hpc4", NetworkBlock: "net4", LocalBlock: "loc4"},
			},
		},
		{
			name: "Case 3: parsing error",
			input: `node1: { "customerHPCIslandId": 123, "customerNetworkBlock": "net1", "customerLocalBlock": "loc1" }
`,
			output: map[string]*topologyData{},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			data, err := parseTopologyOutput(bytes.NewBufferString(tc.input))
			require.NoError(t, err)
			require.Equal(t, tc.output, data)
		})
	}
}

func TestPdshCmd(t *testing.T) {
	expected := fmt.Sprintf(`echo $(curl -s -H "Authorization: Bearer Oracle" -L %s)`, IMDSInstanceURL)
	require.Equal(t, expected, pdshCmd(IMDSInstanceURL))
}
