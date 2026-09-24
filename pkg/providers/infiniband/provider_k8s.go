/*
 * Copyright 2025 NVIDIA CORPORATION
 * SPDX-License-Identifier: Apache-2.0
 */

package infiniband

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

const NAME_K8S = "infiniband-k8s"

type ProviderK8S struct {
	config      *rest.Config
	client      *kubernetes.Clientset
	nodeListOpt *metav1.ListOptions
	accelerator accelerator.Discoverer
}

func NamedLoaderK8S() (string, providers.Loader) {
	return NAME_K8S, LoaderK8S
}

func LoaderK8S(ctx context.Context, config providers.Config) (providers.Provider, *httperr.Error) {
	nodeListOpt, err := k8s.NodeListOptions(config.Params)
	if err != nil {
		return nil, httperr.NewError(http.StatusBadRequest, err.Error())
	}
	acceleratorDiscoverer, err := accelerator.NewKubernetesDiscoverer(
		accelerator.SectionFromProviderParams(config.Params),
	)
	if err != nil {
		return nil, httperr.NewError(http.StatusBadRequest, err.Error())
	}

	cfg, err := rest.InClusterConfig()
	if err != nil {
		return nil, httperr.NewError(http.StatusBadGateway, err.Error())
	}

	client, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, httperr.NewError(http.StatusBadGateway, err.Error())
	}

	return &ProviderK8S{
		config:      cfg,
		client:      client,
		nodeListOpt: nodeListOpt,
		accelerator: acceleratorDiscoverer,
	}, nil
}

func (p *ProviderK8S) GenerateTopologyConfig(ctx context.Context, _ *int, cis []topology.ComputeInstances) (*topology.Graph, *httperr.Error) {
	if len(cis) > 1 {
		return nil, httperr.NewError(http.StatusBadRequest, "on-prem does not support multi-region topology requests")
	}

	nodes, err := k8s.GetNodes(ctx, p.client, p.nodeListOpt)
	if err != nil {
		return nil, httperr.NewError(http.StatusBadGateway, err.Error())
	}

	domainMap, err := accelerator.DiscoverKubernetesDomains(ctx, p.accelerator, nodes, cis)
	if err != nil {
		return nil, httperr.NewError(http.StatusBadGateway, fmt.Sprintf("failed to discover accelerator domains: %v", err))
	}

	ibnetdiscover := NewIBNetDiscoverK8S(p.config, p.client)
	treeRoot, err := getIbTree(ctx, cis, ibnetdiscover)
	if err != nil {
		return nil, httperr.NewError(http.StatusInternalServerError, fmt.Sprintf("getIbTree failed: %v", err))
	}

	return &topology.Graph{
		Tiers:   treeRoot,
		Domains: domainMap,
	}, nil
}
