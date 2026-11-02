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

package nscale

import (
	"context"
	"net/http"

	"k8s.io/klog/v2"

	"github.com/dsx-ai-factory/topograph/internal/httperr"
	"github.com/dsx-ai-factory/topograph/pkg/topology"
)

const (
	defaultPageSize = 100
)

func (p *baseProvider) generateInstanceTopology(ctx context.Context, pSize *int, cis []topology.ComputeInstances) (*topology.ClusterTopology, *httperr.Error) {
	topo := topology.NewClusterTopology()

	pageSize := defaultPageSize
	if pSize != nil {
		pageSize = *pSize
	}

	for _, ci := range cis {
		if err := p.generateRegionInstanceTopology(ctx, topo, pageSize, &ci); err != nil {
			return nil, err
		}
	}

	return topo, nil
}

func (p *baseProvider) generateRegionInstanceTopology(ctx context.Context, topo *topology.ClusterTopology, pageSize int, ci *topology.ComputeInstances) *httperr.Error {
	if len(ci.Region) == 0 {
		return httperr.NewError(http.StatusBadRequest, "must specify region")
	}
	klog.InfoS("Getting instance topology", "region", ci.Region)

	offset := 0
	for {
		resp, err := p.client.Topology(ctx, ci.Region, pageSize, offset)
		if err != nil {
			return err
		}

		n := len(resp)
		if n == 0 {
			klog.V(4).Infof("Total processed nodes: %d", topo.Len())
			return nil
		}
		offset += n

		for _, inst := range resp {
			t := &topology.InstanceTopology{
				InstanceID: inst.ServerID,
			}

			t.FabricTiers = topology.RootFirstFabricTiers(inst.NetworkPath...)

			if inst.BlockID != nil {
				t.XclrDomainID = *inst.BlockID
			}

			klog.Infof("Adding topology: %s", t.String())
			topo.Append(t)
		}
	}
}
