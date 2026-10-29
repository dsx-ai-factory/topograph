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
	"net/http"
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/dsx-ai-factory/topograph/internal/httperr"
	"github.com/dsx-ai-factory/topograph/pkg/providers"
	"github.com/dsx-ai-factory/topograph/pkg/providers/infiniband"
	"github.com/dsx-ai-factory/topograph/pkg/topology"
)

const testInstanceLabelKey = "example.com/gpu-product"

func TestNamedLoader(t *testing.T) {
	name, _ := NamedLoader()
	require.Equal(t, NAME, name)
}

func TestGetParameters(t *testing.T) {
	testCases := []struct {
		name   string
		params map[string]any
		want   *Params
	}{
		{
			name:   "empty params",
			params: nil,
			want:   &Params{},
		},
		{
			name:   "valid topologyConfigPath",
			params: map[string]any{"topologyConfigPath": "/var/lib/graph/out.json"},
			want:   &Params{TopologyConfigPath: "/var/lib/graph/out.json"},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := getParameters(tc.params)
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestLoader(t *testing.T) {
	eng, herr := Loader(context.Background(), map[string]any{"topologyConfigPath": "/tmp/x"})
	require.Nil(t, herr)
	require.NotNil(t, eng)
	ge, ok := eng.(*GraphEngine)
	require.True(t, ok)
	require.Equal(t, "/tmp/x", ge.params.TopologyConfigPath)
}

func TestGenerateOutput(t *testing.T) {
	t.Run("empty compute instances returns empty document", func(t *testing.T) {
		eng := &GraphEngine{params: &Params{}}
		ctx := context.Background()
		out, herr := eng.GenerateOutput(ctx, nil, nil)
		require.Nil(t, herr)
		var doc topology.Instances
		require.NoError(t, json.Unmarshal(out, &doc))
		require.Empty(t, doc.Instances)
	})

	t.Run("sorts by id", func(t *testing.T) {
		eng := &GraphEngine{params: &Params{}}
		ctx := context.Background()
		graph := &topology.Graph{
			Instances: map[string]topology.Instance{
				"z": {ID: "z"},
				"a": {ID: "a"},
			},
		}
		out, herr := eng.GenerateOutput(ctx, graph, nil)
		require.Nil(t, herr)
		var doc topology.Instances
		require.NoError(t, json.Unmarshal(out, &doc))
		require.Len(t, doc.Instances, 2)
		require.Equal(t, "a", doc.Instances[0].ID)
		require.Equal(t, "z", doc.Instances[1].ID)
	})

	t.Run("no output path returns JSON bytes", func(t *testing.T) {
		eng := &GraphEngine{params: &Params{}}
		ctx := context.Background()
		graph := &topology.Graph{Instances: map[string]topology.Instance{"n1": {ID: "n1"}}}
		out, herr := eng.GenerateOutput(ctx, graph, nil)
		require.Nil(t, herr)
		require.True(t, json.Valid(out))
		require.NotEqual(t, "OK\n", string(out))
	})

	t.Run("output path existing file writes JSON and returns OK", func(t *testing.T) {
		f, err := os.CreateTemp(t.TempDir(), "graph-out-*.json")
		require.NoError(t, err)
		path := f.Name()
		require.NoError(t, f.Close())

		eng := &GraphEngine{params: &Params{TopologyConfigPath: path}}
		ctx := context.Background()
		graph := &topology.Graph{Instances: map[string]topology.Instance{"n1": {
			ID:     "n1",
			Labels: map[string]string{testInstanceLabelKey: "H100"},
		}}}
		out, herr := eng.GenerateOutput(ctx, graph, nil)
		require.Nil(t, herr)
		require.Equal(t, "OK\n", string(out))

		written, err := os.ReadFile(path)
		require.NoError(t, err)
		var doc topology.Instances
		require.NoError(t, json.Unmarshal(written, &doc))
		require.Len(t, doc.Instances, 1)
		require.Equal(t, "n1", doc.Instances[0].ID)
		require.Equal(t, "H100", doc.Instances[0].Labels[testInstanceLabelKey])
	})
}

func TestGetComputeInstances(t *testing.T) {
	eng := &GraphEngine{params: &Params{}}
	cis, herr := eng.ResolveComputeInstances(context.Background(), nil, nil)
	require.Nil(t, cis)
	require.NotNil(t, herr)
	require.Equal(t, http.StatusBadRequest, herr.Code())

	expected := []topology.ComputeInstances{{
		Region:    "region",
		Instances: map[string]string{"instance": "node"},
	}}
	cis, herr = eng.ResolveComputeInstances(context.Background(), expected, nil)
	require.Nil(t, herr)
	require.Equal(t, expected, cis)
	require.Equal(t, map[string]bool{"instance": true, "node": true}, eng.requested)
}

func TestResolveComputeInstancesRequestedState(t *testing.T) {
	ctx := context.Background()
	resolve := func(eng *GraphEngine, instances map[string]string) *httperr.Error {
		_, herr := eng.ResolveComputeInstances(ctx, []topology.ComputeInstances{{Region: "r", Instances: instances}}, nil)
		return herr
	}

	t.Run("successive calls replace the requested set", func(t *testing.T) {
		eng := &GraphEngine{params: &Params{}}
		require.Nil(t, resolve(eng, map[string]string{"i-1": "node1"}))
		require.Nil(t, resolve(eng, map[string]string{"i-2": "node2"}))
		require.Equal(t, map[string]bool{"i-2": true, "node2": true}, eng.requested)
	})

	t.Run("failed call keeps the previous requested set", func(t *testing.T) {
		eng := &GraphEngine{params: &Params{}}
		require.Nil(t, resolve(eng, map[string]string{"i-1": "node1"}))
		_, herr := eng.ResolveComputeInstances(ctx, nil, nil)
		require.NotNil(t, herr)
		require.Equal(t, map[string]bool{"i-1": true, "node1": true}, eng.requested)
	})

	t.Run("empty instance map requests nothing", func(t *testing.T) {
		eng := &GraphEngine{params: &Params{}}
		require.Nil(t, resolve(eng, map[string]string{}))
		require.NotNil(t, eng.requested)
		require.Empty(t, eng.requested)

		graph := &topology.Graph{
			Tiers: &topology.Vertex{Vertices: map[string]*topology.Vertex{
				"sw": {ID: "sw", Vertices: map[string]*topology.Vertex{"i-1": {ID: "i-1", Name: "node1"}}},
			}},
		}
		out, herr := eng.GenerateOutput(ctx, graph, nil)
		require.Nil(t, herr)
		var doc topology.Instances
		require.NoError(t, json.Unmarshal(out, &doc))
		require.Empty(t, doc.Instances)
	})

	t.Run("empty IDs and names are not recorded", func(t *testing.T) {
		eng := &GraphEngine{params: &Params{}}
		require.Nil(t, resolve(eng, map[string]string{"i-1": "", "": "node2"}))
		require.Equal(t, map[string]bool{"i-1": true, "node2": true}, eng.requested)

		// a leaf without a name must not be matched by an empty requested name
		graph := &topology.Graph{
			Tiers: &topology.Vertex{Vertices: map[string]*topology.Vertex{
				"sw": {ID: "sw", Vertices: map[string]*topology.Vertex{"i-9": {ID: "i-9"}}},
			}},
		}
		require.Empty(t, graphInstances(graph, eng.requested))
	})

	t.Run("nil instance map is accepted", func(t *testing.T) {
		eng := &GraphEngine{params: &Params{}}
		require.Nil(t, resolve(eng, nil))
		require.Empty(t, eng.requested)
	})

	t.Run("attached records stay authoritative and only requested ones are completed", func(t *testing.T) {
		eng := &GraphEngine{params: &Params{}}
		require.Nil(t, resolve(eng, map[string]string{"i-1": "node1"}))

		graph := &topology.Graph{
			Tiers: &topology.Vertex{Vertices: map[string]*topology.Vertex{
				"sw": {ID: "sw", Vertices: map[string]*topology.Vertex{
					"i-1": {ID: "i-1", Name: "node1"},
					"i-2": {ID: "i-2", Name: "node2"},
				}},
			}},
			Domains: topology.DomainMap{"d": {
				"node1": {Domain: "d", InstanceID: "i-1", HostName: "node1"},
				"node2": {Domain: "d", InstanceID: "i-2", HostName: "node2"},
			}},
			Instances: map[string]topology.Instance{"i-1": {ID: "i-1"}, "i-2": {ID: "i-2"}},
		}
		out, herr := eng.GenerateOutput(ctx, graph, nil)
		require.Nil(t, herr)
		var doc topology.Instances
		require.NoError(t, json.Unmarshal(out, &doc))
		require.Equal(t, []topology.Instance{
			{ID: "i-1", NetworkLayers: []string{"sw"}, Labels: map[string]string{topology.KeyTopologyXclrDomain: "d"}},
			{ID: "i-2"},
		}, doc.Instances)
	})
}

func TestDeriveInstances(t *testing.T) {
	leaf := func(id, name string) *topology.Vertex { return &topology.Vertex{ID: id, Name: name} }
	sw := func(id, name string, children ...*topology.Vertex) *topology.Vertex {
		v := &topology.Vertex{ID: id, Name: name, Vertices: map[string]*topology.Vertex{}}
		for _, c := range children {
			v.Vertices[c.ID] = c
		}
		return v
	}

	t.Run("layers closest-first and domain labels", func(t *testing.T) {
		graph := &topology.Graph{
			Tiers: sw("", "", sw("spine", "", sw("leaf", "", leaf("i-1", "node1")))),
			Domains: topology.DomainMap{
				"nvl-1": {"node1": {Domain: "nvl-1", SubDomain: "rack-1", InstanceID: "i-1", HostName: "node1"}},
			},
		}
		got := graphInstances(graph, nil)
		require.Equal(t, map[string]topology.Instance{
			"i-1": {
				ID:            "i-1",
				NetworkLayers: []string{"leaf", "spine"},
				Labels: map[string]string{
					topology.KeyTopologyXclrDomain:    "nvl-1",
					topology.KeyTopologyXclrSubDomain: "rack-1",
				},
			},
		}, got)
	})

	t.Run("no-topology pseudo switch and switch names", func(t *testing.T) {
		graph := &topology.Graph{
			Tiers: sw("", "",
				sw("id-s1", "S1", leaf("i-1", "node1")),
				sw(topology.NoTopology, "", leaf("i-2", "node2"))),
		}
		got := graphInstances(graph, nil)
		require.Equal(t, []string{"S1"}, got["i-1"].NetworkLayers)
		require.Contains(t, got, "i-2")
		require.Equal(t, []string{}, got["i-2"].NetworkLayers)
	})

	t.Run("domain host matched by name when ids differ", func(t *testing.T) {
		graph := &topology.Graph{
			Tiers: sw("", "", sw("sw", "", leaf("guid-1", "node1"))),
			Domains: topology.DomainMap{
				"d": {"node1": {Domain: "d", HostName: "node1"}},
			},
		}
		got := graphInstances(graph, nil)
		require.Len(t, got, 1)
		require.Equal(t, "d", got["guid-1"].Labels[topology.KeyTopologyXclrDomain])
	})

	t.Run("domain host matched by host name equal to leaf id", func(t *testing.T) {
		graph := &topology.Graph{
			Tiers: sw("", "", sw("sw", "", leaf("node1", ""))),
			Domains: topology.DomainMap{
				"d": {"node1": {Domain: "d", InstanceID: "other", HostName: "node1"}},
			},
		}
		got := graphInstances(graph, nil)
		require.Len(t, got, 1)
		require.Equal(t, "d", got["node1"].Labels[topology.KeyTopologyXclrDomain])
	})

	t.Run("domains only", func(t *testing.T) {
		graph := &topology.Graph{
			Domains: topology.DomainMap{"d": {"n": {Domain: "d", InstanceID: "n", HostName: "n"}}},
		}
		got := graphInstances(graph, nil)
		require.Equal(t, []string{}, got["n"].NetworkLayers)
		require.Equal(t, "d", got["n"].Labels[topology.KeyTopologyXclrDomain])
	})

	t.Run("no-topology switch with a name is still dropped", func(t *testing.T) {
		graph := &topology.Graph{
			Tiers: sw("", "", sw(topology.NoTopology, "unknown", leaf("i-2", "node2"))),
		}
		got := graphInstances(graph, nil)
		require.Contains(t, got, "i-2")
		require.Equal(t, []string{}, got["i-2"].NetworkLayers)
	})

	t.Run("host in several domains gets a deterministic single domain", func(t *testing.T) {
		graph := &topology.Graph{
			Tiers: sw("", "", sw("sw", "", leaf("i-1", "node1"))),
			Domains: topology.DomainMap{
				"d-b": {"node1": {Domain: "d-b", InstanceID: "i-1", HostName: "node1"}},
				"d-a": {"node1": {Domain: "d-a", SubDomain: "d-a.r1", InstanceID: "i-1", HostName: "node1"}},
			},
		}
		for range 20 {
			got := graphInstances(graph, nil)["i-1"]
			require.Equal(t, "d-a", got.Labels[topology.KeyTopologyXclrDomain])
			require.Equal(t, "d-a.r1", got.Labels[topology.KeyTopologyXclrSubDomain])
		}
	})

	t.Run("repeated leaf id keeps the first branch", func(t *testing.T) {
		graph := &topology.Graph{
			Tiers: sw("", "", sw("sw-a", "", leaf("n", "n")), sw("sw-b", "", leaf("n", "n"))),
		}
		for range 20 {
			require.Equal(t, []string{"sw-a"}, graphInstances(graph, nil)["n"].NetworkLayers)
		}
	})

	t.Run("tree nodes outside the request are skipped", func(t *testing.T) {
		graph := &topology.Graph{
			Tiers: sw("", "", sw("sw", "", leaf("i-1", "node1"), leaf("i-2", "node2"))),
		}
		got := graphInstances(graph, map[string]bool{"node1": true})
		require.Len(t, got, 1)
		require.Contains(t, got, "i-1")
	})

	t.Run("request for unknown nodes yields no instances", func(t *testing.T) {
		graph := &topology.Graph{
			Tiers: sw("", "", sw("sw", "", leaf("i-1", "node1"))),
			Domains: topology.DomainMap{
				"d": {"node1": {Domain: "d", InstanceID: "i-1", HostName: "node1"}},
			},
		}
		require.Empty(t, graphInstances(graph, map[string]bool{"i-x": true, "nodex": true}))
	})

	t.Run("domain hosts outside the request are skipped", func(t *testing.T) {
		graph := &topology.Graph{
			Tiers: sw("", "", sw("sw", "", leaf("i-1", "node1"))),
			Domains: topology.DomainMap{
				"d": {
					"node1": {Domain: "d", InstanceID: "i-1", HostName: "node1"},
					"node2": {Domain: "d", InstanceID: "i-2", HostName: "node2"},
					"node3": {Domain: "d", InstanceID: "i-3", HostName: "node3"},
				},
			},
		}
		requested := map[string]bool{"i-1": true, "node1": true, "i-3": true, "node3": true}
		got := graphInstances(graph, requested)
		require.Len(t, got, 2)
		require.Equal(t, "d", got["i-1"].Labels[topology.KeyTopologyXclrDomain])
		require.Equal(t, []string{}, got["i-3"].NetworkLayers)
		require.Equal(t, "d", got["i-3"].Labels[topology.KeyTopologyXclrDomain])
	})

	t.Run("entries without usable identifiers are ignored", func(t *testing.T) {
		for name, graph := range map[string]*topology.Graph{
			"empty leaf ids": {Tiers: sw("", "", sw("sw", "", leaf("", "")))},
			"domain host without ids": {
				Domains: topology.DomainMap{"d": {"x": {Domain: "d"}}},
			},
			"empty domain map": {Domains: topology.DomainMap{"d": {}}},
			"empty graph":      {},
		} {
			t.Run(name, func(t *testing.T) {
				require.Empty(t, graphInstances(graph, nil))
			})
		}
	})

	t.Run("attached instances win", func(t *testing.T) {
		graph := &topology.Graph{
			Tiers:     sw("", "", sw("sw", "", leaf("a", "a"))),
			Instances: map[string]topology.Instance{"x": {ID: "x"}},
		}
		require.Equal(t, graph.Instances, graphInstances(graph, nil))
	})

	t.Run("attached instances are completed from the graph", func(t *testing.T) {
		attached := map[string]topology.Instance{
			"i-1": {ID: "i-1", Labels: map[string]string{testInstanceLabelKey: "H100"}},
			"i-2": {
				ID:            "i-2",
				NetworkLayers: []string{"custom"},
				Labels:        map[string]string{topology.KeyTopologyXclrDomain: "own"},
			},
			"i-3": {ID: "i-3"},
		}
		graph := &topology.Graph{
			Tiers: sw("", "",
				sw("sw1", "", leaf("i-1", "node1")),
				sw("sw2", "", leaf("i-2", "node2"))),
			Domains: topology.DomainMap{
				"d": {
					"node1": {Domain: "d", SubDomain: "d.r1", InstanceID: "i-1", HostName: "node1"},
					"node2": {Domain: "d", SubDomain: "d.r2", InstanceID: "i-2", HostName: "node2"},
				},
			},
			Instances: attached,
		}
		got := graphInstances(graph, nil)
		require.Equal(t, topology.Instance{
			ID:            "i-1",
			NetworkLayers: []string{"sw1"},
			Labels: map[string]string{
				testInstanceLabelKey:              "H100",
				topology.KeyTopologyXclrDomain:    "d",
				topology.KeyTopologyXclrSubDomain: "d.r1",
			},
		}, got["i-1"])
		// existing layers and domain are kept, and a foreign sub-domain is not added
		require.Equal(t, []string{"custom"}, got["i-2"].NetworkLayers)
		require.Equal(t, map[string]string{topology.KeyTopologyXclrDomain: "own"}, got["i-2"].Labels)
		// instances unknown to the graph are returned unchanged
		require.Equal(t, topology.Instance{ID: "i-3"}, got["i-3"])
		// the provider's records are not modified
		require.Nil(t, attached["i-1"].NetworkLayers)
		require.Len(t, attached["i-1"].Labels, 1)
	})
}

func TestGenerateOutputInfinibandSim(t *testing.T) {
	ctx := context.Background()
	_, loader := infiniband.NamedLoaderSim()
	prov, herr := loader(ctx, providers.Config{Params: map[string]any{
		"ibnetdiscoverFileName": "../../../tests/output/ibnetdiscover/example.out",
	}})
	require.Nil(t, herr)

	cis := []topology.ComputeInstances{{
		Region:    "local",
		Instances: map[string]string{"b07-p1-dgx-07-c01": "b07-p1-dgx-07-c01"},
	}}
	graph, herr := prov.GenerateTopologyConfig(ctx, nil, cis)
	require.Nil(t, herr)
	require.Empty(t, graph.Instances)

	out, herr := (&GraphEngine{params: &Params{}}).GenerateOutput(ctx, graph, nil)
	require.Nil(t, herr)
	var doc topology.Instances
	require.NoError(t, json.Unmarshal(out, &doc))
	require.Equal(t, []topology.Instance{{
		ID: "b07-p1-dgx-07-c01",
		NetworkLayers: []string{
			"S-2c5eab0300c26140", "S-2c5eab0300c25f00", "S-2c5eab0300b879c0",
		},
	}}, doc.Instances)
}

func TestGenerateOutputRequestedFilter(t *testing.T) {
	ctx := context.Background()
	eng := &GraphEngine{params: &Params{}}
	_, herr := eng.ResolveComputeInstances(ctx, []topology.ComputeInstances{{
		Region:    "r",
		Instances: map[string]string{"i-1": "node1", "i-3": "node3"},
	}}, nil)
	require.Nil(t, herr)

	graph := &topology.Graph{
		Tiers: &topology.Vertex{Vertices: map[string]*topology.Vertex{
			"sw": {ID: "sw", Vertices: map[string]*topology.Vertex{
				"i-1": {ID: "i-1", Name: "node1"},
			}},
		}},
		Domains: topology.DomainMap{"d": {
			"node1": {Domain: "d", InstanceID: "i-1", HostName: "node1"},
			"node2": {Domain: "d", InstanceID: "i-2", HostName: "node2"},
			"node3": {Domain: "d", InstanceID: "i-3", HostName: "node3"},
		}},
	}
	out, herr := eng.GenerateOutput(ctx, graph, nil)
	require.Nil(t, herr)

	var doc topology.Instances
	require.NoError(t, json.Unmarshal(out, &doc))
	require.Len(t, doc.Instances, 2)
	require.Equal(t, "i-1", doc.Instances[0].ID)
	require.Equal(t, []string{"sw"}, doc.Instances[0].NetworkLayers)
	require.Equal(t, "i-3", doc.Instances[1].ID)
	require.Equal(t, []string{}, doc.Instances[1].NetworkLayers)
	require.Equal(t, "d", doc.Instances[1].Labels[topology.KeyTopologyXclrDomain])
}

func TestGenerateOutputCompletesAttachedInstances(t *testing.T) {
	graph := &topology.Graph{
		Tiers: &topology.Vertex{Vertices: map[string]*topology.Vertex{
			"sw": {ID: "sw", Vertices: map[string]*topology.Vertex{"n1": {ID: "n1"}}},
		}},
		Domains:   topology.DomainMap{"d": {"n1": {Domain: "d", InstanceID: "n1", HostName: "n1"}}},
		Instances: map[string]topology.Instance{"n1": {ID: "n1"}},
	}
	out, herr := (&GraphEngine{params: &Params{}}).GenerateOutput(context.Background(), graph, nil)
	require.Nil(t, herr)
	var doc topology.Instances
	require.NoError(t, json.Unmarshal(out, &doc))
	require.Equal(t, []topology.Instance{{
		ID:            "n1",
		NetworkLayers: []string{"sw"},
		Labels:        map[string]string{topology.KeyTopologyXclrDomain: "d"},
	}}, doc.Instances)
}
