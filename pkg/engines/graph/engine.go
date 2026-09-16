/*
 * Copyright 2026, NVIDIA CORPORATION
 * SPDX-License-Identifier: Apache-2.0
 */
package graph

import (
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"slices"

	"github.com/dsx-ai-factory/topograph/internal/config"
	"github.com/dsx-ai-factory/topograph/internal/files"
	"github.com/dsx-ai-factory/topograph/internal/httperr"
	"github.com/dsx-ai-factory/topograph/pkg/engines"
	"github.com/dsx-ai-factory/topograph/pkg/topology"
)

const NAME = "graph"

type GraphEngine struct {
	params *Params
}

type Params struct {
	TopologyConfigPath string `mapstructure:"topologyConfigPath"`
}

func NamedLoader() (string, engines.Loader) {
	return NAME, Loader
}

func Loader(_ context.Context, params engines.Config) (engines.Engine, *httperr.Error) {
	p, err := getParameters(params)
	if err != nil {
		return nil, httperr.NewError(http.StatusBadRequest, err.Error())
	}

	return &GraphEngine{
		params: p,
	}, nil
}

func getParameters(params engines.Config) (*Params, error) {
	p := &Params{}
	if err := config.Decode(params, p); err != nil {
		return nil, err
	}

	return p, nil
}

func (eng *GraphEngine) GenerateOutput(_ context.Context, graph *topology.Graph, _ map[string]any) ([]byte, *httperr.Error) {
	doc := makeInstancesDocument(graphInstances(graph))
	data, err := json.Marshal(doc)
	if err != nil {
		return nil, httperr.NewError(http.StatusInternalServerError, err.Error())
	}

	if eng.params == nil || eng.params.TopologyConfigPath == "" {
		return data, nil
	}

	if err := files.Create(eng.params.TopologyConfigPath, data); err != nil {
		return nil, httperr.NewError(http.StatusInternalServerError, err.Error())
	}

	return []byte("OK\n"), nil
}

func (eng *GraphEngine) ResolveComputeInstances(_ context.Context, instances []topology.ComputeInstances, _ any) ([]topology.ComputeInstances, *httperr.Error) {
	if len(instances) != 0 {
		return instances, nil
	}
	return nil, httperr.NewError(http.StatusBadRequest,
		"graph engine requires nodes in the request or a provider that can supply compute instances")
}

func graphInstances(graph *topology.Graph) map[string]topology.Instance {
	if graph == nil {
		return nil
	}
	return graph.Instances
}

func makeInstancesDocument(instances map[string]topology.Instance) *topology.Instances {
	keys := slices.Sorted(maps.Keys(instances))
	doc := &topology.Instances{
		Instances: make([]topology.Instance, 0, len(keys)),
	}
	for _, key := range keys {
		doc.Instances = append(doc.Instances, instances[key])
	}
	return doc
}
