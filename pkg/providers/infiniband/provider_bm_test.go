/*
 * Copyright 2026 NVIDIA CORPORATION
 * SPDX-License-Identifier: Apache-2.0
 */

package infiniband

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/dsx-ai-factory/topograph/pkg/accelerator"
	"github.com/dsx-ai-factory/topograph/pkg/providers"
	"github.com/dsx-ai-factory/topograph/pkg/topology"
)

func TestProviderBMNamedLoader(t *testing.T) {
	name, loader := NamedLoaderBM()
	require.Equal(t, NAME_BM, name)
	require.NotNil(t, loader)
	// Invoke loader to verify LoaderBM wires ibNetDiscover (regression for #492).
	p, httpErr := loader(context.Background(), providers.Config{})
	require.Nil(t, httpErr)
	require.IsType(t, &ProviderBM{}, p)
	require.NotNil(t, p.(*ProviderBM).ibNetDiscover)
}

func TestProviderBMRejectsMultiRegionRequest(t *testing.T) {
	provider := &ProviderBM{}
	graph, httpErr := provider.GenerateTopologyConfig(context.Background(), nil, []topology.ComputeInstances{
		{Region: "region-1"},
		{Region: "region-2"},
	})

	require.Nil(t, graph)
	require.NotNil(t, httpErr)
	require.Equal(t, http.StatusBadRequest, httpErr.Code())
	require.ErrorContains(t, httpErr, "on-prem does not support multi-region topology requests")
}

func TestProviderBMGenerateTopologyConfig(t *testing.T) {
	// testIBNetDiscover (from common_test.go) reads the real ibnetdiscover fixture
	// captured from InfiniBand hardware in tests/output/ibnetdiscover/example.out.
	// IBNetDiscoverBM.Run (exec.Pdsh) is not testable without a running IB cluster;
	// that path remains at 0% coverage by design — only the transport layer is excluded.
	cis := []topology.ComputeInstances{
		{
			Region: "on-prem",
			Instances: map[string]string{
				"b07-p1-dgx-07-c01": "b07-p1-dgx-07-c01",
				"b07-p1-dgx-07-c02": "b07-p1-dgx-07-c02",
				"b07-p1-dgx-07-c03": "b07-p1-dgx-07-c03",
				"b07-p1-dgx-07-c04": "b07-p1-dgx-07-c04",
			},
		},
	}

	t.Run("ibnetdiscover error produces empty tiers — getIbTree treats errors as warnings", func(t *testing.T) {
		p := &ProviderBM{
			accelerator:   mustNoneDiscoverer(t),
			ibNetDiscover: &testIBNetDiscover{err: true},
		}
		graph, httpErr := p.GenerateTopologyConfig(context.Background(), nil, cis)
		require.Nil(t, httpErr)
		require.NotNil(t, graph)
		// getIbTree logs a warning on Run errors and returns an empty tree (not an error)
		require.Empty(t, graph.Tiers.Vertices)
	})

	t.Run("valid fixture produces graph with tiers", func(t *testing.T) {
		p := &ProviderBM{
			accelerator:   mustNoneDiscoverer(t),
			ibNetDiscover: &testIBNetDiscover{},
		}
		graph, httpErr := p.GenerateTopologyConfig(context.Background(), nil, cis)
		require.Nil(t, httpErr)
		require.NotNil(t, graph)
		require.NotNil(t, graph.Tiers)
		require.NotEmpty(t, graph.Tiers.Vertices)
	})
}

func TestProviderBMInstances2NodeMap(t *testing.T) {
	p := &ProviderBM{}

	t.Run("identity mapping", func(t *testing.T) {
		i2n, err := p.Instances2NodeMap(context.Background(), []string{"node-1", "node-2"})
		require.NoError(t, err)
		require.Equal(t, map[string]string{"node-1": "node-1", "node-2": "node-2"}, i2n)
	})

	t.Run("empty input returns empty map", func(t *testing.T) {
		i2n, err := p.Instances2NodeMap(context.Background(), nil)
		require.NoError(t, err)
		require.Empty(t, i2n)
	})

	t.Run("duplicate node — idempotent, cardinality matches unique keys", func(t *testing.T) {
		i2n, err := p.Instances2NodeMap(context.Background(), []string{"node-1", "node-1"})
		require.NoError(t, err)
		require.Len(t, i2n, 1)
		require.Equal(t, "node-1", i2n["node-1"])
	})
}

func TestProviderBMGetInstancesRegions(t *testing.T) {
	p := &ProviderBM{}

	t.Run("all regions are local", func(t *testing.T) {
		regions, err := p.GetInstancesRegions(context.Background(), []string{"node-1", "node-2"})
		require.NoError(t, err)
		require.Equal(t, map[string]string{"node-1": "local", "node-2": "local"}, regions)
	})

	t.Run("empty input returns empty map", func(t *testing.T) {
		regions, err := p.GetInstancesRegions(context.Background(), nil)
		require.NoError(t, err)
		require.Empty(t, regions)
	})

	t.Run("duplicate node — idempotent, cardinality matches unique keys", func(t *testing.T) {
		regions, err := p.GetInstancesRegions(context.Background(), []string{"node-1", "node-1"})
		require.NoError(t, err)
		require.Len(t, regions, 1)
		require.Equal(t, "local", regions["node-1"])
	})
}

// mustNoneDiscoverer returns an accelerator.Discoverer with source=none,
// so GenerateTopologyConfig tests focus on the IB fabric path.
func mustNoneDiscoverer(t *testing.T) accelerator.Discoverer {
	t.Helper()
	d, err := accelerator.NewCommandDiscoverer(
		accelerator.SectionFromProviderParams(map[string]any{
			"accelerator": map[string]any{"source": accelerator.SourceNone},
		}),
		nil,
	)
	require.NoError(t, err)
	return d
}

func TestLoaderBMAcceleratorSource(t *testing.T) {
	tests := []struct {
		name   string
		params map[string]any
		err    string
	}{
		{name: "no accelerator discovery by default"},
		{
			name:   "empty accelerator section disables discovery",
			params: map[string]any{"accelerator": map[string]any{}},
		},
		{
			name:   "null accelerator section",
			params: map[string]any{"accelerator": nil},
			err:    "accelerator section must be an object with a source",
		},
		{
			name: "non-empty accelerator section requires source",
			params: map[string]any{"accelerator": map[string]any{
				"nvidiaSmi": map[string]any{"gpuOperatorNamespace": "gpu-operator"},
			}},
			err: "accelerator source must be set",
		},
		{
			name: "nvidia-smi",
			params: map[string]any{"accelerator": map[string]any{
				"source": accelerator.SourceNvidiaSMI,
			}},
		},
		{
			name: "none",
			params: map[string]any{"accelerator": map[string]any{
				"source": accelerator.SourceNone,
			}},
		},
		{
			name: "Kubernetes label is unsupported",
			params: map[string]any{"accelerator": map[string]any{
				"source": accelerator.SourceKubernetesLabel,
				"kubernetesLabel": map[string]any{
					"key": "example.com/domain",
				},
			}},
			err: `accelerator source "kubernetes-label" is not supported by command discovery`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			provider, httpErr := LoaderBM(context.Background(), providers.Config{Params: test.params})
			if test.err != "" {
				require.EqualError(t, httpErr, test.err)
				return
			}
			require.Nil(t, httpErr)
			require.IsType(t, &ProviderBM{}, provider)
		})
	}
}
