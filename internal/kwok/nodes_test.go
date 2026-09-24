/*
 * Copyright 2026 NVIDIA CORPORATION
 * SPDX-License-Identifier: Apache-2.0
 */

package kwok

import (
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/dsx-ai-factory/topograph/pkg/models"
	"github.com/dsx-ai-factory/topograph/pkg/topology"
)

func TestNodesFromModel(t *testing.T) {
	model, err := models.NewModelFromFile("../../tests/models/small-tree.yaml")
	require.NoError(t, err)

	nodes, err := NodesFromModel(model, Capacity{
		CPU:              "8",
		Memory:           "64Gi",
		Pods:             "42",
		EphemeralStorage: "1Ti",
		GPUs:             8,
	})
	require.NoError(t, err)
	require.Len(t, nodes, 6)

	node := nodes[0]
	require.Equal(t, "i21", node.Name)
	require.Equal(t, map[string]string{
		NodeSelectorKey:            NodeSelectorValue,
		models.LabelTopologyRegion: "none",
	}, node.Labels)
	require.Equal(t, map[string]string{
		NodeSelectorKey:                    NodeSelectorValue,
		topology.KeyNodeInstance:           "i-I21",
		topology.KeyNodeRegion:             "none",
		"accelerator.topology.test/domain": "nvl2",
	}, node.Annotations)
	require.Equal(t, "kwok://i21", node.Spec.ProviderID)
	require.Equal(t, resource.MustParse("8"), node.Status.Capacity[corev1.ResourceCPU])
	require.Equal(t, resource.MustParse("64Gi"), node.Status.Capacity[corev1.ResourceMemory])
	require.Equal(t, resource.MustParse("42"), node.Status.Capacity[corev1.ResourcePods])
	require.Equal(t, resource.MustParse("1Ti"), node.Status.Capacity[corev1.ResourceEphemeralStorage])
	require.Equal(t, resource.MustParse("8"), node.Status.Capacity[corev1.ResourceName(defaultGPUResourceName)])
	require.Equal(t, node.Status.Capacity, node.Status.Allocatable)
}

func TestNodesFromModelUsesDerivedMetadata(t *testing.T) {
	model, err := models.NewModelFromFile("../../tests/models/medium.yaml")
	require.NoError(t, err)

	nodes, err := NodesFromModel(model, DefaultCapacity())
	require.NoError(t, err)

	var node *corev1.Node
	for _, candidate := range nodes {
		if candidate.Name == "1301" {
			node = candidate
			break
		}
	}
	require.NotNil(t, node)
	require.Equal(t, "i-1301", node.Annotations[topology.KeyNodeInstance])
	require.Equal(t, "us-west", node.Annotations[topology.KeyNodeRegion])
	require.Equal(t, "us-west", node.Labels[models.LabelTopologyRegion])
	require.Equal(t, "zone2", node.Labels[models.LabelTopologyZone])
	require.Equal(t, "nvl3", node.Annotations["accelerator.topology.test/domain"])
}

func TestNodesFromModelKeepsRequiredMetadata(t *testing.T) {
	model, err := models.NewModelFromFile("../../tests/models/small-tree.yaml")
	require.NoError(t, err)

	instance := model.Nodes["I21"]
	instance.Labels[NodeSelectorKey] = "overridden"
	instance.Labels["custom-label"] = "value"
	instance.Annotations[NodeSelectorKey] = "overridden"
	instance.Annotations[topology.KeyNodeInstance] = "overridden"
	instance.Annotations[topology.KeyNodeRegion] = "overridden"
	instance.Annotations["custom-annotation"] = "value"

	nodes, err := NodesFromModel(model, DefaultCapacity())
	require.NoError(t, err)

	node := nodes[0]
	require.Equal(t, NodeSelectorValue, node.Labels[NodeSelectorKey])
	require.Equal(t, "value", node.Labels["custom-label"])
	require.Equal(t, NodeSelectorValue, node.Annotations[NodeSelectorKey])
	require.Equal(t, "i-I21", node.Annotations[topology.KeyNodeInstance])
	require.Equal(t, "none", node.Annotations[topology.KeyNodeRegion])
	require.Equal(t, "value", node.Annotations["custom-annotation"])
}

func TestMarshalNodeManifest(t *testing.T) {
	model, err := models.NewModelFromFile("../../tests/models/small-tree.yaml")
	require.NoError(t, err)
	nodes, err := NodesFromModel(model, DefaultCapacity())
	require.NoError(t, err)

	data, err := MarshalNodeManifest(nodes)
	require.NoError(t, err)

	manifest := string(data)
	require.Contains(t, manifest, "apiVersion: v1")
	require.Contains(t, manifest, "kind: List")
	require.Contains(t, manifest, "name: i21")
	require.Contains(t, manifest, "topograph.run/instance: i-I21")
	require.Contains(t, manifest, "accelerator.topology.test/domain: nvl2")
}

func TestCapacityRejectsInvalidQuantities(t *testing.T) {
	_, err := (Capacity{CPU: "not-a-quantity"}).ResourceList()
	require.ErrorContains(t, err, `invalid cpu quantity "not-a-quantity"`)
}

func TestKubernetesNodeName(t *testing.T) {
	require.Equal(t, "n-i21", kubernetesNodeName("n-I21"))
	require.Equal(t, "node-name", kubernetesNodeName(".Node_Name."))
	require.Equal(t, "node", kubernetesNodeName("___"))
}
