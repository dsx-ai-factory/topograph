/*
 * Copyright (c) 2026, NVIDIA CORPORATION.  All rights reserved.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package infiniband

import (
	"bytes"
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/dsx-ai-factory/topograph/pkg/providers"
	"github.com/dsx-ai-factory/topograph/pkg/topology"
)

func TestSwitchSelectorValidation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		params map[string]any
		err    string
	}{
		{"not an object", map[string]any{"switchSelector": "compute"}, "switchSelector must be an object"},
		{"empty", map[string]any{"switchSelector": map[string]any{}}, "switchSelector requires include or exclude patterns"},
		{"invalid list", map[string]any{"switchSelector": map[string]any{"include": "compute"}}, "switchSelector.include must be a non-empty list"},
		{"invalid regex", map[string]any{"switchSelector": map[string]any{"include": []any{"["}}}, "invalid switchSelector.include pattern"},
		{"valid", map[string]any{"switchSelector": map[string]any{"include": []any{"^compute"}, "exclude": []any{"bad$"}}}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, httpErr := LoaderBM(context.Background(), providers.Config{Params: tc.params})
			if tc.err == "" {
				require.Nil(t, httpErr)
			} else {
				require.ErrorContains(t, httpErr, tc.err)
			}
		})
	}
}

func TestSwitchSelectorFromYAML(t *testing.T) {
	var params map[string]any
	require.NoError(t, yaml.Unmarshal([]byte("switchSelector:\n  include:\n    - '^compute'\n  exclude:\n    - 'storage$'\n"), &params))
	selector, err := newSwitchSelector(params)
	require.NoError(t, err)
	require.True(t, selector.acceptsLeaf("compute-leaf"))
	require.False(t, selector.acceptsLeaf("storage-leaf"))
}

func TestParseIBPorts(t *testing.T) {
	ports, err := parseIBPorts(bytes.NewBufferString("mlx5_2 1\nmlx5_0 2\n"))
	require.NoError(t, err)
	require.Equal(t, []IBPort{{CA: "mlx5_0", Port: "2"}, {CA: "mlx5_2", Port: "1"}}, ports)
	_, err = parseIBPorts(bytes.NewBufferString("mlx5_0;touch /tmp/unwanted 1\n"))
	require.ErrorContains(t, err, "invalid IB port listing")
}

type multiFabricDiscoverer struct {
	outputs map[string]string
}

func (d multiFabricDiscoverer) Run(context.Context, string) (*bytes.Buffer, error) {
	return bytes.NewBufferString(d.outputs["storage"]), nil
}

func (d multiFabricDiscoverer) Ports(context.Context, string) ([]IBPort, error) {
	return []IBPort{{CA: "mlx5_1", Port: "1"}, {CA: "mlx5_0", Port: "1"}}, nil
}

func (d multiFabricDiscoverer) RunPort(_ context.Context, _ string, port IBPort) (*bytes.Buffer, error) {
	if port.CA == "mlx5_0" {
		return bytes.NewBufferString(d.outputs["storage"]), nil
	}
	return bytes.NewBufferString(d.outputs["compute"]), nil
}

func ibFixture(kind string, leaves map[string][]string) string {
	var out bytes.Buffer
	out.WriteString("# Topology file: test\n\n")
	for leaf, nodes := range leaves {
		fmt.Fprintf(&out, "Switch 8 \"S-%s\" # \"%s\"\n", leaf, leaf)
		if kind == "compute" {
			out.WriteString("[1] \"S-spine\"[1] # \"compute-spine\"\n")
		}
		for i, node := range nodes {
			fmt.Fprintf(&out, "[%d] \"H-%s\"[1] # \"%s mlx5_0\"\n", i+2, node, node)
		}
		out.WriteByte('\n')
	}
	if kind == "compute" {
		out.WriteString("Switch 8 \"S-spine\" # \"compute-spine\"\n")
		for i, leaf := range []string{"compute-leaf-1", "compute-leaf-2"} {
			fmt.Fprintf(&out, "[%d] \"S-%s\"[1] # \"%s\"\n", i+1, leaf, leaf)
		}
		out.WriteByte('\n')
	}
	for _, nodes := range leaves {
		for _, node := range nodes {
			fmt.Fprintf(&out, "Ca 1 \"H-%s\" # \"%s mlx5_0\"\n\n", node, node)
		}
	}
	return out.String()
}

func TestSwitchSelectorKeepsComputeFabric(t *testing.T) {
	discoverer := multiFabricDiscoverer{outputs: map[string]string{
		"storage": ibFixture("storage", map[string][]string{"storage-leaf": {"A", "B", "C", "D", "E", "F"}}),
		"compute": ibFixture("compute", map[string][]string{
			"compute-leaf-1": {"A", "B", "C"},
			"compute-leaf-2": {"D", "E", "F"},
		}),
	}}
	cis := []topology.ComputeInstances{{Instances: map[string]string{}}}
	for _, node := range []string{"A", "B", "C", "D", "E", "F"} {
		cis[0].Instances[node] = node
	}
	for _, tc := range []struct {
		name    string
		include string
		exclude string
		groups  map[string][]string
		err     string
	}{
		{"both compute groups", "^compute-leaf", "", map[string][]string{
			"S-compute-leaf-1": {"A", "B", "C"},
			"S-compute-leaf-2": {"D", "E", "F"},
		}, ""},
		{"exclude wins", "^compute-leaf", "-2$", map[string][]string{
			"S-compute-leaf-1": {"A", "B", "C"},
		}, ""},
		{"no match", "^missing", "", nil, "switchSelector matched no usable InfiniBand topology"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			section := map[string]any{"include": []any{tc.include}}
			if tc.exclude != "" {
				section["exclude"] = []any{tc.exclude}
			}
			selector, err := newSwitchSelector(map[string]any{"switchSelector": section})
			require.NoError(t, err)
			root, err := getIbTree(context.Background(), cis, discoverer, selector)
			if tc.err != "" {
				require.ErrorContains(t, err, tc.err)
				return
			}
			require.NoError(t, err)
			require.Contains(t, root.Vertices, "S-spine")
			require.Len(t, root.Vertices["S-spine"].Vertices, len(tc.groups))
			for leafID, nodes := range tc.groups {
				leaf := root.Vertices["S-spine"].Vertices[leafID]
				require.NotNil(t, leaf)
				require.ElementsMatch(t, nodes, mapKeys(leaf.Vertices))
			}
		})
	}
}

func mapKeys(m map[string]*topology.Vertex) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	return keys
}
