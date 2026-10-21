/*
 * Copyright 2026 NVIDIA CORPORATION
 * SPDX-License-Identifier: Apache-2.0
 */

package k8s

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/dsx-ai-factory/topograph/pkg/topology"
)

func TestNodeMetadataValidation(t *testing.T) {
	for _, tc := range []struct {
		name     string
		metadata *NodeMetadata
		err      string
	}{
		{"defaults", nil, ""},
		{"empty section", &NodeMetadata{}, "nodeMetadata.instanceID is required"},
		{"missing instance", &NodeMetadata{Region: &MetadataSource{Type: "label", Key: "region"}}, "nodeMetadata.instanceID is required"},
		{"missing region", &NodeMetadata{InstanceID: &MetadataSource{Type: "label", Key: "id"}}, "nodeMetadata.region is required"},
		{"labels", &NodeMetadata{InstanceID: &MetadataSource{Type: "label", Key: "id"}, Region: &MetadataSource{Type: "label", Key: "region"}}, ""},
		{"annotations", &NodeMetadata{InstanceID: &MetadataSource{Type: "annotation", Key: "id"}, Region: &MetadataSource{Type: "annotation", Key: "region"}}, ""},
		{"invalid type", &NodeMetadata{InstanceID: &MetadataSource{Type: "field", Key: "id"}, Region: &MetadataSource{Type: "label", Key: "region"}}, "nodeMetadata.instanceID.type"},
		{"missing type", &NodeMetadata{InstanceID: &MetadataSource{Type: "label", Key: "id"}, Region: &MetadataSource{Key: "region"}}, "nodeMetadata.region.type"},
		{"missing key", &NodeMetadata{InstanceID: &MetadataSource{Type: "label"}, Region: &MetadataSource{Type: "label", Key: "region"}}, "nodeMetadata.instanceID.key"},
		{"invalid key", &NodeMetadata{InstanceID: &MetadataSource{Type: "label", Key: "id"}, Region: &MetadataSource{Type: "annotation", Key: "bad key"}}, "nodeMetadata.region.key"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.metadata.Validate()
			if tc.err == "" {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, tc.err)
			}
		})
	}
}

func TestGetNodesMetadataSources(t *testing.T) {
	for _, tc := range []struct {
		name             string
		metadata         *NodeMetadata
		instance, region string
	}{
		{"defaults", nil, "default-id", "default-region"},
		{"labels", &NodeMetadata{InstanceID: &MetadataSource{"label", "example.com/id"}, Region: &MetadataSource{"label", "example.com/region"}}, "label-id", "label-region"},
		{"annotations", &NodeMetadata{InstanceID: &MetadataSource{"annotation", "example.com/id"}, Region: &MetadataSource{"annotation", "example.com/region"}}, "annotation-id", "annotation-region"},
		{"mixed", &NodeMetadata{InstanceID: &MetadataSource{"annotation", "example.com/id"}, Region: &MetadataSource{"label", "example.com/region"}}, "annotation-id", "label-region"},
		{"missing overrides default", &NodeMetadata{InstanceID: &MetadataSource{"label", "example.com/missing"}, Region: &MetadataSource{"annotation", topology.KeyNodeRegion}}, "", "default-region"},
		{"empty overrides default", &NodeMetadata{InstanceID: &MetadataSource{"annotation", topology.KeyNodeInstance}, Region: &MetadataSource{"annotation", "example.com/empty"}}, "default-id", ""},
		{"whitespace overrides default", &NodeMetadata{InstanceID: &MetadataSource{"annotation", "example.com/whitespace"}, Region: &MetadataSource{"annotation", topology.KeyNodeRegion}}, "", "default-region"},
		{"canonical sources can cross", &NodeMetadata{InstanceID: &MetadataSource{"annotation", topology.KeyNodeRegion}, Region: &MetadataSource{"annotation", topology.KeyNodeInstance}}, "default-region", "default-id"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{
				Name:   "worker",
				Labels: map[string]string{"example.com/id": "label-id", "example.com/region": "label-region"},
				Annotations: map[string]string{
					topology.KeyNodeInstance: "default-id", topology.KeyNodeRegion: "default-region",
					"example.com/id": " annotation-id ", "example.com/region": "annotation-region",
					"example.com/empty": "", "example.com/whitespace": " \t ",
				},
			}}
			client := fake.NewClientset(node)
			ctx := WithNodeMetadata(context.Background(), tc.metadata)
			nodes, err := GetNodes(ctx, client, nil)
			require.NoError(t, err)
			require.Equal(t, tc.instance, nodes.Items[0].Annotations[topology.KeyNodeInstance])
			require.Equal(t, tc.region, nodes.Items[0].Annotations[topology.KeyNodeRegion])
			instances := GetComputeInstances(nodes)
			if tc.instance == "" || tc.region == "" {
				require.Empty(t, instances)
			} else {
				require.Equal(t, []topology.ComputeInstances{{Region: tc.region, Instances: map[string]string{tc.instance: "worker"}}}, instances)
			}
			stored, err := client.CoreV1().Nodes().Get(context.Background(), "worker", metav1.GetOptions{})
			require.NoError(t, err)
			require.Equal(t, node, stored)
			// Source selection is scoped to this request.
			defaults, err := GetNodes(context.Background(), client, nil)
			require.NoError(t, err)
			require.Equal(t, "default-id", defaults.Items[0].Annotations[topology.KeyNodeInstance])
		})
	}
}

func TestGetNodesMetadataWithoutBroker(t *testing.T) {
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "worker", Labels: map[string]string{
		"example.com/id": "id", "topology.kubernetes.io/region": "region",
	}}}
	client := fake.NewClientset(node)
	ctx := WithNodeMetadata(context.Background(), &NodeMetadata{
		InstanceID: &MetadataSource{"label", "example.com/id"},
		Region:     &MetadataSource{"label", "topology.kubernetes.io/region"},
	})
	nodes, err := GetNodes(ctx, client, nil)
	require.NoError(t, err)
	require.Equal(t, []topology.ComputeInstances{{Region: "region", Instances: map[string]string{"id": "worker"}}}, GetComputeInstances(nodes))
	require.Nil(t, node.Annotations)
	for _, action := range client.Actions() {
		require.Equal(t, "list", action.GetVerb())
		require.Equal(t, "nodes", action.GetResource().Resource)
	}
}
