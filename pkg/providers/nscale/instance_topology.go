/*
 * Copyright 2026 NVIDIA CORPORATION
 * SPDX-License-Identifier: Apache-2.0
 */

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
