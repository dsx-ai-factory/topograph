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

package dsx

import (
	"context"
	"fmt"
	"net/http"

	"github.com/mitchellh/mapstructure"
	"k8s.io/klog/v2"

	"github.com/dsx-ai-factory/topograph/internal/httperr"
	"github.com/dsx-ai-factory/topograph/pkg/providers"
	"github.com/dsx-ai-factory/topograph/pkg/topology"
)

const (
	NAME = "dsx"

	paramBaseURL = "base_url"
	credToken    = "token"
)

type baseProvider struct {
	clientFactory ClientFactory
	trimTiers     int
}

type ClientFactory func() (Client, error)

type Client interface {
	GetTopology(ctx context.Context, vpcID string, nodeIDs []string, pageSize int, pageToken string) (*TopologyResponse, error)
}

// TopologyResponse mirrors the models.TopologyResponse envelope from the DSX API spec.
// Switches is an ordered list of single-key entries (switch name → its adjacency).
type TopologyResponse struct {
	Switches      []map[string]SwitchAdjacency `json:"switches"`
	NextPageToken string                       `json:"next_page_token,omitempty"`
}

// SwitchAdjacency mirrors models.SwitchAdjacency from the DSX API spec.
type SwitchAdjacency struct {
	Switches []string   `json:"switches,omitempty"`
	Nodes    []NodeInfo `json:"nodes,omitempty"`
}

// NodeInfo mirrors models.Node from the DSX API spec.
type NodeInfo struct {
	NodeID               string `json:"node_id"`
	AcceleratedNetworkID string `json:"accelerated_network_id,omitempty"`
}

type paramsConfig struct {
	BaseURL string `mapstructure:"base_url"`
}

func NamedLoader() (string, providers.Loader) {
	return NAME, Loader
}

func Loader(ctx context.Context, config providers.Config) (providers.Provider, *httperr.Error) {
	var params paramsConfig
	if err := mapstructure.Decode(config.Params, &params); err != nil {
		return nil, httperr.NewError(http.StatusBadRequest, "parameters error: "+err.Error())
	}
	if params.BaseURL == "" {
		return nil, httperr.NewError(http.StatusBadRequest, fmt.Sprintf("parameters error: missing '%s'", paramBaseURL))
	}

	trimTiers, err := providers.GetTrimTiers(config.Params)
	if err != nil {
		return nil, httperr.NewError(http.StatusBadRequest, "parameters error: "+err.Error())
	}

	var token string
	if t, ok := config.Creds[credToken]; ok {
		s, ok := t.(string)
		if !ok {
			return nil, httperr.NewError(http.StatusBadRequest,
				fmt.Sprintf("credentials error: '%s' must be a string", credToken))
		}
		token = s
	}

	klog.InfoS("Loaded DSX provider", "base_url", params.BaseURL, "auth", map[bool]string{true: "bearer-token", false: "ambient-svid"}[token != ""])

	factory := func() (Client, error) {
		return NewHTTPClient(params.BaseURL, token), nil
	}
	return New(factory, trimTiers), nil
}

func (p *baseProvider) GenerateTopologyConfig(ctx context.Context, pageSize *int, instances []topology.ComputeInstances) (*topology.Graph, *httperr.Error) {
	topo, err := p.generateInstanceTopology(ctx, pageSize, instances)
	if err != nil {
		return nil, err
	}

	klog.Infof("Extracted topology for %d instances", topo.Len())

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
