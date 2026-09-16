/*
 * Copyright 2026, NVIDIA CORPORATION
 * SPDX-License-Identifier: Apache-2.0
 */

package lambdai

import (
	"context"
	"fmt"
	"net/http"
	"strconv"

	"github.com/dsx-ai-factory/topograph/internal/httperr"
	"github.com/dsx-ai-factory/topograph/pkg/models"
	"github.com/dsx-ai-factory/topograph/pkg/providers"
	"github.com/dsx-ai-factory/topograph/pkg/topology"
)

const (
	NAME_SIM = "lambdai-sim"

	errNone = iota
	errClientFactory
	errInstanceList
)

type simClient struct {
	model       *models.Model
	pageSize    int
	instanceIDs []string
	apiErr      int
}

func (c *simClient) WorkspaceID() string {
	return "simulation"
}

func (c *simClient) PageSize() int {
	return c.pageSize
}

func (c *simClient) InstanceList(ctx context.Context, req *InstanceListRequest) (*InstanceListResponse, error) {
	if c.apiErr == errInstanceList {
		return &InstanceListResponse{}, providers.ErrAPIError
	}

	resp := InstanceListResponse{
		Items: make([]InstanceTopology, 0, len(c.model.Nodes)),
	}

	var indx int
	from := getPage(req.PageToken)
	for indx = from; indx < from+c.pageSize && indx < len(c.instanceIDs); indx++ {
		node, exists := c.model.Nodes[c.instanceIDs[indx]]
		if !exists {
			continue
		}
		netPath := make([]NetworkHop, len(node.NetLayers))
		for i, layer := range node.NetLayers {
			netPath[i] = NetworkHop{ID: layer}
		}
		instance := InstanceTopology{
			ID:          node.ID,
			NetworkPath: netPath,
			//TODO: check whether the below mapping is correct
			NVLink: &NVLinkInfo{
				DomainID: node.AcceleratorDomain(),
				CliqueID: "simulation",
			},
		}

		resp.Items = append(resp.Items, instance)
	}

	if indx < len(c.instanceIDs) {
		resp.NextPageToken = fmt.Sprintf("%d", indx)
	}

	return &resp, nil
}

func getPage(page string) int {
	if len(page) == 0 {
		return 0
	}

	val, _ := strconv.ParseInt(page, 10, 32)
	return int(val)
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

	clientFactory := func(pageSize *int) (Client, error) {
		if p.APIError == errClientFactory {
			return nil, providers.ErrAPIError
		}

		pSize := len(instanceIDs)
		if pageSize != nil {
			pSize = *pageSize
		}

		return &simClient{
			model:       model,
			pageSize:    pSize,
			instanceIDs: instanceIDs,
			apiErr:      p.APIError,
		}, nil
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
