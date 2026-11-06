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

package dsx

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
	NAME_SIM = "dsx-sim"

	errNone = iota
	errClientFactory
	errAPIError
)

type simClient struct {
	model  *models.Model
	apiErr int
}

func (client *simClient) GetTopology(ctx context.Context, _ string, pageSize int, pageToken string) (*TopologyResponse, error) {
	if client.apiErr == errAPIError {
		return nil, providers.ErrAPIError
	}

	// Build the ordered list of single-key entries matching the API wire format.
	var switches []map[string]SwitchAdjacency
	for _, sw := range client.model.Switches {
		adj := SwitchAdjacency{}
		for _, nodeName := range sw.Nodes {
			node, exists := client.model.Nodes[nodeName]
			if !exists {
				continue
			}
			adj.Nodes = append(adj.Nodes, NodeInfo{NodeID: nodeName, AcceleratedNetworkID: node.AcceleratorDomain()})
		}
		adj.Switches = append(adj.Switches, sw.Switches...)
		if len(adj.Nodes) == 0 && len(adj.Switches) == 0 {
			continue
		}
		switches = append(switches, map[string]SwitchAdjacency{sw.Name: adj})
	}

	return &TopologyResponse{Switches: switches}, nil
}

func NamedLoaderSim() (string, providers.Loader) {
	return NAME_SIM, LoaderSim
}

func LoaderSim(ctx context.Context, cfg providers.Config) (providers.Provider, *httperr.Error) {
	p, err := providers.GetSimulationParams(cfg.Params)
	if err != nil {
		return nil, httperr.NewError(http.StatusBadRequest, err.Error())
	}

	model, err := models.NewModelFromFile(p.ModelFileName)
	if err != nil {
		return nil, httperr.NewError(http.StatusBadRequest, fmt.Sprintf("failed to load model file: %v", err))
	}

	sim := &simClient{
		model:  model,
		apiErr: p.APIError,
	}

	clientFactory := func() (Client, error) {
		if p.APIError == errClientFactory {
			return nil, providers.ErrAPIError
		}
		return sim, nil
	}

	return NewSim(clientFactory, p.TrimTiers, model), nil
}

type simProvider struct {
	baseProvider
	*providers.BaseSimProvider
}

func NewSim(clientFactory ClientFactory, trimTiers int, model *models.Model) *simProvider {
	return &simProvider{
		baseProvider: baseProvider{
			clientFactory: clientFactory,
		},
		BaseSimProvider: providers.NewBaseSimProvider(model, trimTiers),
	}
}

// Engine support

func (p *simProvider) GenerateTopologyConfig(ctx context.Context, pageSize *int, instances []topology.ComputeInstances) (*topology.Graph, *httperr.Error) {
	topo, err := p.generateInstanceTopology(ctx, pageSize, instances)
	if err != nil {
		return nil, err
	}
	return p.ToGraph(NAME_SIM, topo, instances, false), nil
}
