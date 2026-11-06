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
	"net/url"
	"regexp"
	"strings"

	"github.com/mitchellh/mapstructure"
	"k8s.io/klog/v2"

	"github.com/dsx-ai-factory/topograph/internal/httperr"
	"github.com/dsx-ai-factory/topograph/internal/oauth"
	"github.com/dsx-ai-factory/topograph/pkg/providers"
	"github.com/dsx-ai-factory/topograph/pkg/topology"
)

const (
	NAME = "dsx"

	paramBaseURL = "base_url"
	paramVPCID   = "vpc_id"
	credToken    = "token"
)

// validVPCID restricts vpc_id to one safe URL path segment: "/", "?", "#", "%"
// and "." / ".." are rejected.
var validVPCID = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._-]*$`)

// baseProvider holds the state shared by the real and the simulated provider.
type baseProvider struct {
	clientFactory ClientFactory
	trimTiers     int
	vpcID         string
}

// ClientFactory creates the Client used for one topology generation.
type ClientFactory func() (Client, error)

// Client fetches one page of the VPC topology from the DSX API.
type Client interface {
	GetTopology(ctx context.Context, vpcID string, pageSize int, pageToken string) (*TopologyResponse, error)
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

// paramsConfig holds the provider parameters.
type paramsConfig struct {
	BaseURL string `mapstructure:"base_url"`
	// VPCID selects GET /v1/topology/vpcs/{vpc_id}/nodes. It is embedded in the
	// request path, so it is restricted to a single safe path segment.
	VPCID string `mapstructure:"vpc_id"`
}

// NamedLoader returns the provider name and its loader for the registry.
func NamedLoader() (string, providers.Loader) {
	return NAME, Loader
}

// Loader validates the parameters and credentials and returns the DSX provider.
func Loader(ctx context.Context, config providers.Config) (providers.Provider, *httperr.Error) {
	var params paramsConfig
	if err := mapstructure.Decode(config.Params, &params); err != nil {
		return nil, httperr.NewError(http.StatusBadRequest, "parameters error: "+err.Error())
	}
	if params.BaseURL == "" {
		return nil, httperr.NewError(http.StatusBadRequest, fmt.Sprintf("parameters error: missing '%s'", paramBaseURL))
	}

	if u, err := url.Parse(params.BaseURL); err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, httperr.NewError(http.StatusBadRequest, fmt.Sprintf("parameters error: invalid '%s'", paramBaseURL))
	}

	// The server's credentials are sent to base_url, so a request that brings no
	// credentials of its own must not point them at another endpoint.
	if config.ServerCreds {
		serverURL, _ := config.ServerParams[paramBaseURL].(string)
		if strings.TrimRight(params.BaseURL, "/") != strings.TrimRight(serverURL, "/") {
			return nil, httperr.NewError(http.StatusForbidden, fmt.Sprintf(
				"parameters error: '%s' must match the server configuration when the request supplies no credentials", paramBaseURL))
		}
	}

	if params.VPCID == "" {
		return nil, httperr.NewError(http.StatusBadRequest, fmt.Sprintf("parameters error: missing '%s'", paramVPCID))
	}
	if !validVPCID.MatchString(params.VPCID) {
		return nil, httperr.NewError(http.StatusBadRequest, fmt.Sprintf("parameters error: invalid '%s'", paramVPCID))
	}

	trimTiers, err := providers.GetTrimTiers(config.Params)
	if err != nil {
		return nil, httperr.NewError(http.StatusBadRequest, "parameters error: "+err.Error())
	}

	var token string
	if v, ok := config.Creds[credToken]; ok {
		t, ok := v.(string)
		if !ok {
			return nil, httperr.NewError(http.StatusBadRequest,
				fmt.Sprintf("credentials error: '%s' must be a string", credToken))
		}
		token = t
	}

	// The OAuth2 client-credentials settings decode straight from the credentials.
	oauthCfg, err := oauth.DecodeConfig(config.Creds)
	if err != nil {
		return nil, httperr.NewError(http.StatusBadRequest, "credentials error: "+err.Error())
	}
	if oauthCfg.Configured() && token != "" {
		return nil, httperr.NewError(http.StatusBadRequest,
			fmt.Sprintf("credentials error: provide either '%s' or the OAuth2 client credentials, not both", credToken))
	}

	// Bearer tokens must not travel in cleartext; without credentials the Envoy
	// sidecar authenticates in-cluster callers and plain http is acceptable.
	if token != "" || oauthCfg.Configured() {
		if err := oauth.ValidateURL(params.BaseURL); err != nil {
			return nil, httperr.NewError(http.StatusBadRequest,
				fmt.Sprintf("parameters error: '%s' must use https when credentials are configured: %v", paramBaseURL, err))
		}
	}

	var auth string
	var factory ClientFactory
	switch {
	case oauthCfg.Configured():
		ts, err := oauth.NewClientCredentialsSource(oauthCfg)
		if err != nil {
			return nil, httperr.NewError(http.StatusBadRequest, "credentials error: "+err.Error())
		}
		auth = "client-credentials"
		factory = func() (Client, error) {
			return NewHTTPClientWithTokenSource(params.BaseURL, ts), nil
		}
	default:
		if token != "" {
			auth = "bearer-token"
		}
		factory = func() (Client, error) {
			return NewHTTPClient(params.BaseURL, token), nil
		}
	}

	klog.InfoS("Loaded DSX provider", "base_url", params.BaseURL, "vpc_id", params.VPCID, "auth", auth)

	p := New(factory, trimTiers)
	p.vpcID = params.VPCID
	return p, nil
}

// GenerateTopologyConfig fetches the VPC topology and converts it to a graph.
func (p *baseProvider) GenerateTopologyConfig(ctx context.Context, pageSize *int, instances []topology.ComputeInstances) (*topology.Graph, *httperr.Error) {
	topo, err := p.generateInstanceTopology(ctx, pageSize, instances)
	if err != nil {
		return nil, err
	}

	klog.Infof("Extracted topology for %d instances", topo.Len())

	return topo.ToGraph(NAME, instances, p.trimTiers, false), nil
}

// GetNodeInstances implements slurm.instanceMapper. Slurm node names are the DSX
// node IDs, so each node maps to itself. The region is unused by this provider.
func (p *baseProvider) GetNodeInstances(_ context.Context, nodes []string) ([]topology.ComputeInstances, error) {
	return providers.SelfNamedInstances(nodes, "local"), nil
}

// Provider is the DSX topology provider.
type Provider struct {
	baseProvider
}

// New returns a Provider that obtains its API client from clientFactory.
func New(clientFactory ClientFactory, trimTiers int) *Provider {
	return &Provider{
		baseProvider: baseProvider{
			clientFactory: clientFactory,
			trimTiers:     trimTiers,
		},
	}
}
