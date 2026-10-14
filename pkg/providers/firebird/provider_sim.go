/*
 * Copyright 2026 NVIDIA CORPORATION
 * SPDX-License-Identifier: Apache-2.0
 */

package firebird

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strconv"

	"github.com/dsx-ai-factory/topograph/internal/httperr"
	"github.com/dsx-ai-factory/topograph/pkg/models"
	"github.com/dsx-ai-factory/topograph/pkg/providers"
	"github.com/dsx-ai-factory/topograph/pkg/topology"
)

const NAME_SIM = "firebird-sim"

// As in other API simulations, api_error: 1 fails topology discovery.
const errTopology = 1

type simClient struct {
	nodes  []Node
	apiErr int
}

type simProvider struct {
	*Provider
	*providers.BaseSimProvider
}

func NamedLoaderSim() (string, providers.Loader) {
	return NAME_SIM, LoaderSim
}

func LoaderSim(_ context.Context, cfg providers.Config) (providers.Provider, *httperr.Error) {
	simulation, err := providers.GetSimulationParams(cfg.Params)
	if err != nil {
		return nil, httperr.NewError(http.StatusBadRequest, err.Error())
	}
	trim, err := providers.GetTrimTiers(cfg.Params)
	if err != nil {
		return nil, httperr.NewError(http.StatusBadRequest, err.Error())
	}
	params := ProviderParams{TrimTiers: trim}
	model, err := models.NewModelFromFile(simulation.ModelFileName)
	if err != nil {
		return nil, httperr.NewError(http.StatusBadRequest, fmt.Sprintf("failed to load model file: %v", err))
	}
	names := make([]string, 0, len(model.Nodes))
	for name := range model.Nodes {
		names = append(names, name)
	}
	sort.Strings(names)
	nodes := make([]Node, 0, len(names))
	for _, name := range names {
		node := model.Nodes[name]
		path := slices.Clone(node.NetLayers)
		nodes = append(nodes, Node{
			NodeID:         node.ID,
			NodeKind:       "bm",
			TopologyStatus: "complete",
			NetworkPath:    path,
		})
	}
	return &simProvider{
		Provider:        &Provider{params: params, client: &simClient{nodes: nodes, apiErr: simulation.APIError}},
		BaseSimProvider: providers.NewBaseSimProvider(model, trim),
	}, nil
}

// Topology uses offsets as opaque continuation tokens and returns a partial
// final page without requiring an extra empty-page request.
func (c *simClient) Topology(ctx context.Context, pageSize int, token string) (*TopologyResult, *httperr.Error) {
	if ctx.Err() != nil {
		return nil, httperr.NewError(http.StatusBadGateway, ctx.Err().Error())
	}
	switch c.apiErr {
	case errTopology:
		return nil, httperr.NewError(http.StatusBadGateway, providers.ErrAPIError.Error())
	case http.StatusBadRequest:
		return nil, apiError(c.apiErr, []byte(`{"error":{"code":"INVALID_ARGUMENT","message":"Invalid nodeKind, pageSize or pageToken"}}`))
	case http.StatusNotFound:
		return nil, apiError(c.apiErr, []byte(`{"error":{"code":"NOT_FOUND","message":"switchId not found"}}`))
	case http.StatusTooManyRequests:
		return nil, apiError(c.apiErr, []byte(`{"error":{"code":"RATE_LIMITED","message":"Rate limit exceeded — try again later"}}`))
	}
	if pageSize <= 0 {
		return nil, httperr.NewError(http.StatusBadRequest, "pageSize must be positive")
	}
	offset := 0
	if token != "" {
		var err error
		offset, err = strconv.Atoi(token)
		if err != nil || offset < 0 || offset > len(c.nodes) {
			return nil, httperr.NewError(http.StatusBadRequest, "invalid simulation pageToken")
		}
	}
	end := offset + min(pageSize, len(c.nodes)-offset)
	result := &TopologyResult{Nodes: append([]Node{}, c.nodes[offset:end]...)}
	if end < len(c.nodes) {
		result.NextPageToken = strconv.Itoa(end)
	}
	return result, nil
}

func (p *simProvider) GenerateTopologyConfig(ctx context.Context, pageSize *int, instances []topology.ComputeInstances) (*topology.Graph, *httperr.Error) {
	cluster, err := p.generateInstanceTopology(ctx, pageSize, instances)
	if err != nil {
		return nil, err
	}
	return p.ToGraph(NAME_SIM, cluster, instances, false), nil
}
