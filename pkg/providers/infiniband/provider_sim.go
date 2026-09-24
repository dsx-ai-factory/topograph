/*
 * Copyright 2026 NVIDIA CORPORATION
 * SPDX-License-Identifier: Apache-2.0
 */

package infiniband

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/dsx-ai-factory/topograph/internal/httperr"
	"github.com/dsx-ai-factory/topograph/pkg/ib"
	"github.com/dsx-ai-factory/topograph/pkg/providers"
	"github.com/dsx-ai-factory/topograph/pkg/topology"
)

const NAME_SIM = "infiniband-sim"

type ProviderSim struct {
	output         []byte
	instances      []topology.ComputeInstances
	switchSelector *switchSelector
}

func NamedLoaderSim() (string, providers.Loader) {
	return NAME_SIM, LoaderSim
}

// LoaderSim loads one captured ibnetdiscover output for offline fabric discovery.
func LoaderSim(_ context.Context, config providers.Config) (providers.Provider, *httperr.Error) {
	if _, exists := config.Params["modelFileName"]; exists {
		return nil, httperr.NewError(http.StatusBadRequest, "provider.params.modelFileName is not supported by infiniband-sim; use ibnetdiscoverFileName")
	}
	selector, err := newSwitchSelector(config.Params)
	if err != nil {
		return nil, httperr.NewError(http.StatusBadRequest, err.Error())
	}
	fileName, ok := config.Params["ibnetdiscoverFileName"].(string)
	if !ok || strings.TrimSpace(fileName) == "" {
		return nil, httperr.NewError(http.StatusBadRequest, "provider.params.ibnetdiscoverFileName must be a non-empty file path")
	}

	output, err := os.ReadFile(fileName)
	if err != nil {
		return nil, httperr.NewError(http.StatusBadRequest, fmt.Sprintf("failed to read ibnetdiscover output: %v", err))
	}
	if !strings.Contains(string(output), "Topology file:") {
		return nil, httperr.NewError(http.StatusBadRequest, "ibnetdiscover output has no topology header")
	}
	switches, hca, err := ib.ParseIbnetdiscoverFile(output)
	if err != nil {
		return nil, httperr.NewError(http.StatusBadRequest, fmt.Sprintf("failed to parse ibnetdiscover output: %v", err))
	}
	if len(switches) == 0 || len(hca) == 0 {
		return nil, httperr.NewError(http.StatusBadRequest, "ibnetdiscover output has no switches or named hosts")
	}

	instances := make(map[string]string, len(hca))
	for _, sw := range switches {
		for id := range sw.Conn {
			if node, ok := hca[id]; ok {
				instances[node] = node
			}
		}
	}
	if len(instances) == 0 {
		return nil, httperr.NewError(http.StatusBadRequest, "ibnetdiscover output has no named hosts connected to switches")
	}
	return &ProviderSim{
		output:         output,
		instances:      []topology.ComputeInstances{{Region: "local", Instances: instances}},
		switchSelector: selector,
	}, nil
}

func (p *ProviderSim) GenerateTopologyConfig(_ context.Context, _ *int, cis []topology.ComputeInstances) (*topology.Graph, *httperr.Error) {
	if len(cis) > 1 {
		return nil, httperr.NewError(http.StatusBadRequest, "on-prem does not support multi-region topology requests")
	}

	var roots []*topology.Vertex
	var err error
	if p.switchSelector == nil {
		roots, _, err = ib.GenerateTopologyConfig(p.output, cis)
	} else {
		roots, _, err = ib.GenerateTopologyConfigFiltered(p.output, cis, p.switchSelector.acceptsLeaf, p.switchSelector.excludes)
	}
	if err != nil {
		return nil, httperr.NewError(http.StatusInternalServerError, fmt.Sprintf("failed to build InfiniBand topology: %v", err))
	}
	if p.switchSelector != nil && len(roots) == 0 {
		return nil, httperr.NewError(http.StatusInternalServerError, "switchSelector matched no usable InfiniBand topology")
	}
	tiers := &topology.Vertex{Vertices: make(map[string]*topology.Vertex, len(roots))}
	for _, root := range roots {
		tiers.Vertices[root.ID] = root
	}
	return &topology.Graph{Tiers: tiers, Domains: topology.DomainMap{}}, nil
}

func (p *ProviderSim) GetComputeInstances(_ context.Context) ([]topology.ComputeInstances, *httperr.Error) {
	return p.instances, nil
}

func (p *ProviderSim) Instances2NodeMap(_ context.Context, nodes []string) (map[string]string, error) {
	result := make(map[string]string, len(nodes))
	for _, node := range nodes {
		result[node] = node
	}
	return result, nil
}

func (p *ProviderSim) GetInstancesRegions(_ context.Context, nodes []string) (map[string]string, error) {
	result := make(map[string]string, len(nodes))
	for _, node := range nodes {
		result[node] = "local"
	}
	return result, nil
}
