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

package minerva

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"github.com/mitchellh/mapstructure"
	"k8s.io/klog/v2"

	"github.com/dsx-ai-factory/topograph/internal/config"
	"github.com/dsx-ai-factory/topograph/internal/httperr"
	"github.com/dsx-ai-factory/topograph/pkg/providers"
	"github.com/dsx-ai-factory/topograph/pkg/topology"
)

const NAME = "minerva"

const (
	// paramKeyApiURL and credKeyApiKey are the provider's params/creds map
	// keys. Struct tags below must repeat these values verbatim — Go struct
	// tags are compile-time string literals and can't reference a constant.
	paramKeyApiURL            = "apiUrl"
	paramKeyFailOnEmpty       = "failOnEmpty"
	credKeyApiKey             = "apiKey"
	credKeyInsecureSkipVerify = "insecureSkipVerify"

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

	// FailOnEmpty selects when an empty layer fails the request:
	//   "both" (default): only when the east-west and nvlink layers both fail or yield nothing;
	//   "east-west":      when the east-west layer fails or places no requested node in a switch tree;
	//   "nvlink":         when the nvlink layer fails or yields no NVLink domains.
	FailOnEmpty string `mapstructure:"failOnEmpty"`
}

const (
	failOnEmptyBoth     = "both"
	failOnEmptyEastWest = "east-west"
	failOnEmptyNVLink   = "nvlink"
)

// layerResult is the outcome of one exported layer: the error that ended it,
// or whether it contributed nothing.
type layerResult struct {
	err   *httperr.Error
	empty bool
}

// bad reports whether the layer failed or contributed nothing. The failOnEmpty
// policy treats the two the same way.
func (r layerResult) bad() bool { return r.err != nil || r.empty }

// check applies the failOnEmpty policy to the outcome of the two layers. A
// layer that failed counts as empty, so the policy decides whether the request
// survives it; the layer's own error is returned when it is the reason the
// request fails.
func (p *ProviderParams) check(tree, domains layerResult) *httperr.Error {
	reason := func(r layerResult, msg string) *httperr.Error {
		if r.err != nil {
			return r.err
		}
		return httperr.NewError(http.StatusBadGateway, msg)
	}

	switch p.FailOnEmpty {
	case failOnEmptyEastWest:
		if tree.bad() {
			return reason(tree, "no EastWest topology available from Minerva")
		}
	case failOnEmptyNVLink:
		if domains.bad() {
			return reason(domains, "no NVLink domains available from Minerva")
		}
	default:
		if tree.bad() && domains.bad() {
			if tree.err != nil {
				return tree.err
			}
			return reason(domains, "no EastWest topology or NVLink domains available from Minerva")
		}
	}
	return nil
}

type Credentials struct {
	ApiKey string `mapstructure:"apiKey"`

	// InsecureSkipVerify turns off verification of Minerva's TLS certificate. It
	// is a convenience for quickly validating against an instance in a trusted
	// environment, and is off by default.
	InsecureSkipVerify bool `mapstructure:"insecureSkipVerify"`
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

	if creds.InsecureSkipVerify {
		klog.Warningf("Minerva TLS certificate verification is disabled by %s: the server is not authenticated", credKeyInsecureSkipVerify)
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
	// The API key travels in a request header, so refuse to send it in clear text.
	u, err := url.Parse(p.ApiURL)
	if err != nil || u.Scheme != "https" || len(u.Host) == 0 {
		return nil, fmt.Errorf("%s must be an https URL", paramKeyApiURL)
	}

	p.FailOnEmpty = strings.ToLower(strings.TrimSpace(p.FailOnEmpty))
	switch p.FailOnEmpty {
	case "", failOnEmptyBoth, failOnEmptyEastWest, failOnEmptyNVLink:
	default:
		return nil, fmt.Errorf("%s: unsupported value %q, expected %q, %q or %q", paramKeyFailOnEmpty, p.FailOnEmpty, failOnEmptyBoth, failOnEmptyEastWest, failOnEmptyNVLink)
	}

	return p, nil
}

// GenerateTopologyConfig exports the east-west and nvlink layers concurrently
// and combines them into one graph. The failOnEmpty parameter decides which
// layer the request depends on: a layer that fails, times out or comes back
// empty is tolerated, with a warning and without its part of the graph, unless
// the policy needs it. A canceled request always fails.
func (p *Provider) GenerateTopologyConfig(ctx context.Context, pageSize *int, instances []topology.ComputeInstances) (*topology.Graph, *httperr.Error) {
	var (
		treeRoot  *topology.Vertex
		resolved  bool
		treeErr   *httperr.Error
		domains   topology.DomainMap
		domainErr *httperr.Error
		wg        sync.WaitGroup
	)
	wg.Add(2)
	go func() {
		defer wg.Done()
		treeRoot, resolved, treeErr = p.buildNetworkTree(ctx, pageSize, instances)
	}()
	go func() {
		defer wg.Done()
		domains, domainErr = p.getDomains(ctx, pageSize, instances)
	}()
	wg.Wait()

	// A canceled or expired request fails whatever the policy says, even if a
	// layer finished before the cancellation.
	if ctxErr := ctx.Err(); ctxErr != nil {
		switch {
		case treeErr != nil:
			return nil, treeErr
		case domainErr != nil:
			return nil, domainErr
		}
		return nil, httperr.NewError(http.StatusBadGateway, ctxErr.Error())
	}

	tree := layerResult{err: treeErr, empty: !resolved}
	dom := layerResult{err: domainErr, empty: len(domains) == 0}
	if err := p.params.check(tree, dom); err != nil {
		return nil, err
	}
	if treeErr != nil || len(treeRoot.Vertices) == 0 {
		treeRoot = nil
	}
	if treeErr != nil {
		klog.Warningf("Minerva east-west topology is unavailable, continuing without it: %v", treeErr)
	}
	if domainErr != nil {
		klog.Warningf("Minerva NVLink domains are unavailable, continuing without them: %v", domainErr)
		domains = nil
	}

	return &topology.Graph{
		Tiers:   treeRoot,
		Domains: domains,
	}, nil
}

// GetNodeInstances implements slurm.instanceMapper. Minerva identifies compute
// nodes by name, so each node is its own instance.
func (p *Provider) GetNodeInstances(_ context.Context, nodes []string) ([]topology.ComputeInstances, error) {
	return providers.SelfNamedInstances(nodes, regionLocal), nil
}
