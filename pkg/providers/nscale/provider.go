/*
 * Copyright 2026 NVIDIA CORPORATION
 * SPDX-License-Identifier: Apache-2.0
 */

package nscale

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"sync"

	"github.com/dsx-ai-factory/topograph/internal/config"
	"github.com/dsx-ai-factory/topograph/internal/httperr"
	"github.com/dsx-ai-factory/topograph/internal/httpreq"
	"github.com/dsx-ai-factory/topograph/pkg/providers"
	"github.com/dsx-ai-factory/topograph/pkg/topology"
)

const (
	NAME = "nscale"

	urlTopologyPath = "/v2/topology"
)

type imdsFetchFunc func(ctx context.Context, nodes []string, imdsURL string) (map[string]*imdsMetadata, error)

// imdsCall represents a single in-flight fetchIMDSMetadata load. Callers
// that arrive while a load is in progress wait on done instead of issuing a
// redundant pdsh sweep; a canceled ctx lets a waiter stop waiting without
// affecting the in-flight load itself.
type imdsCall struct {
	done chan struct{}
	data map[string]*imdsMetadata
	err  error
}

type baseProvider struct {
	params *ProviderParams
	creds  *Credentials
	client Client

	imdsMu       sync.Mutex
	imdsNodes    []string
	imdsData     map[string]*imdsMetadata
	imdsInFlight *imdsCall
	imdsFetch    imdsFetchFunc
}

type ProviderParams struct {
	RadarApiUrl string `mapstructure:"radarApiUrl"`
	TrimTiers   int    `mapstructure:"trimTiers"`
	IMDSUrl     string `mapstructure:"-"`
}

type Credentials struct {
	Org   string `mapstructure:"org"`
	Token string `mapstructure:"token"`
	// Region, when set, restricts Slurm auto-discovery to nodes whose IMDS
	// region matches. Nodes whose IMDS region differs are excluded from the
	// topology query and logged as a warning.
	Region string `mapstructure:"region"`
}

type Client interface {
	Topology(context.Context, string, int, int) ([]InstanceTopology, *httperr.Error)
}

// nscaleClient is a Radar topology API client.
type nscaleClient struct {
	radarAPIURL string
	org         string
	token       string
}

// InstanceTopology represents the topology of a single instance.
type InstanceTopology struct {
	ServerID    string   `json:"server_id"`
	NetworkPath []string `json:"network_node_path"`
	BlockID     *string  `json:"block_id,omitempty"`
}

// TopologyResult represents the topology of a single instance.
type TopologyResult struct {
	Instances []InstanceTopology `json:"results"`
}

func (c *nscaleClient) Topology(ctx context.Context, region string, pageSize, offset int) ([]InstanceTopology, *httperr.Error) {
	headers := map[string]string{
		"Authorization":  "Bearer " + c.token,
		"X-Organization": c.org,
		"X-Region":       region,
	}
	query := map[string]string{
		"limit":  strconv.Itoa(pageSize),
		"offset": strconv.Itoa(offset),
	}
	f := httpreq.GetRequestFunc(ctx, http.MethodGet, headers, query, nil, c.radarAPIURL, urlTopologyPath)

	body, httpErr := httpreq.DoRequestWithRetries(f, false)
	if httpErr != nil {
		return nil, httpErr
	}

	resp := TopologyResult{}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, httperr.NewError(http.StatusBadGateway, err.Error())
	}

	return resp.Instances, nil
}

type Provider struct {
	baseProvider
}

func NamedLoader() (string, providers.Loader) {
	return NAME, Loader
}

func Loader(ctx context.Context, config providers.Config) (providers.Provider, *httperr.Error) {
	params, err := getParams(config.Params)
	if err != nil {
		return nil, httperr.NewError(http.StatusBadRequest, err.Error())
	}

	creds, err := getCreds(config.Creds)
	if err != nil {
		return nil, httperr.NewError(http.StatusBadRequest, err.Error())
	}

	return &Provider{
		baseProvider: baseProvider{
			client: &nscaleClient{
				radarAPIURL: params.RadarApiUrl,
				org:         creds.Org,
				token:       creds.Token,
			},
			params:    params,
			creds:     creds,
			imdsFetch: fetchIMDSMetadata,
		},
	}, nil
}

func getParams(params map[string]any) (*ProviderParams, error) {
	p := &ProviderParams{}
	if err := config.Decode(params, p); err != nil {
		return nil, fmt.Errorf("failed to decode params: %v", err)
	}
	if len(p.RadarApiUrl) == 0 {
		return nil, fmt.Errorf("missing 'radarApiUrl'")
	}

	imdsURL, err := providers.GetIMDSURL(params)
	if err != nil {
		return nil, err
	}
	p.IMDSUrl = imdsURL

	return p, nil
}

func getCreds(creds map[string]any) (*Credentials, error) {
	c := &Credentials{}
	if err := config.Decode(creds, c); err != nil {
		return nil, fmt.Errorf("failed to decode creds: %v", err)
	}
	if len(c.Org) == 0 {
		return nil, fmt.Errorf("missing 'org'")
	}
	if len(c.Token) == 0 {
		return nil, fmt.Errorf("missing 'token'")
	}

	return c, nil
}

func (p *baseProvider) GenerateTopologyConfig(ctx context.Context, pageSize *int, instances []topology.ComputeInstances) (*topology.Graph, *httperr.Error) {
	topo, err := p.generateInstanceTopology(ctx, pageSize, instances)
	if err != nil {
		return nil, err
	}

	return topo.ToGraph(NAME, instances, p.params.TrimTiers, false), nil
}

// Instances2NodeMap implements slurm.instanceMapper
func (p *Provider) Instances2NodeMap(ctx context.Context, nodes []string) (map[string]string, error) {
	nodeMeta, err := p.fetchIMDSMetadata(ctx, nodes)
	if err != nil {
		return nil, err
	}
	return instanceToNodeMap(nodeMeta), nil
}

// GetInstancesRegions implements slurm.instanceMapper
func (p *Provider) GetInstancesRegions(ctx context.Context, nodes []string) (map[string]string, error) {
	nodeMeta, err := p.fetchIMDSMetadata(ctx, nodes)
	if err != nil {
		return nil, err
	}
	return getRegions(nodeMeta), nil
}

// fetchIMDSMetadata returns parsed IMDS metadata for the given nodes, caching
// the result so that repeated calls with the same node list (as done by
// Instances2NodeMap and GetInstancesRegions) issue a single pdsh sweep. Nodes
// whose IMDS region does not match the configured 'region' credential are
// excluded, with a warning logged per excluded node.
//
// imdsMu only guards the cache and the in-flight bookkeeping, never the pdsh
// call itself: a caller that finds a load already in progress waits on that
// call's done channel rather than holding the lock, so a canceled ctx lets
// it stop waiting immediately instead of blocking for the full sweep.
func (p *baseProvider) fetchIMDSMetadata(ctx context.Context, nodes []string) (map[string]*imdsMetadata, error) {
	for {
		p.imdsMu.Lock()
		if p.imdsData != nil && slices.Equal(p.imdsNodes, nodes) {
			data := p.imdsData
			p.imdsMu.Unlock()
			return data, nil
		}

		if call := p.imdsInFlight; call != nil {
			p.imdsMu.Unlock()
			select {
			case <-call.done:
				continue
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}

		call := &imdsCall{done: make(chan struct{})}
		p.imdsInFlight = call
		p.imdsMu.Unlock()

		p.runIMDSFetch(ctx, nodes, call)
		return call.data, call.err
	}
}

// runIMDSFetch executes the pdsh sweep for call outside imdsMu, then
// publishes its result to the cache (on success) and wakes any waiters. The
// cleanup runs via defer so a canceled ctx, an error, or a panic unwinding
// through this frame all leave imdsInFlight cleared and call.done closed.
func (p *baseProvider) runIMDSFetch(ctx context.Context, nodes []string, call *imdsCall) {
	defer func() {
		p.imdsMu.Lock()
		if p.imdsInFlight == call {
			p.imdsInFlight = nil
		}
		if call.err == nil {
			p.imdsNodes = slices.Clone(nodes)
			p.imdsData = call.data
		}
		p.imdsMu.Unlock()
		close(call.done)
	}()

	nodeMeta, err := p.imdsFetchFunc()(ctx, nodes, p.imdsURL())
	if err != nil {
		call.err = err
		return
	}

	var expectedRegion string
	if p.creds != nil {
		expectedRegion = p.creds.Region
	}
	call.data = filterByRegion(nodeMeta, expectedRegion)
}

// imdsFetchFunc returns the configured IMDS loader, falling back to the
// package-level fetchIMDSMetadata for a baseProvider constructed without
// going through Loader.
func (p *baseProvider) imdsFetchFunc() imdsFetchFunc {
	if p.imdsFetch != nil {
		return p.imdsFetch
	}
	return fetchIMDSMetadata
}

// imdsURL returns the configured 'imdsUrl' provider parameter, falling back
// to the default IMDS endpoint when it is not set.
func (p *baseProvider) imdsURL() string {
	if p.params != nil && len(p.params.IMDSUrl) > 0 {
		return p.params.IMDSUrl
	}

	return IMDSURL
}
