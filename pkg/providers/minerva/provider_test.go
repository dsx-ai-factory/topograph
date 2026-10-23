/*
 * Copyright 2026 NVIDIA CORPORATION
 * SPDX-License-Identifier: Apache-2.0
 */

package minerva

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

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
		name   string
		config providers.Config
		err    string
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
			name: "Case 3: valid input",
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
			_, err := Loader(ctx, tc.config)
			if len(tc.err) != 0 {
				require.NotNil(t, err)
				require.Contains(t, err.Error(), tc.err)
			} else {
				require.Nil(t, err)
			}
		})
	}
}

// TestGenerateTopologyConfig exercises the full HTTP round trip: the request
// carries the X-Api-Key header and the optional page size as "limit", and a
// successful response is parsed into the switch tree.
func TestGenerateTopologyConfig(t *testing.T) {
	fixture, err := os.ReadFile("../../../tests/output/minerva/export-topology.json")
	require.NoError(t, err)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// this runs on the server's goroutine, so it reports with assert, not require
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/"+ExportTopologyURL, r.URL.Path)
		assert.Equal(t, "test-key", r.Header.Get("X-Api-Key"))

		var body ExportTopologyRequest
		assert.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		assert.Equal(t, 50, body.Limit)

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(fixture)
	}))
	defer srv.Close()

	p := &Provider{
		params: &ProviderParams{ApiURL: srv.URL},
		creds:  &Credentials{ApiKey: "test-key"},
	}

	pageSize := 50
	cis := []topology.ComputeInstances{
		{Instances: map[string]string{"i-01": "srv-01", "i-02": "srv-02", "i-03": "srv-03"}},
	}
	graph, httpErr := p.GenerateTopologyConfig(context.Background(), &pageSize, cis)
	require.Nil(t, httpErr)
	require.NotNil(t, graph.Tiers)
	require.Contains(t, graph.Tiers.Vertices, "ss1")

	// server-side failure surfaces as an error, not a partial graph
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv2.Close()
	p2 := &Provider{
		params: &ProviderParams{ApiURL: srv2.URL},
		creds:  &Credentials{ApiKey: "bad-key"},
	}
	_, httpErr = p2.GenerateTopologyConfig(context.Background(), nil, cis)
	require.NotNil(t, httpErr)
	require.Equal(t, http.StatusUnauthorized, httpErr.Code())

	// a canceled context must fail fast rather than waiting on the HTTP handler
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, httpErr = p.GenerateTopologyConfig(ctx, nil, cis)
	require.NotNil(t, httpErr)
}

// TestExportTopologyRequest verifies that without a page size the request body
// is an empty object, which exports the entire fabric, and that the JSON
// content type is set.
func TestExportTopologyRequest(t *testing.T) {
	fixture, err := os.ReadFile("../../../tests/output/minerva/export-topology.json")
	require.NoError(t, err)

	var body []byte
	var contentType string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		contentType = r.Header.Get("Content-Type")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(fixture)
	}))
	defer srv.Close()

	p := &Provider{
		params: &ProviderParams{ApiURL: srv.URL},
		creds:  &Credentials{ApiKey: "test-key"},
	}
	cis := []topology.ComputeInstances{{Instances: map[string]string{"i-01": "srv-01"}}}
	_, httpErr := p.GenerateTopologyConfig(context.Background(), nil, cis)
	require.Nil(t, httpErr)
	require.JSONEq(t, `{}`, string(body))
	require.Equal(t, "application/json", contentType)
}

// TestGenerateOutputWithSlurmEngine feeds the graph produced by the Minerva
// provider straight into the Slurm engine's GenerateOutput, exercising the
// provider and engine together end to end and validating the exact
// topology.conf text the switch tree renders to.
func TestGenerateOutputWithSlurmEngine(t *testing.T) {
	fixture, err := os.ReadFile("../../../tests/output/minerva/export-topology.json")
	require.NoError(t, err)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(fixture)
	}))
	defer srv.Close()

	p := &Provider{
		params: &ProviderParams{ApiURL: srv.URL},
		creds:  &Credentials{ApiKey: "test-key"},
	}

	cis := []topology.ComputeInstances{
		{Instances: map[string]string{"i-01": "srv-01", "i-02": "srv-02", "i-03": "srv-03"}},
	}
	graph, httpErr := p.GenerateTopologyConfig(context.Background(), nil, cis)
	require.Nil(t, httpErr)

	data, httpErr := slurm.GenerateOutput(context.Background(), graph, nil)
	require.Nil(t, httpErr)

	expected := `# super-01=ss1
SwitchName=super-01 Switches=p1-spine-01
# p1-spine-01=p1-s1
SwitchName=p1-spine-01 Switches=p1-leaf-[01-02]
# p1-leaf-01=p1-l1
SwitchName=p1-leaf-01 Nodes=srv-[01-02]
# p1-leaf-02=p1-l2
SwitchName=p1-leaf-02 Nodes=srv-03
`
	require.Equal(t, expected, string(data))
}

func TestInstances2NodeMap(t *testing.T) {
	p := &Provider{}
	i2n, err := p.Instances2NodeMap(context.Background(), []string{"node-01", "node-02"})
	require.NoError(t, err)
	require.Equal(t, map[string]string{"node-01": "node-01", "node-02": "node-02"}, i2n)
}

func TestGetInstancesRegions(t *testing.T) {
	p := &Provider{}
	regions, err := p.GetInstancesRegions(context.Background(), []string{"node-01", "node-02"})
	require.NoError(t, err)
	require.Equal(t, map[string]string{"node-01": regionLocal, "node-02": regionLocal}, regions)
}
