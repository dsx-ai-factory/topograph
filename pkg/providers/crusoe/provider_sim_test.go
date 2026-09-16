/*
 * Copyright 2026 NVIDIA CORPORATION
 * SPDX-License-Identifier: Apache-2.0
 */

package crusoe

import (
	"context"
	"sort"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/dsx-ai-factory/topograph/pkg/engines/slurm"
	"github.com/dsx-ai-factory/topograph/pkg/models"
	"github.com/dsx-ai-factory/topograph/pkg/providers"
	"github.com/dsx-ai-factory/topograph/pkg/topology"
)

func TestLoaderSimBadModel(t *testing.T) {
	_, err := LoaderSim(context.Background(), providers.Config{
		Params: map[string]any{"modelFileName": "no-such-model.yaml"},
	})
	require.NotNil(t, err)
	require.ErrorContains(t, err, "failed to load model file")
}

func TestLoaderSimMissingModel(t *testing.T) {
	_, err := LoaderSim(context.Background(), providers.Config{Params: map[string]any{}})
	require.NotNil(t, err)
	require.ErrorContains(t, err, "no model file name for simulation")
}

func TestProviderSim(t *testing.T) {
	ctx := context.Background()

	provider, httpErr := LoaderSim(ctx, providers.Config{
		Params: map[string]any{"modelFileName": "crusoe-small.yaml"},
	})
	require.Nil(t, httpErr)

	sim, ok := provider.(*simProvider)
	require.True(t, ok)

	cis, httpErr := sim.GetComputeInstances(ctx)
	require.Nil(t, httpErr)
	require.Len(t, cis, 1)

	graph, httpErr := provider.GenerateTopologyConfig(ctx, nil, cis)
	require.Nil(t, httpErr)

	data, httpErr := slurm.GenerateOutput(ctx, graph, nil)
	require.Nil(t, httpErr)

	expected := `SwitchName=crusoe Switches=0cf47922-9f9e-4a2e-b187-5c51697e7739,1d0e8b3a-2c47-4f16-9a55-7be3d9c4f082,cpu-partition
SwitchName=0cf47922-9f9e-4a2e-b187-5c51697e7739 Switches=4f6a2e91-8d75-4c30-b1e8-6a9c3f5d7b24,9b1c7d54-3e08-4a92-8f61-2d5a4c7b0e13
SwitchName=1d0e8b3a-2c47-4f16-9a55-7be3d9c4f082 Switches=7c3d9f68-5b21-4e84-a0f3-9e1b8d2c6a57
SwitchName=cpu-partition Switches=cpu-pod
SwitchName=4f6a2e91-8d75-4c30-b1e8-6a9c3f5d7b24 Nodes=gpu-[03-04]
SwitchName=9b1c7d54-3e08-4a92-8f61-2d5a4c7b0e13 Nodes=gpu-[01-02]
SwitchName=7c3d9f68-5b21-4e84-a0f3-9e1b8d2c6a57 Nodes=gpu-[05-06]
SwitchName=cpu-pod Nodes=cpu-[01-02]
`
	require.Equal(t, expected, string(data))
}

func TestProviderSimRejectsMultiRegion(t *testing.T) {
	ctx := context.Background()

	provider, httpErr := LoaderSim(ctx, providers.Config{
		Params: map[string]any{"modelFileName": "crusoe-small.yaml"},
	})
	require.Nil(t, httpErr)

	_, httpErr = provider.GenerateTopologyConfig(ctx, nil, []topology.ComputeInstances{
		{Region: "one", Instances: map[string]string{"gpu-01": "gpu-01"}},
		{Region: "two", Instances: map[string]string{"gpu-02": "gpu-02"}},
	})
	require.NotNil(t, httpErr)
	require.ErrorContains(t, httpErr, "does not support multi-region")
}

// TestProviderSimBlockTopology covers the bridge from the model's shared
// accelerator-domain annotation onto the clique label the provider reads.
// Without it a model following the shared convention silently produces no
// blocks, and the simulation would not exercise the block path at all.
func TestProviderSimBlockTopology(t *testing.T) {
	ctx := context.Background()

	provider, httpErr := LoaderSim(ctx, providers.Config{
		Params: map[string]any{"modelFileName": "crusoe-small.yaml"},
	})
	require.Nil(t, httpErr)

	sim, ok := provider.(*simProvider)
	require.True(t, ok)

	cis, httpErr := sim.GetComputeInstances(ctx)
	require.Nil(t, httpErr)

	graph, httpErr := provider.GenerateTopologyConfig(ctx, nil, cis)
	require.Nil(t, httpErr)

	// Only the nodes under pod-b1 carry an accelerator domain in the model.
	require.Len(t, graph.Domains, 1)
	require.Contains(t, graph.Domains, "29d9a0b8-948d-4a61-8b9e-fbbbf06c521b.32766")

	data, httpErr := slurm.GenerateOutput(ctx, graph, map[string]any{"plugin": topology.TopologyBlock})
	require.Nil(t, httpErr)
	require.Equal(t,
		"# block001=29d9a0b8-948d-4a61-8b9e-fbbbf06c521b.32766\n"+
			"BlockName=block001 Nodes=gpu-[05-06]\n"+
			"BlockSizes=2\n",
		string(data))
}

// TestSimNodeLabelsPrefersExplicitClique confirms a model that sets the live
// label directly is left alone rather than overwritten by the annotation.
func TestSimNodeLabelsPrefersExplicitClique(t *testing.T) {
	node := &models.Node{Annotations: map[string]string{
		"accelerator.topology.test/domain": "from-annotation",
	}}
	node.Labels = map[string]string{labelGPUClique: "from-label"}

	require.Equal(t, "from-label", simNodeLabels(node)[labelGPUClique])
}

// TestProviderSimStandardModel runs the provider against a model written for the
// shared convention rather than for Crusoe, which is the case the annotation
// bridge exists to serve.
//
// It also guards the domain value. Upstream models name NVLink domains "nvl-2-1"
// and similar, so any decoration the provider added on top would surface here as
// a doubled prefix rather than the modeled domain.
func TestProviderSimStandardModel(t *testing.T) {
	ctx := context.Background()

	provider, httpErr := LoaderSim(ctx, providers.Config{
		Params: map[string]any{"modelFileName": "nvl72.yaml"},
	})
	require.Nil(t, httpErr)

	sim, ok := provider.(*simProvider)
	require.True(t, ok)

	cis, httpErr := sim.GetComputeInstances(ctx)
	require.Nil(t, httpErr)

	graph, httpErr := provider.GenerateTopologyConfig(ctx, nil, cis)
	require.Nil(t, httpErr)

	domains := make([]string, 0, len(graph.Domains))
	for domain := range graph.Domains {
		domains = append(domains, domain)
	}
	sort.Strings(domains)
	require.Equal(t, []string{"nvl-1-1", "nvl-1-2", "nvl-2-1", "nvl-2-2"}, domains)
}
