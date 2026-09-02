/*
 * Copyright 2026 NVIDIA CORPORATION
 * SPDX-License-Identifier: Apache-2.0
 */

package minerva

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/NVIDIA/topograph/pkg/engines/slurm"
	"github.com/NVIDIA/topograph/pkg/providers"
	"github.com/NVIDIA/topograph/pkg/topology"
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
					"apiUrl": "url",
				},
			},
			err: "missing 'apiKey'",
		},
		{
			name: "Case 2b: empty apiKey",
			config: providers.Config{
				Params: map[string]any{
					"apiUrl": "url",
				},
				Creds: map[string]any{
					"apiKey": "",
				},
			},
			err: "'apiKey' must not be empty",
		},
		{
			name: "Case 3: valid input",
			config: providers.Config{
				Params: map[string]any{
					"apiUrl": "url",
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
				require.EqualError(t, err, tc.err)
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
		require.Equal(t, "/"+ExportTopologyURL, r.URL.Path)
		require.Equal(t, "test-key", r.Header.Get("X-Api-Key"))

		var body ExportTopologyRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		require.Equal(t, 50, body.Limit)

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
		{Instances: map[string]string{"i-01": "node-01", "i-02": "node-02", "i-03": "node-03"}},
	}
	graph, httpErr := p.GenerateTopologyConfig(context.Background(), &pageSize, cis)
	require.Nil(t, httpErr)
	require.NotNil(t, graph.Tiers)
	require.Contains(t, graph.Tiers.Vertices, "d-0001")

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
		{Instances: map[string]string{"i-01": "node-01", "i-02": "node-02", "i-03": "node-03"}},
	}
	graph, httpErr := p.GenerateTopologyConfig(context.Background(), nil, cis)
	require.Nil(t, httpErr)

	data, httpErr := slurm.GenerateOutput(context.Background(), graph, nil)
	require.Nil(t, httpErr)

	expected := `# spine-01=d-0001
SwitchName=spine-01 Switches=leaf-[01-02]
# leaf-01=d-1001
SwitchName=leaf-01 Nodes=node-[01-02]
# leaf-02=d-1002
SwitchName=leaf-02 Nodes=node-03
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
