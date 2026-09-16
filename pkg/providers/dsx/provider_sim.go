/*
 * Copyright 2026, NVIDIA CORPORATION
 * SPDX-License-Identifier: Apache-2.0
 */

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

func (client *simClient) GetTopology(ctx context.Context, _ string, nodeIDs []string, pageSize int, pageToken string) (*TopologyResponse, error) {
	if client.apiErr == errAPIError {
		return nil, providers.ErrAPIError
	}

	// For simulation, generate topology from model
	response := &TopologyResponse{
		Switches: make(map[string]SwitchInfo),
	}

	want := make(map[string]struct{})
	for _, nodeID := range nodeIDs {
		want[nodeID] = struct{}{}
	}

	//Iterate over the switches from the model and add them to the switch map
	for _, sw := range client.model.Switches {
		swInfo := SwitchInfo{
			Switches: make([]string, 0),
			Nodes:    make([]NodeInfo, 0),
		}

		if len(sw.Nodes) > 0 {
			//If it is a leaf switch, add the nodes to the switch info
			for _, nodeName := range sw.Nodes {
				if _, exists := want[nodeName]; !exists {
					continue
				}
				node, exists := client.model.Nodes[nodeName]
				if !exists {
					continue
				}
				swInfo.Nodes = append(swInfo.Nodes, NodeInfo{NodeID: nodeName, AcceleratedNetworkID: node.AcceleratorDomain()})
			}
		} else {
			//If it is not a leaf switch, add the child switches to the switch info
			swInfo.Switches = append(swInfo.Switches, sw.Switches...)
		}
		response.Switches[sw.Name] = swInfo
	}

	//Return the response
	return response, nil
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
