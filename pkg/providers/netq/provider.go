/*
 * Copyright 2025 NVIDIA CORPORATION
 * SPDX-License-Identifier: Apache-2.0
 */

package netq

import (
	"context"
	"fmt"
	"net/http"

	"github.com/mitchellh/mapstructure"
	"k8s.io/klog/v2"

	"github.com/dsx-ai-factory/topograph/internal/config"
	"github.com/dsx-ai-factory/topograph/internal/httperr"
	"github.com/dsx-ai-factory/topograph/pkg/providers"
	"github.com/dsx-ai-factory/topograph/pkg/topology"
)

const NAME = "netq"

type Provider struct {
	params *ProviderParams
	creds  *Credentials
}

type ProviderParams struct {
	ApiURL string `mapstructure:"apiUrl"`
}

type Credentials struct {
	Username string `mapstructure:"username"`
	Password string `mapstructure:"password"`
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
		params: params,
		creds:  creds,
	}, nil
}

func getCreds(creds map[string]any) (*Credentials, error) {
	c := &Credentials{}
	if err := mapstructure.Decode(creds, c); err != nil {
		return nil, fmt.Errorf("failed to decode creds: %w", err)
	}

	for _, key := range []string{"username", "password"} {
		if v, ok := creds[key]; !ok || v == nil {
			return nil, fmt.Errorf("missing '%s'", key)
		}
	}

	return c, nil
}

func getParams(params map[string]any) (*ProviderParams, error) {
	p := &ProviderParams{}
	if err := config.Decode(params, p); err != nil {
		return nil, fmt.Errorf("failed to decode params: %w", err)
	}
	if len(p.ApiURL) == 0 {
		return nil, fmt.Errorf("apiUrl not provided")
	}

	return p, nil
}

func (p *Provider) GenerateTopologyConfig(ctx context.Context, _ *int, instances []topology.ComputeInstances) (*topology.Graph, *httperr.Error) {
	treeRoot, err := p.getNetworkTree(ctx, instances)
	if err != nil {
		return nil, err
	}

	graph := &topology.Graph{
		Tiers: treeRoot,
	}

	if domains, err := p.getNvlDomains(ctx); err != nil {
		klog.Warningf("Failed to get NVL domains: %v", err)
	} else {
		graph.Domains = domains
	}

	return graph, nil
}

// Instances2NodeMap implements slurm.instanceMapper
func (p *Provider) Instances2NodeMap(ctx context.Context, nodes []string) (map[string]string, error) {
	i2n := make(map[string]string)
	for _, node := range nodes {
		i2n[node] = node
	}

	return i2n, nil
}

// GetInstancesRegions implements slurm.instanceMapper
func (p *Provider) GetInstancesRegions(ctx context.Context, nodes []string) (map[string]string, error) {
	res := make(map[string]string)
	for _, node := range nodes {
		res[node] = "local"
	}
	return res, nil
}
