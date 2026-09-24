/*
 * Copyright 2024-2026 NVIDIA CORPORATION
 * SPDX-License-Identifier: Apache-2.0
 */

package models

import (
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"sort"

	"gopkg.in/yaml.v3"

	"github.com/dsx-ai-factory/topograph/internal/cluset"
	"github.com/dsx-ai-factory/topograph/pkg/topology"
	"github.com/dsx-ai-factory/topograph/tests"
)

const (
	LabelTopologyRegion            = "topology.kubernetes.io/region"
	LabelTopologyZone              = "topology.kubernetes.io/zone"
	defaultRegion                  = "none"
	annotationAcceleratorDomain    = "accelerator.topology.test/domain"
	annotationAcceleratorSubDomain = "accelerator.topology.test/sub-domain"
)

// Switch is a switch vertex in a simulation model YAML tree (tests/models).
type Switch struct {
	Name        string            `yaml:"name,omitempty"`
	Labels      map[string]string `yaml:"labels"`
	Annotations map[string]string `yaml:"annotations"`
	Switches    []string          `yaml:"switches"`
	Nodes       []string          `yaml:"-"`
}

// CapacityBlock is the blocks entry shape in simulation model YAML.
type CapacityBlock struct {
	Switch      string            `yaml:"switch"`
	Nodes       []string          `yaml:"nodes"`
	Labels      map[string]string `yaml:"labels"`
	Annotations map[string]string `yaml:"annotations"`
}

// Node is a compute instance in a simulation model. Annotations hold
// provider-specific test inputs and are not exported as topology labels.
type Node struct {
	topology.Instance
	Annotations map[string]string `yaml:"annotations"`
}

// AcceleratorDomain returns the simulated accelerator-domain identifier.
func (node *Node) AcceleratorDomain() string {
	return node.Annotations[annotationAcceleratorDomain]
}

// AcceleratorSubDomain returns the simulated accelerator sub-domain identifier.
func (node *Node) AcceleratorSubDomain() string {
	return node.Annotations[annotationAcceleratorSubDomain]
}

type Model struct {
	Switches       map[string]*Switch `yaml:"switches"`
	Nodes          map[string]*Node   `yaml:"-"`
	CapacityBlocks []CapacityBlock    `yaml:"blocks"`

	// derived
	Instances []topology.ComputeInstances `yaml:"-"`
}

func NewModelFromFile(fname string) (*Model, error) {
	var err error
	var data []byte
	//Check if the fileName includes the path (absolute or relative).
	//If it is just the fileName, load it from the testdata directory first, otherwise fallback to the given path
	dir, fileName := filepath.Split(fname)
	if len(dir) == 0 {
		data, err = tests.GetModelFileData(fileName)
	} else {
		data, err = os.ReadFile(fname)
	}

	if err != nil {
		return nil, fmt.Errorf("failed to read %s: %v", fname, err)
	}
	return NewModelFromData(data, fname)
}

func NewModelFromData(data []byte, fname string) (*Model, error) {
	model := &Model{}
	if err := yaml.Unmarshal(data, model); err != nil {
		return nil, fmt.Errorf("failed to parse %s: %v", fname, err)
	}

	if err := model.derive(); err != nil {
		return nil, err
	}

	return model, nil
}

func (m *Model) UnmarshalYAML(value *yaml.Node) error {
	var raw struct {
		Switches map[string]*Switch `yaml:"switches"`
		Blocks   []CapacityBlock    `yaml:"blocks"`
	}
	if err := value.Decode(&raw); err != nil {
		return err
	}

	m.Switches = raw.Switches
	m.CapacityBlocks = raw.Blocks
	for name, sw := range m.Switches {
		if sw == nil {
			return fmt.Errorf("switch %q has empty definition", name)
		}
		if sw.Name != "" && sw.Name != name {
			return fmt.Errorf("switch key %q does not match switch name %q", name, sw.Name)
		}
		sw.Name = name
	}

	// Leaf switches have no configuration of their own, so model files may omit
	// their otherwise-empty top-level definitions. Materialize every switch
	// referenced by a parent to keep the derived model and graph complete.
	for _, parent := range m.Switches {
		for _, name := range parent.Switches {
			if _, ok := m.Switches[name]; !ok {
				m.Switches[name] = &Switch{Name: name}
			}
		}
	}
	return nil
}

type switchMaps struct {
	parentBySwitch map[string]*Switch
	switchByNode   map[string]*Switch
}

func (m *Model) buildSwitchMaps() (*switchMaps, error) {
	// switch map child:parent
	swmap := make(map[string]*Switch)
	nodeMap := make(map[string]*Switch)

	for _, parent := range m.Switches {
		for _, sw := range parent.Switches {
			if _, exists := m.Switches[sw]; !exists {
				return nil, fmt.Errorf("switch %q references unknown sub-switch %q", parent.Name, sw)
			}
			if p, ok := swmap[sw]; ok {
				// a child switch cannot have more than one parent switch
				return nil, fmt.Errorf("switch %q has two parent switches %q and %q", sw, parent.Name, p.Name)
			}
			swmap[sw] = parent
		}
		parent.Nodes = cluset.Expand(parent.Nodes)
		for _, node := range parent.Nodes {
			if p, ok := nodeMap[node]; ok {
				// a node cannot be attached to more than one switch
				return nil, fmt.Errorf("node %q has two switches %q and %q", node, parent.Name, p.Name)
			}
			nodeMap[node] = parent
		}
	}

	return &switchMaps{
		parentBySwitch: swmap,
		switchByNode:   nodeMap,
	}, nil
}

func (m *Model) derive() error {
	if err := m.completeCapacityBlocks(); err != nil {
		return err
	}

	maps, err := m.buildSwitchMaps()
	if err != nil {
		return err
	}

	if len(m.Nodes) == 0 && len(maps.switchByNode) != 0 {
		return fmt.Errorf("switches reference nodes that are not declared by blocks")
	}

	for node := range maps.switchByNode {
		if _, ok := m.Nodes[node]; !ok {
			return fmt.Errorf("switch references unknown node %q", node)
		}
	}

	regions := make(map[string]map[string]string)
	for hostName, node := range m.Nodes {
		var netLayers []string
		var labels map[string]string
		var annotations map[string]string

		if sw, ok := maps.switchByNode[hostName]; ok {
			netLayers, labels, annotations, err = getNetworkLayers(sw, maps.parentBySwitch)
			if err != nil {
				return err
			}
		}

		node.Labels = withDefaultRegion(mergeMaps(labels, node.Labels))
		node.Annotations = mergeMaps(annotations, node.Annotations)
		node.NetLayers = netLayers
		addInstanceRegion(regions, node.Labels, hostName)
	}

	m.setInstances(regions)
	return nil
}

func (m *Model) completeCapacityBlocks() error {
	for capacityBlockIndex := range m.CapacityBlocks {
		capacityBlock := m.CapacityBlocks[capacityBlockIndex]
		capacityBlock.Nodes = cluset.Expand(capacityBlock.Nodes)
		if len(capacityBlock.Nodes) == 0 {
			return fmt.Errorf("capacity block at index %d must declare at least one node", capacityBlockIndex)
		}
		if capacityBlock.Switch != "" {
			sw, ok := m.Switches[capacityBlock.Switch]
			if !ok {
				return fmt.Errorf("capacity block at index %d references unknown switch %q", capacityBlockIndex, capacityBlock.Switch)
			}
			for _, node := range capacityBlock.Nodes {
				sw.Nodes = appendUnique(sw.Nodes, node)
			}
		}
		m.CapacityBlocks[capacityBlockIndex] = capacityBlock
		for _, name := range capacityBlock.Nodes {
			if m.Nodes == nil {
				m.Nodes = make(map[string]*Node)
			}
			if _, ok := m.Nodes[name]; ok {
				return fmt.Errorf("node %q belongs to more than one capacity block", name)
			}
			m.Nodes[name] = &Node{
				Instance: topology.Instance{
					ID:     name,
					Labels: cloneStringMap(capacityBlock.Labels),
				},
				Annotations: cloneStringMap(capacityBlock.Annotations),
			}
		}
	}

	return nil
}

func appendUnique(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func cloneStringMap(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}
	return maps.Clone(values)
}

// mergeMaps copies overlay into base, with overlay values taking precedence.
func mergeMaps(base, overlay map[string]string) map[string]string {
	if len(overlay) == 0 {
		return base
	}
	if base == nil {
		base = make(map[string]string, len(overlay))
	}
	maps.Copy(base, overlay)
	return base
}

func withDefaultRegion(labels map[string]string) map[string]string {
	if labels[LabelTopologyRegion] != "" {
		return labels
	}
	if labels == nil {
		labels = make(map[string]string)
	}
	labels[LabelTopologyRegion] = defaultRegion
	return labels
}

func addInstanceRegion(regions map[string]map[string]string, labels map[string]string, hostName string) {
	region := labels[LabelTopologyRegion]
	r, ok := regions[region]
	if !ok {
		r = make(map[string]string)
		regions[region] = r
	}
	r[InstanceID(hostName)] = hostName
}

func (m *Model) setInstances(regions map[string]map[string]string) {
	regionNames := make([]string, 0, len(regions))
	for region := range regions {
		regionNames = append(regionNames, region)
	}
	sort.Strings(regionNames)

	m.Instances = make([]topology.ComputeInstances, 0, len(regions))
	for _, region := range regionNames {
		m.Instances = append(m.Instances, topology.ComputeInstances{
			Region:    region,
			Instances: regions[region],
		})
	}
}

func getNetworkLayers(sw *Switch, swmap map[string]*Switch) ([]string, map[string]string, map[string]string, error) {
	visited := make(map[string]struct{})
	layers := []string{}
	path := []*Switch{}

	for {
		name := sw.Name
		// check for circular switch topology
		if _, ok := visited[name]; ok {
			return nil, nil, nil, fmt.Errorf("circular dependency detected in topology for switch %q", name)
		}
		visited[name] = struct{}{}
		layers = append(layers, name)
		path = append(path, sw)
		parent, ok := swmap[name]
		if !ok {
			labels := map[string]string(nil)
			annotations := map[string]string(nil)
			for i := len(path) - 1; i >= 0; i-- {
				labels = mergeMaps(labels, path[i].Labels)
				annotations = mergeMaps(annotations, path[i].Annotations)
			}
			return layers, labels, annotations, nil
		}
		sw = parent
	}
}

func (model *Model) ToGraph(instances []topology.ComputeInstances) (*topology.Graph, map[string]string) {
	instance2node := make(map[string]string)
	nodeVertexMap := make(map[string]*topology.Vertex)
	swVertexMap := make(map[string]*topology.Vertex)
	swRootMap := make(map[string]bool)
	domainMap := topology.NewDomainMap()

	for hostName := range model.Nodes {
		instance2node[InstanceID(hostName)] = hostName
	}

	// Create all the vertices for each node.
	for hostName := range model.Nodes {
		instanceID := InstanceID(hostName)
		nodeVertexMap[hostName] = &topology.Vertex{
			ID:   instanceID,
			Name: instance2node[instanceID],
		}
	}

	// Initialize all the vertices for each switch (setting each on to be a possible root)
	for _, sw := range model.Switches {
		swVertexMap[sw.Name] = &topology.Vertex{ID: sw.Name, Vertices: make(map[string]*topology.Vertex)}
		swRootMap[sw.Name] = true
	}

	// Initializes accelerator-network membership.
	for hostName, instance := range model.Nodes {
		if domain := instance.AcceleratorDomain(); domain != "" {
			instanceID := InstanceID(hostName)
			domainMap.AddHostInfo(&topology.HostInfo{
				Domain:     domain,
				InstanceID: instanceID,
				HostName:   instance2node[instanceID],
				SubDomain:  instance.AcceleratorSubDomain(),
			})
		}
	}

	// Connect all the switches to their sub-switches and sub-nodes
	for _, sw := range model.Switches {
		for _, subsw := range sw.Switches {
			swRootMap[subsw] = false
			swVertexMap[sw.Name].Vertices[subsw] = swVertexMap[subsw]
		}
		for _, node := range sw.Nodes {
			vertex := nodeVertexMap[node]
			swVertexMap[sw.Name].Vertices[vertex.ID] = vertex
		}
	}

	// Connects all root vertices to the hidden root
	treeRoot := &topology.Vertex{Vertices: make(map[string]*topology.Vertex)}
	for k, v := range swRootMap {
		if v {
			treeRoot.Vertices[k] = swVertexMap[k]
		}
	}
	graph := &topology.Graph{Tiers: treeRoot}
	if len(domainMap) != 0 {
		graph.Domains = domainMap
	}
	if instanceMap := model.graphInstanceMap(instances); len(instanceMap) != 0 {
		graph.Instances = instanceMap
	}

	return graph, instance2node
}

func (model *Model) graphInstanceMap(computeInstances []topology.ComputeInstances) map[string]topology.Instance {
	wanted := requestedInstanceIDs(computeInstances)
	instances := make(map[string]topology.Instance)

	for hostName, inst := range model.Nodes {
		instanceID := InstanceID(hostName)
		if len(wanted) != 0 {
			if _, ok := wanted[instanceID]; !ok {
				continue
			}
		}
		clone := inst.CloneForTopology()
		clone.ID = instanceID
		instances[instanceID] = clone
	}
	return instances
}

func (model *Model) InstanceMap(computeInstances []topology.ComputeInstances) map[string]topology.Instance {
	wanted := requestedInstanceIDs(computeInstances)
	instances := make(map[string]topology.Instance)

	for _, inst := range model.Nodes {
		if len(wanted) != 0 {
			if _, ok := wanted[inst.ID]; !ok {
				continue
			}
		}
		instances[inst.ID] = inst.CloneForTopology()
	}
	return instances
}

func requestedInstanceIDs(computeInstances []topology.ComputeInstances) map[string]struct{} {
	ids := make(map[string]struct{})
	for _, ci := range computeInstances {
		for instanceID := range ci.Instances {
			ids[instanceID] = struct{}{}
		}
	}
	return ids
}

// InstanceID returns the simulated instance ID for a model hostname.
func InstanceID(hostName string) string {
	return fmt.Sprintf("i-%s", hostName)
}
