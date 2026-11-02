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
	// requested holds the instance IDs and node names resolved for the request.
	// When set, accelerator-domain hosts outside it are not reported.
	requested map[string]bool
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
	doc := makeInstancesDocument(graphInstances(graph, eng.requested))
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
		eng.requested = make(map[string]bool)
		for _, ci := range instances {
			for id, name := range ci.Instances {
				// an empty ID or name must not match nodes that lack one
				for _, key := range []string{id, name} {
					if key != "" {
						eng.requested[key] = true
					}
				}
			}
		}
		return instances, nil
	}
	return nil, httperr.NewError(http.StatusBadRequest,
		"graph engine requires nodes in the request or a provider that can supply compute instances")
}

// graphInstances returns the per-instance records for the graph. When the
// provider did not attach any, they are derived from the fabric tree and the
// accelerator domains, so that every provider works with this engine. Attached
// records are authoritative, but any network layers or accelerator domain
// labels they lack are filled in from the graph.
func graphInstances(graph *topology.Graph, requested map[string]bool) map[string]topology.Instance {
	if graph == nil {
		return nil
	}
	derived := deriveInstances(graph, requested)
	if len(graph.Instances) == 0 {
		return derived
	}

	instances := make(map[string]topology.Instance, len(graph.Instances))
	for id, inst := range graph.Instances {
		instances[id] = fillInstance(inst, derived[id])
	}
	return instances
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

// deriveInstances builds instance records from Graph.Tiers and Graph.Domains.
// Leaf vertices of the tier tree are compute nodes; their ancestors, listed
// closest-first, are the network layers. Nodes under the no-topology
// pseudo-switch get no layers, and hosts that appear only in an accelerator
// domain are reported without layers. Iteration is sorted so that the result
// is deterministic when an ID or name is repeated: the first one wins. When
// requested is not nil, only requested nodes are reported: a provider may hand
// back a tree or domain map that covers more nodes than the request, and a
// request for unknown nodes must not fall back to the whole fabric.
func deriveInstances(graph *topology.Graph, requested map[string]bool) map[string]topology.Instance {
	instances := make(map[string]topology.Instance)
	nameToID := make(map[string]string) // compute node name -> instance ID

	// path holds root-first switch display names, skipping the no-topology marker
	var walk func(v *topology.Vertex, path []string)
	walk = func(v *topology.Vertex, path []string) {
		for _, key := range slices.Sorted(maps.Keys(v.Vertices)) {
			child := v.Vertices[key]
			if len(child.Vertices) != 0 {
				next := path
				if child.ID != topology.NoTopology {
					layer := child.Name
					if layer == "" {
						layer = child.ID
					}
					next = append(path[:len(path):len(path)], layer)
				}
				walk(child, next)
				continue
			}
			id := child.ID
			if id == "" {
				id = child.Name
			}
			if id == "" {
				continue
			}
			if _, ok := instances[id]; ok {
				continue
			}
			if requested != nil && !requested[id] && !requested[child.Name] {
				continue
			}
			layers := make([]string, 0, len(path))
			for i := len(path) - 1; i >= 0; i-- {
				layers = append(layers, path[i])
			}
			instances[id] = topology.Instance{ID: id, NetworkLayers: layers}
			if child.Name != "" && child.Name != id {
				if _, ok := nameToID[child.Name]; !ok {
					nameToID[child.Name] = id
				}
			}
		}
	}
	if graph.Tiers != nil {
		walk(graph.Tiers, nil)
	}

	for _, domain := range slices.Sorted(maps.Keys(graph.Domains)) {
		hosts := graph.Domains[domain]
		for _, hostKey := range slices.Sorted(maps.Keys(hosts)) {
			host := hosts[hostKey]
			id := host.InstanceID
			if _, ok := instances[id]; !ok {
				if mapped, ok := nameToID[host.HostName]; ok {
					id = mapped
				} else if _, ok := instances[host.HostName]; ok {
					id = host.HostName
				}
			}
			if id == "" {
				id = host.HostName
			}
			if id == "" {
				continue
			}
			inst, ok := instances[id]
			if !ok {
				if requested != nil && !requested[id] && !requested[host.HostName] {
					continue
				}
				inst = topology.Instance{ID: id, NetworkLayers: []string{}}
			}
			if inst.Labels[topology.KeyTopologyXclrDomain] != "" {
				continue // a host belongs to one domain; first one wins
			}
			if inst.Labels == nil {
				inst.Labels = make(map[string]string)
			}
			inst.Labels[topology.KeyTopologyXclrDomain] = domain
			if host.SubDomain != "" {
				inst.Labels[topology.KeyTopologyXclrSubDomain] = host.SubDomain
			}
			instances[id] = inst
		}
	}

	return instances
}

// fillInstance completes inst with the network layers and accelerator labels of
// the derived record. Values already set on inst are kept. The input maps and
// slices are not modified.
func fillInstance(inst, derived topology.Instance) topology.Instance {
	if len(inst.NetworkLayers) == 0 && len(derived.NetworkLayers) != 0 {
		inst.NetworkLayers = derived.NetworkLayers
	}
	for _, key := range []string{topology.KeyTopologyXclrDomain, topology.KeyTopologyXclrSubDomain} {
		val := derived.Labels[key]
		if val == "" || inst.Labels[key] != "" {
			continue
		}
		// a sub-domain only makes sense within the domain it was derived with
		if key == topology.KeyTopologyXclrSubDomain &&
			inst.Labels[topology.KeyTopologyXclrDomain] != derived.Labels[topology.KeyTopologyXclrDomain] {
			continue
		}
		labels := make(map[string]string, len(inst.Labels)+1)
		maps.Copy(labels, inst.Labels)
		labels[key] = val
		inst.Labels = labels
	}
	return inst
}
