// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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

package nscale

import (
	"context"
	"fmt"
	"net/http"

	"github.com/dsx-ai-factory/topograph/internal/httperr"
	"github.com/dsx-ai-factory/topograph/pkg/models"
	"github.com/dsx-ai-factory/topograph/pkg/providers"
	"github.com/dsx-ai-factory/topograph/pkg/topology"
)

const (
	NAME_SIM = "nscale-sim"

	errTopology = iota
)

type simClient struct {
	model       *models.Model
	instanceIDs []string
	apiErr      int
}

type simProvider struct {
	baseProvider
	*providers.BaseSimProvider
}

func (c *simClient) Topology(ctx context.Context, _ string, pageSize, offset int) ([]InstanceTopology, *httperr.Error) {
	if c.apiErr == errTopology {
		return nil, httperr.NewError(http.StatusBadGateway, providers.ErrAPIError.Error())
	}

	resp := make([]InstanceTopology, 0, pageSize)

	var indx int
	for indx = offset; indx < offset+pageSize && indx < len(c.instanceIDs); indx++ {
		node, exists := c.model.Nodes[c.instanceIDs[indx]]
		if !exists {
			continue
		}
		nl := node.NetLayers
		path := make([]string, len(nl))
		for i, v := range nl {
			path[len(nl)-1-i] = v
		}
		instance := InstanceTopology{
			ServerID:    node.ID,
			NetworkPath: path,
		}
		domainID := node.AcceleratorDomain()
		if domainID != "" {
			instance.BlockID = &domainID
		}

		resp = append(resp, instance)
	}

	return resp, nil
}

func NamedLoaderSim() (string, providers.Loader) {
	return NAME_SIM, LoaderSim
}

func LoaderSim(_ context.Context, cfg providers.Config) (providers.Provider, *httperr.Error) {
	p, err := providers.GetSimulationParams(cfg.Params)
	if err != nil {
		return nil, httperr.NewError(http.StatusBadRequest, err.Error())
	}

	model, err := models.NewModelFromFile(p.ModelFileName)
	if err != nil {
		return nil, httperr.NewError(http.StatusBadRequest, fmt.Sprintf("failed to load model file: %v", err))
	}

	instanceIDs := make([]string, 0, len(model.Nodes))
	for _, node := range model.Nodes {
		instanceIDs = append(instanceIDs, node.ID)
	}

	return &simProvider{
		baseProvider: baseProvider{
			client: &simClient{
				model:       model,
				instanceIDs: instanceIDs,
				apiErr:      p.APIError,
			},
			params: &ProviderParams{},
		},
		BaseSimProvider: providers.NewBaseSimProvider(model, p.TrimTiers),
	}, nil
}

// Engine support

func (p *simProvider) GenerateTopologyConfig(ctx context.Context, pageSize *int, instances []topology.ComputeInstances) (*topology.Graph, *httperr.Error) {
	topo, err := p.generateInstanceTopology(ctx, pageSize, instances)
	if err != nil {
		return nil, err
	}
	return p.ToGraph(NAME_SIM, topo, instances, false), nil
}
