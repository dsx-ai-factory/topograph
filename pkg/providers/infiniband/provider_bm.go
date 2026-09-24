/*
 * Copyright 2024 NVIDIA CORPORATION
 * SPDX-License-Identifier: Apache-2.0
 */

package infiniband

import (
	"context"
	"fmt"
	"net/http"

	"github.com/dsx-ai-factory/topograph/internal/httperr"
	"github.com/dsx-ai-factory/topograph/pkg/accelerator"
	"github.com/dsx-ai-factory/topograph/pkg/providers"
	"github.com/dsx-ai-factory/topograph/pkg/topology"
)

const NAME_BM = "infiniband-bm"

type ProviderBM struct {
	accelerator   accelerator.Discoverer
	ibNetDiscover IBNetDiscover
}

func NamedLoaderBM() (string, providers.Loader) {
	return NAME_BM, LoaderBM
}

func LoaderBM(_ context.Context, providerConfig providers.Config) (providers.Provider, *httperr.Error) {
	discoverer, err := accelerator.NewCommandDiscoverer(
		accelerator.SectionFromProviderParams(providerConfig.Params),
		pdshNvidiaSMIRunner{},
	)
	if err != nil {
		return nil, httperr.NewError(http.StatusBadRequest, err.Error())
	}

	return &ProviderBM{accelerator: discoverer, ibNetDiscover: &IBNetDiscoverBM{}}, nil
}

func (p *ProviderBM) GenerateTopologyConfig(ctx context.Context, _ *int, cis []topology.ComputeInstances) (*topology.Graph, *httperr.Error) {
	if len(cis) > 1 {
		return nil, httperr.NewError(http.StatusBadRequest, "on-prem does not support multi-region topology requests")
	}

	targets := accelerator.TargetsFromComputeInstances(cis)
	assignments, err := p.accelerator.Discover(ctx, targets)
	if err != nil {
		return nil, httperr.NewError(http.StatusInternalServerError, fmt.Sprintf("failed to discover accelerator domains: %v", err))
	}
	domainMap := accelerator.DomainMapFromAssignments(assignments, targets)

	treeRoot, err := getIbTree(ctx, cis, p.ibNetDiscover)
	if err != nil {
		return nil, httperr.NewError(http.StatusInternalServerError, fmt.Sprintf("getIbTree failed: %v", err))
	}

	return &topology.Graph{
		Tiers:   treeRoot,
		Domains: domainMap,
	}, nil
}

// Instances2NodeMap implements slurm.instanceMapper
func (p *ProviderBM) Instances2NodeMap(ctx context.Context, nodes []string) (map[string]string, error) {
	i2n := make(map[string]string)
	for _, node := range nodes {
		i2n[node] = node
	}

	return i2n, nil
}

// GetInstancesRegions implements slurm.instanceMapper
func (p *ProviderBM) GetInstancesRegions(ctx context.Context, nodes []string) (map[string]string, error) {
	res := make(map[string]string)
	for _, node := range nodes {
		res[node] = "local"
	}
	return res, nil
}
