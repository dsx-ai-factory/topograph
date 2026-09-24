/*
 * Copyright 2024-2026 NVIDIA CORPORATION
 * SPDX-License-Identifier: Apache-2.0
 */

package gcp

import (
	"context"
	"fmt"
	"net/http"
	"strconv"

	"cloud.google.com/go/compute/apiv1/computepb"
	"github.com/agrea/ptr"
	gax "github.com/googleapis/gax-go/v2"
	"google.golang.org/api/iterator"

	"github.com/dsx-ai-factory/topograph/internal/httperr"
	"github.com/dsx-ai-factory/topograph/pkg/models"
	"github.com/dsx-ai-factory/topograph/pkg/providers"
	"github.com/dsx-ai-factory/topograph/pkg/topology"
)

const (
	NAME_SIM = "gcp-sim"

	errNone = iota
	errClientFactory
	errInstances
)

type simClient struct {
	model       *models.Model
	pageSize    *uint32
	instanceIDs []string
	apiErr      int
}

type simInstanceIter struct {
	instances []*computepb.Instance
	indx      int
	err       error
}

func (iter *simInstanceIter) Next() (*computepb.Instance, error) {
	if iter.err != nil {
		return nil, iter.err
	}

	if iter.indx >= len(iter.instances) {
		return nil, iterator.Done
	}
	ret := iter.instances[iter.indx]
	iter.indx++

	return ret, nil
}

func (c *simClient) PageSize() *uint32 {
	return c.pageSize
}

func (c *simClient) ProjectID() string {
	return ""
}

func (c *simClient) Instances(ctx context.Context, req *computepb.ListInstancesRequest, opts ...gax.CallOption) (InstanceIterator, string) {
	if c.apiErr == errInstances {
		return &simInstanceIter{err: providers.ErrAPIError}, ""
	}

	var indx int
	from := getPage(req.PageToken)
	iter := &simInstanceIter{instances: make([]*computepb.Instance, 0)}

	for indx = from; indx < from+int(*c.pageSize) && indx < len(c.instanceIDs); indx++ {
		node := c.model.Nodes[c.instanceIDs[indx]]
		instanceID, err := strconv.ParseUint(node.ID, 10, 64)
		if err != nil {
			return &simInstanceIter{err: fmt.Errorf("invalid instance ID %q; must be numerical", node.ID)}, ""
		}
		instance := &computepb.Instance{
			Id:   &instanceID,
			Name: &node.ID,
		}
		if len(node.NetLayers) >= 3 {
			instance.ResourceStatus = &computepb.ResourceStatus{
				PhysicalHostTopology: &computepb.ResourceStatusPhysicalHostTopology{
					Cluster:  &node.NetLayers[2],
					Block:    &node.NetLayers[1],
					Subblock: &node.NetLayers[0],
				},
			}
		}
		iter.instances = append(iter.instances, instance)
	}

	var token string
	if indx < len(c.instanceIDs) {
		token = fmt.Sprintf("%d", indx)
	}

	return iter, token
}

func getPage(page *string) int {
	if page == nil {
		return 0
	}

	val, _ := strconv.ParseInt(*page, 10, 32)
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

		limit := castPageSize(pageSize)
		if limit == nil {
			limit = ptr.Uint32(uint32(len(instanceIDs)))
		}

		return &simClient{
			model:       model,
			pageSize:    limit,
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
