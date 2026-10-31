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

package akamai

import (
	"context"
	"fmt"
	"maps"
	"net/http"
	"os"
	"slices"

	"github.com/mitchellh/mapstructure"
	"sigs.k8s.io/yaml"

	"github.com/dsx-ai-factory/topograph/internal/httperr"
	"github.com/dsx-ai-factory/topograph/pkg/models"
	"github.com/dsx-ai-factory/topograph/pkg/providers"
	"github.com/dsx-ai-factory/topograph/pkg/topology"
)

const NAME_SIM = "akamai-sim"

const errTopology = 1

type simClient struct {
	regions map[string][]Switch
	apiErr  int
}

type simProvider struct {
	*Provider
	*providers.BaseSimProvider
}

type simulationParams struct {
	providers.SimulationParams `mapstructure:",squash"`
	TopologyFileName           string `mapstructure:"topologyFileName"`
}

func NamedLoaderSim() (string, providers.Loader) { return NAME_SIM, LoaderSim }

func LoaderSim(_ context.Context, cfg providers.Config) (providers.Provider, *httperr.Error) {
	var simulation simulationParams
	if err := mapstructure.Decode(cfg.Params, &simulation); err != nil {
		return nil, httperr.NewError(http.StatusBadRequest, err.Error())
	}
	if (simulation.ModelFileName == "") == (simulation.TopologyFileName == "") {
		return nil, httperr.NewError(http.StatusBadRequest, "specify exactly one of modelFileName or topologyFileName")
	}
	params, err := getParams(cfg.Params)
	if err != nil {
		return nil, httperr.NewError(http.StatusBadRequest, err.Error())
	}
	if simulation.TopologyFileName != "" {
		return loadSnapshot(params, simulation)
	}
	model, err := models.NewModelFromFile(simulation.ModelFileName)
	if err != nil {
		return nil, httperr.NewError(http.StatusBadRequest, fmt.Sprintf("failed to load model file: %v", err))
	}
	return &simProvider{
		Provider:        &Provider{params: params, metadata: model.InstanceMap(nil), client: newSimClient(model, simulation.APIError)},
		BaseSimProvider: providers.NewBaseSimProvider(model, params.TrimTiers),
	}, nil
}

// loadSnapshot replays one complete region snapshot through the same paged
// client interface, retaining redundant links that tree model files cannot hold.
func loadSnapshot(params ProviderParams, simulation simulationParams) (providers.Provider, *httperr.Error) {
	data, err := os.ReadFile(simulation.TopologyFileName)
	if err != nil {
		return nil, httperr.NewError(http.StatusBadRequest, "failed to read Akamai topology file: "+err.Error())
	}
	var snapshot TopologyResult
	if err := yaml.Unmarshal(data, &snapshot); err != nil || snapshot.Region == "" || snapshot.Data == nil || snapshot.Results != len(snapshot.Data) {
		return nil, httperr.NewError(http.StatusBadRequest, "invalid Akamai topology file: expected a complete region snapshot")
	}
	if snapshot.Page != 1 || snapshot.Pages != 1 {
		return nil, httperr.NewError(http.StatusBadRequest, "Akamai topology file must contain all records in one page")
	}
	client := &simClient{regions: map[string][]Switch{snapshot.Region: snapshot.Data}, apiErr: simulation.APIError}
	return &simProvider{
		Provider:        &Provider{params: params, client: client},
		BaseSimProvider: providers.NewBaseSimProvider(snapshotModel(snapshot), params.TrimTiers),
	}, nil
}

func snapshotModel(snapshot TopologyResult) *models.Model {
	model := &models.Model{Nodes: map[string]*models.Node{}}
	instances := map[string]string{}
	for _, sw := range snapshot.Data {
		for _, host := range sw.Hosts {
			instances[host.UUID] = host.UUID
			model.Nodes[host.UUID] = &models.Node{Instance: topology.Instance{ID: host.UUID}}
		}
	}
	model.Instances = []topology.ComputeInstances{{Region: snapshot.Region, Instances: instances}}
	return model
}

// newSimClient materializes the API's switch records rather than bypassing
// discovery with precomputed paths. Sorting makes page boundaries reproducible.
func newSimClient(model *models.Model, apiErr int) *simClient {
	index := map[string]map[string]*Switch{}
	for _, name := range slices.Sorted(maps.Keys(model.Nodes)) {
		node := model.Nodes[name]
		region := node.Labels[models.LabelTopologyRegion]
		if index[region] == nil {
			index[region] = map[string]*Switch{}
		}
		for i, swName := range node.NetLayers {
			sw := index[region][swName]
			if sw == nil {
				sw = &Switch{Fabric: "fabric", Name: swName, Serial: "sim-" + swName, Connections: []Link{}, Hosts: []Host{}}
				index[region][swName] = sw
			}
			if i == 0 {
				sw.Hosts = append(sw.Hosts, Host{UUID: node.ID, Port: "sim-host-" + node.ID})
			}
			if i+1 < len(node.NetLayers) {
				sw.Uplink = &Link{Device: node.NetLayers[i+1], LocalPort: "sim-up", RemotePort: "sim-down"}
			}
		}
	}
	client := &simClient{regions: map[string][]Switch{}, apiErr: apiErr}
	for region, switches := range index {
		for _, name := range slices.Sorted(maps.Keys(switches)) {
			sw := switches[name]
			if sw.Uplink != nil {
				parent := switches[sw.Uplink.Device]
				parent.Connections = append(parent.Connections, Link{Device: name, LocalPort: sw.Uplink.RemotePort, RemotePort: sw.Uplink.LocalPort})
			}
		}
		for _, name := range slices.Sorted(maps.Keys(switches)) {
			client.regions[region] = append(client.regions[region], *switches[name])
		}
	}
	return client
}

func (c *simClient) Topology(ctx context.Context, region string, pageSize, page int) (*TopologyResult, *httperr.Error) {
	if ctx.Err() != nil {
		return nil, httperr.NewError(http.StatusBadGateway, ctx.Err().Error())
	}
	if c.apiErr == errTopology {
		return nil, httperr.NewError(http.StatusBadGateway, providers.ErrAPIError.Error())
	}
	if c.apiErr >= 400 && c.apiErr <= 599 {
		return nil, httperr.NewError(c.apiErr, "simulated Akamai topology failure")
	}
	records := c.regions[region]
	if pageSize <= 0 || page <= 0 {
		return nil, httperr.NewError(http.StatusBadRequest, "invalid simulation page or pageSize")
	}
	pages := max(1, (len(records)-1)/pageSize+1)
	if page > pages {
		return nil, httperr.NewError(http.StatusBadRequest, "simulation page out of range")
	}
	start := (page - 1) * pageSize
	end := start + min(pageSize, len(records)-start)
	return &TopologyResult{Region: region, Data: append([]Switch{}, records[start:end]...), Page: page, Pages: pages, Results: len(records)}, nil
}

func (p *simProvider) GenerateTopologyConfig(ctx context.Context, pageSize *int, instances []topology.ComputeInstances) (*topology.Graph, *httperr.Error) {
	return p.generateGraph(ctx, NAME_SIM, pageSize, instances)
}
