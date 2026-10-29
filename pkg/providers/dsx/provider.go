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
	"net/http"

	"k8s.io/klog/v2"

	"github.com/dsx-ai-factory/topograph/internal/httperr"
	"github.com/dsx-ai-factory/topograph/pkg/providers"
	"github.com/dsx-ai-factory/topograph/pkg/topology"
)

const NAME = "dsx"

type baseProvider struct {
	clientFactory ClientFactory
	trimTiers     int
}

type ClientFactory func() (Client, error)

type Client interface {
	GetTopology(ctx context.Context, vpcID string, nodeIDs []string, pageSize int, pageToken string) (*TopologyResponse, error)
}

type TopologyResponse struct {
	Switches map[string]SwitchInfo `json:"switches"`
}

type SwitchInfo struct {
	Switches []string   `json:"switches,omitempty"`
	Nodes    []NodeInfo `json:"nodes,omitempty"`
}

type NodeInfo struct {
	NodeID               string `json:"node_id"`
	AcceleratedNetworkID string `json:"accelerated_network_id,omitempty"`
}

func NamedLoader() (string, providers.Loader) {
	return NAME, Loader
}

func Loader(ctx context.Context, config providers.Config) (providers.Provider, *httperr.Error) {
	// TODO: Implement real loader with authentication
	return nil, httperr.NewError(http.StatusNotImplemented, "dsx provider not implemented")
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
