/*
 * Copyright (c) 2024, NVIDIA CORPORATION.  All rights reserved.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package oci

import (
	"context"
	"errors"
	"testing"

	"github.com/agrea/ptr"
	"github.com/oracle/oci-go-sdk/v65/core"
	"github.com/oracle/oci-go-sdk/v65/identity"
	"github.com/stretchr/testify/require"

	"github.com/dsx-ai-factory/topograph/pkg/topology"
)

var errListComputeHostsCallLimit = errors.New("ListComputeHosts call limit exceeded")

type repeatingPageClient struct {
	listCalls    int
	getCalls     int
	maxListCalls int
}

func (c *repeatingPageClient) TenantID() *string { return ptr.String("tenant") }

func (c *repeatingPageClient) Limit() *int { return ptr.Int(1) }

func (c *repeatingPageClient) ListAvailabilityDomains(context.Context, identity.ListAvailabilityDomainsRequest) (identity.ListAvailabilityDomainsResponse, error) {
	return identity.ListAvailabilityDomainsResponse{}, nil
}

func (c *repeatingPageClient) ListComputeHosts(context.Context, core.ListComputeHostsRequest) (core.ListComputeHostsResponse, error) {
	c.listCalls++
	if c.listCalls > c.maxListCalls {
		return core.ListComputeHostsResponse{}, errListComputeHostsCallLimit
	}

	return core.ListComputeHostsResponse{
		ComputeHostCollection: core.ComputeHostCollection{
			Items: []core.ComputeHostSummary{
				{
					Id:         ptr.String("host"),
					InstanceId: ptr.String("instance"),
				},
			},
		},
		OpcNextPage: ptr.String("repeat"),
	}, nil
}

func (c *repeatingPageClient) GetComputeHost(context.Context, core.GetComputeHostRequest) (core.GetComputeHostResponse, error) {
	c.getCalls++
	return core.GetComputeHostResponse{
		ComputeHost: core.ComputeHost{
			Id:             ptr.String("host"),
			InstanceId:     ptr.String("instance"),
			LocalBlockId:   ptr.String("local"),
			NetworkBlockId: ptr.String("network"),
			HpcIslandId:    ptr.String("island"),
		},
	}, nil
}

func TestGetComputeHostSummaryRejectsRepeatedPageTokenBeforeCallLimit(t *testing.T) {
	client := &repeatingPageClient{maxListCalls: 2}
	topo := topology.NewClusterTopology()

	err := getComputeHostSummary(
		context.Background(),
		client,
		ptr.String("availability-domain"),
		topo,
		map[string]string{"instance": "node"},
	)

	require.EqualError(t, err, `ListComputeHosts returned repeated page token "repeat"`)
	require.NotErrorIs(t, err, errListComputeHostsCallLimit)
	require.Equal(t, 2, client.listCalls)
	require.Equal(t, 1, client.getCalls)
	require.Len(t, topo.Instances, 1)
}

func TestConvert(t *testing.T) {
	leaf, spine, root := "leaf", "net", "core"
	valid := &topology.InstanceTopology{
		InstanceID:  "id",
		FabricTiers: topology.ClosestFirstFabricTiers(leaf, spine, root),
	}

	testCases := []struct {
		name string
		host *core.ComputeHostSummary
		topo *topology.InstanceTopology
		err  string
	}{
		{
			name: "Case 1: missing InstanceId",
			host: &core.ComputeHostSummary{},
			err:  "missing InstanceId in ComputeHostSummary",
		},
		{
			name: "Case 2: missing LocalBlock",
			host: &core.ComputeHostSummary{
				InstanceId: &valid.InstanceID,
			},
			err: `missing LocalBlockId for instance "id"`,
		},
		{
			name: "Case 3: missing NetworkBlockId",
			host: &core.ComputeHostSummary{
				InstanceId:   &valid.InstanceID,
				LocalBlockId: &leaf,
			},
			err: `missing NetworkBlockId for instance "id"`,
		},
		{
			name: "Case 4: missing HpcIslandId",
			host: &core.ComputeHostSummary{
				InstanceId:     &valid.InstanceID,
				LocalBlockId:   &leaf,
				NetworkBlockId: &spine,
			},
			err: `missing HpcIslandId for instance "id"`,
		},
		{
			name: "Case 5: valid input",
			host: &core.ComputeHostSummary{
				InstanceId:     &valid.InstanceID,
				LocalBlockId:   &leaf,
				NetworkBlockId: &spine,
				HpcIslandId:    &root,
			},
			topo: valid,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			topo, err := convert(tc.host)
			if len(tc.err) != 0 {
				require.EqualError(t, err, tc.err)
			} else {
				require.NoError(t, err)
				require.Equal(t, tc.topo, topo)
			}
		})
	}
}
