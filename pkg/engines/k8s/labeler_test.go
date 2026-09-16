/*
 * Copyright 2024-2026 NVIDIA CORPORATION
 * SPDX-License-Identifier: Apache-2.0
 */

package k8s

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/dsx-ai-factory/topograph/pkg/topology"
	"github.com/dsx-ai-factory/topograph/pkg/translate"
)

type testLabeler struct {
	data map[string]map[string]string
}

func (l *testLabeler) AddNodeLabels(_ context.Context, nodeName string, labels map[string]string) error {
	if _, ok := l.data[nodeName]; ok {
		return fmt.Errorf("duplicate entry for %s", nodeName)
	}
	l.data[nodeName] = labels
	return nil
}

func TestApplyNodeLabelsWithTree(t *testing.T) {
	root, _ := translate.GetTreeTestSet(true)
	labeler := &testLabeler{data: make(map[string]map[string]string)}
	data := map[string]map[string]string{
		"Node201": {"fabric.topograph.run/tier-0": "S2", "fabric.topograph.run/tier-1": "S1"},
		"Node202": {"fabric.topograph.run/tier-0": "S2", "fabric.topograph.run/tier-1": "S1"},
		"Node205": {"fabric.topograph.run/tier-0": "S2", "fabric.topograph.run/tier-1": "S1"},
		"Node304": {"fabric.topograph.run/tier-0": "xf946c4acef2d5939", "fabric.topograph.run/tier-1": "S1"},
		"Node305": {"fabric.topograph.run/tier-0": "xf946c4acef2d5939", "fabric.topograph.run/tier-1": "S1"},
		"Node306": {"fabric.topograph.run/tier-0": "xf946c4acef2d5939", "fabric.topograph.run/tier-1": "S1"},
	}

	err := NewTopologyLabeler(NewTopologyLabelKeys(nil, "")).ApplyNodeLabels(context.TODO(), root, labeler)
	require.NoError(t, err)
	require.Equal(t, data, labeler.data)
}

func TestApplyNodeLabelsWithBlock(t *testing.T) {
	root, _ := translate.GetBlockWithMultiIBTestSet()
	labeler := &testLabeler{data: make(map[string]map[string]string)}
	data := map[string]map[string]string{
		"Node104": {
			"accelerator.topograph.run/domain": "B1",
			"fabric.topograph.run/tier-0":      "S2",
			"fabric.topograph.run/tier-1":      "S1",
			"fabric.topograph.run/tier-2":      "IB2",
		},
		"Node105": {
			"accelerator.topograph.run/domain": "B1",
			"fabric.topograph.run/tier-0":      "S2",
			"fabric.topograph.run/tier-1":      "S1",
			"fabric.topograph.run/tier-2":      "IB2",
		},
		"Node106": {
			"accelerator.topograph.run/domain": "B1",
			"fabric.topograph.run/tier-0":      "S2",
			"fabric.topograph.run/tier-1":      "S1",
			"fabric.topograph.run/tier-2":      "IB2",
		},
		"Node201": {
			"accelerator.topograph.run/domain": "B2",
			"fabric.topograph.run/tier-0":      "S3",
			"fabric.topograph.run/tier-1":      "S1",
			"fabric.topograph.run/tier-2":      "IB2",
		},
		"Node202": {
			"accelerator.topograph.run/domain": "B2",
			"fabric.topograph.run/tier-0":      "S3",
			"fabric.topograph.run/tier-1":      "S1",
			"fabric.topograph.run/tier-2":      "IB2",
		},
		"Node205": {
			"accelerator.topograph.run/domain": "B2",
			"fabric.topograph.run/tier-0":      "S3",
			"fabric.topograph.run/tier-1":      "S1",
			"fabric.topograph.run/tier-2":      "IB2",
		},
		"Node301": {
			"accelerator.topograph.run/domain": "B3",
			"fabric.topograph.run/tier-0":      "S5",
			"fabric.topograph.run/tier-1":      "S4",
			"fabric.topograph.run/tier-2":      "IB1",
		},
		"Node302": {
			"accelerator.topograph.run/domain": "B3",
			"fabric.topograph.run/tier-0":      "S5",
			"fabric.topograph.run/tier-1":      "S4",
			"fabric.topograph.run/tier-2":      "IB1",
		},
		"Node303": {
			"accelerator.topograph.run/domain": "B3",
			"fabric.topograph.run/tier-0":      "S5",
			"fabric.topograph.run/tier-1":      "S4",
			"fabric.topograph.run/tier-2":      "IB1",
		},
		"Node401": {
			"accelerator.topograph.run/domain": "B4",
			"fabric.topograph.run/tier-0":      "S6",
			"fabric.topograph.run/tier-1":      "S4",
			"fabric.topograph.run/tier-2":      "IB1",
		},
		"Node402": {
			"accelerator.topograph.run/domain": "B4",
			"fabric.topograph.run/tier-0":      "S6",
			"fabric.topograph.run/tier-1":      "S4",
			"fabric.topograph.run/tier-2":      "IB1",
		},
		"Node403": {
			"accelerator.topograph.run/domain": "B4",
			"fabric.topograph.run/tier-0":      "S6",
			"fabric.topograph.run/tier-1":      "S4",
			"fabric.topograph.run/tier-2":      "IB1",
		},
	}

	err := NewTopologyLabeler(NewTopologyLabelKeys(nil, "")).ApplyNodeLabels(context.TODO(), root, labeler)
	require.NoError(t, err)
	require.Equal(t, data, labeler.data)
}

func TestTopologyLabelKeysUseDefaultsWhenCustomLabelsAreOmitted(t *testing.T) {
	keys := NewTopologyLabelKeys(nil, "")
	require.Equal(t, topology.FabricTierKey(2), keys.FabricKey(2))
	require.Equal(t, topology.KeyTopologyXclrDomain, keys.XclrDomainKey())
	require.Equal(t, topology.KeyTopologyXclrSubDomain, keys.XclrSubDomainKey())
}

func TestTopologyLabelKeysUseOnlyConfiguredLabels(t *testing.T) {
	keys := NewTopologyLabelKeys(
		[]string{"example.com/rack", "example.com/pod"},
		"example.com/nvl",
	)
	require.Equal(t, "example.com/rack", keys.FabricKey(0))
	require.Equal(t, "example.com/pod", keys.FabricKey(1))
	require.Empty(t, keys.FabricKey(2))
	require.Equal(t, "example.com/nvl", keys.XclrDomainKey())
	require.Equal(t, topology.KeyTopologyXclrSubDomain, keys.XclrSubDomainKey())

	fabricOnly := NewTopologyLabelKeys([]string{"example.com/rack"}, "")
	require.Equal(t, "example.com/rack", fabricOnly.FabricKey(0))
	require.Equal(t, topology.KeyTopologyXclrDomain, fabricOnly.XclrDomainKey())
	require.Equal(t, topology.KeyTopologyXclrSubDomain, fabricOnly.XclrSubDomainKey())
}

func TestBuildNodeLabelsWithVariableLevels(t *testing.T) {
	node := &topology.Vertex{ID: "instance-1", Name: "node-1"}
	fabric0 := &topology.Vertex{ID: "fabric-0", Vertices: map[string]*topology.Vertex{node.ID: node}}
	fabric1 := &topology.Vertex{ID: "fabric-1", Vertices: map[string]*topology.Vertex{fabric0.ID: fabric0}}
	fabric2 := &topology.Vertex{ID: "fabric-2", Vertices: map[string]*topology.Vertex{fabric1.ID: fabric1}}
	fabric3 := &topology.Vertex{ID: "fabric-3", Vertices: map[string]*topology.Vertex{fabric2.ID: fabric2}}
	graph := &topology.Graph{
		Tiers:   &topology.Vertex{Vertices: map[string]*topology.Vertex{fabric3.ID: fabric3}},
		Domains: topology.DomainMap{"accelerator": {"node-1": &topology.HostInfo{}}},
	}

	keys := NewTopologyLabelKeys(
		[]string{"example.com/fabric-0", "example.com/fabric-1"},
		"example.com/accelerator",
	)
	labels, err := NewTopologyLabeler(keys).BuildNodeLabels(graph)
	require.NoError(t, err)
	require.Equal(t, map[string]string{
		"example.com/fabric-0":    "fabric-0",
		"example.com/fabric-1":    "fabric-1",
		"example.com/accelerator": "accelerator",
	}, labels["node-1"])
}

func TestBuildNodeLabelsWithXclrSubDomain(t *testing.T) {
	cluster := topology.NewClusterTopology()
	cluster.Append(&topology.InstanceTopology{
		InstanceID:      "instance-1",
		XclrDomainID:    "domain-1",
		XclrSubDomainID: "domain-1.rack-1",
	})
	cluster.Append(&topology.InstanceTopology{
		InstanceID:   "instance-2",
		XclrDomainID: "domain-1",
	})
	graph := cluster.ToGraph("test", []topology.ComputeInstances{{
		Instances: map[string]string{
			"instance-1": "node-1",
			"instance-2": "node-2",
		},
	}}, 0, false)

	labels, err := NewTopologyLabeler(NewTopologyLabelKeys(nil, "")).BuildNodeLabels(graph)
	require.NoError(t, err)
	require.Equal(t, map[string]string{
		topology.KeyTopologyXclrDomain:    "domain-1",
		topology.KeyTopologyXclrSubDomain: "domain-1.rack-1",
	}, labels["node-1"])
	require.Equal(t, map[string]string{
		topology.KeyTopologyXclrDomain: "domain-1",
	}, labels["node-2"])
}

func TestBuildNodeLabelsWithMixedDepthSharedRoot(t *testing.T) {
	cluster := topology.NewClusterTopology()
	cluster.Append(&topology.InstanceTopology{
		InstanceID:  "instance-1",
		FabricTiers: topology.ClosestFirstFabricTiers("leaf-1", "shared-root"),
	})
	cluster.Append(&topology.InstanceTopology{
		InstanceID:  "instance-2",
		FabricTiers: topology.ClosestFirstFabricTiers("leaf-2", "spine-2", "shared-root"),
	})
	graph := cluster.ToGraph("test", []topology.ComputeInstances{{
		Instances: map[string]string{
			"instance-1": "node-1",
			"instance-2": "node-2",
		},
	}}, 0, false)

	labels, err := NewTopologyLabeler(NewTopologyLabelKeys(nil, "")).BuildNodeLabels(graph)
	require.NoError(t, err)
	require.Equal(t, map[string]string{
		topology.FabricTierKey(0): "leaf-1",
		topology.FabricTierKey(1): "shared-root",
	}, labels["node-1"])
	require.Equal(t, map[string]string{
		topology.FabricTierKey(0): "leaf-2",
		topology.FabricTierKey(1): "spine-2",
		topology.FabricTierKey(2): "shared-root",
	}, labels["node-2"])
}

func TestApplyNodeLabelsSkipsUnnamedComputeNodes(t *testing.T) {
	unnamed := &topology.Vertex{ID: "instance-unnamed"}
	valid := &topology.Vertex{ID: "instance-valid", Name: "node-valid"}
	leaf := &topology.Vertex{
		ID: "leaf",
		Vertices: map[string]*topology.Vertex{
			unnamed.ID: unnamed,
			valid.ID:   valid,
		},
	}
	graph := &topology.Graph{
		Tiers: &topology.Vertex{Vertices: map[string]*topology.Vertex{leaf.ID: leaf}},
		Domains: topology.DomainMap{
			"accelerator": {
				"":           &topology.HostInfo{},
				"node-valid": &topology.HostInfo{},
			},
		},
	}
	labeler := &testLabeler{data: make(map[string]map[string]string)}

	err := NewTopologyLabeler(NewTopologyLabelKeys(nil, "")).ApplyNodeLabels(context.Background(), graph, labeler)

	require.NoError(t, err)
	require.NotContains(t, labeler.data, "")
	require.Equal(t, map[string]map[string]string{
		"node-valid": {
			topology.KeyTopologyXclrDomain: "accelerator",
			topology.FabricTierKey(0):      "leaf",
		},
	}, labeler.data)
}
