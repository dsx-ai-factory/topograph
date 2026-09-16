/*
 * Copyright 2024-2026 NVIDIA CORPORATION
 * SPDX-License-Identifier: Apache-2.0
 */

package slurm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/dsx-ai-factory/topograph/pkg/engines"
	"github.com/dsx-ai-factory/topograph/pkg/topology"
	"github.com/dsx-ai-factory/topograph/pkg/translate"
)

func TestNamedLoader(t *testing.T) {
	name, loader := NamedLoader()
	require.Equal(t, NAME, name)
	require.NotNil(t, loader)
	eng, httpErr := loader(context.Background(), engines.Config{})
	require.Nil(t, httpErr)
	require.NotNil(t, eng)
	require.IsType(t, &SlurmEngine{}, eng)
}

func TestLoader(t *testing.T) {
	// Loader has no external dependencies — it simply returns a SlurmEngine.
	// The paths that call scontrol (GetNodeList, getPartitionNodes, reconfigure)
	// are not tested here because exec.Exec is a free function, not an interface,
	// and those paths require a running Slurm daemon.
	eng, httpErr := Loader(context.Background(), engines.Config{})
	require.Nil(t, httpErr)
	require.NotNil(t, eng)
	require.IsType(t, &SlurmEngine{}, eng)
}

func TestSlurmEngineGenerateOutput(t *testing.T) {
	// SlurmEngine.GenerateOutput delegates to the package-level GenerateOutput.
	// This test exercises the method receiver path, which TestGenerateOutput
	// does not reach because it calls the free function directly.
	ctx := context.TODO()
	graph, _ := translate.GetTreeTestSet(false)
	eng := &SlurmEngine{}
	out, httpErr := eng.GenerateOutput(ctx, graph, nil)
	require.Nil(t, httpErr)
	require.NotEmpty(t, out)
}

func TestGenerateOutputParamsFilePath(t *testing.T) {
	ctx := context.TODO()
	graph, _ := translate.GetTreeTestSet(false)

	t.Run("success — file written and OK returned", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "topology.conf")
		params := &Params{
			BaseParams:     BaseParams{Plugin: topology.TopologyTree},
			TopoConfigPath: path,
			// Reconfigure: false — reconfigure calls exec.Exec("scontrol", "reconfigure")
			// which requires a running Slurm daemon and is not interface-injectable.
		}
		out, httpErr := GenerateOutputParams(ctx, graph, params)
		require.Nil(t, httpErr)
		require.Equal(t, "OK\n", string(out))
		data, err := os.ReadFile(path)
		require.NoError(t, err)
		require.Contains(t, string(data), fmt.Sprintf(TopologyHeader, topology.TopologyTree))
		require.Contains(t, string(data), "SwitchName=")
	})

	t.Run("file creation failure returns InternalServerError", func(t *testing.T) {
		// Use a path whose parent directory does not exist to force a write failure.
		failPath := filepath.Join(t.TempDir(), "nonexistent-subdir", "topology.conf")
		params := &Params{
			BaseParams:     BaseParams{Plugin: topology.TopologyTree},
			TopoConfigPath: failPath,
		}
		_, httpErr := GenerateOutputParams(ctx, graph, params)
		require.NotNil(t, httpErr)
		require.Equal(t, http.StatusInternalServerError, httpErr.Code())
	})
}

func TestResolveComputeInstances(t *testing.T) {
	ctx := context.Background()
	eng := &SlurmEngine{}

	t.Run("instances already provided — early return", func(t *testing.T) {
		existing := []topology.ComputeInstances{{Region: "r1", Instances: map[string]string{"i1": "n1"}}}
		out, httpErr := eng.ResolveComputeInstances(ctx, existing, nil)
		require.Nil(t, httpErr)
		require.Equal(t, existing, out)
	})

	t.Run("environment does not implement instanceMapper", func(t *testing.T) {
		out, httpErr := eng.ResolveComputeInstances(ctx, nil, "not-an-instancemapper")
		require.Nil(t, out)
		require.NotNil(t, httpErr)
		require.Equal(t, http.StatusBadRequest, httpErr.Code())
		require.ErrorContains(t, httpErr, "environment must implement instanceMapper")
	})

	// The path through GetNodeList is not tested here: GetNodeList calls
	// exec.Exec("scontrol", "show", "nodes", ...) which is a free function
	// (not interface-injectable) and requires a running Slurm daemon.
}

func TestGetPartitionNodes(t *testing.T) {
	ctx := context.Background()

	t.Run("empty partition name returns error without invoking finder", func(t *testing.T) {
		called := false
		finder := &TopologyNodeFinder{
			GetPartitionNodes: func(context.Context, string, []any) (string, error) {
				called = true
				return "", nil
			},
		}
		_, err := GetPartitionNodes(ctx, "", finder)
		require.EqualError(t, err, "missing partition name")
		require.False(t, called, "finder must not be invoked when partition name is empty")
	})

	t.Run("finder error is propagated", func(t *testing.T) {
		finder := &TopologyNodeFinder{
			GetPartitionNodes: func(_ context.Context, partition string, _ []any) (string, error) {
				require.Equal(t, "my_partition", partition)
				return "", fmt.Errorf("scontrol failed")
			},
		}
		_, err := GetPartitionNodes(ctx, "my_partition", finder)
		require.ErrorContains(t, err, "scontrol failed")
	})

	t.Run("injectable finder — partition name and context forwarded, result parsed", func(t *testing.T) {
		fixture := `PartitionName=my_partition
   Nodes=node[001-004]
`
		testCtx := context.WithValue(ctx, struct{ key string }{"k"}, "v")
		var receivedCtx context.Context
		var receivedPartition string
		finder := &TopologyNodeFinder{
			GetPartitionNodes: func(c context.Context, partition string, _ []any) (string, error) {
				receivedCtx = c
				receivedPartition = partition
				return fixture, nil
			},
		}
		nodes, err := GetPartitionNodes(testCtx, "my_partition", finder)
		require.NoError(t, err)
		require.Equal(t, testCtx, receivedCtx)
		require.Equal(t, "my_partition", receivedPartition)
		require.Equal(t, []string{"node[001-004]"}, nodes)
	})

	t.Run("canceled context is forwarded to finder and error propagates", func(t *testing.T) {
		cancelCtx, cancel := context.WithCancel(ctx)
		cancel()
		var receivedCtx context.Context
		finder := &TopologyNodeFinder{
			GetPartitionNodes: func(c context.Context, _ string, _ []any) (string, error) {
				receivedCtx = c
				return "", c.Err()
			},
		}
		_, err := GetPartitionNodes(cancelCtx, "my_partition", finder)
		require.ErrorIs(t, err, context.Canceled)
		require.Equal(t, cancelCtx, receivedCtx)
	})

	// getPartitionNodes (the concrete implementation used in production) calls
	// exec.Exec("scontrol", ...) and is not testable without a running Slurm daemon.
}

func TestAggregateComputeInstances(t *testing.T) {
	testCases := []struct {
		name    string
		i2n     map[string]string
		regions map[string]string
		cis     []topology.ComputeInstances
	}{
		{
			name: "Case 1: no data",
			cis:  []topology.ComputeInstances{},
		},
		{
			name:    "Case 2: full match",
			i2n:     map[string]string{"i1": "n1", "i2": "n2", "i3": "n3", "i4": "n4", "i5": "n5"},
			regions: map[string]string{"n1": "r1", "n2": "r1", "n3": "r2", "n4": "r2", "n5": "r3"},
			cis: []topology.ComputeInstances{
				{
					Region:    "r1",
					Instances: map[string]string{"i1": "n1", "i2": "n2"},
				},
				{
					Region:    "r2",
					Instances: map[string]string{"i3": "n3", "i4": "n4"},
				},
				{
					Region:    "r3",
					Instances: map[string]string{"i5": "n5"},
				},
			},
		},
		{
			name:    "Case 3: partial match",
			i2n:     map[string]string{"i1": "n1", "i2": "n2", "i3": "n3", "i4": "n4", "i5": "n5"},
			regions: map[string]string{"n1": "r1", "n3": "r2", "n4": "r2"},
			cis: []topology.ComputeInstances{
				{
					Region:    "r1",
					Instances: map[string]string{"i1": "n1"},
				},
				{
					Region:    "r2",
					Instances: map[string]string{"i3": "n3", "i4": "n4"},
				},
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			cis := aggregateComputeInstances(tc.i2n, tc.regions)
			require.ElementsMatch(t, tc.cis, cis)
		})
	}
}

func TestParsePartitionNodes(t *testing.T) {
	testCases := []struct {
		name string
		in   string
		out  []string
		err  string
	}{
		{
			name: "Case 1: no nodes",
			in: `PartitionName=my_partition
   AllowGroups=ALL AllowAccounts=ALL AllowQos=ALL
   AllocNodes=ALL Default=NO QoS=N/A
   DefaultTime=NONE DisableRootJobs=NO ExclusiveUser=NO ExclusiveTopo=NO GraceTime=0 Hidden=NO
   MaxNodes=UNLIMITED MaxTime=UNLIMITED MinNodes=0 LLN=NO MaxCPUsPerNode=UNLIMITED MaxCPUsPerSocket=UNLIMITED
   NodeSets=my_partition
   PriorityJobFactor=1 PriorityTier=1 RootOnly=NO ReqResv=NO OverSubscribe=NO
   OverTimeLimit=NONE PreemptMode=OFF
   State=UP TotalCPUs=384 TotalNodes=2 SelectTypeParameters=NONE
   JobDefaults=(null)
   DefMemPerNode=UNLIMITED MaxMemPerNode=UNLIMITED
   TRES=cpu=384,mem=4095888M,node=2,billing=384,gres/gpu=16
`,
			err: `partition "test" has no nodes`,
		},
		{
			name: "Case 2: valid input",
			in: `PartitionName=my_partition
   AllowGroups=ALL AllowAccounts=ALL AllowQos=ALL
   AllocNodes=ALL Default=NO QoS=N/A
   DefaultTime=NONE DisableRootJobs=NO ExclusiveUser=NO ExclusiveTopo=NO GraceTime=0 Hidden=NO
   MaxNodes=UNLIMITED MaxTime=UNLIMITED MinNodes=0 LLN=NO MaxCPUsPerNode=UNLIMITED MaxCPUsPerSocket=UNLIMITED
   NodeSets=my_partition
   Nodes=dgx[0001-0010],dgx[0021-0030]
   PriorityJobFactor=1 PriorityTier=1 RootOnly=NO ReqResv=NO OverSubscribe=NO
   OverTimeLimit=NONE PreemptMode=OFF
   Topology=topo_my_partition
   State=UP TotalCPUs=384 TotalNodes=2 SelectTypeParameters=NONE
   JobDefaults=(null)
   DefMemPerNode=UNLIMITED MaxMemPerNode=UNLIMITED
   TRES=cpu=384,mem=4095888M,node=2,billing=384,gres/gpu=16
`,
			out: []string{"dgx[0001-0010,0021-0030]"},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := parsePartitionNodes("test", tc.in)
			if len(tc.err) != 0 {
				require.EqualError(t, err, tc.err)
			} else {
				require.NoError(t, err)
				require.Equal(t, tc.out, out)
			}
		})
	}
}

func TestGetParams(t *testing.T) {
	testCases := []struct {
		name   string
		in     string
		params *Params
		err    string
	}{
		{
			name: "Case1: bad input",
			in:   `{"topologies": "bad"}`,
			err:  "could not decode configuration: 1 error(s) decoding:\n\n* 'topologies' expected a map, got 'string'",
		},
		{
			name: "Case2: valid input",
			in: `
{
  "plugin": "123",
  "topologies": {
	"topo1": {
	  "plugin": "topology/block",
	  "blockSizes": [2,4]
	},
	"topo2": {
	  "plugin": "topology/block",
	  "blockSizes": [8,16],
	  "nodes": ["n1", "n2", "n3"]
	},
	"topo3": {
	  "plugin": "topology/flat",
	  "clusterDefault": true
	}
  }
}
`,
			params: &Params{
				BaseParams: BaseParams{
					Plugin: "123",
				},
				Topologies: map[string]*Topology{
					"topo1": {
						Plugin:     "topology/block",
						BlockSizes: []int{2, 4},
					},
					"topo2": {
						Plugin:     "topology/block",
						BlockSizes: []int{8, 16},
						Nodes: []string{
							"n1",
							"n2",
							"n3",
						},
					},
					"topo3": {
						Plugin:  "topology/flat",
						Default: true,
					},
				},
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			var result map[string]any

			err := json.Unmarshal([]byte(tc.in), &result)
			require.NoError(t, err, "failed to unmarshal")

			params, err := getParams(result)
			if len(tc.err) != 0 {
				require.EqualError(t, err, tc.err)
			} else {
				require.NoError(t, err)
				require.Equal(t, tc.params, params)
			}
		})
	}
}

func TestGetTranslateConfig(t *testing.T) {
	ctx := context.TODO()
	testCases := []struct {
		name       string
		params     *BaseParams
		topologies map[string]*Topology
		finder     *TopologyNodeFinder
		cfg        *translate.Config
		err        string
	}{
		{
			name: "Case 1: minimal input",
			params: &BaseParams{
				Plugin: topology.TopologyTree,
			},
			cfg: &translate.Config{
				Plugin: topology.TopologyTree,
			},
		},
		{
			name: "Case 2: valid blocksize",
			params: &BaseParams{
				Plugin:     topology.TopologyBlock,
				BlockSizes: []int{2, 8, 32},
				BlockName: &translate.BlockNameConfig{
					NodeNameRegexp: `^([^-]+)-`,
					Format:         `${1}`,
				},
			},
			cfg: &translate.Config{
				Plugin:     topology.TopologyBlock,
				BlockSizes: []int{2, 8, 32},
				BlockName: &translate.BlockNameConfig{
					NodeNameRegexp: `^([^-]+)-`,
					Format:         `${1}`,
				},
			},
		},
		{
			name: "Case 3: invalid top-level blocksize ratio",
			params: &BaseParams{
				Plugin:     topology.TopologyBlock,
				BlockSizes: []int{2, 6},
			},
			err: "blockSizes[1]=6 must be a power-of-two multiple of blockSizes[0]=2",
		},
		{
			name: "Case 4: invalid top-level blocksize multiple",
			params: &BaseParams{
				Plugin:     topology.TopologyBlock,
				BlockSizes: []int{2, 5},
			},
			err: "blockSizes[1]=5 must be a multiple of blockSizes[0]=2",
		},
		{
			name: "Case 4a: too many blockSizes entries",
			params: &BaseParams{
				Plugin: topology.TopologyBlock,
				BlockSizes: func() []int {
					bs := make([]int, maxBlockSizesLen+1)
					bs[0] = 1
					for i := 1; i <= maxBlockSizesLen; i++ {
						bs[i] = bs[i-1] * 2
					}
					return bs
				}(),
			},
			err: fmt.Sprintf("blockSizes has too many entries (%d); max allowed is %d", maxBlockSizesLen+1, maxBlockSizesLen),
		},
		{
			name: "Case 4b: blockSizes value exceeds maximum",
			params: &BaseParams{
				Plugin:     topology.TopologyBlock,
				BlockSizes: []int{maxBlockSizeValue + 1},
			},
			err: fmt.Sprintf("blockSizes[0]=%d exceeds maximum allowed value %d", maxBlockSizeValue+1, maxBlockSizeValue),
		},
		{
			name:   "Case 5: invalid partition topology",
			params: &BaseParams{},
			topologies: map[string]*Topology{
				"topo1": {
					Plugin: topology.TopologyBlock,
					Nodes:  []string{"node[001-100]"},
				},
				"topo2": {
					Plugin: topology.TopologyTree,
				},
			},
			err: "missing partition name",
		},
		{
			name:   "Case 6: with valid partition topology",
			params: &BaseParams{},
			topologies: map[string]*Topology{
				"default": {
					Plugin:  topology.TopologyFlat,
					Default: true,
				},
				"topo": {
					Plugin: topology.TopologyBlock,
					BlockName: &translate.BlockNameConfig{
						NodeNameRegexp: `^(node)[0-9]+`,
						Format:         `${1}`,
					},
					Nodes: []string{"node[001-100]"},
				},
			},
			cfg: &translate.Config{
				Topologies: map[string]*translate.TopologySpec{
					"default": {
						Plugin:         topology.TopologyFlat,
						ClusterDefault: true,
					},
					"topo": {
						Plugin: topology.TopologyBlock,
						BlockName: &translate.BlockNameConfig{
							NodeNameRegexp: `^(node)[0-9]+`,
							Format:         `${1}`,
						},
						Nodes: []string{"node[001-100]"},
					},
				},
			},
		},
		{
			name:   "Case 7: explicit empty nodes do not use partition discovery",
			params: &BaseParams{},
			topologies: map[string]*Topology{
				"topo": {
					Plugin:    topology.TopologyFlat,
					Partition: "would-fallback-without-explicit-nodes",
					Nodes:     []string{},
				},
			},
			finder: &TopologyNodeFinder{
				GetPartitionNodes: func(context.Context, string, []any) (string, error) {
					return "", errors.New("unexpected partition discovery")
				},
			},
			cfg: &translate.Config{
				Topologies: map[string]*translate.TopologySpec{
					"topo": {
						Plugin: topology.TopologyFlat,
						Nodes:  []string{},
					},
				},
			},
		},
		{
			name:   "Case 8: invalid partition blocksize ratio",
			params: &BaseParams{},
			topologies: map[string]*Topology{
				"topo": {
					Plugin:     topology.TopologyBlock,
					BlockSizes: []int{2, 6},
					Nodes:      []string{"node[001-100]"},
				},
			},
			err: `topology "topo": blockSizes[1]=6 must be a power-of-two multiple of blockSizes[0]=2`,
		},
		{
			name: "Case 9: invalid cluster-wide node name regexp",
			params: &BaseParams{
				Plugin: topology.TopologyBlock,
				BlockName: &translate.BlockNameConfig{
					NodeNameRegexp: `[`,
					Format:         `${1}`,
				},
			},
			err: "invalid blockName.nodeNameRegexp \"[\": error parsing regexp: missing closing ]: `[`",
		},
		{
			name:   "Case 10: invalid partition node name regexp",
			params: &BaseParams{},
			topologies: map[string]*Topology{
				"topo": {
					Plugin: topology.TopologyBlock,
					BlockName: &translate.BlockNameConfig{
						NodeNameRegexp: `[`,
						Format:         `${1}`,
					},
					Nodes: []string{"node001"},
				},
			},
			err: "topology \"topo\": invalid blockName.nodeNameRegexp \"[\": error parsing regexp: missing closing ]: `[`",
		},
		{
			name: "Case 11: missing node name regexp",
			params: &BaseParams{
				Plugin: topology.TopologyBlock,
				BlockName: &translate.BlockNameConfig{
					Format: `${1}`,
				},
			},
			err: "blockName.nodeNameRegexp must not be empty",
		},
		{
			name: "Case 12: missing block name format",
			params: &BaseParams{
				Plugin: topology.TopologyBlock,
				BlockName: &translate.BlockNameConfig{
					NodeNameRegexp: `^node([0-9]+)`,
				},
			},
			err: "blockName.format must not be empty",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := GetTranslateConfig(ctx, tc.params, tc.topologies, tc.finder)
			if len(tc.err) != 0 {
				require.EqualError(t, err, tc.err)
			} else {
				require.Nil(t, err)
				require.NoError(t, translate.ValidateBlockNameConfig(tc.cfg.BlockName))
				for _, spec := range tc.cfg.Topologies {
					require.NoError(t, translate.ValidateBlockNameConfig(spec.BlockName))
				}
				require.Equal(t, tc.cfg, cfg)
			}
		})
	}
}

func TestGenerateOutput(t *testing.T) {
	ctx := context.TODO()
	graph, _ := translate.GetTreeTestSet(false)
	cfg := `SwitchName=S1 Switches=S[2-3]
SwitchName=S2 Nodes=Node[201-202,205]
SwitchName=S3 Nodes=Node[304-306]
`

	testCases := []struct {
		name   string
		graph  *topology.Graph
		params map[string]any
		cfg    string
		err    string
		code   int
	}{
		{
			name:   "Case 1: invalid params",
			params: map[string]any{"reconfigure": "bad"},
			err:    "could not decode configuration: 1 error(s) decoding:\n\n* error decoding 'reconfigure': invalid bool \"bad\"",
			code:   http.StatusBadRequest,
		},
		{
			name:   "Case 2: invalid blocksize",
			graph:  graph,
			params: map[string]any{"blockSizes": "bad"},
			err:    "could not decode configuration: 1 error(s) decoding:\n\n* 'blockSizes': source data must be an array or slice, got string",
			code:   http.StatusBadRequest,
		},
		{
			name:   "Case 3: invalid semantic blocksize",
			graph:  graph,
			params: map[string]any{"blockSizes": []int{2, 6}},
			err:    "blockSizes[1]=6 must be a power-of-two multiple of blockSizes[0]=2",
			code:   http.StatusBadRequest,
		},
		{
			name:  "Case 4: valid input",
			graph: graph,
			cfg:   cfg,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := GenerateOutput(ctx, tc.graph, tc.params)
			if len(tc.err) != 0 {
				require.NotNil(t, err)
				require.EqualError(t, err, tc.err)
				require.Equal(t, tc.code, err.Code())
			} else {
				require.Nil(t, err)
				require.Equal(t, tc.cfg, string(cfg))
			}
		})
	}
}
