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

// Package http implements the generic HTTP API provider. It reads fabric
// paths and optional accelerator domains from a configurable JSON topology
// endpoint and converts them into the canonical topology graph.
package http

import (
	"context"
	nethttp "net/http"
	"net/url"
	"time"

	"github.com/dsx-ai-factory/topograph/internal/httperr"
	"github.com/dsx-ai-factory/topograph/pkg/providers"
	"github.com/dsx-ai-factory/topograph/pkg/topology"
)

const NAME = "http"

type Provider struct {
	cfg     *config
	client  *nethttp.Client
	newAuth func() authenticator
	backoff func(resp *nethttp.Response, attempt int) time.Duration
}

func NamedLoader() (string, providers.Loader) {
	return NAME, Loader
}

func Loader(_ context.Context, cfg providers.Config) (providers.Provider, *httperr.Error) {
	return newProvider(cfg, newHTTPClient())
}

// newHTTPClient returns a client with verified TLS that never follows redirects,
// so credentials are only sent to the configured hosts.
func newHTTPClient() *nethttp.Client {
	transport := nethttp.DefaultTransport.(*nethttp.Transport).Clone()
	return &nethttp.Client{
		Transport: transport,
		CheckRedirect: func(*nethttp.Request, []*nethttp.Request) error {
			return nethttp.ErrUseLastResponse
		},
	}
}

func newProvider(cfg providers.Config, client *nethttp.Client) (*Provider, *httperr.Error) {
	creds, err := credentialsFromConfig(cfg.Creds)
	if err != nil {
		return nil, err
	}
	var rawAuth map[string]any
	if v, ok := cfg.Params["auth"]; ok && v != nil {
		m, isMap := v.(map[string]any)
		if !isMap {
			return nil, badRequest("'auth' must be a map")
		}
		rawAuth = m
	}
	authHeader, newAuth, err := parseAuth(rawAuth, creds, client)
	if err != nil {
		return nil, err
	}
	c, err := parseConfig(cfg.Params, authHeader)
	if err != nil {
		return nil, err
	}
	return &Provider{cfg: c, client: client, newAuth: newAuth, backoff: defaultBackoff}, nil
}

func (p *Provider) GenerateTopologyConfig(ctx context.Context, pageSize *int, instances []topology.ComputeInstances) (*topology.Graph, *httperr.Error) {
	requested, err := newRequestedSet(instances)
	if err != nil {
		return nil, err
	}
	cluster := topology.NewClusterTopology()
	if len(requested.byInstance) == 0 {
		return cluster.ToGraph(NAME, instances, p.cfg.trimTiers, false), nil
	}

	size := defaultPageSize
	if pageSize != nil && *pageSize > 0 {
		size = *pageSize
	}

	// One deadline covers token acquisition, pagination, and retries.
	ctx, cancel := context.WithTimeout(ctx, p.cfg.timeout)
	defer cancel()

	records, err := p.fetchAll(ctx, p.newAuth(), size)
	if err != nil {
		return nil, err
	}
	if err := requested.match(records, cluster, p.cfg.missingNodes); err != nil {
		return nil, err
	}
	return cluster.ToGraph(NAME, instances, p.cfg.trimTiers, false), nil
}

func cloneValues(v url.Values) url.Values {
	out := make(url.Values, len(v))
	for key, values := range v {
		out[key] = append([]string(nil), values...)
	}
	return out
}
