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

package gcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"

	compute "cloud.google.com/go/compute/apiv1"
	computepb "cloud.google.com/go/compute/apiv1/computepb"
	"cloud.google.com/go/compute/metadata"
	gax "github.com/googleapis/gax-go/v2"
	"github.com/mitchellh/mapstructure"
	"k8s.io/klog/v2"

	"github.com/dsx-ai-factory/topograph/internal/httperr"
	"github.com/dsx-ai-factory/topograph/pkg/providers"
	"github.com/dsx-ai-factory/topograph/pkg/topology"
)

const NAME = "gcp"

type baseProvider struct {
	clientFactory ClientFactory
	trimTiers     int
}

type projectConfig struct {
	ProjectID string `mapstructure:"project_id"`
}

type ClientFactory func(pageSize *int) (Client, error)

type InstanceIterator interface {
	Next() (*computepb.Instance, error)
}

type Client interface {
	ProjectID() string
	Instances(ctx context.Context, req *computepb.ListInstancesRequest, opts ...gax.CallOption) (InstanceIterator, string)
	PageSize() *uint32
}

type gcpClient struct {
	instanceClient *compute.InstancesClient
	projectID      string
	pageSize       *uint32
}

func (c *gcpClient) PageSize() *uint32 {
	return c.pageSize
}

func (c *gcpClient) ProjectID() string {
	return c.projectID
}

func (c *gcpClient) Instances(ctx context.Context, req *computepb.ListInstancesRequest, opts ...gax.CallOption) (InstanceIterator, string) {
	now := time.Now()
	iter := c.instanceClient.List(ctx, req, opts...)
	requestLatency.WithLabelValues("ListInstances").Observe(time.Since(now).Seconds())
	return iter, iter.PageInfo().Token
}

func NamedLoader() (string, providers.Loader) {
	return NAME, Loader
}

func Loader(ctx context.Context, config providers.Config) (providers.Provider, *httperr.Error) {
	projectID, err := getProjectID(ctx, config.Params)
	if err != nil {
		return nil, httperr.NewError(http.StatusBadRequest, err.Error())
	}
	trimTiers, err := providers.GetTrimTiers(config.Params)
	if err != nil {
		return nil, httperr.NewError(http.StatusBadRequest, "parameters error: "+err.Error())
	}
	clientFactory := func(pageSize *int) (Client, error) {
		instanceClient, err := compute.NewInstancesRESTClient(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to get instances client: %s", err.Error())
		}

		return &gcpClient{
			instanceClient: instanceClient,
			projectID:      projectID,
			pageSize:       castPageSize(pageSize),
		}, nil
	}

	return New(clientFactory, trimTiers), nil
}

func getProjectID(ctx context.Context, params map[string]any) (string, error) {
	// check project ID in params
	projectID, err := decodeProjectID(params, false)
	if err != nil {
		return "", fmt.Errorf("error in topology request parameters: %v", err)
	}
	if len(projectID) != 0 {
		klog.Infof("Getting project_id from topology request: %s", projectID)
		return projectID, nil
	}

	// if GOOGLE_APPLICATION_CREDENTIALS env var is set, get project ID from there
	if filePath := os.Getenv("GOOGLE_APPLICATION_CREDENTIALS"); len(filePath) != 0 {
		data, err := os.ReadFile(filePath)
		if err != nil {
			return "", fmt.Errorf("failed to read GOOGLE_APPLICATION_CREDENTIALS %s: %v", filePath, err)
		}

		var creds map[string]any
		if err = json.Unmarshal(data, &creds); err != nil {
			return "", fmt.Errorf("failed to parse GOOGLE_APPLICATION_CREDENTIALS %s: %v", filePath, err)
		}

		projectID, err = decodeProjectID(creds, true)
		if err != nil {
			return "", fmt.Errorf("error in GOOGLE_APPLICATION_CREDENTIALS: %v", err)
		}

		klog.Infof("Getting project_id from GOOGLE_APPLICATION_CREDENTIALS: %s", projectID)
		return projectID, nil
	}

	// otherwise get it from node metadata
	klog.Info("Getting project_id from node metadata")
	return metadata.ProjectIDWithContext(ctx)
}

func decodeProjectID(input map[string]any, must bool) (string, error) {
	c := &projectConfig{}
	if err := mapstructure.Decode(input, c); err != nil {
		return "", err
	}

	if must && c.ProjectID == "" {
		return "", fmt.Errorf("missing 'project_id'")
	}

	return c.ProjectID, nil
}

func (p *baseProvider) GenerateTopologyConfig(ctx context.Context, pageSize *int, instances []topology.ComputeInstances) (*topology.Graph, *httperr.Error) {
	topo, err := p.generateInstanceTopology(ctx, pageSize, instances)
	if err != nil {
		return nil, err
	}

	return topo.ToGraph(NAME, instances, p.trimTiers, false), nil
}

type Provider struct {
	baseProvider
}

func New(clientFactory ClientFactory, trimTiers int) *Provider {
	return &Provider{
		baseProvider: baseProvider{
			clientFactory: clientFactory,
			trimTiers:     trimTiers,
		},
	}
}

// Engine support

// Instances2NodeMap implements slurm.instanceMapper
func (p *Provider) Instances2NodeMap(ctx context.Context, nodes []string) (map[string]string, error) {
	return instanceToNodeMap(ctx, nodes)
}

// GetInstancesRegions implements slurm.instanceMapper
func (p *Provider) GetInstancesRegions(ctx context.Context, nodes []string) (map[string]string, error) {
	return getRegions(ctx, nodes)
}
