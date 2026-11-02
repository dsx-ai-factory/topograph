// SPDX-FileCopyrightText: Copyright (c) 2024-2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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

package topology

import (
	"testing"

	"github.com/stretchr/testify/require"
)

const testInstanceLabelKey = "example.com/gpu-product"

var (
	instances = []*InstanceTopology{
		{
			InstanceID:   "i-001",
			FabricTiers:  ClosestFirstFabricTiers("nn-11111111", "nn-55555555", "nn-77777777"),
			XclrDomainID: "acc-111111",
		},
		{
			InstanceID:   "i-002",
			FabricTiers:  ClosestFirstFabricTiers("nn-22222222", "nn-55555555", "nn-77777777"),
			XclrDomainID: "acc-222222",
		},
		{
			InstanceID:  "i-003",
			FabricTiers: ClosestFirstFabricTiers("nn-33333333", "nn-66666666", "nn-77777777"),
		},
		{
			InstanceID:  "i-004",
			FabricTiers: ClosestFirstFabricTiers("nn-44444444", "nn-66666666", "nn-77777777"),
		},
	}

	n1 = &Vertex{ID: "i-001", Name: "node1"}
	n2 = &Vertex{ID: "i-002", Name: "node2"}
	n3 = &Vertex{ID: "i-003", Name: "node3"}
	n4 = &Vertex{ID: "i-004", Name: "node4"}
	n5 = &Vertex{ID: "i-cpu", Name: "node5"}

	none = &Vertex{ID: NoTopology, Vertices: map[string]*Vertex{"i-cpu": n5}}

	i2n = map[string]string{
		"i-001": "node1",
		"i-002": "node2",
		"i-003": "node3",
		"i-004": "node4",
		"i-cpu": "node5",
	}
)

func TestToGraphNoNorm(t *testing.T) {
	topo := NewClusterTopology()
	for _, inst := range instances {
		topo.Append(inst)
	}
	require.Equal(t, len(instances), topo.Len())

	inst0 := "Instance:i-001 Fabric-Tier-0:nn-11111111 Fabric-Tier-1:nn-55555555 Fabric-Tier-2:nn-77777777 XclrDomain:acc-111111"
	require.Equal(t, inst0, topo.Instances[0].String())

	inst2 := "Instance:i-003 Fabric-Tier-0:nn-33333333 Fabric-Tier-1:nn-66666666 Fabric-Tier-2:nn-77777777"
	require.Equal(t, inst2, topo.Instances[2].String())

	v31 := &Vertex{ID: "nn-11111111", Vertices: map[string]*Vertex{"i-001": n1}}
	v32 := &Vertex{ID: "nn-22222222", Vertices: map[string]*Vertex{"i-002": n2}}
	v33 := &Vertex{ID: "nn-33333333", Vertices: map[string]*Vertex{"i-003": n3}}
	v34 := &Vertex{ID: "nn-44444444", Vertices: map[string]*Vertex{"i-004": n4}}

	v21 := &Vertex{
		ID: "nn-55555555",
		Vertices: map[string]*Vertex{
			"nn-11111111": v31,
			"nn-22222222": v32,
		},
	}

	v22 := &Vertex{
		ID: "nn-66666666",
		Vertices: map[string]*Vertex{
			"nn-33333333": v33,
			"nn-44444444": v34,
		},
	}

	v1 := &Vertex{
		ID:       "nn-77777777",
		Vertices: map[string]*Vertex{"nn-55555555": v21, "nn-66666666": v22},
	}

	v0 := &Vertex{
		Vertices: map[string]*Vertex{
			"nn-77777777": v1,
			NoTopology:    none,
		},
	}

	domains := NewDomainMap()
	domains.AddHost("acc-111111", "i-001", "node1")
	domains.AddHost("acc-222222", "i-002", "node2")

	expected := &Graph{
		Tiers:   v0,
		Domains: domains,
	}

	graph := topo.ToGraph("test", []ComputeInstances{{Instances: i2n}}, 0, false)
	require.Equal(t, expected, graph)
}

func TestToGraphNorm(t *testing.T) {
	topo := NewClusterTopology()
	for _, inst := range instances {
		topo.Append(inst)
	}
	require.Equal(t, len(instances), topo.Len())

	v31 := &Vertex{Name: "switch.1.1", ID: "nn-11111111", Vertices: map[string]*Vertex{"i-001": n1}}
	v32 := &Vertex{Name: "switch.1.2", ID: "nn-22222222", Vertices: map[string]*Vertex{"i-002": n2}}
	v33 := &Vertex{Name: "switch.1.3", ID: "nn-33333333", Vertices: map[string]*Vertex{"i-003": n3}}
	v34 := &Vertex{Name: "switch.1.4", ID: "nn-44444444", Vertices: map[string]*Vertex{"i-004": n4}}

	v21 := &Vertex{
		Name: "switch.2.1",
		ID:   "nn-55555555",
		Vertices: map[string]*Vertex{
			"nn-11111111": v31,
			"nn-22222222": v32,
		},
	}

	v22 := &Vertex{
		Name: "switch.2.2",
		ID:   "nn-66666666",
		Vertices: map[string]*Vertex{
			"nn-33333333": v33,
			"nn-44444444": v34,
		},
	}

	v1 := &Vertex{
		Name:     "switch.3.1",
		ID:       "nn-77777777",
		Vertices: map[string]*Vertex{"nn-55555555": v21, "nn-66666666": v22},
	}

	v0 := &Vertex{
		Vertices: map[string]*Vertex{
			"nn-77777777": v1,
			NoTopology:    none,
		},
	}

	domains := NewDomainMap()
	domains.AddHost("acc-111111", "i-001", "node1")
	domains.AddHost("acc-222222", "i-002", "node2")

	expected := &Graph{
		Tiers:   v0,
		Domains: domains,
	}

	graph := topo.ToGraph("test", []ComputeInstances{{Instances: i2n}}, 0, true)
	require.Equal(t, expected, graph)

	inst0 := "Instance:i-001 Fabric-Tier-0:nn-11111111 (switch.1.1) Fabric-Tier-1:nn-55555555 (switch.2.1) Fabric-Tier-2:nn-77777777 (switch.3.1) XclrDomain:acc-111111"
	require.Equal(t, inst0, topo.Instances[0].String())

	inst2 := "Instance:i-003 Fabric-Tier-0:nn-33333333 (switch.1.3) Fabric-Tier-1:nn-66666666 (switch.2.2) Fabric-Tier-2:nn-77777777 (switch.3.1)"
	require.Equal(t, inst2, topo.Instances[2].String())
}

func TestToGraphIncludesInstanceData(t *testing.T) {
	topo := NewClusterTopology()
	topo.Append(&InstanceTopology{
		InstanceID:      "i-001",
		FabricTiers:     ClosestFirstFabricTiers("leaf-1", "spine-1", "core-1"),
		XclrDomainID:    "nvl-1",
		XclrSubDomainID: "nvl-1.rack-1",
		Instance: &Instance{
			ID:            "i-001",
			NetworkLayers: []string{"leaf-1", "spine-1", "core-1"},
			Labels:        map[string]string{testInstanceLabelKey: "H100"},
		},
	})

	graph := topo.ToGraph("test", []ComputeInstances{
		{
			Region:    "region-1",
			Instances: map[string]string{"i-001": "node1"},
		},
	}, 1, false)

	require.Equal(t, map[string]Instance{
		"i-001": {
			ID:            "i-001",
			NetworkLayers: []string{"leaf-1", "spine-1"},
			Labels: map[string]string{
				testInstanceLabelKey:     "H100",
				KeyTopologyXclrDomain:    "nvl-1",
				KeyTopologyXclrSubDomain: "nvl-1.rack-1",
			},
		},
	}, graph.Instances)
}

func TestToInstancePreservesExplicitXclrSubDomainLabel(t *testing.T) {
	inst := &InstanceTopology{
		InstanceID:      "i-001",
		XclrSubDomainID: "discovered-sub-domain",
		Instance: &Instance{
			Labels: map[string]string{
				KeyTopologyXclrDomain:    "explicit-domain",
				KeyTopologyXclrSubDomain: "explicit-sub-domain",
			},
		},
	}

	instance := inst.toInstance(0)

	require.Equal(t, "explicit-sub-domain", instance.Labels[KeyTopologyXclrSubDomain])
}

func TestToInstanceOmitsXclrSubDomainWithoutDomainLabel(t *testing.T) {
	inst := &InstanceTopology{
		InstanceID:      "i-001",
		XclrSubDomainID: "discovered-sub-domain",
		Instance:        &Instance{},
	}

	instance := inst.toInstance(0)

	require.NotContains(t, instance.Labels, KeyTopologyXclrSubDomain)
}

func TestToInstancePreservesUnrelatedAcceleratorDomainLabel(t *testing.T) {
	const acceleratorDomainLabel = "example.cpm/accelerator-domain"

	inst := &InstanceTopology{
		InstanceID:      "i-001",
		XclrDomainID:    "discovered-domain",
		XclrSubDomainID: "discovered-sub-domain",
		Instance: &Instance{
			Labels: map[string]string{
				acceleratorDomainLabel: "external-domain",
			},
		},
	}

	instance := inst.toInstance(0)

	require.Equal(t, "external-domain", instance.Labels[acceleratorDomainLabel])
	require.Equal(t, "discovered-domain", instance.Labels[KeyTopologyXclrDomain])
	require.Equal(t, "discovered-sub-domain", instance.Labels[KeyTopologyXclrSubDomain])
}

func TestTrimTiers(t *testing.T) {
	tests := []struct {
		name      string
		trimTiers int
		in        InstanceTopology
		out       []string
	}{
		{
			name:      "Case 1: trim none",
			trimTiers: 0,
			in: InstanceTopology{
				FabricTiers: ClosestFirstFabricTiers("leaf1", "spine1", "core1"),
			},
			out: []string{"leaf1", "spine1", "core1"},
		},
		{
			name:      "Case 2: trim 1 tier",
			trimTiers: 1,
			in: InstanceTopology{
				FabricTiers: ClosestFirstFabricTiers("leaf1", "spine1", "core1"),
			},
			out: []string{"leaf1", "spine1"},
		},
		{
			name:      "Case 3: trim 2 tiers",
			trimTiers: 2,
			in: InstanceTopology{
				FabricTiers: ClosestFirstFabricTiers("leaf1", "spine1", "core1"),
			},
			out: []string{"leaf1"},
		},
		{
			name:      "Case 4: trim all tiers",
			trimTiers: 3,
			in: InstanceTopology{
				FabricTiers: ClosestFirstFabricTiers("leaf1", "spine1", "core1"),
			},
			out: []string{},
		},
		{
			name:      "Case 5: trim more than available",
			trimTiers: 10,
			in: InstanceTopology{
				FabricTiers: ClosestFirstFabricTiers("leaf1", "spine1", "core1"),
			},
			out: []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inst := tt.in
			tiers := trimmedTiers(&inst, tt.trimTiers)
			ids := make([]string, len(tiers))
			for i := range tiers {
				ids[i] = tiers[i].ID
			}
			require.Equal(t, tt.out, ids)
		})
	}
}

func TestToGraphSupportsVariableTierCount(t *testing.T) {
	topo := NewClusterTopology()
	topo.Append(&InstanceTopology{
		InstanceID:   "instance-1",
		FabricTiers:  ClosestFirstFabricTiers("fabric-0", "fabric-1", "fabric-2", "fabric-3"),
		XclrDomainID: "accelerator",
	})

	graph := topo.ToGraph("test", []ComputeInstances{{
		Instances: map[string]string{"instance-1": "node-1"},
	}}, 0, false)

	vertex := graph.Tiers.Vertices["fabric-3"]
	for _, id := range []string{"fabric-2", "fabric-1", "fabric-0", "instance-1"} {
		require.NotNil(t, vertex)
		vertex = vertex.Vertices[id]
	}
	require.Equal(t, "node-1", vertex.Name)
	require.Contains(t, graph.Domains["accelerator"], "node-1")
}

func TestToGraphKeepsSameIDAtDifferentLevelsDistinct(t *testing.T) {
	topo := NewClusterTopology()
	topo.Append(&InstanceTopology{
		InstanceID:  "instance-1",
		FabricTiers: ClosestFirstFabricTiers("shared", "shared"),
	})

	graph := topo.ToGraph("test", []ComputeInstances{{
		Instances: map[string]string{"instance-1": "node-1"},
	}}, 0, false)

	outer := graph.Tiers.Vertices["shared"]
	inner := outer.Vertices["shared"]
	require.NotSame(t, outer, inner)
	require.Equal(t, "node-1", inner.Vertices["instance-1"].Name)
}

func TestToGraphMergesMixedDepthPathsAtSharedRoot(t *testing.T) {
	topo := NewClusterTopology()
	topo.Append(&InstanceTopology{
		InstanceID:  "instance-1",
		FabricTiers: ClosestFirstFabricTiers("leaf-1", "shared-root"),
	})
	topo.Append(&InstanceTopology{
		InstanceID:  "instance-2",
		FabricTiers: ClosestFirstFabricTiers("leaf-2", "spine-2", "shared-root"),
	})

	graph := topo.ToGraph("test", []ComputeInstances{{
		Instances: map[string]string{
			"instance-1": "node-1",
			"instance-2": "node-2",
		},
	}}, 0, false)

	root := graph.Tiers.Vertices["shared-root"]
	require.NotNil(t, root)
	require.Contains(t, root.Vertices, "leaf-1")
	require.Contains(t, root.Vertices, "spine-2")
	require.Equal(t, "node-1", root.Vertices["leaf-1"].Vertices["instance-1"].Name)
	require.Equal(t, "node-2", root.Vertices["spine-2"].Vertices["leaf-2"].Vertices["instance-2"].Name)
}

func TestTrimTiersTreatsNegativeAsZero(t *testing.T) {
	inst := &InstanceTopology{FabricTiers: ClosestFirstFabricTiers("fabric-0", "fabric-1")}
	require.Equal(t, inst.FabricTiers, trimmedTiers(inst, -1))
}

// TestToGraphXclrSubDomainIDCreatesDualLevelDomain verifies that instances
// with XclrSubDomainID set land in a DomainMap entry keyed by XclrDomainID,
// with SubDomain populated from XclrSubDomainID. The XclrSubDomainID values
// must not appear as top-level domain keys.
func TestToGraphXclrSubDomainIDCreatesDualLevelDomain(t *testing.T) {
	topo := NewClusterTopology()
	topo.Append(&InstanceTopology{
		InstanceID:      "i-001",
		FabricTiers:     ClosestFirstFabricTiers("leaf-1", "spine-1"),
		XclrDomainID:    "nvl-domain-01",
		XclrSubDomainID: "rack-01",
	})
	topo.Append(&InstanceTopology{
		InstanceID:      "i-002",
		FabricTiers:     ClosestFirstFabricTiers("leaf-2", "spine-1"),
		XclrDomainID:    "nvl-domain-01",
		XclrSubDomainID: "rack-02",
	})

	graph := topo.ToGraph("test", []ComputeInstances{{
		Instances: map[string]string{"i-001": "node1", "i-002": "node2"},
	}}, 0, false)

	domain := graph.Domains["nvl-domain-01"]
	require.NotNil(t, domain, "accelerator domain nvl-domain-01 must be present")
	require.Len(t, domain, 2)

	hi1 := domain["node1"]
	require.NotNil(t, hi1)
	require.Equal(t, "nvl-domain-01", hi1.Domain)
	require.Equal(t, "rack-01", hi1.SubDomain)
	require.Equal(t, "i-001", hi1.InstanceID)

	hi2 := domain["node2"]
	require.NotNil(t, hi2)
	require.Equal(t, "nvl-domain-01", hi2.Domain)
	require.Equal(t, "rack-02", hi2.SubDomain)
	require.Equal(t, "i-002", hi2.InstanceID)

	// Sub-domain names must not appear as independent top-level domain keys.
	require.Nil(t, graph.Domains["rack-01"])
	require.Nil(t, graph.Domains["rack-02"])
}

// TestToGraphMixedXclrDomainTypes verifies that a ClusterTopology containing
// both single-level (XclrDomainID only) and dual-level (XclrDomainID plus
// XclrSubDomainID) instances produces the correct DomainMap entries for each type.
func TestToGraphMixedXclrDomainTypes(t *testing.T) {
	topo := NewClusterTopology()
	// Dual-level: goes into nvl-domain-01 with SubDomain = rack-01.
	topo.Append(&InstanceTopology{
		InstanceID:      "i-001",
		FabricTiers:     ClosestFirstFabricTiers("leaf-1"),
		XclrDomainID:    "nvl-domain-01",
		XclrSubDomainID: "rack-01",
	})
	// Single-level: goes into acc-standalone with no SubDomain.
	topo.Append(&InstanceTopology{
		InstanceID:   "i-002",
		FabricTiers:  ClosestFirstFabricTiers("leaf-2"),
		XclrDomainID: "acc-standalone",
	})

	graph := topo.ToGraph("test", []ComputeInstances{{
		Instances: map[string]string{"i-001": "node1", "i-002": "node2"},
	}}, 0, false)

	// Dual-level host: SubDomain populated.
	hi1 := graph.Domains["nvl-domain-01"]["node1"]
	require.NotNil(t, hi1)
	require.Equal(t, "rack-01", hi1.SubDomain)

	// Single-level host: SubDomain empty.
	hi2 := graph.Domains["acc-standalone"]["node2"]
	require.NotNil(t, hi2)
	require.Equal(t, "", hi2.SubDomain)
}

// TestToGraphXclrDomainIDWithoutSubDomain verifies that when XclrDomainID is set
// but XclrSubDomainID is empty, the host is placed directly in the domain and
// SubDomain is empty.
func TestToGraphXclrDomainIDWithoutSubDomain(t *testing.T) {
	topo := NewClusterTopology()
	topo.Append(&InstanceTopology{
		InstanceID:   "i-001",
		FabricTiers:  ClosestFirstFabricTiers("leaf-1"),
		XclrDomainID: "nvl-domain-01",
	})

	graph := topo.ToGraph("test", []ComputeInstances{{
		Instances: map[string]string{"i-001": "node1"},
	}}, 0, false)

	domain := graph.Domains["nvl-domain-01"]
	require.NotNil(t, domain, "accelerator domain must be present without XclrSubDomainID")
	hi := domain["node1"]
	require.NotNil(t, hi)
	require.Equal(t, "nvl-domain-01", hi.Domain)
	require.Equal(t, "", hi.SubDomain)
}
