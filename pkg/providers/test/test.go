/*
 * Copyright 2024-2026 NVIDIA CORPORATION
 * SPDX-License-Identifier: Apache-2.0
 */

package test

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"

	"k8s.io/klog/v2"

	"github.com/dsx-ai-factory/topograph/internal/config"
	"github.com/dsx-ai-factory/topograph/internal/httperr"
	"github.com/dsx-ai-factory/topograph/pkg/models"
	"github.com/dsx-ai-factory/topograph/pkg/providers"
	"github.com/dsx-ai-factory/topograph/pkg/topology"
	"github.com/dsx-ai-factory/topograph/pkg/translate"
)

const NAME = "test"

type Provider struct {
	graph         *topology.Graph
	instance2node map[string]string
	model         *models.Model
}

type Params struct {
	TestcaseName         string `mapstructure:"testcaseName"`
	Description          string `mapstructure:"description"`
	GenerateResponseCode int    `mapstructure:"generateResponseCode"`
	TopologyResponseCode int    `mapstructure:"topologyResponseCode"`
	ModelFileName        string `mapstructure:"modelFileName"`
	ErrorMessage         string `mapstructure:"errorMessage"`
}

func NamedLoader() (string, providers.Loader) {
	return NAME, Loader
}

func NewParams() *Params {
	//Default params
	p := Params{
		GenerateResponseCode: http.StatusAccepted,
		TopologyResponseCode: http.StatusOK,
	}

	return &p
}

func HandleTestProviderRequest(w http.ResponseWriter, tr *topology.Request) bool {

	//If not test provider request, continue with the normal flow
	if tr.Provider.Name != NAME {
		return false
	}

	klog.InfoS("Using test provider; returning simulated response immediately")

	//Parse the params
	p := NewParams()
	if err := config.Decode(tr.Provider.Params, p); err != nil {
		http.Error(w, fmt.Sprintf("error decoding params: %v", err), http.StatusBadRequest)
		return true
	}

	//check and see if we need to short-circuit the request
	if 400 <= p.GenerateResponseCode && p.GenerateResponseCode <= 599 {
		http.Error(w, p.ErrorMessage, p.GenerateResponseCode)
		return true
	} else if p.GenerateResponseCode != http.StatusAccepted {
		http.Error(w, "Unsupported response code.", http.StatusBadRequest)
		return true
	}

	//continue with the normal flow
	return false
}

func Loader(_ context.Context, cfg providers.Config) (providers.Provider, *httperr.Error) {
	p := NewParams()
	if err := config.Decode(cfg.Params, p); err != nil {
		return nil, httperr.NewError(http.StatusBadRequest, fmt.Sprintf("error decoding params: %v", err))
	}
	provider := &Provider{}

	if (400 <= p.TopologyResponseCode && p.TopologyResponseCode <= 599) || p.TopologyResponseCode == 202 {
		return nil, httperr.NewError(p.TopologyResponseCode, p.ErrorMessage)
	} else if p.TopologyResponseCode != 200 {
		return nil, httperr.NewError(http.StatusBadRequest, fmt.Sprintf("Invalid topology response code: %v", p.TopologyResponseCode))
	}

	if len(p.ModelFileName) != 0 {
		if filepath.Base(p.ModelFileName) != p.ModelFileName {
			return nil, httperr.NewError(http.StatusBadRequest,
				fmt.Sprintf("modelFileName %q must be a bare filename, not a path", p.ModelFileName))
		}
		klog.InfoS("Using simulated topology from", "modelFileName", p.ModelFileName)
		model, err := models.NewModelFromFile(p.ModelFileName)
		if err != nil {
			return nil, httperr.NewError(http.StatusBadRequest, fmt.Sprintf("failed to read model file %s: %v", p.ModelFileName, err))
		}

		provider.model = model
		provider.graph, provider.instance2node = model.ToGraph(nil)
	} else {
		provider.graph, provider.instance2node = translate.GetTreeTestSet(false)
	}
	return provider, nil
}

func (p *Provider) GetComputeInstances(_ context.Context) ([]topology.ComputeInstances, *httperr.Error) {
	return []topology.ComputeInstances{
		{
			Instances: p.instance2node,
		},
	}, nil
}

func (p *Provider) GenerateTopologyConfig(_ context.Context, _ *int, instances []topology.ComputeInstances) (*topology.Graph, *httperr.Error) {
	if p.model != nil {
		graph, _ := p.model.ToGraph(instances)
		return graph, nil
	}
	return p.graph, nil
}
