/*
 * Copyright 2024-2026 NVIDIA CORPORATION
 * SPDX-License-Identifier: Apache-2.0
 */

package slurm

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"k8s.io/klog/v2"

	"github.com/dsx-ai-factory/topograph/internal/cluset"
	"github.com/dsx-ai-factory/topograph/internal/config"
	"github.com/dsx-ai-factory/topograph/internal/exec"
	"github.com/dsx-ai-factory/topograph/internal/files"
	"github.com/dsx-ai-factory/topograph/internal/httperr"
	"github.com/dsx-ai-factory/topograph/pkg/engines"
	"github.com/dsx-ai-factory/topograph/pkg/topology"
	"github.com/dsx-ai-factory/topograph/pkg/translate"
)

const TopologyHeader = `
###############################################################
# Slurm's network topology configuration file for use with the
# %s plugin
###############################################################
`

const NAME = "slurm"

type SlurmEngine struct{}

type BaseParams struct {
	Plugin     string                     `mapstructure:"plugin"`
	BlockSizes []int                      `mapstructure:"blockSizes"`
	BlockName  *translate.BlockNameConfig `mapstructure:"blockName"`
}

type Topology struct {
	Partition  string                     `mapstructure:"partition"`
	Plugin     string                     `mapstructure:"plugin"`
	BlockSizes []int                      `mapstructure:"blockSizes"`
	BlockName  *translate.BlockNameConfig `mapstructure:"blockName"`
	Nodes      []string                   `mapstructure:"nodes"`
	Default    bool                       `mapstructure:"clusterDefault"`
}

type Params struct {
	BaseParams     `mapstructure:",squash"`
	Topologies     map[string]*Topology `mapstructure:"topologies,omitempty"`
	TopoConfigPath string               `mapstructure:"topologyConfigPath"`
	Reconfigure    bool                 `mapstructure:"reconfigure"`
}

type TopologyNodeFinder struct {
	GetPartitionNodes func(context.Context, string, []any) (string, error)
	Params            []any
}

type instanceMapper interface {
	// Instances2NodeMap receives a list of SLURM node names and returns a map of
	// the service provider assigned compute instance IDs to the node names
	Instances2NodeMap(context.Context, []string) (map[string]string, error)
	// GetInstancesRegions receives a list of SLURM node names and returns a map
	// of node names to their deployed regions
	GetInstancesRegions(context.Context, []string) (map[string]string, error)
}

var partitionNodesRe *regexp.Regexp

func init() {
	partitionNodesRe = regexp.MustCompile(`\sNodes=([^\s]+)`)
}

func NamedLoader() (string, engines.Loader) {
	return NAME, Loader
}

func Loader(_ context.Context, _ engines.Config) (engines.Engine, *httperr.Error) {
	return &SlurmEngine{}, nil
}

func (eng *SlurmEngine) ResolveComputeInstances(ctx context.Context, instances []topology.ComputeInstances, environment any) ([]topology.ComputeInstances, *httperr.Error) {
	if len(instances) != 0 {
		return instances, nil
	}

	instanceMapper, ok := environment.(instanceMapper)
	if !ok {
		return nil, httperr.NewError(http.StatusBadRequest, "environment must implement instanceMapper")
	}

	nodes, err := GetNodeList(ctx)
	if err != nil {
		return nil, httperr.NewError(http.StatusInternalServerError, err.Error())
	}

	if len(nodes) == 0 {
		return nil, nil
	}

	i2n, err := instanceMapper.Instances2NodeMap(ctx, nodes)
	if err != nil {
		return nil, httperr.NewError(http.StatusInternalServerError, err.Error())
	}
	klog.V(4).Infof("Detected instance map: %v", i2n)

	nodeRegions, err := instanceMapper.GetInstancesRegions(ctx, nodes)
	if err != nil {
		return nil, httperr.NewError(http.StatusInternalServerError, err.Error())
	}

	return aggregateComputeInstances(i2n, nodeRegions), nil
}

func aggregateComputeInstances(i2n, nodeRegions map[string]string) []topology.ComputeInstances {
	// regions maps region name to the corresponding index in "cis"
	regions := make(map[string]int)
	cis := []topology.ComputeInstances{}

	for instance, node := range i2n {
		region, ok := nodeRegions[node]
		if !ok {
			klog.Warningf("Failed to find region for node %s", node)
			continue
		}
		indx, ok := regions[region]
		if !ok {
			indx = len(regions)
			regions[region] = indx
			cis = append(cis, topology.ComputeInstances{
				Region:    region,
				Instances: map[string]string{instance: node},
			})
		} else {
			cis[indx].Instances[instance] = node
		}
	}
	klog.V(4).Infof("Detected regions: %v", regions)

	return cis
}

func GetNodeList(ctx context.Context) ([]string, error) {
	stdout, err := exec.Exec(ctx, "scontrol", []string{"show", "nodes", "-o"}, nil)
	if err != nil {
		return nil, err
	}

	klog.V(4).Infof("stdout: %s", stdout.String())

	nodes := []string{}
	scanner := bufio.NewScanner(strings.NewReader(stdout.String()))
	for scanner.Scan() {
		arr := strings.Split(scanner.Text(), " ")
		if len(arr) > 0 {
			line := arr[0]
			if strings.HasPrefix(line, "NodeName=") {
				nodes = append(nodes, line[9:])
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("failed scan output: %v", err)
	}

	return nodes, nil
}

func getPartitionNodes(ctx context.Context, partition string, _ []any) (string, error) {
	args := []string{"show", "partition", partition}
	stdout, err := exec.Exec(ctx, "scontrol", args, nil)
	if err != nil {
		return "", err
	}
	out := stdout.String()
	return out, nil
}

func GetPartitionNodes(ctx context.Context, partition string, f *TopologyNodeFinder) ([]string, error) {
	if len(partition) == 0 {
		return nil, fmt.Errorf("missing partition name")
	}
	out, err := f.GetPartitionNodes(ctx, partition, f.Params)
	if err != nil {
		return nil, err
	}
	klog.V(4).Infof("GetPartitionNodes: %s", out)
	return parsePartitionNodes(partition, out)
}

func parsePartitionNodes(partition string, data string) ([]string, error) {
	match := partitionNodesRe.FindStringSubmatch(data)
	if len(match) > 1 {
		// If the nodes are NONE, return an empty list
		if match[1] == "NONE" {
			return []string{}, nil
		}
		return cluset.Compact(cluset.ExpandList(match[1])), nil
	}

	return nil, fmt.Errorf("partition %q has no nodes", partition)
}

func (eng *SlurmEngine) GenerateOutput(ctx context.Context, graph *topology.Graph, params map[string]any) ([]byte, *httperr.Error) {
	return GenerateOutput(ctx, graph, params)
}

func GenerateOutput(ctx context.Context, graph *topology.Graph, params map[string]any) ([]byte, *httperr.Error) {
	p, err := getParams(params)
	if err != nil {
		return nil, httperr.NewError(http.StatusBadRequest, err.Error())
	}

	return GenerateOutputParams(ctx, graph, p)
}

func GenerateOutputParams(ctx context.Context, graph *topology.Graph, params *Params) ([]byte, *httperr.Error) {
	// apply legacy default plugin value
	if len(params.Plugin) == 0 && len(params.Topologies) == 0 {
		params.Plugin = topology.TopologyTree
	}

	cfg, httpErr := GetTranslateConfig(ctx, &params.BaseParams, params.Topologies, &TopologyNodeFinder{GetPartitionNodes: getPartitionNodes})
	if httpErr != nil {
		return nil, httpErr
	}

	nt, err := translate.NewNetworkTopology(graph, cfg)
	if err != nil {
		return nil, httperr.NewError(http.StatusBadRequest, err.Error())
	}

	path := params.TopoConfigPath
	buf := &bytes.Buffer{}
	if len(path) != 0 {
		if _, err := fmt.Fprintf(buf, TopologyHeader, params.Plugin); err != nil {
			return nil, httperr.NewError(http.StatusInternalServerError, err.Error())
		}
	}

	if httpErr = nt.Generate(buf); httpErr != nil {
		return nil, httpErr
	}

	data := buf.Bytes()

	if len(path) == 0 {
		klog.Info("Returning topology config")
		return data, nil
	}

	klog.Infof("Writing topology config in %q", path)
	if err = files.Create(path, data); err != nil {
		return nil, httperr.NewError(http.StatusInternalServerError, err.Error())
	}
	if params.Reconfigure {
		if err = reconfigure(ctx); err != nil {
			return nil, httperr.NewError(http.StatusInternalServerError, err.Error())
		}
	}

	return []byte("OK\n"), nil
}

func GetTranslateConfig(ctx context.Context, params *BaseParams, topologies map[string]*Topology, f *TopologyNodeFinder) (*translate.Config, *httperr.Error) {
	if err := validateBlockSizes(params.BlockSizes); err != nil {
		return nil, httperr.NewError(http.StatusBadRequest, err.Error())
	}
	if err := translate.ValidateBlockNameConfig(params.BlockName); err != nil {
		return nil, httperr.NewError(http.StatusBadRequest, err.Error())
	}

	cfg := &translate.Config{
		Plugin:     params.Plugin,
		BlockSizes: params.BlockSizes,
		BlockName:  params.BlockName,
	}

	// set per-partition topologies
	if len(topologies) != 0 {
		cfg.Topologies = make(map[string]*translate.TopologySpec)
		for topo, sect := range topologies {
			if err := validateBlockSizes(sect.BlockSizes); err != nil {
				return nil, httperr.NewError(http.StatusBadRequest, fmt.Sprintf("topology %q: %v", topo, err))
			}
			if err := translate.ValidateBlockNameConfig(sect.BlockName); err != nil {
				return nil, httperr.NewError(http.StatusBadRequest, fmt.Sprintf("topology %q: %v", topo, err))
			}
			spec := &translate.TopologySpec{
				Plugin:         sect.Plugin,
				BlockSizes:     sect.BlockSizes,
				BlockName:      sect.BlockName,
				ClusterDefault: sect.Default,
			}
			klog.InfoS("Adding partition topology", "name", topo, "plugin", sect.Plugin, "default", sect.Default, "partition", sect.Partition)
			if sect.Nodes != nil {
				klog.V(4).Infof("%s %q provides nodes %v", sect.Plugin, topo, sect.Nodes)
				spec.Nodes = sect.Nodes
			} else {
				if sect.Default && sect.Plugin == topology.TopologyFlat && len(sect.Partition) == 0 {
					klog.V(4).Infof("skip node discovery for default %s %q", sect.Plugin, topo)
				} else if nodes, err := GetPartitionNodes(ctx, sect.Partition, f); err == nil {
					klog.V(4).Infof("%s %q discovered nodes %v", sect.Plugin, topo, nodes)
					spec.Nodes = nodes
				} else {
					return nil, httperr.NewError(http.StatusInternalServerError, err.Error())
				}
			}
			cfg.Topologies[topo] = spec
		}
	}

	return cfg, nil
}

// maxBlockSizesLen caps the number of entries in a blockSizes list.
// maxBlockSizeValue caps the largest allowed block size value.
// Together they bound the O(blockSizes[last]) tree allocation in buildBlockTree
// and prevent a decompression-bomb DoS via an attacker-controlled blockSizes list.
const (
	maxBlockSizesLen  = 20
	maxBlockSizeValue = 65_536 // 64Ki nodes — larger than any real cluster block
)

func validateBlockSizes(blockSizes []int) error {
	if len(blockSizes) == 0 {
		return nil
	}
	if len(blockSizes) > maxBlockSizesLen {
		return fmt.Errorf("blockSizes has too many entries (%d); max allowed is %d", len(blockSizes), maxBlockSizesLen)
	}
	prev := blockSizes[0]
	if prev <= 0 {
		return fmt.Errorf("blockSizes[0]=%d must be positive", prev)
	}
	if prev > maxBlockSizeValue {
		return fmt.Errorf("blockSizes[0]=%d exceeds maximum allowed value %d", prev, maxBlockSizeValue)
	}
	for i := 1; i < len(blockSizes); i++ {
		cur := blockSizes[i]
		if cur <= 0 {
			return fmt.Errorf("blockSizes[%d]=%d must be positive", i, cur)
		}
		if cur > maxBlockSizeValue {
			return fmt.Errorf("blockSizes[%d]=%d exceeds maximum allowed value %d", i, cur, maxBlockSizeValue)
		}
		if cur <= prev {
			return fmt.Errorf("blockSizes[%d]=%d must be greater than blockSizes[%d]=%d", i, cur, i-1, prev)
		}
		if cur%prev != 0 {
			return fmt.Errorf("blockSizes[%d]=%d must be a multiple of blockSizes[%d]=%d", i, cur, i-1, prev)
		}
		ratio := cur / prev
		if ratio&(ratio-1) != 0 {
			return fmt.Errorf("blockSizes[%d]=%d must be a power-of-two multiple of blockSizes[%d]=%d", i, cur, i-1, prev)
		}
		prev = cur
	}
	return nil
}

func getParams(params map[string]any) (*Params, error) {
	var p Params
	err := config.Decode(params, &p)
	return &p, err
}

func reconfigure(ctx context.Context) error {
	stdout, err := exec.Exec(ctx, "scontrol", []string{"reconfigure"}, nil)
	if err != nil {
		return err
	}

	klog.V(4).Infof("stdout: %s", stdout.String())

	return nil
}
