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

package aws

import (
	"context"
	"fmt"
	"net/http"
	"strconv"

	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"

	"github.com/dsx-ai-factory/topograph/internal/httperr"
	"github.com/dsx-ai-factory/topograph/pkg/models"
	"github.com/dsx-ai-factory/topograph/pkg/providers"
	"github.com/dsx-ai-factory/topograph/pkg/topology"
)

const (
	NAME_SIM = "aws-sim"

	AvailabilityZoneKey = models.LabelTopologyZone

	DEFAULT_MAX_RESULTS = 20

	errNone = iota
	errClientFactory
	errDescribeInstanceTopology
)

type simClient struct {
	model       *models.Model
	outputs     map[string]([]types.InstanceTopology)
	nextTokens  map[string]string
	instanceIds []string
	apiErr      int
}

func (client *simClient) DescribeInstanceTopology(ctx context.Context, params *ec2.DescribeInstanceTopologyInput, optFns ...func(*ec2.Options)) (*ec2.DescribeInstanceTopologyOutput, error) {
	if client.apiErr == errDescribeInstanceTopology {
		return nil, providers.ErrAPIError
	}
	// If we need to calculate new results (a previous token was not given)
	var instanceIds []string
	if len(params.InstanceIds) != 0 {
		instanceIds = params.InstanceIds
	} else {
		instanceIds = client.instanceIds
	}

	givenToken := params.NextToken
	if givenToken == nil {
		// Refreshes the clients internal storage for outputs
		client.outputs = make(map[string][]types.InstanceTopology)
		client.nextTokens = make(map[string]string)

		// Sets the maximum number of results to return per output
		maxResults := DEFAULT_MAX_RESULTS
		if params.MaxResults != nil {
			maxResults = int(*params.MaxResults)
		}

		// Creates the list of instances whose topology is requested
		var firstToken string
		var instanceIdx int
		for instanceIdx < len(instanceIds) {
			// Only collect a list up to params.MaxResults
			var instances []types.InstanceTopology
			var i int
			for i = 0; i < maxResults && i+instanceIdx < len(instanceIds); i++ {
				// Gets the instance ID
				instanceId := instanceIds[instanceIdx+i]

				// Gets the availability zone of the instance
				node, ok := client.model.Nodes[instanceId]
				if !ok {
					continue
				}
				var az string
				if len(node.Labels) != 0 {
					az = node.Labels[AvailabilityZoneKey]
				}
				if len(az) == 0 {
					return nil, fmt.Errorf("availability zone not found for instance %q in AWS simulation", instanceId)
				}

				// Sets up the structure for the instance
				var netLayers []string
				for j := len(node.NetLayers) - 1; j >= 0; j-- {
					netLayers = append(netLayers, node.NetLayers[j])
				}
				domainID := node.AcceleratorDomain()
				instTopo := types.InstanceTopology{
					InstanceId:       &instanceId,
					AvailabilityZone: &az,
					ZoneId:           &az,
					CapacityBlockId:  &domainID,
					NetworkNodes:     netLayers,
				}
				instances = append(instances, instTopo)
			}

			token := strconv.Itoa(instanceIdx)
			if instanceIdx == 0 {
				firstToken = token
			}
			client.outputs[token] = instances
			instanceIdx += i
			if instanceIdx < len(instanceIds) {
				client.nextTokens[token] = strconv.Itoa(instanceIdx)
			}
		}

		// Sets the given token to the first token generated, then proceed normally
		givenToken = &firstToken
	}

	// Otherwise return the requested, already calculated output
	output := ec2.DescribeInstanceTopologyOutput{
		Instances: client.outputs[*givenToken],
	}
	nextToken, ok := client.nextTokens[*givenToken]
	if ok {
		output.NextToken = &nextToken
	}
	return &output, nil
}

func NamedLoaderSim() (string, providers.Loader) {
	return NAME_SIM, LoaderSim
}

func LoaderSim(ctx context.Context, cfg providers.Config) (providers.Provider, *httperr.Error) {
	p, err := providers.GetSimulationParams(cfg.Params)
	if err != nil {
		return nil, httperr.NewError(http.StatusBadRequest, err.Error())
	}

	model, err := models.NewModelFromFile(p.ModelFileName)
	if err != nil {
		return nil, httperr.NewError(http.StatusBadRequest, fmt.Sprintf("failed to load model file: %v", err))
	}

	sim := &simClient{
		model:       model,
		instanceIds: make([]string, 0, len(model.Nodes)),
		apiErr:      p.APIError,
	}
	for _, node := range model.Nodes {
		sim.instanceIds = append(sim.instanceIds, node.ID)
	}

	clientFactory := func(region string, pageSize *int) (*Client, error) {
		if p.APIError == errClientFactory {
			return nil, providers.ErrAPIError
		}

		return &Client{
			ec2:      sim,
			pageSize: setPageSize(pageSize),
		}, nil
	}

	return NewSim(clientFactory, p.TrimTiers, model), nil
}

type simProvider struct {
	baseProvider
	*providers.BaseSimProvider
}

func NewSim(clientFactory ClientFactory, trimTiers int, model *models.Model) *simProvider {
	return &simProvider{
		baseProvider: baseProvider{
			clientFactory: clientFactory,
		},
		BaseSimProvider: providers.NewBaseSimProvider(model, trimTiers),
	}
}

// Engine support

func (p *simProvider) GenerateTopologyConfig(ctx context.Context, pageSize *int, instances []topology.ComputeInstances) (*topology.Graph, *httperr.Error) {
	topo, err := p.generateInstanceTopology(ctx, pageSize, instances)
	if err != nil {
		return nil, err
	}
	return p.ToGraph(NAME_SIM, topo, instances, false), nil
}
