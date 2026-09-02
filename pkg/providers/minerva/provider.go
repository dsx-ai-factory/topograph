/*
 * Copyright 2026 NVIDIA CORPORATION
 * SPDX-License-Identifier: Apache-2.0
 */

package minerva

import (
	"context"
	"fmt"
	"net/http"

	"github.com/mitchellh/mapstructure"

	"github.com/NVIDIA/topograph/internal/config"
	"github.com/NVIDIA/topograph/internal/httperr"
	"github.com/NVIDIA/topograph/pkg/providers"
	"github.com/NVIDIA/topograph/pkg/topology"
)

const NAME = "minerva"

const (
	// paramKeyApiURL and credKeyApiKey are the provider's params/creds map
	// keys. Struct tags below must repeat these values verbatim — Go struct
	// tags are compile-time string literals and can't reference a constant.
	paramKeyApiURL = "apiUrl"
	credKeyApiKey  = "apiKey"

	// regionLocal is the placeholder region reported for every node: Minerva
	// has no concept of region, so there is nothing meaningful to report.
	regionLocal = "local"
)

type Provider struct {
	params *ProviderParams
	creds  *Credentials
}

type ProviderParams struct {
	ApiURL string `mapstructure:"apiUrl"`
}

type Credentials struct {
	ApiKey string `mapstructure:"apiKey"`
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

	if v, ok := creds[credKeyApiKey]; !ok || v == nil {
		return nil, fmt.Errorf("missing '%s'", credKeyApiKey)
	}
	if len(c.ApiKey) == 0 {
		return nil, fmt.Errorf("'%s' must not be empty", credKeyApiKey)
	}

	return c, nil
}

func getParams(params map[string]any) (*ProviderParams, error) {
	p := &ProviderParams{}
	if err := config.Decode(params, p); err != nil {
		return nil, fmt.Errorf("failed to decode params: %w", err)
	}
	if len(p.ApiURL) == 0 {
		return nil, fmt.Errorf("%s not provided", paramKeyApiURL)
	}

	return p, nil
}

func (p *Provider) GenerateTopologyConfig(ctx context.Context, pageSize *int, instances []topology.ComputeInstances) (*topology.Graph, *httperr.Error) {
	treeRoot, err := p.getNetworkTree(ctx, pageSize, instances)
	if err != nil {
		return nil, err
	}

	return &topology.Graph{
		Tiers: treeRoot,
	}, nil
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
		res[node] = regionLocal
	}
	return res, nil
}
