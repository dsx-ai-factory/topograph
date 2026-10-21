/*
 * Copyright 2026 NVIDIA CORPORATION
 * SPDX-License-Identifier: Apache-2.0
 */

package server

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/dsx-ai-factory/topograph/internal/httperr"
	"github.com/dsx-ai-factory/topograph/internal/k8s"
	"github.com/dsx-ai-factory/topograph/pkg/config"
	"github.com/dsx-ai-factory/topograph/pkg/engines"
	"github.com/dsx-ai-factory/topograph/pkg/providers"
	"github.com/dsx-ai-factory/topograph/pkg/registry"
	"github.com/dsx-ai-factory/topograph/pkg/topology"
)

type metadataEngine struct {
	resolvingEngine
	resolve func(context.Context) []topology.ComputeInstances
}

func (e *metadataEngine) ResolveComputeInstances(ctx context.Context, _ []topology.ComputeInstances, _ any) ([]topology.ComputeInstances, *httperr.Error) {
	e.received = e.resolve(ctx)
	return e.received, nil
}

type metadataProvider struct {
	generate func(context.Context, []topology.ComputeInstances)
}

func (p *metadataProvider) GenerateTopologyConfig(ctx context.Context, _ *int, instances []topology.ComputeInstances) (*topology.Graph, *httperr.Error) {
	p.generate(ctx, instances)
	return &topology.Graph{}, nil
}

func TestProcessTopologyRequestUsesNodeMetadata(t *testing.T) {
	// Only existing metadata is present: no broker annotations or DaemonSet.
	client := fake.NewClientset(&corev1.Node{ObjectMeta: metav1.ObjectMeta{
		Name: "worker", Labels: map[string]string{"example.com/id": "instance", "example.com/region": "region"},
	}})
	want := []topology.ComputeInstances{{Region: "region", Instances: map[string]string{"instance": "worker"}}}
	read := func(ctx context.Context) []topology.ComputeInstances {
		nodes, err := k8s.GetNodes(ctx, client, nil)
		require.NoError(t, err)
		instances := k8s.GetComputeInstances(nodes)
		require.Equal(t, want, instances)
		return instances
	}
	previousServer := srv
	srv = &HttpServer{cfg: &config.Config{NodeMetadata: &k8s.NodeMetadata{
		InstanceID: &k8s.MetadataSource{Type: "label", Key: "example.com/id"},
		Region:     &k8s.MetadataSource{Type: "label", Key: "example.com/region"},
	}}}
	const name = "metadata-test"
	previousEngine, hadEngine := registry.Engines[name]
	previousProvider, hadProvider := registry.Providers[name]
	t.Cleanup(func() {
		srv = previousServer
		delete(registry.Engines, name)
		delete(registry.Providers, name)
		if hadEngine {
			registry.Engines[name] = previousEngine
		}
		if hadProvider {
			registry.Providers[name] = previousProvider
		}
	})
	eng := &metadataEngine{resolve: read}
	registry.Engines[name] = func(ctx context.Context, _ engines.Config) (engines.Engine, *httperr.Error) {
		read(ctx)
		return eng, nil
	}
	providerCalled := false
	registry.Providers[name] = func(ctx context.Context, _ providers.Config) (providers.Provider, *httperr.Error) {
		read(ctx)
		return &metadataProvider{generate: func(ctx context.Context, instances []topology.ComputeInstances) {
			providerCalled = true
			require.Equal(t, want, instances)
			read(ctx)
		}}, nil
	}
	_, httpErr := processTopologyRequest(&topology.Request{
		Engine: topology.Engine{Name: name}, Provider: topology.Provider{Name: name},
	})
	require.Nil(t, httpErr)
	require.True(t, providerCalled)
	require.Equal(t, want, eng.received)
}
