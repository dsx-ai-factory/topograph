/*
 * Copyright 2026 NVIDIA CORPORATION
 * SPDX-License-Identifier: Apache-2.0
 */

package infiniband

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

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
	require.True(t, selector.AcceptsLeaf("compute-leaf"))
	require.False(t, selector.AcceptsLeaf("storage-leaf"))
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

type scriptedIBNetDiscover struct {
	ports   func(string) ([]IBPort, error)
	runPort func(context.Context, string, IBPort) (*bytes.Buffer, error)
}

func (d scriptedIBNetDiscover) Run(context.Context, string) (*bytes.Buffer, error) {
	return nil, errors.New("unexpected unfiltered discovery")
}

func (d scriptedIBNetDiscover) Ports(_ context.Context, node string) ([]IBPort, error) {
	return d.ports(node)
}

func (d scriptedIBNetDiscover) RunPort(ctx context.Context, node string, port IBPort) (*bytes.Buffer, error) {
	return d.runPort(ctx, node, port)
}

func testComputeSelector(t *testing.T) *switchSelector {
	t.Helper()
	selector, err := newSwitchSelector(map[string]any{
		"switchSelector": map[string]any{"include": []string{"^compute-"}},
	})
	require.NoError(t, err)
	return selector
}

func TestSwitchSelectorChecksLaterNodePorts(t *testing.T) {
	base := ibFixture("storage", map[string][]string{"compute-leaf": {"A", "B"}})
	conflicting := ibFixture("storage", map[string][]string{
		"compute-other-A": {"A"},
		"compute-other-B": {"B"},
	})
	discoverer := scriptedIBNetDiscover{
		ports: func(node string) ([]IBPort, error) {
			ports := []IBPort{{CA: "base", Port: "1"}}
			if node == "B" {
				ports = append(ports, IBPort{CA: "extra", Port: "1"})
			}
			return ports, nil
		},
		runPort: func(_ context.Context, _ string, port IBPort) (*bytes.Buffer, error) {
			if port.CA == "extra" {
				return bytes.NewBufferString(conflicting), nil
			}
			return bytes.NewBufferString(base), nil
		},
	}
	// Separate single-node entries make A the first discovery target.
	cis := []topology.ComputeInstances{
		{Instances: map[string]string{"A": "A"}},
		{Instances: map[string]string{"B": "B"}},
	}
	_, err := getIbTree(context.Background(), cis, discoverer, testComputeSelector(t))
	require.ErrorContains(t, err, "selected IB ports give conflicting leaf groups for node")
}

func TestSwitchSelectorRejectsOverlappingNodeSets(t *testing.T) {
	first := ibFixture("storage", map[string][]string{"compute-A": {"A"}})
	second := ibFixture("storage", map[string][]string{
		"compute-A": {"A"},
		"compute-B": {"B"},
	})
	discoverer := scriptedIBNetDiscover{
		ports: func(string) ([]IBPort, error) {
			return []IBPort{{CA: "first", Port: "1"}, {CA: "second", Port: "1"}}, nil
		},
		runPort: func(_ context.Context, _ string, port IBPort) (*bytes.Buffer, error) {
			if port.CA == "first" {
				return bytes.NewBufferString(first), nil
			}
			return bytes.NewBufferString(second), nil
		},
	}
	cis := []topology.ComputeInstances{{Instances: map[string]string{"A": "A", "B": "B"}}}
	_, err := getIbTree(context.Background(), cis, discoverer, testComputeSelector(t))
	require.ErrorContains(t, err, "selected IB ports have overlapping but incomplete node sets")
}

func TestSwitchSelectorPortFailures(t *testing.T) {
	portFailure := errors.New("port discovery failed")
	for _, tc := range []struct {
		name    string
		ports   func(string) ([]IBPort, error)
		runPort func(context.Context, string, IBPort) (*bytes.Buffer, error)
	}{
		{
			name:  "cannot list ports",
			ports: func(string) ([]IBPort, error) { return nil, portFailure },
			runPort: func(context.Context, string, IBPort) (*bytes.Buffer, error) {
				return nil, errors.New("unexpected port discovery")
			},
		},
		{
			name:    "cannot run discovery on port",
			ports:   func(string) ([]IBPort, error) { return []IBPort{{CA: "compute", Port: "1"}}, nil },
			runPort: func(context.Context, string, IBPort) (*bytes.Buffer, error) { return nil, portFailure },
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cis := []topology.ComputeInstances{{Instances: map[string]string{"A": "A"}}}
			_, err := getIbTree(context.Background(), cis, scriptedIBNetDiscover{
				ports: tc.ports, runPort: tc.runPort,
			}, testComputeSelector(t))
			require.ErrorContains(t, err, "switchSelector matched no usable InfiniBand topology")
		})
	}
}

func TestDiscoverIBPortsUsesBoundedConcurrencyAndKeepsResultOrder(t *testing.T) {
	discoveries := make([]ibPortDiscovery, maxConcurrentIBPortDiscoveries*2)
	for i := range discoveries {
		discoveries[i] = ibPortDiscovery{node: "node", port: IBPort{CA: "mlx5_0", Port: strconv.Itoa(i + 1)}}
	}

	started := make(chan struct{}, len(discoveries))
	release := make(chan struct{})
	var active atomic.Int32
	var maxActive atomic.Int32
	discoverer := scriptedIBNetDiscover{runPort: func(_ context.Context, _ string, port IBPort) (*bytes.Buffer, error) {
		current := active.Add(1)
		for maximum := maxActive.Load(); current > maximum; maximum = maxActive.Load() {
			if maxActive.CompareAndSwap(maximum, current) {
				break
			}
		}
		started <- struct{}{}
		<-release
		active.Add(-1)
		return bytes.NewBufferString(port.Port), nil
	}}

	resultCh := make(chan []ibPortDiscovery, 1)
	errCh := make(chan error, 1)
	go func() {
		results, err := discoverIBPorts(context.Background(), discoverer, discoveries)
		resultCh <- results
		errCh <- err
	}()

	for range maxConcurrentIBPortDiscoveries {
		select {
		case <-started:
		case <-time.After(3 * time.Second):
			close(release)
			t.Fatal("bounded discovery did not start its worker pool")
		}
	}
	require.EqualValues(t, maxConcurrentIBPortDiscoveries, maxActive.Load())
	close(release)

	var results []ibPortDiscovery
	select {
	case results = <-resultCh:
	case <-time.After(3 * time.Second):
		t.Fatal("bounded discovery did not finish after workers were released")
	}
	require.NoError(t, <-errCh)
	require.Len(t, results, len(discoveries))
	for i, result := range results {
		require.Equal(t, discoveries[i].port, result.port)
		require.Equal(t, discoveries[i].port.Port, result.output.String())
	}
}

func TestDiscoverIBPortsHonorsCancellation(t *testing.T) {
	discoveries := make([]ibPortDiscovery, maxConcurrentIBPortDiscoveries+1)
	for i := range discoveries {
		discoveries[i] = ibPortDiscovery{node: "node", port: IBPort{CA: "mlx5_0", Port: strconv.Itoa(i + 1)}}
	}
	started := make(chan struct{}, maxConcurrentIBPortDiscoveries)
	discoverer := scriptedIBNetDiscover{runPort: func(ctx context.Context, _ string, _ IBPort) (*bytes.Buffer, error) {
		started <- struct{}{}
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := make(chan error, 1)
	go func() {
		_, err := discoverIBPorts(ctx, discoverer, discoveries)
		errCh <- err
	}()

	for range maxConcurrentIBPortDiscoveries {
		select {
		case <-started:
		case <-time.After(3 * time.Second):
			cancel()
			t.Fatal("bounded discovery did not start its worker pool")
		}
	}
	cancel()
	select {
	case err := <-errCh:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(3 * time.Second):
		t.Fatal("bounded discovery did not stop after context cancellation")
	}
}

func mapKeys(m map[string]*topology.Vertex) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	return keys
}
