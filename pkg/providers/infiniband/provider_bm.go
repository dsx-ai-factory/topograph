// SPDX-FileCopyrightText: Copyright (c) 2024 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

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

// GetNodeInstances implements slurm.instanceMapper
func (p *ProviderBM) GetNodeInstances(_ context.Context, nodes []string) ([]topology.ComputeInstances, error) {
	return providers.SelfNamedInstances(nodes, "local"), nil
}
