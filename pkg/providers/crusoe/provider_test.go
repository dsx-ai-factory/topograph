/*
 * Copyright 2026 NVIDIA CORPORATION
 * SPDX-License-Identifier: Apache-2.0
 */

package crusoe

import (
	"bytes"
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/dsx-ai-factory/topograph/pkg/topology"
	"github.com/dsx-ai-factory/topograph/pkg/translate"
)

func node(name string, labels map[string]string) *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels},
	}
}

func TestGenerateTopologyConfig(t *testing.T) {
	ctx := context.Background()

	client := fake.NewSimpleClientset(
		node("gpu-01", map[string]string{labelPartitionID: partitionA, labelPodID: podA1}),
		node("gpu-02", map[string]string{labelPartitionID: partitionA, labelPodID: podA1}),
		node("cpu-01", nil),
	)

	provider := &Provider{client: client}

	graph, httpErr := provider.GenerateTopologyConfig(ctx, nil, []topology.ComputeInstances{{
		Region:    region,
		Instances: map[string]string{"gpu-01": "gpu-01", "gpu-02": "gpu-02", "cpu-01": "cpu-01"},
	}})
	require.Nil(t, httpErr)
	require.NotNil(t, graph.Tiers)
	// This provider discovers the switch fabric only, so it publishes no
	// accelerator domains.
	require.Empty(t, graph.Domains)

	nt, err := translate.NewNetworkTopology(graph, &translate.Config{Plugin: topology.TopologyTree})
	require.NoError(t, err)

	var buf bytes.Buffer
	require.Nil(t, nt.Generate(&buf))
	require.Contains(t, buf.String(), "SwitchName="+podA1+" Nodes=gpu-[01-02]")
	require.Contains(t, buf.String(), "SwitchName="+cpuPod+" Nodes=cpu-01")
}

func TestGenerateTopologyConfigHonoursNodeSelector(t *testing.T) {
	ctx := context.Background()

	client := fake.NewSimpleClientset(
		node("gpu-01", map[string]string{
			labelPartitionID:                    partitionA,
			labelPodID:                          podA1,
			"slurm.crusoe.ai/compute-node-type": "true",
		}),
		node("control-plane-01", nil),
	)

	provider := &Provider{
		client: client,
		nodeListOpt: &metav1.ListOptions{
			LabelSelector: "slurm.crusoe.ai/compute-node-type=true",
		},
	}

	graph, httpErr := provider.GenerateTopologyConfig(ctx, nil, []topology.ComputeInstances{{
		Region: region,
		Instances: map[string]string{
			"gpu-01":           "gpu-01",
			"control-plane-01": "control-plane-01",
		},
	}})
	require.Nil(t, httpErr)

	nt, err := translate.NewNetworkTopology(graph, &translate.Config{Plugin: topology.TopologyTree})
	require.NoError(t, err)

	var buf bytes.Buffer
	require.Nil(t, nt.Generate(&buf))
	require.Contains(t, buf.String(), "SwitchName="+podA1+" Nodes=gpu-01")
	// The selector filtered the control-plane node out before the provider saw
	// it, so it has no fabric tiers. The request still named it, so it lands
	// under no-topology rather than disappearing.
	require.Contains(t, buf.String(), "SwitchName=no-topology Nodes=control-plane-01")
	require.NotContains(t, buf.String(), cpuPod)
}

func TestGenerateTopologyConfigRejectsMultiRegion(t *testing.T) {
	provider := &Provider{client: fake.NewSimpleClientset()}

	graph, httpErr := provider.GenerateTopologyConfig(context.Background(), nil, []topology.ComputeInstances{
		{Region: "one", Instances: map[string]string{"gpu-01": "gpu-01"}},
		{Region: "two", Instances: map[string]string{"gpu-02": "gpu-02"}},
	})

	require.Nil(t, graph)
	require.ErrorContains(t, httpErr, "does not support multi-region topology requests")
}

func TestInstanceMapper(t *testing.T) {
	ctx := context.Background()
	provider := &Provider{}
	nodes := []string{"gpu-01", "gpu-02"}

	i2n, err := provider.Instances2NodeMap(ctx, nodes)
	require.NoError(t, err)
	require.Equal(t, map[string]string{"gpu-01": "gpu-01", "gpu-02": "gpu-02"}, i2n)

	regions, err := provider.GetInstancesRegions(ctx, nodes)
	require.NoError(t, err)
	require.Equal(t, map[string]string{"gpu-01": region, "gpu-02": region}, regions)
}

func TestGetNodeAnnotations(t *testing.T) {
	annotations, err := GetNodeAnnotations(context.Background(), "gpu-01")
	require.NoError(t, err)
	require.Equal(t, map[string]string{
		topology.KeyNodeInstance: "gpu-01",
		topology.KeyNodeRegion:   region,
	}, annotations)
}
