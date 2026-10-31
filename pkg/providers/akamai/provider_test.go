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
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	graphengine "github.com/dsx-ai-factory/topograph/pkg/engines/graph"
	"github.com/dsx-ai-factory/topograph/pkg/engines/slurm"
	"github.com/dsx-ai-factory/topograph/pkg/providers"
	"github.com/dsx-ai-factory/topograph/pkg/topology"
)

func fakeCredentials() map[string]any {
	return map[string]any{"token": "fake-test-token"}
}

func fixture(t *testing.T) TopologyResult {
	t.Helper()
	data, err := os.ReadFile("testdata/topology.json")
	require.NoError(t, err)
	var result TopologyResult
	require.NoError(t, json.Unmarshal(data, &result))
	return result
}

func requestedFixture(t *testing.T) []topology.ComputeInstances {
	t.Helper()
	return []topology.ComputeInstances{{Region: "fake1", Instances: map[string]string{
		fixture(t).Data[4].Hosts[0].UUID: "worker1",
		fixture(t).Data[4].Hosts[1].UUID: "worker2",
		fixture(t).Data[4].Hosts[2].UUID: "worker3",
		"missing":                        "worker4",
	}}}
}

func TestLoader(t *testing.T) {
	name, loader := NamedLoader()
	require.Equal(t, "akamai", name)
	_, err := loader(context.Background(), providers.Config{Creds: fakeCredentials()})
	require.NotNil(t, err)
	require.Contains(t, err.Error(), "apiUrl is required")
	for _, params := range []map[string]any{
		{"apiUrl": "relative"}, {"apiUrl": "ftp://example.invalid"}, {"apiUrl": "https://user:fake@example.invalid"},
		{"apiUrl": "https://example.invalid?secret=fake"}, {"apiUrl": "https://example.invalid#fragment"},
		{"apiUrl": "%"}, {"apiUrl": 123}, {"trimTiers": 3}, {"trimTiers": "invalid"},
	} {
		_, err := loader(context.Background(), providers.Config{Params: params, Creds: fakeCredentials()})
		require.NotNil(t, err, "%v", params)
		require.Equal(t, http.StatusBadRequest, err.Code())
	}
	for _, creds := range []map[string]any{nil, {}, {"token": nil}, {"token": 123}, {"token": ""}, {"token": " "}, {"token": "fake\r\nheader"}, {"clientId": "fake", "clientSecret": "fake"}} {
		_, err := loader(context.Background(), providers.Config{Creds: creds})
		require.NotNil(t, err)
		require.Equal(t, http.StatusBadRequest, err.Code())
	}
}

func TestAuthenticatedPagination(t *testing.T) {
	snapshot := fixture(t)
	var pageCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "application/json", r.Header.Get("Accept"))
		assert.Equal(t, "/api/topology", r.URL.Path)
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, "Bearer fake-test-token", r.Header.Get("Authorization"))
		assert.Equal(t, "fake1", r.URL.Query().Get("region"))
		assert.Equal(t, "2", r.URL.Query().Get("page_size"))
		pageCalls.Add(1)
		page, parseErr := strconv.Atoi(r.URL.Query().Get("page"))
		if !assert.NoError(t, parseErr) || page < 1 || page > 5 {
			w.WriteHeader(400)
			return
		}
		// Switches and their parents appear on different pages.
		assert.NoError(t, json.NewEncoder(w).Encode(TopologyResult{Region: snapshot.Region, Page: page, Pages: 5, Results: len(snapshot.Data), Data: snapshot.Data[(page-1)*2 : page*2]}))
	}))
	defer server.Close()
	p, err := Loader(context.Background(), providers.Config{Creds: fakeCredentials(), Params: map[string]any{"apiUrl": server.URL + "/api/"}})
	require.Nil(t, err)
	size := 2
	generate := func() {
		g, err := p.GenerateTopologyConfig(context.Background(), &size, requestedFixture(t))
		if assert.Nil(t, err) {
			assertFixtureGraph(t, g)
		}
	}
	generate()
	var wg sync.WaitGroup
	for range 3 {
		wg.Go(generate)
	}
	wg.Wait()
	require.EqualValues(t, 20, pageCalls.Load())
}

func assertFixtureGraph(t *testing.T, g *topology.Graph) {
	t.Helper()
	require.Len(t, g.Tiers.Vertices, 3)
	border := g.Tiers.Vertices[switchID("fake1", "r01.border01.fake1.fab")]
	require.NotNil(t, border)
	leaf := border.Vertices[switchID("fake1", "r01.stem01.fake1.fab")].Vertices[switchID("fake1", "r01.spine01.fake1.fab")].Vertices[switchID("fake1", "r01.leaf01.fake1.fab")]
	require.Len(t, leaf.Vertices, 2, "both redundant host attachments must survive")
	for _, tor := range leaf.Vertices {
		require.Len(t, tor.Vertices, 3)
	}
	gspine := g.Tiers.Vertices[switchID("fake1", "r01.gspine01.fake1.fab")]
	require.NotNil(t, gspine, "fabric field must be ignored")
	require.Len(t, gspine.Vertices, 3)
	require.Len(t, gspine.Vertices[switchID("fake1", "r07a.gtor01.fake1.gpu")].Vertices, 3, "multiple ports must not duplicate hosts")
	require.Len(t, gspine.Vertices[switchID("fake1", "r07b.gtor02.fake1.gpu")].Vertices, 1)
	require.Len(t, gspine.Vertices[switchID("fake1", "r08a.gtor02.fake1.gpu")].Vertices, 2)
	require.Equal(t, "worker4", g.Tiers.Vertices[topology.NoTopology].Vertices["missing"].Name)
	require.Nil(t, g.Domains, "GPU fabric does not imply accelerator domains")
	require.Len(t, g.Instances, 3)
	for _, instance := range g.Instances {
		require.Equal(t, []string{"r07a.gtor01.fake1.gpu", "r01.gspine01.fake1.fab"}, instance.NetworkLayers)
	}
}

func simpleRecords() []Switch {
	return []Switch{{Name: "leaf", Uplink: &Link{Device: "root"}, Hosts: []Host{{UUID: "n1"}}}, {Name: "root", Hosts: []Host{}}}
}

func testProvider(records []Switch, region string) *Provider {
	return &Provider{client: &simClient{regions: map[string][]Switch{region: records}}}
}

func TestMultipleParentsAndTrim(t *testing.T) {
	records := []Switch{
		{Name: "leaf", Uplink: &Link{Device: "a"}, Hosts: []Host{{UUID: "n1"}, {UUID: "n1"}}},
		{Name: "leaf", Uplink: &Link{Device: "b"}, Hosts: []Host{{UUID: "n1"}}},
		{Name: "a", Uplink: &Link{Device: "root"}}, {Name: "b", Uplink: &Link{Device: "root"}}, {Name: "root"},
	}
	p := testProvider(records, "r")
	requested := []topology.ComputeInstances{{Region: "r", Instances: map[string]string{"n1": "worker"}}}
	g, err := p.GenerateTopologyConfig(context.Background(), nil, requested)
	require.Nil(t, err)
	root := g.Tiers.Vertices[switchID("r", "root")]
	require.Len(t, root.Vertices, 2)
	for _, parent := range root.Vertices {
		require.Equal(t, "worker", parent.Vertices[switchID("r", "leaf")].Vertices["n1"].Name)
	}
	p.params.TrimTiers = 1
	g, err = p.GenerateTopologyConfig(context.Background(), nil, requested)
	require.Nil(t, err)
	require.Len(t, g.Tiers.Vertices, 2)
	require.Equal(t, []string{"leaf", "a"}, g.Instances["n1"].NetworkLayers)
}

func TestDiscoveryFailures(t *testing.T) {
	for _, tt := range []struct {
		name    string
		records []Switch
		message string
	}{
		{"cycle", []Switch{{Name: "leaf", Uplink: &Link{Device: "root"}, Hosts: []Host{{UUID: "n1"}}}, {Name: "root", Uplink: &Link{Device: "leaf"}}}, "cycle"},
		{"dangling", []Switch{{Name: "leaf", Uplink: &Link{Device: "absent"}, Hosts: []Host{{UUID: "n1"}}}}, "unknown switch"},
		{"no name", []Switch{{Hosts: []Host{{UUID: "n1"}}}}, "no name"},
		{"empty parent", []Switch{{Name: "leaf", Uplink: &Link{}, Hosts: []Host{{UUID: "n1"}}}}, "empty uplink"},
		{"empty host", []Switch{{Name: "leaf", Hosts: []Host{{}}}}, "empty host"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			g, err := testProvider(tt.records, "r").GenerateTopologyConfig(context.Background(), nil, []topology.ComputeInstances{{Region: "r", Instances: map[string]string{"n1": "worker"}}})
			require.Nil(t, g)
			require.NotNil(t, err)
			require.Equal(t, 502, err.Code())
			require.ErrorContains(t, err, tt.message)
		})
	}
}

func TestRegionIsolationAndEncoding(t *testing.T) {
	regions := []string{"region +/&=", "second"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		region := r.URL.Query().Get("region")
		assert.Contains(t, regions, region)
		id := "n1"
		if region == "second" {
			id = "n2"
		}
		records := simpleRecords()
		records[0].Hosts = []Host{{UUID: id}}
		assert.NoError(t, json.NewEncoder(w).Encode(TopologyResult{Region: region, Data: records, Page: 1, Pages: 1, Results: 2}))
	}))
	defer server.Close()
	p, err := Loader(context.Background(), providers.Config{Creds: fakeCredentials(), Params: map[string]any{"apiUrl": server.URL}})
	require.Nil(t, err)
	g, err := p.GenerateTopologyConfig(context.Background(), nil, []topology.ComputeInstances{
		{Region: regions[0], Instances: map[string]string{"n1": "worker1"}}, {Region: regions[1], Instances: map[string]string{"n2": "worker2"}},
	})
	require.Nil(t, err)
	require.Len(t, g.Tiers.Vertices, 2, "same switch names in different regions stay separate")
	for _, region := range regions {
		require.NotNil(t, g.Tiers.Vertices[switchID(region, "root")])
	}
}

func TestRequestValidationAndCancellation(t *testing.T) {
	p := testProvider(simpleRecords(), "r")
	for _, requested := range [][]topology.ComputeInstances{
		{{Instances: map[string]string{"n1": "worker"}}}, {{Region: "r", Instances: map[string]string{"": "worker"}}},
		{{Region: "r", Instances: map[string]string{"n1": "worker"}}, {Region: "other", Instances: map[string]string{"n1": "worker"}}},
		{{Region: "r", Instances: map[string]string{"n1": "worker"}}, {Region: "r", Instances: map[string]string{"n1": "different"}}},
	} {
		_, err := p.GenerateTopologyConfig(context.Background(), nil, requested)
		require.NotNil(t, err)
		require.Equal(t, 400, err.Code())
	}
	for _, size := range []int{0, -1} {
		_, err := p.GenerateTopologyConfig(context.Background(), &size, nil)
		require.Equal(t, 400, err.Code())
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := p.GenerateTopologyConfig(ctx, nil, []topology.ComputeInstances{{Region: "r", Instances: map[string]string{"n1": "worker"}}})
	require.NotNil(t, err)
	require.Equal(t, 502, err.Code())
	// Empty requests do not use the client.
	g, err := (&Provider{}).GenerateTopologyConfig(context.Background(), nil, nil)
	require.Nil(t, err)
	require.Empty(t, g.Tiers.Vertices)
}

func TestEngineIntegration(t *testing.T) {
	p := testProvider(simpleRecords(), "r")
	g, err := p.GenerateTopologyConfig(context.Background(), nil, []topology.ComputeInstances{{Region: "r", Instances: map[string]string{"n1": "worker"}}})
	require.Nil(t, err)
	output, err := slurm.GenerateOutput(context.Background(), g, nil)
	require.Nil(t, err)
	require.Contains(t, string(output), "SwitchName=root Switches=leaf")
	require.Contains(t, string(output), "SwitchName=leaf Nodes=worker")
	engine, err := graphengine.Loader(context.Background(), nil)
	require.Nil(t, err)
	output, err = engine.GenerateOutput(context.Background(), g, nil)
	require.Nil(t, err)
	var instances topology.Instances
	require.NoError(t, json.Unmarshal(output, &instances))
	require.Equal(t, []string{"leaf", "root"}, instances.Instances[0].NetworkLayers)
	// A redundant fabric is deliberately left for a later merge.
	snapshot := fixture(t)
	g, err = testProvider(snapshot.Data, snapshot.Region).GenerateTopologyConfig(context.Background(), nil, requestedFixture(t))
	require.Nil(t, err)
	_, err = slurm.GenerateOutput(context.Background(), g, nil)
	require.NotNil(t, err)
	require.ErrorContains(t, err, "topology must be a tree")
	output, err = engine.GenerateOutput(context.Background(), g, nil)
	require.Nil(t, err)
	require.NoError(t, json.Unmarshal(output, &instances))
	require.Len(t, instances.Instances, 3)
}
