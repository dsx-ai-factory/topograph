/*
 * Copyright 2026, NVIDIA CORPORATION
 * SPDX-License-Identifier: Apache-2.0
 */

package dsx

import (
	"context"
	"fmt"
	"net/http"

	"k8s.io/klog/v2"

	"github.com/dsx-ai-factory/topograph/internal/httperr"
	"github.com/dsx-ai-factory/topograph/pkg/topology"
)

func (p *baseProvider) generateInstanceTopology(ctx context.Context, pageSize *int, cis []topology.ComputeInstances) (*topology.ClusterTopology, *httperr.Error) {
	client, err := p.clientFactory()
	if err != nil {
		return nil, httperr.NewError(http.StatusBadGateway, fmt.Sprintf("failed to get client: %v", err))
	}

	var nodeIDs []string
	for _, ci := range cis {
		for instanceID := range ci.Instances {
			nodeIDs = append(nodeIDs, instanceID)
		}
	}

	pageSizeVal := 0
	if pageSize != nil {
		pageSizeVal = *pageSize
	}

	response, apiErr := client.GetTopology(ctx, "", nodeIDs, pageSizeVal, "")
	if apiErr != nil {
		return nil, httperr.NewError(http.StatusBadGateway, fmt.Sprintf("API error: %v", apiErr))
	}

	return responseToClusterTopology(response, cis), nil
}

// responseToClusterTopology maps switch/node API output to per-instance records for ToGraph.
func responseToClusterTopology(response *TopologyResponse, cis []topology.ComputeInstances) *topology.ClusterTopology {
	want := make(map[string]struct{})
	for _, ci := range cis {
		for instanceID := range ci.Instances {
			want[instanceID] = struct{}{}
		}
	}

	parentOf := make(map[string]string)
	for swName, info := range response.Switches {
		for _, child := range info.Switches {
			parentOf[child] = swName
		}
	}

	topo := topology.NewClusterTopology()
	for swName, info := range response.Switches {
		for _, n := range info.Nodes {
			if _, ok := want[n.NodeID]; !ok {
				continue
			}
			leafID := swName
			spineID := parentOf[leafID]
			coreID := ""
			if spineID != "" {
				coreID = parentOf[spineID]
			}

			//create the instance topology
			inst := &topology.InstanceTopology{
				InstanceID:  n.NodeID,
				FabricTiers: topology.ClosestFirstFabricTiers(leafID, spineID, coreID),
			}
			if n.AcceleratedNetworkID != "" {
				inst.XclrDomainID = n.AcceleratedNetworkID
			}
			klog.V(4).Infof("Adding instance topology %s", inst.String())
			topo.Append(inst)
		}
	}

	return topo
}
