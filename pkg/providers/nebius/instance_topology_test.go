/*
 * Copyright 2026 NVIDIA CORPORATION
 * SPDX-License-Identifier: Apache-2.0
 */

package nebius

import (
	"context"
	"fmt"
	"testing"

	common "github.com/nebius/gosdk/proto/nebius/common/v1"
	compute "github.com/nebius/gosdk/proto/nebius/compute/v1"
	"github.com/stretchr/testify/require"

	"github.com/dsx-ai-factory/topograph/pkg/topology"
)

type instanceTopologyClient struct {
	response *compute.ListInstancesResponse
}

func (c *instanceTopologyClient) ProjectID() string {
	return "project"
}

func (c *instanceTopologyClient) PageSize() int64 {
	return 100
}

func (c *instanceTopologyClient) GetComputeInstanceList(context.Context, *compute.ListInstancesRequest) (*compute.ListInstancesResponse, error) {
	return c.response, nil
}

func TestGenerateRegionInstanceTopologyRequiresThreeTiers(t *testing.T) {
	for tierCount := 0; tierCount <= 4; tierCount++ {
		t.Run(fmt.Sprintf("%d tiers", tierCount), func(t *testing.T) {
			path := []string{"root", "middle", "leaf", "extra"}[:tierCount]
			client := &instanceTopologyClient{response: &compute.ListInstancesResponse{
				Items: []*compute.Instance{{
					Metadata: &common.ResourceMetadata{Id: "instance-1"},
					Status: &compute.InstanceStatus{
						GpuClusterTopology: &compute.InstanceStatus_InfinibandTopologyPath{
							InfinibandTopologyPath: &compute.InstanceStatusInfinibandTopologyPath{Path: path},
						},
					},
				}},
			}}
			cluster := topology.NewClusterTopology()
			ci := &topology.ComputeInstances{
				Region:    "region",
				Instances: map[string]string{"instance-1": "node-1"},
			}

			httpErr := (&baseProvider{}).generateRegionInstanceTopology(context.Background(), client, cluster, ci)

			require.Nil(t, httpErr)
			if tierCount != nebiusFabricTierCount {
				require.Empty(t, cluster.Instances)
				return
			}
			require.Equal(t, topology.ClosestFirstFabricTiers("leaf", "middle", "root"), cluster.Instances[0].FabricTiers)
		})
	}
}
