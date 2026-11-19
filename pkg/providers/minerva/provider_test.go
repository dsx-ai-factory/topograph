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
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dsx-ai-factory/topograph/pkg/engines/slurm"
	"github.com/dsx-ai-factory/topograph/pkg/providers"
	"github.com/dsx-ai-factory/topograph/pkg/topology"
)

func TestNamedLoader(t *testing.T) {
	name, loader := NamedLoader()
	require.Equal(t, NAME, name)
	require.NotNil(t, loader)
}

func TestLoader(t *testing.T) {
	ctx := context.TODO()

	testCases := []struct {
		name       string
		config     providers.Config
		err        string
		skipVerify bool // the InsecureSkipVerify the loaded provider must carry
	}{
		{
			name:   "Case 1: missing apiUrl",
			config: providers.Config{},
			err:    "apiUrl not provided",
		},
		{
			name: "Case 2: missing apiKey",
			config: providers.Config{
				Params: map[string]any{
					"apiUrl": "https://minerva.example.com/minerva/api",
				},
			},
			err: "missing 'apiKey'",
		},
		{
			name: "Case 2b: empty apiKey",
			config: providers.Config{
				Params: map[string]any{
					"apiUrl": "https://minerva.example.com/minerva/api",
				},
				Creds: map[string]any{
					"apiKey": "",
				},
			},
			err: "'apiKey' must not be empty",
		},
		{
			name: "Case 2c: http apiUrl",
			config: providers.Config{
				Params: map[string]any{"apiUrl": "http://minerva.example.com/minerva/api"},
				Creds:  map[string]any{"apiKey": "key"},
			},
			err: "apiUrl must be an https URL",
		},
		{
			name: "Case 2d: apiUrl without scheme or host",
			config: providers.Config{
				Params: map[string]any{"apiUrl": "url"},
				Creds:  map[string]any{"apiKey": "key"},
			},
			err: "apiUrl must be an https URL",
		},
		{
			name: "Case 2e: creds of the wrong type",
			config: providers.Config{
				Params: map[string]any{"apiUrl": "https://minerva.example.com/minerva/api"},
				Creds:  map[string]any{"apiKey": map[string]any{"nested": "key"}},
			},
			err: "failed to decode creds",
		},
		{
			name: "Case 2f: apiUrl of the wrong type",
			config: providers.Config{
				Params: map[string]any{"apiUrl": map[string]any{"nested": "url"}},
				Creds:  map[string]any{"apiKey": "key"},
			},
			err: "apiUrl must be an https URL",
		},
		{
			name: "Case 2g: insecureSkipVerify of the wrong type",
			config: providers.Config{
				Params: map[string]any{"apiUrl": "https://minerva.example.com/minerva/api"},
				Creds:  map[string]any{"apiKey": "key", "insecureSkipVerify": []any{"yes"}},
			},
			err: "failed to decode creds",
		},
		{
			name: "Case 2h: insecureSkipVerify does not relax the https requirement",
			config: providers.Config{
				Params: map[string]any{"apiUrl": "http://minerva.example.com/minerva/api"},
				Creds:  map[string]any{"apiKey": "key", "insecureSkipVerify": true},
			},
			err: "apiUrl must be an https URL",
		},
		{
			name: "Case 3a: insecureSkipVerify enabled",
			config: providers.Config{
				Params: map[string]any{"apiUrl": "https://minerva.example.com/minerva/api"},
				Creds:  map[string]any{"apiKey": "key", "insecureSkipVerify": true},
			},
			skipVerify: true,
		},
		{
			name: "Case 3b: insecureSkipVerify disabled explicitly",
			config: providers.Config{
				Params: map[string]any{"apiUrl": "https://minerva.example.com/minerva/api"},
				Creds:  map[string]any{"apiKey": "key", "insecureSkipVerify": false},
			},
		},
		{
			name: "Case 3: valid input, certificates are verified by default",
			config: providers.Config{
				Params: map[string]any{
					"apiUrl": "https://minerva.example.com/minerva/api",
				},
				Creds: map[string]any{
					"apiKey": "key",
				},
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			p, err := Loader(ctx, tc.config)
			if len(tc.err) != 0 {
				require.NotNil(t, err)
				require.Contains(t, err.Error(), tc.err)
				return
			}
			require.Nil(t, err)
			provider, ok := p.(*Provider)
			require.True(t, ok)
			require.Equal(t, tc.skipVerify, provider.creds.InsecureSkipVerify)
		})
	}
}

// TestGenerateTopologyConfig exercises the full HTTP round trip: both layers
// are requested with the X-Api-Key header and the page size as deviceLimit,
// the east-west layer becomes the switch tree and the nvlink layer the
// accelerator domains.
func TestGenerateTopologyConfig(t *testing.T) {
	// The layers are exported concurrently, so the handler runs on several
	// goroutines and the recorded requests have no fixed order.
	var mu sync.Mutex
	var limits []int
	var layers []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// this runs on the server's goroutine, so it reports with assert, not require
		var body ExportTopologyRequest
		assert.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		mu.Lock()
		limits = append(limits, body.DeviceLimit)
		layers = append(layers, body.Layers...)
		mu.Unlock()

		fixture := fixtureEastWest
		if len(body.Layers) == 1 && body.Layers[0] == LayerNVLink {
			fixture = fixtureNVLink
		}
		data, err := os.ReadFile(fixture)
		assert.NoError(t, err)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(data)
	}))
	defer srv.Close()

	p := newProvider(srv)
	pageSize := 50
	cis := []topology.ComputeInstances{
		{Instances: map[string]string{"i-01": "srv-01", "i-02": "srv-02", "i-03": "srv-03"}},
	}
	graph, httpErr := p.GenerateTopologyConfig(context.Background(), &pageSize, cis)
	require.Nil(t, httpErr)
	require.NotNil(t, graph.Tiers)
	require.Contains(t, graph.Tiers.Vertices, "super-01")
	// compute leaves carry the requested instance IDs and node names
	leaf01 := graph.Tiers.Vertices["super-01"].Vertices["p1-spine-01"].Vertices["p1-leaf-01"]
	require.Equal(t, map[string]*topology.Vertex{
		"i-01": {ID: "i-01", Name: "srv-01"},
		"i-02": {ID: "i-02", Name: "srv-02"},
	}, leaf01.Vertices)
	require.ElementsMatch(t, []string{LayerEastWest, LayerNVLink}, layers)
	require.Equal(t, []int{50, 50}, limits)

	require.Equal(t, topology.DomainMap{
		"NVL-Rack-1": {
			"srv-01": {Domain: "NVL-Rack-1", InstanceID: "i-01", HostName: "srv-01"},
			"srv-02": {Domain: "NVL-Rack-1", InstanceID: "i-02", HostName: "srv-02"},
		},
		"NVL-Rack-2": {
			"srv-03": {Domain: "NVL-Rack-2", InstanceID: "i-03", HostName: "srv-03"},
		},
	}, graph.Domains)

	// server-side failure surfaces as an error, not a partial graph
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv2.Close()
	_, httpErr = newProvider(srv2).GenerateTopologyConfig(context.Background(), nil, cis)
	require.NotNil(t, httpErr)
	require.Equal(t, http.StatusBadGateway, httpErr.Code())
	require.Contains(t, httpErr.Error(), "status 403")

	// a canceled context must fail fast rather than waiting on the HTTP handler
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, httpErr = p.GenerateTopologyConfig(ctx, nil, cis)
	require.NotNil(t, httpErr)
}

// layerState is what a test server answers for one layer.
type layerState int

const (
	layerOK layerState = iota
	layerEmpty
	layerFailed
)

func (l layerState) String() string { return [...]string{"ok", "empty", "failed"}[l] }

// layerStateServer answers each layer according to its state: the fixture, an
// empty record collection, or a 503.
func layerStateServer(t *testing.T, eastWest, nvlink layerState) *httptest.Server {
	t.Helper()
	fixtures := map[string][]byte{
		LayerEastWest: readFixture(t, fixtureEastWest),
		LayerNVLink:   readFixture(t, fixtureNVLink),
	}
	states := map[string]layerState{LayerEastWest: eastWest, LayerNVLink: nvlink}
	keys := map[string]string{LayerEastWest: "eastWest", LayerNVLink: "nvlink"}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body ExportTopologyRequest
		assert.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		layer := body.Layers[0]
		switch states[layer] {
		case layerFailed:
			w.WriteHeader(http.StatusServiceUnavailable)
		case layerEmpty:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"complete":true,"nextCursor":null,"` + keys[layer] + `":{"records":[]}}`))
		default:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(fixtures[layer])
		}
	}))
}

// TestFailOnEmpty verifies the failOnEmpty policy against every combination
// of a populated, empty or failed east-west and nvlink layer. A failed layer
// counts as empty: the policy decides whether the request survives it, and the
// graph then lacks that layer's part.
func TestFailOnEmpty(t *testing.T) {
	cis := []topology.ComputeInstances{{Instances: map[string]string{"i-01": "srv-01"}}}
	states := []layerState{layerOK, layerEmpty, layerFailed}
	bad := func(l layerState) bool { return l != layerOK }

	for _, mode := range []string{"", failOnEmptyBoth, failOnEmptyEastWest, failOnEmptyNVLink} {
		for _, ew := range states {
			for _, nv := range states {
				t.Run(fmt.Sprintf("mode=%q eastWest=%s nvlink=%s", mode, ew, nv), func(t *testing.T) {
					var wantErr bool
					switch mode {
					case failOnEmptyEastWest:
						wantErr = bad(ew)
					case failOnEmptyNVLink:
						wantErr = bad(nv)
					default:
						wantErr = bad(ew) && bad(nv)
					}

					srv := layerStateServer(t, ew, nv)
					defer srv.Close()
					p := newProvider(srv)
					p.params.FailOnEmpty = mode
					graph, httpErr := p.GenerateTopologyConfig(context.Background(), nil, cis)
					if wantErr {
						require.NotNil(t, httpErr)
						require.Equal(t, http.StatusBadGateway, httpErr.Code())
						require.Nil(t, graph)
						return
					}
					require.Nil(t, httpErr)
					// A failed layer leaves no trace in the graph; an empty
					// east-west layer still groups the node under no-topology.
					require.Equal(t, ew == layerFailed, graph.Tiers == nil)
					require.Equal(t, nv != layerOK, len(graph.Domains) == 0)
				})
			}
		}
	}
}

// TestFailOnEmptyErrors verifies which error a failing request reports: the
// failed layer's own error when a layer failed, a policy message when it was
// merely empty.
func TestFailOnEmptyErrors(t *testing.T) {
	cis := []topology.ComputeInstances{{Instances: map[string]string{"i-01": "srv-01"}}}
	for name, tc := range map[string]struct {
		mode     string
		ew, nv   layerState
		contains string
	}{
		"nvlink failed":                 {failOnEmptyNVLink, layerOK, layerFailed, "(nvlink): status 503"},
		"nvlink empty":                  {failOnEmptyNVLink, layerOK, layerEmpty, "no NVLink domains available from Minerva"},
		"east-west failed":              {failOnEmptyEastWest, layerFailed, layerOK, "(east-west): status 503"},
		"east-west empty":               {failOnEmptyEastWest, layerEmpty, layerOK, "no EastWest topology available from Minerva"},
		"both failed reports east-west": {failOnEmptyBoth, layerFailed, layerFailed, "(east-west): status 503"},
		"both empty":                    {failOnEmptyBoth, layerEmpty, layerEmpty, "no EastWest topology or NVLink domains available from Minerva"},
		"east-west empty nvlink failed": {failOnEmptyBoth, layerEmpty, layerFailed, "(nvlink): status 503"},
	} {
		t.Run(name, func(t *testing.T) {
			srv := layerStateServer(t, tc.ew, tc.nv)
			defer srv.Close()
			p := newProvider(srv)
			p.params.FailOnEmpty = tc.mode
			_, httpErr := p.GenerateTopologyConfig(context.Background(), nil, cis)
			require.NotNil(t, httpErr)
			require.Contains(t, httpErr.Error(), tc.contains)
		})
	}
}

// TestGenerateTopologyConfigConcurrent verifies that the two layers are
// exported at the same time: each request waits for the other to arrive, so
// sequential exports would time out.
func TestGenerateTopologyConfigConcurrent(t *testing.T) {
	cis := []topology.ComputeInstances{{Instances: map[string]string{"i-01": "srv-01"}}}
	fixtures := map[string][]byte{
		LayerEastWest: readFixture(t, fixtureEastWest),
		LayerNVLink:   readFixture(t, fixtureNVLink),
	}
	var arrived sync.WaitGroup
	arrived.Add(2)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body ExportTopologyRequest
		assert.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		arrived.Done()
		done := make(chan struct{})
		go func() { arrived.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			w.WriteHeader(http.StatusGatewayTimeout)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(fixtures[body.Layers[0]])
	}))
	defer srv.Close()

	graph, httpErr := newProvider(srv).GenerateTopologyConfig(context.Background(), nil, cis)
	require.Nil(t, httpErr)
	require.NotNil(t, graph.Tiers)
	require.NotEmpty(t, graph.Domains)
}

// TestGenerateTopologyConfigTimeoutAndCancel verifies a layer that never
// answers is tolerated like any other failure, and a canceled request fails
// whatever the policy says.
func TestGenerateTopologyConfigTimeoutAndCancel(t *testing.T) {
	cis := []topology.ComputeInstances{{Instances: map[string]string{"i-01": "srv-01"}}}
	eastWest := readFixture(t, fixtureEastWest)
	serve := func(nvlink http.HandlerFunc) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var body ExportTopologyRequest
			assert.NoError(t, json.NewDecoder(r.Body).Decode(&body))
			if body.Layers[0] == LayerNVLink {
				nvlink(w, r)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(eastWest)
		}))
	}

	t.Run("nvlink request that never answers", func(t *testing.T) {
		setTimeouts(t, 100*time.Millisecond, time.Minute)
		srv := serve(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() })
		defer srv.Close()

		start := time.Now()
		graph, httpErr := newProvider(srv).GenerateTopologyConfig(context.Background(), nil, cis)
		require.Nil(t, httpErr)
		require.Contains(t, graph.Tiers.Vertices, "super-01")
		require.Nil(t, graph.Domains)
		require.Less(t, time.Since(start), 5*time.Second)
	})

	t.Run("malformed nvlink response", func(t *testing.T) {
		srv := serve(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`not json`)) })
		defer srv.Close()

		graph, httpErr := newProvider(srv).GenerateTopologyConfig(context.Background(), nil, cis)
		require.Nil(t, httpErr)
		require.Nil(t, graph.Domains)
	})

	t.Run("canceled after both layers answered", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		srv := serve(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"complete":true,"nextCursor":null,"nvlink":{"records":[]}}`))
			cancel()
		})
		defer srv.Close()

		_, httpErr := newProvider(srv).GenerateTopologyConfig(ctx, nil, cis)
		require.NotNil(t, httpErr)
	})

	t.Run("canceled during the nvlink request", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		srv := serve(func(w http.ResponseWriter, r *http.Request) {
			cancel()
			w.WriteHeader(http.StatusServiceUnavailable)
		})
		defer srv.Close()

		// Even with the policy that would tolerate the failure.
		_, httpErr := newProvider(srv).GenerateTopologyConfig(ctx, nil, cis)
		require.NotNil(t, httpErr)
	})
}

func TestFailOnEmptyParamValidation(t *testing.T) {
	for _, v := range []string{"both", "east-west", "nvlink"} {
		_, err := getParams(map[string]any{"apiUrl": "https://m.example.com", "failOnEmpty": v})
		require.NoError(t, err)
	}
	_, err := getParams(map[string]any{"apiUrl": "https://m.example.com", "failOnEmpty": "tree"})
	require.Error(t, err)
	_, err = getParams(map[string]any{"apiUrl": "https://m.example.com", "failOnEmpty": []string{"both"}})
	require.Error(t, err)
	p, err := getParams(map[string]any{"apiUrl": "https://m.example.com", "failOnEmpty": " East-West "})
	require.NoError(t, err)
	require.Equal(t, failOnEmptyEastWest, p.FailOnEmpty)
}

// TestExportTopologyRequest verifies the request body: one layer, the largest
// page without a page size, no cursor on the first page, and the JSON content
// types.
func TestExportTopologyRequest(t *testing.T) {
	var mu sync.Mutex
	var bodies []string
	var contentTypes, accepts []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		defer mu.Unlock()
		bodies = append(bodies, string(body))
		contentTypes = append(contentTypes, r.Header.Get("Content-Type"))
		accepts = append(accepts, r.Header.Get("Accept"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"complete":true,"nextCursor":null,"eastWest":{"records":[]},"nvlink":{"records":[]}}`))
	}))
	defer srv.Close()

	cis := []topology.ComputeInstances{{Instances: map[string]string{"i-01": "srv-01"}}}
	// Both layers are empty, which the default failOnEmpty policy rejects; the
	// request bodies are what this test checks.
	_, httpErr := newProvider(srv).GenerateTopologyConfig(context.Background(), nil, cis)
	require.NotNil(t, httpErr)
	require.ElementsMatch(t, []string{
		`{"layers":["east-west"],"deviceLimit":500}`,
		`{"layers":["nvlink"],"deviceLimit":500}`,
	}, bodies)
	require.Equal(t, []string{"application/json", "application/json"}, contentTypes)
	require.Equal(t, []string{"application/json", "application/json"}, accepts)
}

const expectedSlurmTopology = `# super-01=super-01
SwitchName=super-01 Switches=p1-spine-01
# p1-spine-01=p1-spine-01
SwitchName=p1-spine-01 Switches=p1-leaf-[01-02]
# p1-leaf-01=p1-leaf-01
SwitchName=p1-leaf-01 Nodes=srv-[01-02]
# p1-leaf-02=p1-leaf-02
SwitchName=p1-leaf-02 Nodes=srv-03
`

// TestGenerateOutputWithSlurmEngine feeds the graph produced by the Minerva
// provider straight into the Slurm engine's GenerateOutput, exercising the
// provider and engine together end to end and validating the exact
// topology.conf text the switch tree renders to. The graph carries NVLink
// domains in one case and none in the other, and the tree renders the same.
func TestGenerateOutputWithSlurmEngine(t *testing.T) {
	cis := []topology.ComputeInstances{
		{Instances: map[string]string{"i-01": "srv-01", "i-02": "srv-02", "i-03": "srv-03"}},
	}

	for name, nvlink := range map[string][]byte{
		"with NVLink domains":    readFixture(t, fixtureNVLink),
		"without NVLink domains": []byte(`{"complete":true,"nextCursor":null,"nvlink":{"records":[]}}`),
	} {
		t.Run(name, func(t *testing.T) {
			srv := layerServer(t, map[string][]byte{
				LayerEastWest: readFixture(t, fixtureEastWest),
				LayerNVLink:   nvlink,
			})
			defer srv.Close()

			graph, httpErr := newProvider(srv).GenerateTopologyConfig(context.Background(), nil, cis)
			require.Nil(t, httpErr)
			require.Equal(t, name == "with NVLink domains", graph.Domains != nil)

			data, httpErr := slurm.GenerateOutput(context.Background(), graph, nil)
			require.Nil(t, httpErr)
			require.Equal(t, expectedSlurmTopology, string(data))
		})
	}
}

// slurmInstanceMapper is the interface the Slurm engine needs from a provider
// to discover the nodes of a request that lists none.
type slurmInstanceMapper interface {
	GetNodeInstances(context.Context, []string) ([]topology.ComputeInstances, error)
}

func TestGetNodeInstances(t *testing.T) {
	var mapper slurmInstanceMapper = &Provider{}

	cis, err := mapper.GetNodeInstances(context.Background(), []string{"node-01", "node-02"})
	require.NoError(t, err)
	require.Equal(t, []topology.ComputeInstances{{
		Region:    regionLocal,
		Instances: map[string]string{"node-01": "node-01", "node-02": "node-02"},
	}}, cis)

	cis, err = mapper.GetNodeInstances(context.Background(), nil)
	require.NoError(t, err)
	require.Empty(t, cis)
}
