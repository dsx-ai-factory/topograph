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
	"io"
	"net/http"
	"net/http/httptest"
	"os"
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
	var limits []int
	var layers []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// this runs on the server's goroutine, so it reports with assert, not require
		var body ExportTopologyRequest
		assert.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		limits = append(limits, body.DeviceLimit)
		layers = append(layers, body.Layers...)

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
	require.Equal(t, []string{LayerEastWest, LayerNVLink}, layers)
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

// TestGenerateTopologyConfigNVLinkUnavailable verifies that the switch tree
// does not depend on the nvlink layer: when that layer fails, or is empty, the
// graph has the tree and no domains. Only a canceled request still fails.
func TestGenerateTopologyConfigNVLinkUnavailable(t *testing.T) {
	cis := []topology.ComputeInstances{{Instances: map[string]string{"i-01": "srv-01"}}}
	eastWest, err := os.ReadFile(fixtureEastWest)
	require.NoError(t, err)

	nvlink := func(handler http.HandlerFunc) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var body ExportTopologyRequest
			assert.NoError(t, json.NewDecoder(r.Body).Decode(&body))
			if len(body.Layers) == 1 && body.Layers[0] == LayerNVLink {
				handler(w, r)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(eastWest)
		}))
	}

	for name, handler := range map[string]http.HandlerFunc{
		"error status": func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) },
		"malformed": func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`not json`))
		},
		"no nvlink records": func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"complete":true,"nextCursor":null}`))
		},
		"empty": func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"complete":true,"nextCursor":null,"nvlink":{"records":[]}}`))
		},
	} {
		t.Run(name, func(t *testing.T) {
			srv := nvlink(handler)
			defer srv.Close()

			graph, httpErr := newProvider(srv).GenerateTopologyConfig(context.Background(), nil, cis)
			require.Nil(t, httpErr)
			require.Contains(t, graph.Tiers.Vertices, "super-01")
			require.Nil(t, graph.Domains)
		})
	}

	t.Run("nvlink request that never answers", func(t *testing.T) {
		setTimeouts(t, 100*time.Millisecond, time.Minute)
		srv := nvlink(func(w http.ResponseWriter, r *http.Request) {
			<-r.Context().Done()
		})
		defer srv.Close()

		start := time.Now()
		graph, httpErr := newProvider(srv).GenerateTopologyConfig(context.Background(), nil, cis)
		require.Nil(t, httpErr)
		require.Contains(t, graph.Tiers.Vertices, "super-01")
		require.Nil(t, graph.Domains)
		require.Less(t, time.Since(start), 5*time.Second)
	})

	t.Run("canceled during the nvlink request", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		srv := nvlink(func(w http.ResponseWriter, r *http.Request) {
			cancel()
			w.WriteHeader(http.StatusServiceUnavailable)
		})
		defer srv.Close()

		_, httpErr := newProvider(srv).GenerateTopologyConfig(ctx, nil, cis)
		require.NotNil(t, httpErr)
	})
}

// TestExportTopologyRequest verifies the request body: one layer, the largest
// page without a page size, no cursor on the first page, and the JSON content
// types.
func TestExportTopologyRequest(t *testing.T) {
	var bodies []string
	var contentTypes, accepts []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(body))
		contentTypes = append(contentTypes, r.Header.Get("Content-Type"))
		accepts = append(accepts, r.Header.Get("Accept"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"complete":true,"nextCursor":null,"eastWest":{"records":[]},"nvlink":{"records":[]}}`))
	}))
	defer srv.Close()

	cis := []topology.ComputeInstances{{Instances: map[string]string{"i-01": "srv-01"}}}
	_, httpErr := newProvider(srv).GenerateTopologyConfig(context.Background(), nil, cis)
	require.Nil(t, httpErr)
	require.Equal(t, []string{
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
