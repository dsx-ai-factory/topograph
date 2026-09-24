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
	"k8s.io/client-go/kubernetes/fake"

	"github.com/dsx-ai-factory/topograph/pkg/topology"
	"github.com/dsx-ai-factory/topograph/pkg/translate"
)

// Labels captured from four gb200-186gb-nvl-ib.4x nodes: two NVLink cliques
// inside a single InfiniBand partition.
//
// The provider reads Node labels and nothing else — no NVML, no IMEX, no GPU —
// so replaying these labels is a complete reproduction of that cluster for
// topology purposes.
const (
	gb200Partition = "70418291-6cbd-405c-b0b5-c83533c0d4b7"

	gb200CliqueA = "29d9a0b8-948d-4a61-8b9e-fbbbf06c521b.32766"
	gb200CliqueB = "ad92df3f-0583-4633-883a-37db088fc1a7.32766"

	gb200PodA = "4d6f6ef5-b773-39bb-a4f4-253cb43b970b"
	gb200PodB = "83bd2d3f-f6ec-142e-76bb-8089f137f93b"
)

func gb200Node(name, clique, pod string) nodeMetadata {
	return nodeMetadata{
		InstanceID: name,
		Labels: map[string]string{
			labelPartitionID:                    gb200Partition,
			labelPodID:                          pod,
			labelGPUClique:                      clique,
			"slurm.crusoe.ai/compute-node-type": "true",
		},
	}
}

func gb200Fixture() []nodeMetadata {
	return []nodeMetadata{
		gb200Node("np-c0c1035f-3", gb200CliqueA, gb200PodA),
		gb200Node("np-c0c1035f-4", gb200CliqueA, gb200PodA),
		gb200Node("np-c0c1035f-5", gb200CliqueB, gb200PodB),
		gb200Node("np-c0c1035f-6", gb200CliqueB, gb200PodB),
	}
}

// TestGB200BlockTopology replays the captured cluster and pins the rendered
// block file.
//
// Assert on the domain names, not the grouping. On this hardware each clique
// happened to get its own InfiniBand pod, so grouping by pod produces the same
// two pairs as grouping by clique. A test that only checked "two blocks of two"
// would pass even if the provider read the wrong label.
func TestGB200BlockTopology(t *testing.T) {
	got := render(t, gb200Fixture(), &translate.Config{Plugin: topology.TopologyBlock})

	// Block IDs come from the sorted domain names, so the lower clique is
	// block001. Emission order comes from the tree walk and is a function of the
	// fabric shape, not of the domains, so it is stable for a given input but
	// not necessarily ascending. Pinned whole because that stability is the
	// property worth protecting: a reordering would rewrite the topology
	// ConfigMap on every regeneration.
	expected := "# block001=" + gb200CliqueA + "\n" +
		"BlockName=block001 Nodes=np-c0c1035f-[3-4]\n" +
		"# block002=" + gb200CliqueB + "\n" +
		"BlockName=block002 Nodes=np-c0c1035f-[5-6]\n" +
		"BlockSizes=2,4\n"
	require.Equal(t, expected, got)

	// Neither pod ID reaches the block domains. Only the cliques do.
	require.NotContains(t, got, gb200PodA)
	require.NotContains(t, got, gb200PodB)
	require.NotContains(t, got, gb200Partition)
}

// TestGB200BlockIgnoresPodWhenTheyDisagree covers the case the hardware never
// produced: a clique that spans two InfiniBand pods.
//
// On the captured cluster clique and pod agreed, so that fixture cannot tell the
// two apart. Moving one node to a different pod while leaving its clique alone
// must not split its block.
func TestGB200BlockIgnoresPodWhenTheyDisagree(t *testing.T) {
	nodes := gb200Fixture()
	// np-c0c1035f-4 keeps clique A but moves to pod B.
	nodes[1] = gb200Node("np-c0c1035f-4", gb200CliqueA, gb200PodB)

	got := render(t, nodes, &translate.Config{Plugin: topology.TopologyBlock})

	// Still two blocks split by clique, not three split by pod.
	require.Contains(t, got, "BlockName=block001 Nodes=np-c0c1035f-[3-4]")
	require.Contains(t, got, "BlockName=block002 Nodes=np-c0c1035f-[5-6]")
}

// TestGB200TreeUnchangedWithoutClique is the regression guard for the existing
// InfiniBand SKUs. Absence of the clique label is the only thing that selects
// the tree-only path, and block work must leave it alone.
func TestGB200TreeUnchangedWithoutClique(t *testing.T) {
	nodes := gb200Fixture()
	for i := range nodes {
		delete(nodes[i].Labels, labelGPUClique)
	}

	topo, httpErr := buildClusterTopology(nodes, nil)
	require.Nil(t, httpErr)

	instances := make(map[string]string, len(nodes))
	for _, node := range nodes {
		instances[node.InstanceID] = node.InstanceID
	}
	graph := topo.ToGraph(NAME, []topology.ComputeInstances{
		{Region: region, Instances: instances},
	}, 0, false)

	// No clique means no accelerator domain, so nothing for topology/block.
	require.Empty(t, graph.Domains)

	nt, err := translate.NewNetworkTopology(graph, &translate.Config{Plugin: topology.TopologyTree})
	require.NoError(t, err)

	var buf bytes.Buffer
	require.Nil(t, nt.Generate(&buf))
	require.Contains(t, buf.String(), "SwitchName="+gb200PodA+" Nodes=np-c0c1035f-[3-4]")
	require.Contains(t, buf.String(), "SwitchName="+gb200PodB+" Nodes=np-c0c1035f-[5-6]")
	require.Contains(t, buf.String(), "SwitchName="+gb200Partition+" Switches=")
}

// TestGB200LiveProviderThroughKubernetes runs the same labels through the
// Kubernetes client path rather than the helper, so the node listing and label
// reading are covered end to end.
func TestGB200LiveProviderThroughKubernetes(t *testing.T) {
	ctx := context.Background()

	fixture := gb200Fixture()
	client := fake.NewSimpleClientset(
		node(fixture[0].InstanceID, fixture[0].Labels),
		node(fixture[1].InstanceID, fixture[1].Labels),
		node(fixture[2].InstanceID, fixture[2].Labels),
		node(fixture[3].InstanceID, fixture[3].Labels),
	)
	provider := &Provider{client: client}

	instances := map[string]string{}
	for _, n := range gb200Fixture() {
		instances[n.InstanceID] = n.InstanceID
	}

	graph, httpErr := provider.GenerateTopologyConfig(ctx, nil, []topology.ComputeInstances{
		{Region: region, Instances: instances},
	})
	require.Nil(t, httpErr)

	require.Len(t, graph.Domains, 2)
	require.Contains(t, graph.Domains, gb200CliqueA)
	require.Contains(t, graph.Domains, gb200CliqueB)
	require.Len(t, graph.Domains[gb200CliqueA], 2)
	require.Len(t, graph.Domains[gb200CliqueB], 2)
}
