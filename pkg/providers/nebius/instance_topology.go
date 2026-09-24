/*
 * Copyright 2025 NVIDIA CORPORATION
 * SPDX-License-Identifier: Apache-2.0
 */

package nebius

import (
	"context"
	"fmt"
	"net/http"

	compute "github.com/nebius/gosdk/proto/nebius/compute/v1"
	"k8s.io/klog/v2"

	"github.com/dsx-ai-factory/topograph/internal/httperr"
	"github.com/dsx-ai-factory/topograph/pkg/topology"
)

const nebiusFabricTierCount = 3

func (p *baseProvider) generateInstanceTopology(ctx context.Context, pageSize *int, cis []topology.ComputeInstances) (*topology.ClusterTopology, *httperr.Error) {
	client, err := p.clientFactory(pageSize)
	if err != nil {
		return nil, httperr.NewError(http.StatusBadGateway, fmt.Sprintf("failed to create API client: %v", err))
	}

	topo := topology.NewClusterTopology()

	for _, ci := range cis {
		if err := p.generateRegionInstanceTopology(ctx, client, topo, &ci); err != nil {
			return nil, err
		}
	}

	return topo, nil
}

func (p *baseProvider) generateRegionInstanceTopology(ctx context.Context, client Client, topo *topology.ClusterTopology, ci *topology.ComputeInstances) *httperr.Error {
	if len(ci.Region) == 0 {
		return httperr.NewError(http.StatusBadRequest, "must specify region")
	}
	klog.InfoS("Getting instance topology", "region", ci.Region)

	req := &compute.ListInstancesRequest{
		ParentId: client.ProjectID(),
		PageSize: client.PageSize(),
	}

	for {
		resp, err := client.GetComputeInstanceList(ctx, req)
		if err != nil {
			return httperr.NewError(http.StatusBadGateway, fmt.Sprintf("failed to get instance list: %v", err))
		}

		for _, instance := range resp.Items {
			instanceID := instance.GetMetadata().GetId()
			hostname, ok := ci.Instances[instanceID]
			if !ok {
				klog.V(4).Infof("Skipping instance %s", instance.String())
				continue
			}
			ibTopology := instance.GetStatus().GetInfinibandTopologyPath()
			if ibTopology == nil {
				klog.Warningf("missing topology path for node %q", hostname)
				continue
			}

			inst := &topology.InstanceTopology{
				InstanceID:   instanceID,
				XclrDomainID: instance.GetSpec().GetNvlInstanceGroupId(),
			}

			path := ibTopology.GetPath()
			if len(path) != nebiusFabricTierCount {
				klog.Warningf("invalid topology path for node %q: expected %d tiers, got %d", hostname, nebiusFabricTierCount, len(path))
				continue
			}
			inst.FabricTiers = topology.RootFirstFabricTiers(path...)

			klog.Infof("Adding topology: %s", inst.String())
			topo.Append(inst)
		}

		if len(resp.NextPageToken) == 0 {
			klog.V(4).Infof("Total processed nodes: %d", topo.Len())
			return nil
		}
		req.PageToken = resp.NextPageToken
	}
}
