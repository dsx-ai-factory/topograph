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

package providers

import (
	"context"
	"errors"
	"fmt"

	"github.com/dsx-ai-factory/topograph/internal/config"
	"github.com/dsx-ai-factory/topograph/internal/httperr"
	"github.com/dsx-ai-factory/topograph/pkg/models"
	"github.com/dsx-ai-factory/topograph/pkg/topology"
)

var ErrAPIError = errors.New("API error")

type SimulationParams struct {
	ModelFileName string `mapstructure:"modelFileName"`
	APIError      int    `mapstructure:"api_error"`
	TrimTiers     int    `mapstructure:"trimTiers"`
}

func GetSimulationParams(params map[string]any) (*SimulationParams, error) {
	var p SimulationParams
	if err := config.Decode(params, &p); err != nil {
		return nil, fmt.Errorf("error decoding params: %w", err)
	}
	if len(p.ModelFileName) == 0 {
		return nil, fmt.Errorf("no model file name for simulation")
	}
	return &p, nil
}

// BaseSimProvider holds model-derived topology data shared by simulation providers.
type BaseSimProvider struct {
	instances        map[string]topology.Instance
	computeInstances []topology.ComputeInstances
	trimTiers        int
}

// NewBaseSimProvider builds the shared model-derived data used by simulation providers.
func NewBaseSimProvider(model *models.Model, trimTiers int) *BaseSimProvider {
	// Provider-specific simulations must expose IDs that match their simulated
	// APIs (for example, numeric GCP IDs), not the i-prefixed IDs generated for
	// the model-backed test provider.
	computeInstances := make([]topology.ComputeInstances, 0, len(model.Instances))
	for _, ci := range model.Instances {
		providerInstances := make(map[string]string, len(ci.Instances))
		for _, hostName := range ci.Instances {
			providerInstances[model.Nodes[hostName].ID] = hostName
		}
		computeInstances = append(computeInstances, topology.ComputeInstances{
			Region:    ci.Region,
			Instances: providerInstances,
		})
	}
	return &BaseSimProvider{
		instances:        model.InstanceMap(nil),
		computeInstances: computeInstances,
		trimTiers:        trimTiers,
	}
}

// AttachInstances attaches the model's optional instance metadata to provider topology.
func (p *BaseSimProvider) AttachInstances(topo *topology.ClusterTopology) {
	topo.AttachInstances(p.instances)
}

// ToGraph converts provider topology with the shared simulation settings.
func (p *BaseSimProvider) ToGraph(provider string, topo *topology.ClusterTopology, instances []topology.ComputeInstances, normalize bool) *topology.Graph {
	p.AttachInstances(topo)
	return topo.ToGraph(provider, instances, p.trimTiers, normalize)
}

// GetComputeInstances returns model-derived compute instances for engines that need them.
func (p *BaseSimProvider) GetComputeInstances(_ context.Context) ([]topology.ComputeInstances, *httperr.Error) {
	return p.computeInstances, nil
}
