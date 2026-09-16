/*
 * Copyright 2024-2026 NVIDIA CORPORATION
 * SPDX-License-Identifier: Apache-2.0
 */

package models

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/dsx-ai-factory/topograph/pkg/topology"
	"github.com/dsx-ai-factory/topograph/pkg/translate"
)

func acceleratorDomainAnnotations(domain string) map[string]string {
	if domain == "" {
		return nil
	}
	return map[string]string{annotationAcceleratorDomain: domain}
}

func TestNewModelFromFileMedium(t *testing.T) {
	cfg, err := NewModelFromFile("../../tests/models/medium.yaml")
	require.NoError(t, err)

	require.Len(t, cfg.Switches, 7)
	require.Len(t, cfg.CapacityBlocks, 4)
	require.Len(t, cfg.Nodes, 8)

	require.Equal(t, []string{"1101", "1102"}, cfg.Switches["sw11"].Nodes)
	require.Equal(t, []CapacityBlock{
		{
			Switch:      "sw11",
			Nodes:       []string{"1101", "1102"},
			Annotations: acceleratorDomainAnnotations("nvl1"),
		},
		{
			Switch:      "sw12",
			Nodes:       []string{"1201", "1202"},
			Annotations: acceleratorDomainAnnotations("nvl2"),
		},
		{
			Switch:      "sw13",
			Nodes:       []string{"1301", "1302"},
			Annotations: acceleratorDomainAnnotations("nvl3"),
		},
		{
			Switch:      "sw14",
			Nodes:       []string{"1401", "1402"},
			Annotations: acceleratorDomainAnnotations("nvl4"),
		},
	}, cfg.CapacityBlocks)

	require.Equal(t, &Node{
		Instance: topology.Instance{
			ID: "1101",
			Labels: map[string]string{
				LabelTopologyRegion: "us-west",
				LabelTopologyZone:   "zone1",
			},
			NetLayers: []string{"sw11", "sw21", "sw3"},
		},
		Annotations: acceleratorDomainAnnotations("nvl1"),
	}, cfg.Nodes["1101"])

	require.Equal(t, []topology.ComputeInstances{
		{
			Region: "us-west",
			Instances: map[string]string{
				"i-1101": "1101",
				"i-1102": "1102",
				"i-1201": "1201",
				"i-1202": "1202",
				"i-1301": "1301",
				"i-1302": "1302",
				"i-1401": "1401",
				"i-1402": "1402",
			},
		},
	}, cfg.Instances)
}

func TestNewModelFromFileNVL72(t *testing.T) {
	cfg, err := NewModelFromFile("../../tests/models/nvl72.yaml")
	require.NoError(t, err)

	require.Len(t, cfg.Switches, 7)
	require.Len(t, cfg.CapacityBlocks, 4)
	require.Len(t, cfg.Nodes, 72)

	require.Equal(t, &Node{
		Instance: topology.Instance{
			ID: "node2215",
			Labels: map[string]string{
				LabelTopologyRegion: "us-east",
				LabelTopologyZone:   "zone1",
			},
			NetLayers: []string{"leaf-2-2", "spine-2", "core"},
		},
		Annotations: acceleratorDomainAnnotations("nvl-2-2"),
	}, cfg.Nodes["node2215"])
}

func TestModelCompletion(t *testing.T) {
	tests := []struct {
		name  string
		cfg   string
		model *Model
		err   string
	}{
		{
			name: "Case 1: derive nodes from blocks",
			cfg: `
switches:
  core:
    switches: [leaf]
blocks:
- switch: leaf
  nodes: ["n[1-2]"]
  annotations:
    accelerator.topology.test/domain: nvl1
- switch: leaf
  nodes: [n3]
  annotations:
    accelerator.topology.test/domain: nvl2
`,
			model: &Model{
				Switches: map[string]*Switch{
					"core": {
						Name:     "core",
						Switches: []string{"leaf"},
					},
					"leaf": {Name: "leaf", Nodes: []string{"n1", "n2", "n3"}},
				},
				Nodes: map[string]*Node{
					"n1": {
						Instance: topology.Instance{
							ID:        "n1",
							Labels:    map[string]string{LabelTopologyRegion: "none"},
							NetLayers: []string{"leaf", "core"},
						},
						Annotations: acceleratorDomainAnnotations("nvl1"),
					},
					"n2": {
						Instance: topology.Instance{
							ID:        "n2",
							Labels:    map[string]string{LabelTopologyRegion: "none"},
							NetLayers: []string{"leaf", "core"},
						},
						Annotations: acceleratorDomainAnnotations("nvl1"),
					},
					"n3": {
						Instance: topology.Instance{
							ID:        "n3",
							Labels:    map[string]string{LabelTopologyRegion: "none"},
							NetLayers: []string{"leaf", "core"},
						},
						Annotations: acceleratorDomainAnnotations("nvl2"),
					},
				},
				CapacityBlocks: []CapacityBlock{
					{
						Switch:      "leaf",
						Nodes:       []string{"n1", "n2"},
						Annotations: acceleratorDomainAnnotations("nvl1"),
					},
					{
						Switch:      "leaf",
						Nodes:       []string{"n3"},
						Annotations: acceleratorDomainAnnotations("nvl2"),
					},
				},
				Instances: []topology.ComputeInstances{
					{
						Region:    "none",
						Instances: map[string]string{"i-n1": "n1", "i-n2": "n2", "i-n3": "n3"},
					},
				},
			},
		},
		{
			name: "Case 2: switches are optional",
			cfg: `
blocks:
- nodes: ["n[1-2]"]
  annotations:
    accelerator.topology.test/domain: nvl1
`,
			model: &Model{
				Nodes: map[string]*Node{
					"n1": {
						Instance: topology.Instance{
							ID:     "n1",
							Labels: map[string]string{LabelTopologyRegion: "none"},
						},
						Annotations: acceleratorDomainAnnotations("nvl1"),
					},
					"n2": {
						Instance: topology.Instance{
							ID:     "n2",
							Labels: map[string]string{LabelTopologyRegion: "none"},
						},
						Annotations: acceleratorDomainAnnotations("nvl1"),
					},
				},
				CapacityBlocks: []CapacityBlock{
					{
						Nodes:       []string{"n1", "n2"},
						Annotations: acceleratorDomainAnnotations("nvl1"),
					},
				},
				Instances: []topology.ComputeInstances{
					{
						Region:    "none",
						Instances: map[string]string{"i-n1": "n1", "i-n2": "n2"},
					},
				},
			},
		},
		{
			name: "Case 3: block must declare nodes",
			cfg: `
blocks:
- nodes: [n1]
  annotations:
    accelerator.topology.test/domain: nvl1
- labels: {}
`,
			err: `capacity block at index 1 must declare at least one node`,
		},
		{
			name: "Case 4: conflicting block assignment",
			cfg: `
blocks:
- nodes: [n1]
- nodes: [n1]
`,
			err: `node "n1" belongs to more than one capacity block`,
		},
		{
			name: "Case 5: block switch must exist",
			cfg: `
blocks:
- switch: missing
  nodes: [n1]
`,
			err: `capacity block at index 0 references unknown switch "missing"`,
		},
		{
			name: "Case 6: parsing error",
			cfg: `
blocks:
- cb1
`,
			err: "cannot unmarshal !!str `cb1`",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			model, err := NewModelFromData([]byte(tt.cfg), "inline")
			if len(tt.err) != 0 {
				require.NotNil(t, err)
				require.ErrorContains(t, err, tt.err)
			} else {
				require.NoError(t, err)
				require.Equal(t, tt.model, model)
			}
		})
	}
}

// TestBuildSwitchMapsUnknownSubSwitch verifies that buildSwitchMaps rejects a
// programmatically-constructed model whose switch.Switches list references a
// switch that is not present in m.Switches. (The YAML parsing path
// auto-materializes missing sub-switches, so this validation guards against
// programmatic misuse or future code paths that skip UnmarshalYAML.)
func TestBuildSwitchMapsUnknownSubSwitch(t *testing.T) {
	m := &Model{
		Switches: map[string]*Switch{
			"sw1": {Name: "sw1", Switches: []string{"ghost"}},
			// "ghost" is intentionally absent
		},
	}
	_, err := m.buildSwitchMaps()
	require.ErrorContains(t, err, `switch "sw1" references unknown sub-switch "ghost"`)
}

func TestSwitchAndBlockMetadataMerge(t *testing.T) {
	cfg := `
switches:
  core:
    labels:
      topology.kubernetes.io/region: region1
      inherited: core
    annotations:
      core-annotation: core-value
      inherited: core
    switches: [leaf]
  leaf:
    labels:
      topology.kubernetes.io/zone: zone1
      inherited: leaf
    annotations:
      leaf-annotation: leaf-value
      inherited: leaf
blocks:
- switch: leaf
  nodes: [n1]
  labels:
    block-label: block-value
    inherited: block
  annotations:
    block-annotation: block-value
    inherited: block
`

	model, err := NewModelFromData([]byte(cfg), "inline")
	require.NoError(t, err)

	require.Equal(t, map[string]string{
		LabelTopologyRegion: "region1",
		LabelTopologyZone:   "zone1",
		"inherited":         "block",
		"block-label":       "block-value",
	}, model.Nodes["n1"].Labels)
	require.Equal(t, map[string]string{
		"core-annotation":  "core-value",
		"leaf-annotation":  "leaf-value",
		"block-annotation": "block-value",
		"inherited":        "block",
	}, model.Nodes["n1"].Annotations)
	require.Equal(t, []topology.ComputeInstances{
		{
			Region:    "region1",
			Instances: map[string]string{"i-n1": "n1"},
		},
	}, model.Instances)
}

func TestToGraphUsesHostNames(t *testing.T) {
	model, err := NewModelFromData([]byte(`
switches:
  leaf: {}
blocks:
- switch: leaf
  nodes: [instance-1]
  annotations:
    accelerator.topology.test/domain: nvl1
`), "inline")
	require.NoError(t, err)

	graph, instance2node := model.ToGraph(nil)

	require.Equal(t, "instance-1", instance2node["i-instance-1"])
	require.Equal(t, &topology.Vertex{
		ID:   "i-instance-1",
		Name: "instance-1",
	}, graph.Tiers.Vertices["leaf"].Vertices["i-instance-1"])
	require.Equal(t, &topology.HostInfo{
		Domain:     "nvl1",
		InstanceID: "i-instance-1",
		HostName:   "instance-1",
	}, graph.Domains["nvl1"]["instance-1"])
	require.Equal(t, map[string]string{
		LabelTopologyRegion: "none",
	}, graph.Instances["i-instance-1"].Labels)
}

func TestToGraphKeepsModelHostNames(t *testing.T) {
	model, err := NewModelFromData([]byte(`
switches:
  leaf: {}
blocks:
- switch: leaf
  nodes: [node1]
`), "inline")
	require.NoError(t, err)

	graph, instance2node := model.ToGraph([]topology.ComputeInstances{
		{Instances: map[string]string{"i-node1": "request-node1"}},
	})

	require.Equal(t, "node1", instance2node["i-node1"])
	require.Equal(t, "node1", graph.Tiers.Vertices["leaf"].Vertices["i-node1"].Name)
	require.Contains(t, graph.Instances, "i-node1")
	require.Nil(t, graph.Domains)

	_, err = translate.NewNetworkTopology(graph, &translate.Config{Plugin: topology.TopologyBlock})
	require.EqualError(t, err, "missing block topology")
}
