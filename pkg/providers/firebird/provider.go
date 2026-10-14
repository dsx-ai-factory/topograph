/*
 * Copyright 2026 NVIDIA CORPORATION
 * SPDX-License-Identifier: Apache-2.0
 */

package firebird

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/mitchellh/mapstructure"

	"github.com/dsx-ai-factory/topograph/internal/httperr"
	"github.com/dsx-ai-factory/topograph/internal/httpreq"
	"github.com/dsx-ai-factory/topograph/pkg/providers"
	"github.com/dsx-ai-factory/topograph/pkg/topology"
)

const NAME = "firebird"

const defaultPageSize = 100

type ProviderParams struct {
	APIURL    string `mapstructure:"apiUrl"`
	TrimTiers int    `mapstructure:"trimTiers"`
}

type Credentials struct {
	ClientID     string `mapstructure:"clientId" json:"clientId"`
	ClientSecret string `mapstructure:"clientSecret" json:"clientSecret"`
}

// Node is the Firebird /topology/nodes response record. NetworkPath contains
// switch names in leaf-first order; tiers metadata is intentionally ignored.
type Node struct {
	NodeID         string   `json:"nodeId"`
	NetworkPath    []string `json:"networkPath"`
	NodeKind       string   `json:"nodeKind"`
	ObservedAt     string   `json:"observedAt"`
	TopologyStatus string   `json:"topologyStatus"`
}

type TopologyResult struct {
	NextPageToken string `json:"nextPageToken"`
	Nodes         []Node `json:"nodes"`
}

// ErrorResponse is the envelope returned by Firebird for API failures.
type ErrorResponse struct {
	Error *APIError `json:"error"`
}

type APIError struct {
	Code    string            `json:"code"`
	Details map[string]string `json:"details"`
	Message string            `json:"message"`
}

// apiError preserves upstream HTTP statuses without exposing unstructured
// response bodies. Topograph's error contract carries diagnostics as a string.
func apiError(status int, body []byte) *httperr.Error {
	return firebirdAPIError(status, body, "topology")
}

func firebirdAPIError(status int, body []byte, operation string) *httperr.Error {
	message := "Firebird " + operation + " request failed"
	var response ErrorResponse
	if err := json.Unmarshal(body, &response); err == nil && response.Error != nil {
		if response.Error.Code != "" {
			message += ": " + response.Error.Code
		}
		if response.Error.Message != "" {
			message += ": " + response.Error.Message
		}
		if len(response.Error.Details) > 0 {
			details, _ := json.Marshal(response.Error.Details)
			message += fmt.Sprintf(" (details: %s)", details)
		}
	}
	return httperr.NewError(status, message)
}

type Client interface {
	Topology(context.Context, int, string) (*TopologyResult, *httperr.Error)
}

type Provider struct {
	client Client
	params ProviderParams
	creds  Credentials
}

func NamedLoader() (string, providers.Loader) {
	return NAME, Loader
}

func Loader(_ context.Context, cfg providers.Config) (providers.Provider, *httperr.Error) {
	p := &Provider{}
	if err := mapstructure.Decode(cfg.Params, &p.params); err != nil {
		return nil, httperr.NewError(http.StatusBadRequest, "invalid Firebird parameters: "+err.Error())
	}
	u, err := url.Parse(p.params.APIURL)
	if err != nil || u == nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, httperr.NewError(http.StatusBadRequest, "'apiUrl' must be an absolute HTTPS base URL without credentials, query, or fragment")
	}
	trimTiers, err := providers.GetTrimTiers(cfg.Params)
	if err != nil {
		return nil, httperr.NewError(http.StatusBadRequest, err.Error())
	}
	p.params.TrimTiers = trimTiers
	if err := mapstructure.Decode(cfg.Creds, &p.creds); err != nil {
		return nil, httperr.NewError(http.StatusBadRequest, "invalid Firebird credentials: expected 'clientId' and 'clientSecret' strings")
	}
	if strings.TrimSpace(p.creds.ClientID) == "" || strings.TrimSpace(p.creds.ClientSecret) == "" {
		return nil, httperr.NewError(http.StatusBadRequest, "Firebird credentials require non-empty 'clientId' and 'clientSecret' strings")
	}
	return p, nil
}

func (p *Provider) topologyPage(ctx context.Context, pageSize int, pageToken, token string) (*TopologyResult, *httperr.Error) {
	if p.client != nil {
		return p.client.Topology(ctx, pageSize, pageToken)
	}
	query := map[string]string{"pageSize": strconv.Itoa(pageSize)}
	if pageToken != "" {
		query["pageToken"] = pageToken
	}
	headers := map[string]string{"Accept": "application/json", "Authorization": "Bearer " + token}
	f := httpreq.GetRequestFunc(ctx, http.MethodGet, headers, query, nil, p.params.APIURL, "/topology/nodes")
	body, httpErr := httpreq.DoRequestWithRetries(f, false)
	if httpErr != nil {
		if ctx.Err() != nil {
			return nil, httperr.NewError(http.StatusBadGateway, "Firebird topology request canceled")
		}
		return nil, apiError(httpErr.Code(), body)
	}
	var result TopologyResult
	if err := json.Unmarshal(body, &result); err != nil || result.Nodes == nil {
		return nil, httperr.NewError(http.StatusBadGateway, "invalid Firebird topology response: expected a nodes array")
	}
	return &result, nil
}

func (p *Provider) GenerateTopologyConfig(ctx context.Context, pageSize *int, instances []topology.ComputeInstances) (*topology.Graph, *httperr.Error) {
	cluster, err := p.generateInstanceTopology(ctx, pageSize, instances)
	if err != nil {
		return nil, err
	}
	return cluster.ToGraph(NAME, instances, p.params.TrimTiers, false), nil
}

func (p *Provider) generateInstanceTopology(ctx context.Context, pageSize *int, instances []topology.ComputeInstances) (*topology.ClusterTopology, *httperr.Error) {
	size := defaultPageSize
	if pageSize != nil && *pageSize > 0 {
		size = *pageSize
	}
	requested := make(map[string]bool)
	for _, ci := range instances {
		for id := range ci.Instances {
			if strings.TrimSpace(id) == "" {
				return nil, httperr.NewError(http.StatusBadRequest, "Firebird instance IDs must not be empty")
			}
			requested[id] = true
		}
	}
	cluster := topology.NewClusterTopology()
	if len(requested) == 0 {
		return cluster, nil
	}
	// A token belongs to this generation request and is valid across all pages.
	var token string
	if p.client == nil {
		var authErr *httperr.Error
		token, authErr = p.accessToken(ctx)
		if authErr != nil {
			return nil, authErr
		}
	}
	seenNodes := make(map[string]bool)
	seenTokens := make(map[string]bool)
	pageToken := ""
	for {
		page, httpErr := p.topologyPage(ctx, size, pageToken, token)
		if httpErr != nil {
			return nil, httpErr
		}
		for _, node := range page.Nodes {
			if !requested[node.NodeID] {
				continue
			}
			if seenNodes[node.NodeID] {
				return nil, httperr.NewError(http.StatusBadGateway, "duplicate Firebird nodeId in topology response")
			}
			seenNodes[node.NodeID] = true
			// An absent path leaves the node to ToGraph's missing-topology fallback.
			if len(node.NetworkPath) == 0 {
				continue
			}
			for _, id := range node.NetworkPath {
				if strings.TrimSpace(id) == "" {
					return nil, httperr.NewError(http.StatusBadGateway, "empty switch ID in Firebird networkPath")
				}
			}
			tiers := topology.ClosestFirstFabricTiers(node.NetworkPath...)
			cluster.Append(&topology.InstanceTopology{
				InstanceID:  node.NodeID,
				FabricTiers: tiers,
				Instance:    &topology.Instance{ID: node.NodeID},
			})
		}
		pageToken = page.NextPageToken
		if pageToken == "" {
			break
		}
		if seenTokens[pageToken] {
			return nil, httperr.NewError(http.StatusBadGateway, "repeated Firebird nextPageToken")
		}
		seenTokens[pageToken] = true
	}
	return cluster, nil
}
