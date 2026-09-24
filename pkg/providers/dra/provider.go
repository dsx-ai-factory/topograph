/*
 * Copyright 2025 NVIDIA CORPORATION
 * SPDX-License-Identifier: Apache-2.0
 */

package dra

import (
	"context"
	"fmt"
	"net/http"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/dsx-ai-factory/topograph/internal/httperr"
	"github.com/dsx-ai-factory/topograph/internal/k8s"
	"github.com/dsx-ai-factory/topograph/pkg/accelerator"
	"github.com/dsx-ai-factory/topograph/pkg/providers"
	"github.com/dsx-ai-factory/topograph/pkg/topology"
)

const (
	NAME = "dra"

	defaultDomainLabel = "nvidia.com/gpu.clique"
)

type Provider struct {
	config           *rest.Config
	client           kubernetes.Interface
	nodeListOpt      *metav1.ListOptions
	accelerator      accelerator.Discoverer
	acceleratorLabel string
}

func NamedLoader() (string, providers.Loader) {
	return NAME, Loader
}

func Loader(ctx context.Context, config providers.Config) (providers.Provider, *httperr.Error) {
	nodeListOpt, err := k8s.NodeListOptions(config.Params)
	if err != nil {
		return nil, httperr.NewError(http.StatusBadRequest, err.Error())
	}
	acceleratorDiscoverer, acceleratorLabel, err := newAcceleratorDiscoverer(config.Params)
	if err != nil {
		return nil, httperr.NewError(http.StatusBadRequest, err.Error())
	}

	cfg, err := rest.InClusterConfig()
	if err != nil {
		return nil, httperr.NewError(http.StatusBadGateway, err.Error())
	}

	if err := k8s.ConfigureClientRateLimits(cfg); err != nil {
		return nil, httperr.NewError(http.StatusBadRequest, err.Error())
	}

	client, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, httperr.NewError(http.StatusBadGateway, err.Error())
	}

	return &Provider{
		config:           cfg,
		client:           client,
		nodeListOpt:      nodeListOpt,
		accelerator:      acceleratorDiscoverer,
		acceleratorLabel: acceleratorLabel,
	}, nil
}

func newAcceleratorDiscoverer(params map[string]any) (accelerator.Discoverer, string, error) {
	section := accelerator.SectionFromProviderParams(params)
	if !section.Present() {
		section = accelerator.KubernetesLabelSection(defaultDomainLabel)
	}
	acceleratorConfig, err := accelerator.ParseConfig(section)
	if err != nil {
		return nil, "", err
	}
	if acceleratorConfig.Source != accelerator.SourceKubernetesLabel {
		return nil, "", fmt.Errorf("dra provider supports only accelerator source %q", accelerator.SourceKubernetesLabel)
	}
	acceleratorDiscoverer, err := accelerator.NewKubernetesDiscovererFromConfig(acceleratorConfig)
	if err != nil {
		return nil, "", err
	}
	return acceleratorDiscoverer, acceleratorConfig.KubernetesLabel.Key, nil
}

func (p *Provider) GenerateTopologyConfig(ctx context.Context, _ *int, instances []topology.ComputeInstances) (*topology.Graph, *httperr.Error) {
	nodes, err := k8s.GetNodes(ctx, p.client, p.nodeListOpt)
	if err != nil {
		return nil, httperr.NewError(http.StatusBadGateway, err.Error())
	}

	domainMap, err := accelerator.DiscoverKubernetesDomains(ctx, p.accelerator, nodes, instances)
	if err != nil {
		return nil, httperr.NewError(http.StatusBadGateway, fmt.Sprintf("failed to discover accelerator domains: %v", err))
	}

	if len(domainMap) == 0 {
		return nil, httperr.NewError(http.StatusBadGateway,
			fmt.Sprintf("no matching nodes found; check label %q and annotations %q and %q",
				p.acceleratorLabel, topology.KeyNodeRegion, topology.KeyNodeInstance))
	}

	return &topology.Graph{
		Domains: domainMap,
	}, nil
}

func GetNodeAnnotations(_ context.Context, hostname string) (map[string]string, error) {
	return accelerator.BaseKubernetesNodeAnnotations(hostname), nil
}
