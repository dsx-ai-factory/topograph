/*
 * Copyright (c) 2024-2025, NVIDIA CORPORATION.  All rights reserved.
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
	"fmt"
	"net/http"

	"github.com/dsx-ai-factory/topograph/internal/httperr"
	"github.com/dsx-ai-factory/topograph/pkg/providers"
	"github.com/dsx-ai-factory/topograph/pkg/topology"
)

const (
	NAME_IMDS = "oci-imds"
)

type imdsProvider struct {
	baseProvider
}

func NamedLoaderIMDS() (string, providers.Loader) {
	return NAME_IMDS, LoaderIMDS
}

func LoaderIMDS(_ context.Context, config providers.Config) (providers.Provider, *httperr.Error) {
	trimTiers, err := providers.GetTrimTiers(config.Params)
	if err != nil {
		return nil, httperr.NewError(http.StatusBadRequest, "parameters error: "+err.Error())
	}
	return &imdsProvider{baseProvider: baseProvider{trimTiers: trimTiers}}, nil
}

func (p *imdsProvider) GenerateTopologyConfig(ctx context.Context, _ *int, instances []topology.ComputeInstances) (*topology.Graph, *httperr.Error) {
	topo, err := p.generateInstanceTopology(ctx, instances)
	if err != nil {
		return nil, httperr.NewError(http.StatusInternalServerError, err.Error())
	}

	return topo.ToGraph(NAME, instances, p.trimTiers, true), nil
}

func (p *imdsProvider) generateInstanceTopology(ctx context.Context, cis []topology.ComputeInstances) (*topology.ClusterTopology, error) {
	topo := topology.NewClusterTopology()

	for _, ci := range cis {
		if err := p.getComputeHostInfo(ctx, ci, topo); err != nil {
			return nil, err
		}
	}

	return topo, nil
}

func (p *imdsProvider) getComputeHostInfo(ctx context.Context, ci topology.ComputeInstances, topo *topology.ClusterTopology) error {
	nodes := make([]string, 0, len(ci.Instances))
	for _, node := range ci.Instances {
		nodes = append(nodes, node)
	}

	topoMap, err := getHostTopology(ctx, nodes)
	if err != nil {
		return fmt.Errorf("failed to get node topology: %v", err)
	}

	for instanceID, node := range ci.Instances {
		if nodeTopology, ok := topoMap[node]; ok {
			topo.Instances = append(topo.Instances, &topology.InstanceTopology{
				InstanceID:   instanceID,
				FabricTiers:  topology.ClosestFirstFabricTiers(nodeTopology.LocalBlock, nodeTopology.NetworkBlock, nodeTopology.HPCIslandId),
				XclrDomainID: nodeTopology.GpuMemoryFabric,
			})
		}
	}
	return nil
}

// Engine support

// Instances2NodeMap implements slurm.instanceMapper
func (p *imdsProvider) Instances2NodeMap(ctx context.Context, nodes []string) (map[string]string, error) {
	i2n := make(map[string]string)

	for _, node := range nodes {
		i2n[node] = node
	}

	return i2n, nil
}
