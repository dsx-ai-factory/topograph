/*
 * Copyright 2026, NVIDIA CORPORATION
 * SPDX-License-Identifier: Apache-2.0
 */
package graph

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"testing"

	"github.com/stretchr/testify/require"

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
}
