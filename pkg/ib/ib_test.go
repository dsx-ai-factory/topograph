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

package ib

import (
	"context"
	"fmt"
	"maps"
	"os"
	"reflect"
	"slices"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/dsx-ai-factory/topograph/pkg/engines/slurm"
	"github.com/dsx-ai-factory/topograph/pkg/topology"
)

func TestParseIbnetdiscoverFile(t *testing.T) {
	tests := []struct {
		name        string
		input       []byte
		expectedSw  map[string]*Switch
		expectedHCA map[string]string
		expectedErr bool
	}{
		{
			name:        "Empty file",
			input:       []byte(""),
			expectedSw:  map[string]*Switch{},
			expectedHCA: map[string]string{},
			expectedErr: false,
		},
		{
			name: "Single CA",
			input: []byte(`
Ca	1 "H-043f720300f4bc9e"		# "ngcprd10-luna3086 mlx5_3"
					`),
			expectedSw:  map[string]*Switch{},
			expectedHCA: map[string]string{"H-043f720300f4bc9e": "ngcprd10-luna3086"},
			expectedErr: false,
		},
		{
			name: "Single Switch",
			input: []byte(`
Switch  1   "Switch-1"         # "switch1"
					`),
			expectedSw: map[string]*Switch{
				"Switch-1": {
					ID:    "Switch-1",
					Name:  "switch1",
					Conn:  make(map[string]string),
					Nodes: make(map[string]string),
				},
			},
			expectedHCA: map[string]string{},
			expectedErr: false,
		},
		{
			name: "Switch with connections",
			input: []byte(`
Switch	41 "S-08c0eb03008cc87c"		# "MF0;IB-ComputeSpine-03:MQM8700/U1" enhanced port 0 lid 304 lmc 0
[1]	"S-08c0eb0300539a5c"[23]		# "MF0;IB-ComputeLeaf-101:MQM8700/U1" lid 1 4xHDR
[2]	"S-08c0eb0300539a9c"[23]		# "MF0;IB-ComputeLeaf-102:MQM8700/U1" lid 383 

`),
			expectedSw: map[string]*Switch{
				"S-08c0eb03008cc87c": {
					ID:    "S-08c0eb03008cc87c",
					Name:  "IB-ComputeSpine-03",
					Nodes: make(map[string]string),
					Conn: map[string]string{
						"S-08c0eb0300539a5c": "MF0;IB-ComputeLeaf-101:MQM8700/U1",
						"S-08c0eb0300539a9c": "MF0;IB-ComputeLeaf-102:MQM8700/U1",
					},
				},
			},
			expectedHCA: map[string]string{},
			expectedErr: false,
		},
		{
			name: "Multiple entries",
			input: []byte(`
Switch	41 "S-08c0eb03008cc87c"		# "MF0;IB-ComputeSpine-03:MQM8700/U1" enhanced port 0 lid 304 lmc 0
[1]	"S-08c0eb0300539a5c"[23]		# "MF0;IB-ComputeLeaf-101:MQM8700/U1" lid 1 4xHDR
[2]	"S-08c0eb0300539a9c"[23]		# "MF0;IB-ComputeLeaf-102:MQM8700/U1" lid 383 

Ca	1 "H-043f720300f4bc9e"		# "ngcprd10-luna3086 mlx5_3"

Switch	41 "S-b8cef603008032b8"		# "MF0;SJC3-A04-IB-ComputeLeaf-007:MQM8700/U1" enhanced port 0 lid 2382 lmc 0
[21]	"S-08c0eb03008ccc3c"[39]		# "MF0;IB-ComputeSpine-01:MQM8700/U1" lid 159 4xHDR
[22]	"S-08c0eb03008ccb5c"[39]		# "MF0;IB-ComputeSpine-02:MQM8700/U1" lid 230 4xHDR
[23]	"S-08c0eb03008cc87c"[39]		# "MF0;IB-ComputeSpine-03:MQM8700/U1" lid 304 4xHDR
			`),
			expectedSw: map[string]*Switch{
				"S-08c0eb03008cc87c": {
					ID:    "S-08c0eb03008cc87c",
					Name:  "IB-ComputeSpine-03",
					Nodes: make(map[string]string),
					Conn: map[string]string{
						"S-08c0eb0300539a5c": "MF0;IB-ComputeLeaf-101:MQM8700/U1",
						"S-08c0eb0300539a9c": "MF0;IB-ComputeLeaf-102:MQM8700/U1",
					},
				},
				"S-b8cef603008032b8": {
					ID:   "S-b8cef603008032b8",
					Name: "SJC3-A04-IB-ComputeLeaf-007",
					Conn: map[string]string{
						"S-08c0eb03008ccc3c": "MF0;IB-ComputeSpine-01:MQM8700/U1",
						"S-08c0eb03008ccb5c": "MF0;IB-ComputeSpine-02:MQM8700/U1",
						"S-08c0eb03008cc87c": "MF0;IB-ComputeSpine-03:MQM8700/U1",
					},
					Nodes: make(map[string]string),
				},
			},
			expectedHCA: map[string]string{"H-043f720300f4bc9e": "ngcprd10-luna3086"},
			expectedErr: false,
		},
		{
			name: "Invalid format",
			input: []byte(`
		Invalid entry
					`),
			expectedSw:  map[string]*Switch{},
			expectedHCA: map[string]string{},
			expectedErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			switches, hca, err := ParseIbnetdiscoverFile(tt.input)
			if (err != nil) != tt.expectedErr {
				t.Fatalf("expected error: %v, got: %v", tt.expectedErr, err)
			}

			for key, expectedSwitch := range tt.expectedSw {
				if actualSwitch, ok := switches[key]; ok {
					if !reflect.DeepEqual(*actualSwitch, *expectedSwitch) {
						t.Errorf("expected switch for key %v: %v, got: %v", key, expectedSwitch, actualSwitch)
					}
				} else {
					t.Errorf("expected switch with key %s not found", key)
				}
			}

			if !reflect.DeepEqual(hca, tt.expectedHCA) {
				t.Errorf("expected hca: %v, got: %v", tt.expectedHCA, hca)
			}
		})
	}
}

func TestBuildTree(t *testing.T) {
	// Simulate switches and HCAs
	switches := map[string]*Switch{
		"1": {
			ID: "1",
			Conn: map[string]string{
				"2": "Switch2",
				"3": "Switch3",
			},
			Nodes: make(map[string]string),
		},
		"2": {
			ID: "2",
			Conn: map[string]string{
				"hca1": "HCA1",
				"hca2": "HCA2",
			},
			Nodes: make(map[string]string),
		},
		"3": {
			ID: "3",
			Conn: map[string]string{
				"hca3": "HCA3",
			},
			Nodes: make(map[string]string),
		},
	}

	hca := map[string]string{
		"hca1": "HCA1",
		"hca2": "HCA2",
		"hca3": "HCA3",
	}

	node1 := &topology.Vertex{ID: "HCA1", Name: "HCA1"}
	node2 := &topology.Vertex{ID: "HCA2", Name: "HCA2"}
	node3 := &topology.Vertex{ID: "HCA3", Name: "HCA3"}
	sw2 := &topology.Vertex{ID: "2", Vertices: map[string]*topology.Vertex{"HCA1": node1, "HCA2": node2}}
	sw3 := &topology.Vertex{ID: "3", Vertices: map[string]*topology.Vertex{"HCA3": node3}}
	sw1 := &topology.Vertex{ID: "1", Vertices: map[string]*topology.Vertex{"2": sw2, "3": sw3}}

	nodesInCluster := map[string]bool{
		"HCA1": true,
		"HCA2": true,
		"HCA3": true,
	}

	tree := buildTree(switches, hca, nodesInCluster)
	expected := map[string]*topology.Vertex{"1": sw1}
	require.Equal(t, expected, tree)
}

func TestGenerateTopologyConfigValid(t *testing.T) {
	data, err := os.ReadFile("../../tests/output/ibnetdiscover/example.out")
	require.NoError(t, err)

	instances := []topology.ComputeInstances{
		{
			Region: "local",
			Instances: map[string]string{
				"a05-p1-dgx-01-c03": "a05-p1-dgx-01-c03",
				"a05-p1-dgx-01-c04": "a05-p1-dgx-01-c04",
				"a06-p1-dgx-02-c01": "a06-p1-dgx-02-c01",
				"a06-p1-dgx-02-c02": "a06-p1-dgx-02-c02",
				"a06-p1-dgx-02-c03": "a06-p1-dgx-02-c03",
				"a06-p1-dgx-02-c04": "a06-p1-dgx-02-c04",
				"a06-p1-dgx-02-c05": "a06-p1-dgx-02-c05",
				"a06-p1-dgx-02-c06": "a06-p1-dgx-02-c06",
				"a06-p1-dgx-02-c07": "a06-p1-dgx-02-c07",
				"a06-p1-dgx-02-c08": "a06-p1-dgx-02-c08",
				"a06-p1-dgx-02-c09": "a06-p1-dgx-02-c09",
				"a06-p1-dgx-02-c10": "a06-p1-dgx-02-c10",
				"a06-p1-dgx-02-c11": "a06-p1-dgx-02-c11",
				"a06-p1-dgx-02-c12": "a06-p1-dgx-02-c12",
				"a06-p1-dgx-02-c13": "a06-p1-dgx-02-c13",
				"a06-p1-dgx-02-c14": "a06-p1-dgx-02-c14",
				"a06-p1-dgx-02-c15": "a06-p1-dgx-02-c15",
				"a06-p1-dgx-02-c16": "a06-p1-dgx-02-c16",
				"a06-p1-dgx-02-c17": "a06-p1-dgx-02-c17",
				"a06-p1-dgx-02-c18": "a06-p1-dgx-02-c18",
				"b05-p1-dgx-05-c01": "b05-p1-dgx-05-c01",
				"b05-p1-dgx-05-c02": "b05-p1-dgx-05-c02",
				"b05-p1-dgx-05-c03": "b05-p1-dgx-05-c03",
				"b05-p1-dgx-05-c04": "b05-p1-dgx-05-c04",
				"b05-p1-dgx-05-c05": "b05-p1-dgx-05-c05",
				"b05-p1-dgx-05-c06": "b05-p1-dgx-05-c06",
				"b05-p1-dgx-05-c07": "b05-p1-dgx-05-c07",
				"b05-p1-dgx-05-c08": "b05-p1-dgx-05-c08",
				"b05-p1-dgx-05-c09": "b05-p1-dgx-05-c09",
				"b05-p1-dgx-05-c10": "b05-p1-dgx-05-c10",
				"b05-p1-dgx-05-c11": "b05-p1-dgx-05-c11",
				"b05-p1-dgx-05-c12": "b05-p1-dgx-05-c12",
				"b05-p1-dgx-05-c13": "b05-p1-dgx-05-c13",
				"b05-p1-dgx-05-c14": "b05-p1-dgx-05-c14",
				"b05-p1-dgx-05-c15": "b05-p1-dgx-05-c15",
				"b05-p1-dgx-05-c16": "b05-p1-dgx-05-c16",
				"b05-p1-dgx-05-c17": "b05-p1-dgx-05-c17",
				"b05-p1-dgx-05-c18": "b05-p1-dgx-05-c18",
			},
		},
	}

	forest, _, err := GenerateTopologyConfig(data, instances)
	require.NoError(t, err)

	root := &topology.Vertex{Vertices: make(map[string]*topology.Vertex)}
	for _, v := range forest {
		root.Vertices[v.ID] = v
	}

	tree := &topology.Graph{Tiers: root}

	data, err = slurm.GenerateOutputParams(context.TODO(), tree, &slurm.Params{})
	require.Nil(t, err)

	expected := `SwitchName=S-2c5eab0300b87b40 Switches=S-2c5eab0300c25f00
SwitchName=S-2c5eab0300c25f00 Switches=S-2c5eab0300b879c0,S-2c5eab0300b87a80,S-2c5eab0300c26040
SwitchName=S-2c5eab0300b879c0 Nodes=a05-p1-dgx-01-c[03-04]
SwitchName=S-2c5eab0300b87a80 Nodes=a06-p1-dgx-02-c[01-04,07,10-12,14,16,18]
SwitchName=S-2c5eab0300c26040 Nodes=b05-p1-dgx-05-c[01-18]
`
	require.Equal(t, expected, string(data))
}

func TestGenerateTopologyConfigInvalid(t *testing.T) {
	data, err := os.ReadFile("../../tests/output/ibnetdiscover/example-bad.out")
	require.NoError(t, err)

	instances := []topology.ComputeInstances{
		{
			Region: "local",
			Instances: map[string]string{
				"dgx-gb200-n01-c1": "dgx-gb200-n01-c1",
			},
		},
	}

	forest, _, err := GenerateTopologyConfig(data, instances)
	require.NoError(t, err)

	root := &topology.Vertex{Vertices: make(map[string]*topology.Vertex)}
	for _, v := range forest {
		root.Vertices[v.ID] = v
	}

	tree := &topology.Graph{Tiers: root}

	data, err = slurm.GenerateOutputParams(context.TODO(), tree, &slurm.Params{})
	require.Nil(t, err)

	expected := ""
	require.Equal(t, expected, string(data))
}

func TestResolveNodeNames(t *testing.T) {
	nodes := map[string]bool{
		"node-1.example.com": true,
		"node-2":             true,
		"node-3.rack-a.lab":  true,
		"node-3.rack-b.lab":  true,
		"10.0.0.4":           true,
		"node-5":             true,
		"node-6.a.lab":       true,
		"node-7":             true,
		"node-8":             true,
	}
	hca := map[string]string{
		"H-1":  "node-1",             // short HCA name, FQDN node
		"H-2":  "node-2",             // exact match
		"H-3":  "node-5.example.com", // FQDN HCA name, short node
		"H-4":  "node-3",             // ambiguous: two nodes share the short name
		"H-5":  "10",                 // IP addresses are never shortened
		"H-6":  "other",              // not in the cluster
		"H-7":  "node-6.b.lab",       // two different FQDNs never match
		"H-8":  "node-2.other.lab",   // node-2 is reported exactly by H-2
		"H-9":  "node-7.a.lab",       // two HCAs report different FQDNs
		"H-10": "node-7.b.lab",       // for the same short node
		"H-11": "node-8.example.com", // one host, two HCAs
		"H-12": "node-8.example.com",
	}

	expected := map[string]string{
		"H-1":  "node-1.example.com",
		"H-2":  "node-2",
		"H-3":  "node-5",
		"H-4":  "node-3",
		"H-5":  "10",
		"H-6":  "other",
		"H-7":  "node-6.b.lab",
		"H-8":  "node-2.other.lab",
		"H-9":  "node-7.a.lab",
		"H-10": "node-7.b.lab",
		"H-11": "node-8",
		"H-12": "node-8",
	}
	require.Equal(t, expected, resolveNodeNames(hca, nodes))
}

func TestGenerateTopologyConfigFQDNNodes(t *testing.T) {
	data, err := os.ReadFile("../../tests/output/ibnetdiscover/example.out")
	require.NoError(t, err)

	instances := map[string]string{}
	expected := map[string]*topology.Vertex{}
	for i := 1; i <= 18; i++ {
		name := fmt.Sprintf("b05-p1-dgx-05-c%02d.example.com", i)
		instances[name] = name
		expected[name] = &topology.Vertex{ID: name, Name: name}
	}

	forest, hca, err := GenerateTopologyConfig(data, []topology.ComputeInstances{{Region: "local", Instances: instances}})
	require.NoError(t, err)
	require.Contains(t, slices.Collect(maps.Values(hca)), "b05-p1-dgx-05-c01.example.com")

	var leaf *topology.Vertex
	var find func(v *topology.Vertex)
	find = func(v *topology.Vertex) {
		if v.ID == "S-2c5eab0300c26040" {
			leaf = v
		}
		for _, child := range v.Vertices {
			find(child)
		}
	}
	for _, v := range forest {
		find(v)
	}
	require.NotNil(t, leaf)
	require.Equal(t, expected, leaf.Vertices)
}

// Two leaf switches under one spine. Leaf A has HCA H-a and host node-x,
// leaf B has HCA H-b and host node-y; the extra hosts keep the merger from
// folding the two leaves together.
const twoLeafFabric = `
Switch	2 "S-spine"		# "MF0;spine:MQM8700/U1" enhanced port 0 lid 1 lmc 0
[1]	"S-leaf-a"[1]		# "MF0;leaf-a:MQM8700/U1" lid 2 4xHDR
[2]	"S-leaf-b"[1]		# "MF0;leaf-b:MQM8700/U1" lid 3 4xHDR

Switch	2 "S-leaf-a"		# "MF0;leaf-a:MQM8700/U1" enhanced port 0 lid 2 lmc 0
[1]	"S-spine"[1]		# "MF0;spine:MQM8700/U1" lid 1 4xHDR
[2]	"H-a"[1]		# "%s mlx5_0" lid 4 4xHDR
[3]	"H-x"[1]		# "node-x mlx5_0" lid 6 4xHDR

Switch	2 "S-leaf-b"		# "MF0;leaf-b:MQM8700/U1" enhanced port 0 lid 3 lmc 0
[1]	"S-spine"[2]		# "MF0;spine:MQM8700/U1" lid 1 4xHDR
[2]	"H-b"[1]		# "%s mlx5_0" lid 5 4xHDR
[3]	"H-y"[1]		# "node-y mlx5_0" lid 7 4xHDR

Ca	1 "H-a"		# "%s mlx5_0"
[1](a)	"S-leaf-a"[2]		# "MF0;leaf-a:MQM8700/U1" lid 2 4xHDR

Ca	1 "H-b"		# "%s mlx5_0"
[1](b)	"S-leaf-b"[2]		# "MF0;leaf-b:MQM8700/U1" lid 3 4xHDR

Ca	1 "H-x"		# "node-x mlx5_0"
[1](c)	"S-leaf-a"[3]		# "MF0;leaf-a:MQM8700/U1" lid 2 4xHDR

Ca	1 "H-y"		# "node-y mlx5_0"
[1](d)	"S-leaf-b"[3]		# "MF0;leaf-b:MQM8700/U1" lid 3 4xHDR
`

// leavesOf returns, for each cluster node in the forest, the IDs of the leaf
// switches it sits under.
func leavesOf(forest []*topology.Vertex) map[string][]string {
	leaves := map[string][]string{}
	var walk func(v *topology.Vertex)
	walk = func(v *topology.Vertex) {
		for id, child := range v.Vertices {
			if len(child.Vertices) == 0 && child.Name != "" {
				leaves[id] = append(leaves[id], v.ID)
				continue
			}
			walk(child)
		}
	}
	for _, v := range forest {
		walk(v)
	}
	for _, l := range leaves {
		slices.Sort(l)
	}
	return leaves
}

func generateTwoLeaf(t *testing.T, nameA, nameB string, nodes ...string) map[string][]string {
	t.Helper()
	data := fmt.Sprintf(twoLeafFabric, nameA, nameB, nameA, nameB)
	instances := map[string]string{"node-x": "node-x", "node-y": "node-y"}
	for _, n := range nodes {
		instances[n] = n
	}
	forest, _, err := GenerateTopologyConfig([]byte(data), []topology.ComputeInstances{{Region: "local", Instances: instances}})
	require.NoError(t, err)
	return leavesOf(forest)
}

// Two different FQDNs that share a first label are different hosts.
func TestGenerateTopologyConfigDifferentFQDNs(t *testing.T) {
	leaves := generateTwoLeaf(t, "gpu-01.a.lab", "gpu-01.b.lab", "gpu-01.a.lab")
	require.Equal(t, map[string][]string{"gpu-01.a.lab": {"S-leaf-a"}, "node-x": {"S-leaf-a"}, "node-y": {"S-leaf-b"}}, leaves)
}

// An HCA whose host name matches a node exactly wins over a fallback match
// from an HCA that reports a different host name.
func TestGenerateTopologyConfigExactMatchWins(t *testing.T) {
	leaves := generateTwoLeaf(t, "gpu-01", "gpu-01.other.lab", "gpu-01")
	require.Equal(t, map[string][]string{"gpu-01": {"S-leaf-a"}, "node-x": {"S-leaf-a"}, "node-y": {"S-leaf-b"}}, leaves)
}

// Several HCAs on one host that report the same name may sit under
// different leaves (rail-optimized fabrics).
func TestGenerateTopologyConfigSameHostTwoHCAs(t *testing.T) {
	leaves := generateTwoLeaf(t, "gpu-01", "gpu-01", "gpu-01.example.com")
	require.Equal(t, map[string][]string{"gpu-01.example.com": {"S-leaf-a", "S-leaf-b"}, "node-x": {"S-leaf-a"}, "node-y": {"S-leaf-b"}}, leaves)
}
