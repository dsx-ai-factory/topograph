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
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/mitchellh/mapstructure"

	"github.com/dsx-ai-factory/topograph/internal/httperr"
	"github.com/dsx-ai-factory/topograph/pkg/providers"
	"github.com/dsx-ai-factory/topograph/pkg/topology"
)

const (
	NAME            = "akamai"
	defaultPageSize = 100
	requestTimeout  = 30 * time.Second
)

type ProviderParams struct {
	APIURL    string `mapstructure:"apiUrl"`
	TrimTiers int    `mapstructure:"trimTiers"`
}

type Link struct {
	Device     string `json:"device"`
	LocalPort  string `json:"local_port"`
	RemotePort string `json:"remote_port"`
}

type Host struct {
	UUID string `json:"host_uuid"`
	Port string `json:"port"`
}

type Switch struct {
	Fabric      string `json:"fabric"`
	Name        string `json:"name"`
	Serial      string `json:"serial"`
	Uplink      *Link  `json:"uplink"`
	Connections []Link `json:"connections"`
	Hosts       []Host `json:"hosts"`
	IsHealthy   *bool  `json:"is_healthy,omitempty"`
}

type TopologyResult struct {
	Region  string   `json:"region"`
	Data    []Switch `json:"data"`
	Page    int      `json:"page"`
	Pages   int      `json:"pages"`
	Results int      `json:"results"`
}

// Client lets the simulator exercise the same pagination and discovery as
// the live API. Live clients hold a caller-supplied token.
type Client interface {
	Topology(context.Context, string, int, int) (*TopologyResult, *httperr.Error)
}

type Provider struct {
	params   ProviderParams
	client   Client
	metadata map[string]topology.Instance
}

type apiClient struct {
	params     ProviderParams
	token      string
	httpClient *http.Client
}

func NamedLoader() (string, providers.Loader) { return NAME, Loader }

func getParams(raw map[string]any) (ProviderParams, error) {
	var p ProviderParams
	if err := mapstructure.Decode(raw, &p); err != nil {
		return p, fmt.Errorf("invalid Akamai parameters: %w", err)
	}
	if p.APIURL != "" {
		if err := validateURL(p.APIURL); err != nil {
			return p, fmt.Errorf("invalid apiUrl: %w", err)
		}
		p.APIURL = strings.TrimRight(p.APIURL, "/")
	}
	trim, err := providers.GetTrimTiers(raw)
	p.TrimTiers = trim
	return p, err
}

func validateURL(value string) error {
	u, err := url.Parse(value)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("expected an absolute HTTP(S) URL without credentials, query, or fragment")
	}
	return nil
}

func Loader(_ context.Context, cfg providers.Config) (providers.Provider, *httperr.Error) {
	params, err := getParams(cfg.Params)
	if err != nil {
		return nil, httperr.NewError(http.StatusBadRequest, err.Error())
	}
	if params.APIURL == "" {
		return nil, httperr.NewError(http.StatusBadRequest, "apiUrl is required")
	}
	token, valid := cfg.Creds["token"].(string)
	if !valid || strings.TrimSpace(token) == "" || strings.ContainsAny(token, "\r\n") {
		return nil, httperr.NewError(http.StatusBadRequest, "creds.token must be a non-empty string without line breaks")
	}
	return &Provider{params: params, client: &apiClient{
		params: params, token: token,
		httpClient: &http.Client{Timeout: requestTimeout, CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		}},
	}}, nil
}

func (c *apiClient) Topology(ctx context.Context, region string, pageSize, page int) (*TopologyResult, *httperr.Error) {
	u, _ := url.Parse(c.params.APIURL + "/topology") // Validated by Loader.
	query := u.Query()
	query.Set("region", region)
	query.Set("page", strconv.Itoa(page))
	query.Set("page_size", strconv.Itoa(pageSize))
	u.RawQuery = query.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, httperr.NewError(http.StatusBadRequest, "invalid Akamai topology request")
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.token)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, httperr.NewError(http.StatusBadGateway, "Akamai topology request failed: "+err.Error())
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// Upstream bodies can echo credentials; return only the operation and status.
		status := resp.StatusCode
		if status < 400 {
			status = http.StatusBadGateway
		}
		return nil, httperr.NewError(status, fmt.Sprintf("Akamai topology request failed (HTTP %d)", resp.StatusCode))
	}
	result := &TopologyResult{}
	decoder := json.NewDecoder(resp.Body)
	if err := decoder.Decode(result); err != nil {
		return nil, httperr.NewError(http.StatusBadGateway, "invalid Akamai topology response")
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return nil, httperr.NewError(http.StatusBadGateway, "invalid Akamai topology response: trailing data")
	}
	return result, nil
}

func (p *Provider) GenerateTopologyConfig(ctx context.Context, pageSize *int, instances []topology.ComputeInstances) (*topology.Graph, *httperr.Error) {
	return p.generateGraph(ctx, NAME, pageSize, instances)
}
