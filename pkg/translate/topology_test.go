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

package translate

import (
	"bytes"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/dsx-ai-factory/topograph/pkg/topology"
)

const (
	testTreeConfig = `SwitchName=S1 Switches=S[2-3]
SwitchName=S2 Nodes=Node[201-202,205]
SwitchName=S3 Nodes=Node[304-306]
`

	testBlockConfig1_1 = `# block001=B1
BlockName=block001 Nodes=Node[104-106]
# block002=B2
BlockName=block002 Nodes=Node[201-202,205]
BlockSizes=3
`

	testBlockConfig1_2 = `# block001=B1
BlockName=block001 Nodes=Node[104-106]
# block002=B2
BlockName=block002 Nodes=Node[201-202,205]
# block003=B3
BlockName=block003 Nodes=Node[304-306]
# block004=B4
BlockName=block004 Nodes=Node[401-403]
BlockSizes=3
`

	testBlockConfigDiffNumNodes = `# block001=B1
BlockName=block001 Nodes=Node[104-106]
# block002=B2
BlockName=block002 Nodes=Node[201-202,205-206]
BlockSizes=3,6
`

	testBlockConfig2 = `# block001=B1
BlockName=block001 Nodes=Node[104-106]
# block002=B2
BlockName=block002 Nodes=Node[201-202,205]
# block003=B3
BlockName=block003 Nodes=Node[301-303]
# block004=B4
BlockName=block004 Nodes=Node[401-403]
BlockSizes=3
`

	testBlockConfigDFS = `# block001=B1
BlockName=block001 Nodes=Node202
# block002=B2
BlockName=block002 Nodes=Node104
# block003=B2
BlockName=block003 Nodes=Node105
# block004=B3
BlockName=block004 Nodes=Node205
BlockSizes=1
`

	shortNameExpectedResult = `# switch.3.1=hpcislandid-1
SwitchName=switch.3.1 Switches=switch.2.[1-2]
# switch.2.1=network-block-1
SwitchName=switch.2.1 Switches=switch.1.1
# switch.2.2=network-block-2
SwitchName=switch.2.2 Switches=switch.1.2
# switch.1.1=local-block-1
SwitchName=switch.1.1 Nodes=node-1
# switch.1.2=local-block-2
SwitchName=switch.1.2 Nodes=node-2
`
)

func TestValidateConfig(t *testing.T) {
	emptyRoot := &topology.Graph{}
	emptyBlockRoot := &topology.Graph{Domains: topology.NewDomainMap()}
	domains := topology.NewDomainMap()
	domains.AddHost("domain-1", "instance-1", "node-1")
	blockRoot := &topology.Graph{Domains: domains}

	testCases := []struct {
		name string
		root *topology.Graph
		cfg  *Config
		err  string
	}{
		{
			name: "Case 1: empty config",
			root: emptyRoot,
			cfg:  &Config{},
			err:  `unsupported topology plugin ""`,
		},
		{
			name: "Case 2: missing tree root",
			root: emptyRoot,
			cfg: &Config{
				Plugin: topology.TopologyTree,
			},
			err: "missing tree topology",
		},
		{
			name: "Case 3: missing block root",
			root: emptyRoot,
			cfg: &Config{
				Plugin: topology.TopologyBlock,
			},
			err: "missing block topology",
		},
		{
			name: "Case 3.1: nil block root",
			cfg: &Config{
				Plugin: topology.TopologyBlock,
			},
			err: "missing block topology",
		},
		{
			name: "Case 3.2: empty block root",
			root: emptyBlockRoot,
			cfg: &Config{
				Plugin: topology.TopologyBlock,
			},
			err: "block topology contains no accelerator domains",
		},
		{
			name: "Case 4: mutually exclusive parameters",
			root: emptyRoot,
			cfg: &Config{
				Plugin:     topology.TopologyTree,
				Topologies: map[string]*TopologySpec{"topo": nil},
			},
			err: "plugin and topologies parameters are mutually exclusive",
		},
		{
			name: "Case 5: missing plugin in topology spec",
			root: emptyRoot,
			cfg: &Config{
				Topologies: map[string]*TopologySpec{
					"topo": {}},
			},
			err: `unsupported topology plugin "" for topology "topo"`,
		},
		{
			name: "Case 6: missing tree root in topology spec",
			root: emptyRoot,
			cfg: &Config{
				Topologies: map[string]*TopologySpec{
					"topo": {Plugin: topology.TopologyTree}},
			},
			err: `missing tree topology for topology "topo"`,
		},
		{
			name: "Case 7: missing block root in topology spec",
			root: emptyRoot,
			cfg: &Config{
				Topologies: map[string]*TopologySpec{
					"topo": {Plugin: topology.TopologyBlock}},
			},
			err: `missing block topology for topology "topo"`,
		},
		{
			name: "Case 7.1: nil block root in topology spec",
			cfg: &Config{
				Topologies: map[string]*TopologySpec{
					"topo": {Plugin: topology.TopologyBlock}},
			},
			err: `missing block topology for topology "topo"`,
		},
		{
			name: "Case 7.2: empty block root in topology spec",
			root: emptyBlockRoot,
			cfg: &Config{
				Topologies: map[string]*TopologySpec{
					"topo": {
						Plugin: topology.TopologyBlock,
						Nodes:  []string{"node-1"},
					},
				},
			},
			err: `block topology for topology "topo" contains no accelerator domains`,
		},
		{
			name: "Case 8: missing nodes in topology spec",
			root: blockRoot,
			cfg: &Config{
				Topologies: map[string]*TopologySpec{
					"topo": {Plugin: topology.TopologyBlock}},
			},
			err: `topology "topo" specifies no nodes`,
		},
		{
			name: "Case 8: missing nodes in topology spec",
			root: emptyRoot,
			cfg: &Config{
				Topologies: map[string]*TopologySpec{
					"topo": {Plugin: topology.TopologyFlat}},
			},
		},
		{
			name: "Case 9: block name with cluster-wide tree plugin",
			root: emptyRoot,
			cfg: &Config{
				Plugin: topology.TopologyTree,
				BlockName: &BlockNameConfig{
					NodeNameRegexp: `^node([0-9]+)$`,
					Format:         `block${1}`,
				},
			},
			err: `blockName is only supported with plugin "topology/block"`,
		},
		{
			name: "Case 10: block name with per-topology tree plugin",
			root: emptyRoot,
			cfg: &Config{
				Topologies: map[string]*TopologySpec{
					"topo": {
						Plugin: topology.TopologyTree,
						BlockName: &BlockNameConfig{
							NodeNameRegexp: `^node([0-9]+)$`,
							Format:         `block${1}`,
						},
						Nodes: []string{"node001"},
					},
				},
			},
			err: `topology "topo": blockName is only supported with plugin "topology/block"`,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewNetworkTopology(tc.root, tc.cfg)
			if len(tc.err) != 0 {
				require.EqualError(t, err, tc.err)
			} else {
				require.NoError(t, err)
			}
		})
	}

}

func testDomainMap(domains map[string]map[string]string) topology.DomainMap {
	domainMap := topology.NewDomainMap()
	for domainName, hosts := range domains {
		for hostName, instanceID := range hosts {
			domainMap.AddHost(domainName, instanceID, hostName)
		}
	}

	return domainMap
}

func TestToTreeTopology(t *testing.T) {
	v, _ := GetTreeTestSet(false)
	cfg := &Config{
		Plugin: topology.TopologyTree,
	}
	nt, _ := NewNetworkTopology(v, cfg)
	expected := map[string][]string{
		"":    {"S1"},
		"S1":  {"S2", "S3"},
		"S2":  {"I21", "I22", "I25"},
		"S3":  {"I34", "I35", "I36"},
		"I21": {},
		"I22": {},
		"I25": {},
		"I34": {},
		"I35": {},
		"I36": {},
	}
	require.Equal(t, expected, nt.tree)

	part := nt.getPartitionTree([]string{"I34", "I35"})
	expected = map[string][]string{
		"":   {"S1"},
		"S1": {"S3"},
		"S3": {"I34", "I35"},
	}

	require.Equal(t, expected, part)

	buf := &bytes.Buffer{}
	err := nt.Generate(buf)
	require.Nil(t, err)
	require.Equal(t, testTreeConfig, buf.String())
}

func TestToBlockTopology(t *testing.T) {
	v, _ := getBlockTestSet()
	cfg := &Config{
		Plugin:     topology.TopologyBlock,
		BlockSizes: []int{3},
	}
	nt, _ := NewNetworkTopology(v, cfg)
	buf := &bytes.Buffer{}
	err := nt.Generate(buf)
	require.Nil(t, err)
	require.Equal(t, testBlockConfig1_1, buf.String())
}

func TestToBlockTopologyWithFormattedBlockNames(t *testing.T) {
	v, _ := getBlockTestSet()
	cfg := &Config{
		Plugin: topology.TopologyBlock,
		BlockName: &BlockNameConfig{
			NodeNameRegexp: `^Node([0-9])[0-9]{2}$`,
			Format:         `domain${1}`,
		},
	}
	nt, err := NewNetworkTopology(v, cfg)
	require.NoError(t, err)

	buf := &bytes.Buffer{}
	require.Nil(t, nt.Generate(buf))
	require.Equal(t, `# domain1=B1
BlockName=domain1 Nodes=Node[104-106]
# domain2=B2
BlockName=domain2 Nodes=Node[201-202,205]
BlockSizes=3,6
`, buf.String())
	require.Equal(t, "block001", nt.blocks[0].id)
	require.Equal(t, "block002", nt.blocks[1].id)

	buf.Reset()
	require.Nil(t, nt.Generate(buf))
	require.Contains(t, buf.String(), "BlockName=domain1 ")

	spec, httpErr := nt.GetNodeTopologySpec("Node104", nil)
	require.Nil(t, httpErr)
	require.Equal(t, "default:domain1", spec)
}

func TestBlockNameFormatter(t *testing.T) {
	t.Run("unanchored match", func(t *testing.T) {
		config := &BlockNameConfig{
			NodeNameRegexp: `d([0-9]{2})-r([0-9]{2})`,
			Format:         `domain${1}_rack${2}`,
		}
		require.NoError(t, ValidateBlockNameConfig(config))
		formatter := compileBlockNameFormatter(config)

		name, err := formatter.formatBlockName("block001", "", []string{
			"gpu-d05-r04-srv4",
			"gpu-d05-r04-srv5",
		})
		require.NoError(t, err)
		require.Equal(t, "domain05_rack04", name)
	})

	t.Run("unmatched node", func(t *testing.T) {
		config := &BlockNameConfig{
			NodeNameRegexp: `^domain([0-9]{3})`,
			Format:         `block${1}`,
		}
		require.NoError(t, ValidateBlockNameConfig(config))
		formatter := compileBlockNameFormatter(config)

		_, err := formatter.formatBlockName("block001", "", []string{"domain001", "legacy001"})
		require.EqualError(t, err, `node "legacy001" in block "block001" does not match nodeNameRegexp "^domain([0-9]{3})"`)
	})

	t.Run("nodes produce different names", func(t *testing.T) {
		config := &BlockNameConfig{
			NodeNameRegexp: `^domain([0-9]{3})`,
			Format:         `block${1}`,
		}
		require.NoError(t, ValidateBlockNameConfig(config))
		formatter := compileBlockNameFormatter(config)

		_, err := formatter.formatBlockName("block001", "", []string{"domain001", "domain002"})
		require.EqualError(t, err, `nodes in block "block001" produce different block names "block001" and "block002"`)
	})

	t.Run("format produces empty name", func(t *testing.T) {
		config := &BlockNameConfig{
			NodeNameRegexp: `^node()$`,
			Format:         `${1}`,
		}
		require.NoError(t, ValidateBlockNameConfig(config))
		formatter := compileBlockNameFormatter(config)

		_, err := formatter.formatBlockName("block001", "", []string{"node"})
		require.EqualError(t, err, `blockName format "${1}" produces an empty name for node "node" in block "block001"`)
	})

	t.Run("empty complemented block keeps default name", func(t *testing.T) {
		config := &BlockNameConfig{
			NodeNameRegexp: `^domain([0-9]{3})`,
			Format:         `block${1}`,
		}
		require.NoError(t, ValidateBlockNameConfig(config))
		formatter := compileBlockNameFormatter(config)

		name, err := formatter.formatBlockName("block005", "", nil)
		require.NoError(t, err)
		require.Equal(t, "block005", name)
	})

	t.Run("different blocks produce duplicate names", func(t *testing.T) {
		config := &BlockNameConfig{
			NodeNameRegexp: `^domain[0-9]{3}`,
			Format:         `shared`,
		}
		require.NoError(t, ValidateBlockNameConfig(config))
		formatter := compileBlockNameFormatter(config)

		_, err := formatBlockNames([]*blockInfo{
			{id: "block001", nodes: []string{"domain001"}},
			{id: "block002", nodes: []string{"domain002"}},
		}, formatter)
		require.EqualError(t, err, `blocks "block001" and "block002" produce duplicate block name "shared"`)
	})
}

func TestValidateBlockNameConfigCachesRegexp(t *testing.T) {
	config := &BlockNameConfig{
		NodeNameRegexp: `^node([0-9]+)$`,
		Format:         `block${1}`,
	}

	require.NoError(t, ValidateBlockNameConfig(config))
	compiled := config.compiledRegexp
	require.NotNil(t, compiled)

	require.NoError(t, ValidateBlockNameConfig(config))
	require.Same(t, compiled, config.compiledRegexp)

	config.NodeNameRegexp = `^gpu([0-9]+)$`
	require.NoError(t, ValidateBlockNameConfig(config))
	require.NotSame(t, compiled, config.compiledRegexp)
}

func TestToBlockMultiIBTopology(t *testing.T) {
	v, _ := GetBlockWithMultiIBTestSet()
	cfg := &Config{
		Plugin:     topology.TopologyBlock,
		BlockSizes: []int{3},
	}
	nt, _ := NewNetworkTopology(v, cfg)
	buf := &bytes.Buffer{}
	err := nt.Generate(buf)
	require.Nil(t, err)
	require.Equal(t, testBlockConfig2, buf.String())
}

func TestToBlockIBTopology(t *testing.T) {
	v, _ := getBlockWithIBTestSet()
	cfg := &Config{
		Plugin:     topology.TopologyBlock,
		BlockSizes: []int{3},
	}
	nt, _ := NewNetworkTopology(v, cfg)
	buf := &bytes.Buffer{}
	err := nt.Generate(buf)
	require.Nil(t, err)
	require.Equal(t, testBlockConfig1_2, buf.String())
}

func TestToBlockDiffNumNode(t *testing.T) {
	v, _ := getBlockWithDiffNumNodeTestSet()
	cfg := &Config{
		Plugin: topology.TopologyBlock,
	}
	nt, _ := NewNetworkTopology(v, cfg)
	buf := &bytes.Buffer{}
	err := nt.Generate(buf)
	require.Nil(t, err)
	require.Equal(t, testBlockConfigDiffNumNodes, buf.String())
}

func TestToBlockDFSIBTopology(t *testing.T) {
	v, _ := getBlockWithDFSIBTestSet()
	cfg := &Config{
		Plugin:     topology.TopologyBlock,
		BlockSizes: []int{1},
	}
	nt, _ := NewNetworkTopology(v, cfg)
	buf := &bytes.Buffer{}
	err := nt.Generate(buf)
	require.Nil(t, err)
	require.Equal(t, testBlockConfigDFS, buf.String())
}

func TestToSlurmNameShortener(t *testing.T) {
	v := &topology.Vertex{
		Vertices: map[string]*topology.Vertex{
			"hpcislandid-1": {
				ID:   "hpcislandid-1",
				Name: "switch.3.1",
				Vertices: map[string]*topology.Vertex{
					"network-block-1": {
						ID:   "network-block-1",
						Name: "switch.2.1",
						Vertices: map[string]*topology.Vertex{
							"local-block-1": {
								ID:   "local-block-1",
								Name: "switch.1.1",
								Vertices: map[string]*topology.Vertex{
									"node-1-id": {
										ID:   "node-1-id",
										Name: "node-1",
									},
								},
							},
						},
					},
					"network-block-2": {
						ID:   "network-block-2",
						Name: "switch.2.2",
						Vertices: map[string]*topology.Vertex{
							"local-block-2": {
								ID:   "local-block-2",
								Name: "switch.1.2",
								Vertices: map[string]*topology.Vertex{
									"node-2-id": {
										ID:   "node-2-id",
										Name: "node-2",
									},
								},
							},
						},
					},
				},
			},
		},
	}

	root := &topology.Graph{Tiers: v}

	cfg := &Config{
		Plugin: topology.TopologyTree,
	}
	nt, _ := NewNetworkTopology(root, cfg)
	buf := &bytes.Buffer{}
	err := nt.Generate(buf)
	require.Nil(t, err)
	require.Equal(t, shortNameExpectedResult, buf.String())
}

func getBlockWithIBTestSet() (*topology.Graph, map[string]string) {
	//
	//         ------core------
	//        |                |
	//        L1               R1
	//      /    \           /    \
	//    L2      L3       R2      R3
	//    |       |        |       |
	//   ---     ---      ---     ---
	//   I14\    I21\     I34\    I41\
	//   I15-B1  I22-B2   I35-B3  I42-B4
	//   I16/    I25/     I36/    I43/
	//   ---     ---      ---     ---
	//
	instance2node := map[string]string{
		"I14": "Node104", "I15": "Node105", "I16": "Node106",
		"I21": "Node201", "I22": "Node202", "I25": "Node205",
		"I34": "Node304", "I35": "Node305", "I36": "Node306",
		"I41": "Node401", "I42": "Node402", "I43": "Node403",
	}

	n14 := &topology.Vertex{ID: "I14", Name: "Node104"}
	n15 := &topology.Vertex{ID: "I15", Name: "Node105"}
	n16 := &topology.Vertex{ID: "I16", Name: "Node106"}

	n21 := &topology.Vertex{ID: "I21", Name: "Node201"}
	n22 := &topology.Vertex{ID: "I22", Name: "Node202"}
	n25 := &topology.Vertex{ID: "I25", Name: "Node205"}

	n34 := &topology.Vertex{ID: "I34", Name: "Node304"}
	n35 := &topology.Vertex{ID: "I35", Name: "Node305"}
	n36 := &topology.Vertex{ID: "I36", Name: "Node306"}

	n41 := &topology.Vertex{ID: "I41", Name: "Node401"}
	n42 := &topology.Vertex{ID: "I42", Name: "Node402"}
	n43 := &topology.Vertex{ID: "I43", Name: "Node403"}

	l2 := &topology.Vertex{
		ID:       "L2",
		Vertices: map[string]*topology.Vertex{"I14": n14, "I15": n15, "I16": n16},
	}
	l3 := &topology.Vertex{
		ID:       "L3",
		Vertices: map[string]*topology.Vertex{"I21": n21, "I22": n22, "I25": n25},
	}
	l1 := &topology.Vertex{
		ID:       "l1",
		Vertices: map[string]*topology.Vertex{"L2": l2, "L3": l3},
	}

	r2 := &topology.Vertex{
		ID:       "R2",
		Vertices: map[string]*topology.Vertex{"I34": n34, "I35": n35, "I36": n36},
	}
	r3 := &topology.Vertex{
		ID:       "R3",
		Vertices: map[string]*topology.Vertex{"I41": n41, "I42": n42, "I43": n43},
	}
	r1 := &topology.Vertex{
		ID:       "r1",
		Vertices: map[string]*topology.Vertex{"R2": r2, "R3": r3},
	}

	treeRoot := &topology.Vertex{
		Vertices: map[string]*topology.Vertex{"L1": l1, "R1": r1},
	}

	domains := testDomainMap(map[string]map[string]string{
		"B1": {n14.Name: n14.ID, n15.Name: n15.ID, n16.Name: n16.ID},
		"B2": {n21.Name: n21.ID, n22.Name: n22.ID, n25.Name: n25.ID},
		"B3": {n34.Name: n34.ID, n35.Name: n35.ID, n36.Name: n36.ID},
		"B4": {n41.Name: n41.ID, n42.Name: n42.ID, n43.Name: n43.ID},
	})

	root := &topology.Graph{
		Tiers:   treeRoot,
		Domains: domains,
	}
	return root, instance2node
}

func getBlockWithDFSIBTestSet() (*topology.Graph, map[string]string) {
	//
	//     		 ibRoot1
	//       /      |        \
	//   S1         S2         S3
	//   |          |          |
	//   S4        ---         S5
	//   |         I14\        |
	//  ---			  B2      ---
	//  I22-B1     I15/       I25-B3
	//  ---        ---        ---
	//
	instance2node := map[string]string{
		"I14": "Node104", "I15": "Node105",
		"I22": "Node202", "I25": "Node205",
	}

	n14 := &topology.Vertex{ID: "I14", Name: "Node104"}
	n15 := &topology.Vertex{ID: "I15", Name: "Node105"}

	n22 := &topology.Vertex{ID: "I22", Name: "Node202"}
	n25 := &topology.Vertex{ID: "I25", Name: "Node205"}

	sw2 := &topology.Vertex{
		ID:       "S2",
		Vertices: map[string]*topology.Vertex{"I14": n14, "I15": n15},
	}

	sw4 := &topology.Vertex{
		ID:       "S4",
		Vertices: map[string]*topology.Vertex{"I22": n22},
	}

	sw5 := &topology.Vertex{
		ID:       "S5",
		Vertices: map[string]*topology.Vertex{"I25": n25},
	}

	sw3 := &topology.Vertex{
		ID:       "S3",
		Vertices: map[string]*topology.Vertex{"S5": sw5},
	}
	sw1 := &topology.Vertex{
		ID:       "S1",
		Vertices: map[string]*topology.Vertex{"S4": sw4},
	}

	sw0 := &topology.Vertex{
		ID:       "S0",
		Vertices: map[string]*topology.Vertex{"S1": sw1, "S2": sw2, "S3": sw3},
	}

	treeRoot := &topology.Vertex{
		Vertices: map[string]*topology.Vertex{"S0": sw0},
	}

	domains := testDomainMap(map[string]map[string]string{
		"B1": {n22.Name: n22.ID},
		"B2": {n14.Name: n14.ID, n15.Name: n15.ID},
		"B3": {n25.Name: n25.ID},
	})

	root := &topology.Graph{
		Tiers:   treeRoot,
		Domains: domains,
	}
	return root, instance2node
}

func getBlockTestSet() (*topology.Graph, map[string]string) {
	//
	//	---        ---
	//   I14\      I21\
	//   I15-B1    I22-B2
	//   I16/      I25/
	//   ---       ---
	//
	instance2node := map[string]string{
		"I14": "Node104", "I15": "Node105", "I16": "Node106",
		"I21": "Node201", "I22": "Node202", "I25": "Node205",
	}

	n14 := &topology.Vertex{ID: "I14", Name: "Node104"}
	n15 := &topology.Vertex{ID: "I15", Name: "Node105"}
	n16 := &topology.Vertex{ID: "I16", Name: "Node106"}

	n21 := &topology.Vertex{ID: "I21", Name: "Node201"}
	n22 := &topology.Vertex{ID: "I22", Name: "Node202"}
	n25 := &topology.Vertex{ID: "I25", Name: "Node205"}

	domains := testDomainMap(map[string]map[string]string{
		"B1": {n14.Name: n14.ID, n15.Name: n15.ID, n16.Name: n16.ID},
		"B2": {n21.Name: n21.ID, n22.Name: n22.ID, n25.Name: n25.ID},
	})

	root := &topology.Graph{Domains: domains}
	return root, instance2node
}

func getBlockWithDiffNumNodeTestSet() (*topology.Graph, map[string]string) {
	//
	//     ibRoot1
	//        |
	//        S1
	//      /    \
	//    S2      S3
	//    |       |
	//   ---     ---
	//   I14\    I21\
	//   I15-B1  I22-B2
	//   I16/    I25  /
	//           I26 /
	//   ---     ---
	//
	instance2node := map[string]string{
		"I14": "Node104", "I15": "Node105", "I16": "Node106",
		"I21": "Node201", "I22": "Node202", "I25": "Node205", "I26": "Node206",
	}

	n14 := &topology.Vertex{ID: "I14", Name: "Node104"}
	n15 := &topology.Vertex{ID: "I15", Name: "Node105"}
	n16 := &topology.Vertex{ID: "I16", Name: "Node106"}

	n21 := &topology.Vertex{ID: "I21", Name: "Node201"}
	n22 := &topology.Vertex{ID: "I22", Name: "Node202"}
	n25 := &topology.Vertex{ID: "I25", Name: "Node205"}
	n26 := &topology.Vertex{ID: "I26", Name: "Node206"}

	sw2 := &topology.Vertex{
		ID:       "S2",
		Vertices: map[string]*topology.Vertex{"I14": n14, "I15": n15, "I16": n16},
	}
	sw3 := &topology.Vertex{

		ID:       "S3",
		Vertices: map[string]*topology.Vertex{"I21": n21, "I22": n22, "I25": n25, "I26": n26},
	}
	sw1 := &topology.Vertex{
		ID:       "S1",
		Vertices: map[string]*topology.Vertex{"S2": sw2, "S3": sw3},
	}
	treeRoot := &topology.Vertex{
		Vertices: map[string]*topology.Vertex{"S1": sw1},
	}

	domains := testDomainMap(map[string]map[string]string{
		"B1": {n14.Name: n14.ID, n15.Name: n15.ID, n16.Name: n16.ID},
		"B2": {n21.Name: n21.ID, n22.Name: n22.ID, n25.Name: n25.ID, n26.Name: n26.ID},
	})

	root := &topology.Graph{
		Tiers:   treeRoot,
		Domains: domains,
	}
	return root, instance2node
}

func TestGetNodeTopologySpecInTopologyYaml(t *testing.T) {
	node201Id := "Node201"
	node104Id := "Node104"
	node205Id := "Node205"
	node999Id := "Node999"
	expectedNode201Spec := `topo1:IB2:S1:S3`
	expectedNode104Spec := `topo3:block1`
	expectedNode205Spec := `topo1:IB2:S1:S3,topo3:block2`
	expectedNode999Spec := ``

	v, _ := GetBlockWithMultiIBTestSet()
	cfg := &Config{
		Topologies: map[string]*TopologySpec{
			"topo1": {
				Plugin: topology.TopologyTree,
				Nodes:  []string{"Node[201,205]"},
			},
			"topo2": {
				Plugin: topology.TopologyTree,
				Nodes:  []string{"NodeIsDown"},
			},
			"topo3": {
				Plugin: topology.TopologyBlock,
				Nodes:  []string{"Node[104-105,107-108,205]"},
			},
			"topo4": {
				Plugin:     topology.TopologyBlock,
				Nodes:      []string{"NodeIsDown"},
				BlockSizes: []int{2},
			},
			"topo5": {
				Plugin:         topology.TopologyFlat,
				ClusterDefault: true,
			},
		},
	}
	nt, err := NewNetworkTopology(v, cfg)
	require.Nil(t, err)
	topologies, err := nt.GetTopologies()
	require.Nil(t, err)

	spec, err := nt.GetNodeTopologySpec(node201Id, topologies)
	require.Nil(t, err)
	require.Equal(t, expectedNode201Spec, spec)

	spec, err = nt.GetNodeTopologySpec(node104Id, topologies)
	require.Nil(t, err)
	require.Equal(t, expectedNode104Spec, spec)

	spec, err = nt.GetNodeTopologySpec(node205Id, topologies)
	require.Nil(t, err)
	require.Equal(t, expectedNode205Spec, spec)

	spec, err = nt.GetNodeTopologySpec(node999Id, topologies)
	require.Nil(t, err)
	require.Equal(t, expectedNode999Spec, spec)
}

func TestBlockTopologyYamlWithFormattedBlockNames(t *testing.T) {
	v, _ := getBlockTestSet()
	cfg := &Config{
		Topologies: map[string]*TopologySpec{
			"topo": {
				Plugin:     topology.TopologyBlock,
				BlockSizes: []int{3},
				BlockName: &BlockNameConfig{
					NodeNameRegexp: `^Node([0-9])[0-9]{2}$`,
					Format:         `domain${1}`,
				},
				Nodes: []string{"Node[104-106,201-202,205]"},
			},
		},
	}
	nt, err := NewNetworkTopology(v, cfg)
	require.NoError(t, err)

	topologies, httpErr := nt.GetTopologies()
	require.Nil(t, httpErr)
	require.Len(t, topologies, 1)
	require.Equal(t, "domain1", topologies[0].Block.Blocks[0].Name)
	require.Equal(t, "domain2", topologies[0].Block.Blocks[1].Name)

	spec, httpErr := nt.GetNodeTopologySpec("Node205", topologies)
	require.Nil(t, httpErr)
	require.Equal(t, "topo:domain2", spec)
}

func TestBlockTopologyYamlBlockNameErrorIncludesDomain(t *testing.T) {
	v, _ := getBlockTestSet()
	cfg := &Config{
		Topologies: map[string]*TopologySpec{
			"topo": {
				Plugin: topology.TopologyBlock,
				BlockName: &BlockNameConfig{
					NodeNameRegexp: `^gpu([0-9]+)$`,
					Format:         `domain${1}`,
				},
				Nodes: []string{"Node[104-106]"},
			},
		},
	}
	nt, err := NewNetworkTopology(v, cfg)
	require.NoError(t, err)

	_, httpErr := nt.GetTopologies()
	require.EqualError(
		t,
		httpErr,
		`topology "topo": node "Node104" in block "block1" (domain "B1") does not match nodeNameRegexp "^gpu([0-9]+)$"`,
	)
}

func TestGetNodeTopologySpecInTreeTopologyConf(t *testing.T) {
	node201Id := "Node201"
	node305Id := "Node305"
	node999Id := "Node999"
	expectedNode201Spec := `default:S1:S2`
	expectedNode305Spec := `default:S1:S3`
	expectedNode99Spec := ``

	v, _ := GetTreeTestSet(false)
	cfg := &Config{
		Plugin: topology.TopologyTree,
	}
	nt, err := NewNetworkTopology(v, cfg)
	require.Nil(t, err)
	topologies, err := nt.GetTopologies()
	require.Nil(t, err)
	require.Len(t, topologies, 0)

	spec, err := nt.GetNodeTopologySpec(node201Id, topologies)
	require.Nil(t, err)
	require.Equal(t, expectedNode201Spec, spec)

	spec, err = nt.GetNodeTopologySpec(node305Id, topologies)
	require.Nil(t, err)
	require.Equal(t, expectedNode305Spec, spec)

	spec, err = nt.GetNodeTopologySpec(node999Id, topologies)
	require.Nil(t, err)
	require.Equal(t, expectedNode99Spec, spec)
}

func TestGetNodeTopologySpecInBlockTopologyConf(t *testing.T) {
	node105Id := "Node105"
	node205Id := "Node205"
	node999Id := "Node999"
	expectedNode105Spec := `default:block001`
	expectedNode205Spec := `default:block002`
	expectedNode99Spec := ``

	v, _ := getBlockTestSet()
	cfg := &Config{
		Plugin: topology.TopologyBlock,
	}
	nt, err := NewNetworkTopology(v, cfg)
	require.Nil(t, err)
	topologies, err := nt.GetTopologies()
	require.Nil(t, err)
	require.Len(t, topologies, 0)

	spec, err := nt.GetNodeTopologySpec(node105Id, topologies)
	require.Nil(t, err)
	require.Equal(t, expectedNode105Spec, spec)

	spec, err = nt.GetNodeTopologySpec(node205Id, topologies)
	require.Nil(t, err)
	require.Equal(t, expectedNode205Spec, spec)

	spec, err = nt.GetNodeTopologySpec(node999Id, topologies)
	require.Nil(t, err)
	require.Equal(t, expectedNode99Spec, spec)
}

// TestGetNodeTopologySpecAfterComplementPerPartition verifies that GetNodeTopologySpec
// returns block IDs that match the per-partition topology when complementing is applied.
func TestGetNodeTopologySpecAfterComplementPerPartition(t *testing.T) {
	root, _ := getBlockWithIBTestSet()
	cfg := &Config{
		Topologies: map[string]*TopologySpec{
			"topo1": {
				Plugin:     topology.TopologyBlock,
				Nodes:      []string{"Node[104-105]", "Node[201-202,205]", "Node[304]", "Node[401-403]"},
				BlockSizes: []int{2, 4, 8},
			},
		},
	}

	nt, err := NewNetworkTopology(root, cfg)
	require.Nil(t, err)

	topologies, httpErr := nt.GetTopologies()
	require.Nil(t, httpErr)

	// Expected mapping mirrors cluster-wide complementing but uses per-partition block names (block1..)
	cases := []struct {
		node string
		spec string
	}{
		{"Node104", "topo1:block1"},
		{"Node105", "topo1:block1"},
		{"Node201", "topo1:block3"},
		{"Node202", "topo1:block3"},
		{"Node205", "topo1:block4"},
		{"Node304", "topo1:block5"},
		{"Node401", "topo1:block7"},
		{"Node402", "topo1:block7"},
		{"Node403", "topo1:block8"},
	}

	for _, tc := range cases {
		spec, err := nt.GetNodeTopologySpec(tc.node, topologies)
		require.Nil(t, err, "node %s", tc.node)
		require.Equal(t, tc.spec, spec, "node %s", tc.node)
	}
}

// TestInitTreeNilVertexRejected verifies that a nil child is rejected during
// topology construction before any output path can dereference it.
func TestInitTreeNilVertexRejected(t *testing.T) {
	graph := &topology.Graph{
		Tiers: &topology.Vertex{
			ID: "root",
			Vertices: map[string]*topology.Vertex{
				"nil-child": nil,
			},
		},
	}

	_, err := NewNetworkTopology(graph, &Config{Plugin: topology.TopologyTree})
	require.EqualError(t, err, `vertex "root" contains nil child with key "nil-child"`)
}

// TestTreeSelfEdgeRejected verifies that a self-edge is rejected before
// topology generation can traverse it.
func TestTreeSelfEdgeRejected(t *testing.T) {
	node := &topology.Vertex{ID: "x", Name: "x"}
	sw := &topology.Vertex{ID: "x", Vertices: map[string]*topology.Vertex{"x": node}}
	root := &topology.Vertex{Vertices: map[string]*topology.Vertex{"x": sw}}
	g := &topology.Graph{Tiers: root}

	_, err := NewNetworkTopology(g, &Config{Plugin: topology.TopologyTree})
	require.EqualError(t, err, `vertex "x" contains a self-edge`)
}

// TestTreeMultiVertexCycleRejected verifies that initTree rejects an edge back
// to an already-seen vertex instead of repeatedly traversing A -> B -> A.
func TestTreeMultiVertexCycleRejected(t *testing.T) {
	a := &topology.Vertex{ID: "a", Vertices: make(map[string]*topology.Vertex)}
	b := &topology.Vertex{ID: "b", Vertices: make(map[string]*topology.Vertex)}
	a.Vertices[b.ID] = b
	b.Vertices[a.ID] = a
	root := &topology.Vertex{Vertices: map[string]*topology.Vertex{a.ID: a}}

	done := make(chan error, 1)
	go func() {
		_, err := NewNetworkTopology(&topology.Graph{Tiers: root}, &Config{Plugin: topology.TopologyTree})
		done <- err
	}()

	select {
	case err := <-done:
		require.EqualError(t, err, `vertex "a" is reachable more than once; topology must be a tree`)
	case <-time.After(5 * time.Second):
		t.Fatal("NewNetworkTopology hung: multi-vertex cycle not guarded")
	}
}
