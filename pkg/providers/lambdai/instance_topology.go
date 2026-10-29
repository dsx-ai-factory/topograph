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

package lambdai

import (
	"context"
	"fmt"
	"net/http"

	"k8s.io/klog/v2"

	"github.com/dsx-ai-factory/topograph/internal/httperr"
	"github.com/dsx-ai-factory/topograph/pkg/topology"
)

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

	req := &InstanceListRequest{Region: ci.Region, PageSize: client.PageSize()}

	for {
		resp, err := client.InstanceList(ctx, req)
		if err != nil {
			return httperr.NewError(http.StatusBadGateway, fmt.Sprintf("failed to get instance list: %v", err))
		}

		for _, inst := range resp.Items {
			t := &topology.InstanceTopology{
				InstanceID: inst.ID,
			}

			for _, hop := range inst.NetworkPath {
				t.FabricTiers = append(t.FabricTiers, topology.FabricTier{ID: hop.ID})
			}

			if inst.NVLink != nil {
				if len(inst.NVLink.DomainID) == 0 || len(inst.NVLink.CliqueID) == 0 {
					klog.Warningf("incomplete NVL data for instance %s: DomainID=%q CliqueID=%q", inst.ID, inst.NVLink.DomainID, inst.NVLink.CliqueID)
				} else {
					t.XclrDomainID = inst.NVLink.DomainID + "." + inst.NVLink.CliqueID
				}
			}

			klog.Infof("Adding topology: %s", t.String())
			topo.Append(t)
		}

		if len(resp.NextPageToken) == 0 {
			klog.V(4).Infof("Total processed nodes: %d", topo.Len())
			return nil
		}
		req.PageToken = resp.NextPageToken
	}
}
