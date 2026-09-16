/*
 * Copyright 2025 NVIDIA CORPORATION
 * SPDX-License-Identifier: Apache-2.0
 */

package translate

import (
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"

	"github.com/agrea/ptr"
	"k8s.io/klog/v2"

	"github.com/dsx-ai-factory/topograph/internal/httperr"
	"github.com/dsx-ai-factory/topograph/pkg/topology"
)

type Config struct {
	Plugin     string // topology plugin (cluster-wide)
	BlockSizes []int
	BlockName  *BlockNameConfig
	Topologies map[string]*TopologySpec // per-partiton topology settings
}

// TopologySpec define topology for a partition
type TopologySpec struct {
	Plugin         string
	BlockSizes     []int
	BlockName      *BlockNameConfig
	ClusterDefault bool
	Nodes          []string
}

type BlockNameConfig struct {
	NodeNameRegexp string `mapstructure:"nodeNameRegexp"`
	Format         string `mapstructure:"format"`
	compiledRegexp *regexp.Regexp
}

type blockNameFormatter struct {
	re     *regexp.Regexp
	format string
}

func compileBlockNameFormatter(config *BlockNameConfig) *blockNameFormatter {
	if config == nil {
		return nil
	}
	return &blockNameFormatter{
		re:     config.compiledRegexp,
		format: config.Format,
	}
}

func blockDescription(id, domain string) string {
	description := fmt.Sprintf("block %q", id)
	if domain != "" {
		description += fmt.Sprintf(" (domain %q)", domain)
	}
	return description
}

func (f *blockNameFormatter) formatBlockName(defaultName, domainName string, nodes []string) (string, error) {
	if f == nil || len(nodes) == 0 {
		return defaultName, nil
	}

	description := blockDescription(defaultName, domainName)
	var blockName string
	for _, node := range nodes {
		match := f.re.FindStringSubmatchIndex(node)
		if match == nil {
			return "", fmt.Errorf("node %q in %s does not match nodeNameRegexp %q", node, description, f.re.String())
		}

		nodeBlockName := string(f.re.ExpandString(nil, f.format, node, match))
		if nodeBlockName == "" {
			return "", fmt.Errorf("blockName format %q produces an empty name for node %q in %s", f.format, node, description)
		}
		if blockName == "" {
			blockName = nodeBlockName
		} else if nodeBlockName != blockName {
			return "", fmt.Errorf(
				"nodes in %s produce different block names %q and %q",
				description,
				blockName,
				nodeBlockName,
			)
		}
	}
	return blockName, nil
}

func formatBlockNames(blocks []*blockInfo, formatter *blockNameFormatter) ([]string, error) {
	names := make([]string, len(blocks))
	seen := make(map[string]string, len(blocks))
	for i, block := range blocks {
		name, err := formatter.formatBlockName(block.id, block.name, block.nodes)
		if err != nil {
			return nil, err
		}
		if previousBlock, ok := seen[name]; ok {
			return nil, fmt.Errorf("blocks %q and %q produce duplicate block name %q", previousBlock, block.id, name)
		}
		seen[name] = block.id
		names[i] = name
	}
	return names, nil
}

type NetworkTopology struct {
	config   *Config
	tree     map[string][]string         // adjacency list
	domains  topology.DomainMap          // accelerator domains from provider graph
	blocks   []*blockInfo                // blocks
	vertices map[string]*topology.Vertex // object ID to Vertex map
	nodeInfo map[string]*nodeInfo        // node name to nodeInfo map
}

type blockInfo struct {
	id    string
	name  string
	indx  int
	nodes []string
}

type nodeInfo struct {
	instanceID string
	blockID    string
	blockIndx  *int
	switches   []string
}

func (cfg *Config) Validate(graph *topology.Graph) error {
	if cfg.BlockName != nil && cfg.Plugin != topology.TopologyBlock {
		return fmt.Errorf("blockName is only supported with plugin %q", topology.TopologyBlock)
	}
	if err := ValidateBlockNameConfig(cfg.BlockName); err != nil {
		return err
	}
	if len(cfg.Topologies) != 0 { // per-partition topology
		if len(cfg.Plugin) != 0 {
			return fmt.Errorf("plugin and topologies parameters are mutually exclusive")
		}
		for topo, spec := range cfg.Topologies {
			if spec.BlockName != nil && spec.Plugin != topology.TopologyBlock {
				return fmt.Errorf("topology %q: blockName is only supported with plugin %q", topo, topology.TopologyBlock)
			}
			if err := ValidateBlockNameConfig(spec.BlockName); err != nil {
				return fmt.Errorf("topology %q: %v", topo, err)
			}
			switch spec.Plugin {
			case topology.TopologyTree:
				if graph == nil || graph.Tiers == nil {
					return fmt.Errorf("missing tree topology for topology %q", topo)
				}
			case topology.TopologyBlock:
				if graph == nil || graph.Domains == nil {
					return fmt.Errorf("missing block topology for topology %q", topo)
				}
				if len(graph.Domains) == 0 {
					return fmt.Errorf("block topology for topology %q contains no accelerator domains", topo)
				}
			case topology.TopologyFlat:
				// nop
			default:
				return fmt.Errorf("unsupported topology plugin %q for topology %q", spec.Plugin, topo)
			}
			if len(spec.Nodes) == 0 && spec.Plugin != topology.TopologyFlat {
				return fmt.Errorf("topology %q specifies no nodes", topo)
			}
		}
	} else { // cluster-wide topology
		switch cfg.Plugin {
		case topology.TopologyTree:
			if graph == nil || graph.Tiers == nil {
				return fmt.Errorf("missing tree topology")
			}
		case topology.TopologyBlock:
			if graph == nil || graph.Domains == nil {
				return fmt.Errorf("missing block topology")
			}
			if len(graph.Domains) == 0 {
				return fmt.Errorf("block topology contains no accelerator domains")
			}
		default:
			return fmt.Errorf("unsupported topology plugin %q", cfg.Plugin)
		}
	}
	return nil
}

func ValidateBlockNameConfig(config *BlockNameConfig) error {
	if config == nil {
		return nil
	}
	if config.NodeNameRegexp == "" {
		return fmt.Errorf("blockName.nodeNameRegexp must not be empty")
	}
	if config.Format == "" {
		return fmt.Errorf("blockName.format must not be empty")
	}
	if config.compiledRegexp != nil && config.compiledRegexp.String() == config.NodeNameRegexp {
		return nil
	}
	compiled, err := regexp.Compile(config.NodeNameRegexp)
	if err != nil {
		config.compiledRegexp = nil
		return fmt.Errorf("invalid blockName.nodeNameRegexp %q: %v", config.NodeNameRegexp, err)
	}
	config.compiledRegexp = compiled
	return nil
}

func NewNetworkTopology(graph *topology.Graph, cfg *Config) (*NetworkTopology, error) {
	if err := cfg.Validate(graph); err != nil {
		return nil, err
	}

	nt := &NetworkTopology{
		config:   cfg,
		tree:     make(map[string][]string),
		vertices: make(map[string]*topology.Vertex),
		nodeInfo: make(map[string]*nodeInfo),
	}

	if err := nt.initTree(graph); err != nil {
		return nil, err
	}
	nt.initBlocks(graph)

	return nt, nil
}

func (nt *NetworkTopology) initTree(graph *topology.Graph) error {
	if graph == nil || graph.Tiers == nil {
		return nil
	}

	parentMap := make(map[string][]string)
	seen := map[string]bool{graph.Tiers.ID: true}
	queue := []*topology.Vertex{graph.Tiers}
	for len(queue) > 0 {
		v := queue[0]
		queue = queue[1:]
		_, ok := nt.tree[v.ID]
		if !ok {
			nt.tree[v.ID] = []string{}
			nt.vertices[v.ID] = v
			if len(v.Vertices) == 0 {
				nt.nodeInfo[v.Name] = &nodeInfo{instanceID: v.ID, switches: parentMap[v.ID]}
				klog.V(4).InfoS("initTree: adding nodeInfo", "name", v.Name, "instanceID", v.ID, "switches", parentMap[v.ID])
			}
		}
		for id, w := range v.Vertices {
			if w == nil {
				return fmt.Errorf("vertex %q contains nil child with key %q", v.ID, id)
			}
			if id == v.ID || w.ID == v.ID {
				return fmt.Errorf("vertex %q contains a self-edge", v.ID)
			}
			if seen[w.ID] {
				return fmt.Errorf("vertex %q is reachable more than once; topology must be a tree", w.ID)
			}
			seen[w.ID] = true
			if len(v.ID) != 0 {
				parentMap[w.ID] = append([]string{}, parentMap[v.ID]...)
				parentMap[w.ID] = append(parentMap[w.ID], v.ID)
			}
			nt.tree[v.ID] = append(nt.tree[v.ID], id)
			queue = append(queue, w)
		}
	}

	for _, val := range nt.tree {
		sort.Strings(val)
	}
	return nil
}

func toBlockInfos(domains topology.DomainMap) []*blockInfo {
	domainNames := make([]string, 0, len(domains))
	for domainName := range domains {
		domainNames = append(domainNames, domainName)
	}
	sort.Strings(domainNames)

	blocks := make([]*blockInfo, 0, len(domainNames))
	for i, domainName := range domainNames {
		domain := domains[domainName]
		nodes := make([]string, 0, len(domain))
		for node := range domain {
			nodes = append(nodes, node)
		}
		sort.Strings(nodes)

		blocks = append(blocks, &blockInfo{
			id:    fmt.Sprintf("block%03d", i+1),
			name:  domainName,
			nodes: nodes,
		})
	}

	return blocks
}

func (nt *NetworkTopology) initBlocks(graph *topology.Graph) {
	if graph == nil {
		klog.Warning("block topology data not found")
		return
	}
	domains := graph.Domains
	if domains == nil {
		klog.Warning("block topology data not found")
		return
	}

	if len(domains) == 0 {
		klog.Warning("no blocks found in block topology")
		return
	}

	nt.domains = domains
	domainBlocks := toBlockInfos(domains)
	nt.blocks = make([]*blockInfo, 0, len(domainBlocks))
	indx := 0

	if graph.Tiers == nil { // no tree data
		for _, bInfo := range domainBlocks {
			bInfo.indx = indx
			for _, node := range bInfo.nodes {
				hostInfo := domains[bInfo.name][node]
				if hostInfo == nil {
					klog.Warningf("initBlocks: missing host info for node %q in domain %q", node, bInfo.name)
					continue
				}
				nt.nodeInfo[node] = &nodeInfo{
					instanceID: hostInfo.InstanceID,
					blockID:    bInfo.id,
					blockIndx:  ptr.Int(indx),
				}
				klog.V(4).InfoS("initBlocks: adding nodeInfo", "name", node, "blockID", bInfo.id, "blockIndx", indx)
			}
			nt.blocks = append(nt.blocks, bInfo)
			indx++
		}
	} else {
		// set block ID for each node
		blockMap := make(map[string]*blockInfo)
		for _, block := range domainBlocks {
			blockMap[block.id] = block
			for _, node := range block.nodes {
				if info, ok := nt.nodeInfo[node]; ok {
					info.blockID = block.id
				}
			}
		}
		// sort blocks according to the node appearance in the tree
		queue := []*topology.Vertex{graph.Tiers}
		for len(queue) > 0 {
			v := queue[0]
			queue = queue[1:]

			if len(v.Vertices) == 0 { // a leaf (node)
				// check if this node hasn't been visited
				if nInfo, ok := nt.nodeInfo[v.Name]; ok && len(nInfo.blockID) != 0 && nInfo.blockIndx == nil {
					// mark all nodes in this block
					if block, ok := blockMap[nInfo.blockID]; ok {
						bInfo := nt.markBlockNodes(block, indx)
						nt.blocks = append(nt.blocks, bInfo)
						indx++
					}
				}
			} else {
				keys := make([]string, 0, len(v.Vertices))
				for key := range v.Vertices {
					keys = append(keys, key)
				}
				sort.Strings(keys)
				for _, key := range keys {
					w := v.Vertices[key]
					queue = append(queue, w)
				}
			}
		}
	}
}

// markBlockNodes assigns provided block index to the block nodes
func (nt *NetworkTopology) markBlockNodes(block *blockInfo, indx int) *blockInfo {
	pIndx := ptr.Int(indx)
	bInfo := &blockInfo{
		id:    block.id,
		name:  block.name,
		indx:  indx,
		nodes: append([]string(nil), block.nodes...),
	}
	for _, node := range bInfo.nodes {
		if info, ok := nt.nodeInfo[node]; ok {
			info.blockIndx = pIndx
		}
	}
	return bInfo
}

func (nt *NetworkTopology) Generate(wr io.Writer) *httperr.Error {
	_, err := nt.GenerateTopologyConfig(wr, false)
	return err
}

func (nt *NetworkTopology) GenerateTopologyConfig(wr io.Writer, skeletonOnly bool) ([]*TopologyUnit, *httperr.Error) {
	topologies, httpErr := nt.GetTopologies()
	if httpErr != nil {
		return topologies, httpErr
	}

	if len(nt.config.Topologies) != 0 {
		return topologies, nt.toYamlTopology(wr, topologies, skeletonOnly)
	} else {
		if nt.config.Plugin == topology.TopologyBlock {
			return topologies, nt.toBlockTopology(wr, skeletonOnly)
		}
		return topologies, nt.toTreeTopology(wr, skeletonOnly)
	}
}

func (nt *NetworkTopology) GetNodeTopologySpec(node string, topologies []*TopologyUnit) (string, *httperr.Error) {

	if _, exists := nt.nodeInfo[node]; !exists {
		return "", nil
	}

	if len(nt.config.Topologies) != 0 {
		return getTopologySpec(node, topologies)
	} else {
		nodeInfo, ok := nt.nodeInfo[node]
		if !ok {
			return "", nil
		}
		switch nt.config.Plugin {
		case topology.TopologyBlock:
			return fmt.Sprintf("default:%s", nodeInfo.blockID), nil
		case topology.TopologyTree:
			return fmt.Sprintf("default:%s", strings.Join(nodeInfo.switches, ":")), nil
		default:
			return "", nil
		}
	}
}
