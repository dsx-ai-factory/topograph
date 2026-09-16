/*
 * Copyright 2024-2026 NVIDIA CORPORATION
 * SPDX-License-Identifier: Apache-2.0
 */

package topology

import (
	"fmt"
	"maps"
	"sort"
	"strings"

	"k8s.io/klog/v2"

	"github.com/dsx-ai-factory/topograph/pkg/metrics"
)

type ClusterTopology struct {
	Instances []*InstanceTopology
}

// FabricTier identifies one fabric layer. InstanceTopology stores tiers
// closest-first, so index zero is the switch closest to the compute node.
type FabricTier struct {
	ID   string
	Name string // optional normalized / display name
}

// ClosestFirstFabricTiers converts closest-first switch IDs to fabric tiers.
func ClosestFirstFabricTiers(ids ...string) []FabricTier {
	tiers := make([]FabricTier, len(ids))
	for i, id := range ids {
		tiers[i].ID = id
	}
	return tiers
}

// RootFirstFabricTiers converts root-first switch IDs to closest-first tiers.
func RootFirstFabricTiers(ids ...string) []FabricTier {
	tiers := make([]FabricTier, len(ids))
	for i, id := range ids {
		tiers[len(ids)-i-1].ID = id
	}
	return tiers
}

type InstanceTopology struct {
	InstanceID      string
	FabricTiers     []FabricTier
	XclrDomainID    string // accelerator domain ID
	XclrSubDomainID string // optional accelerator sub-domain ID
	// Instance optionally carries enriched metadata for instance-oriented output.
	Instance *Instance
}

func (inst *InstanceTopology) String() string {
	var buf strings.Builder
	fmt.Fprintf(&buf, "Instance:%s", inst.InstanceID)
	for index, tier := range inst.FabricTiers {
		if tier.ID != "" {
			fmt.Fprintf(&buf, " Fabric-Tier-%d:%s", index, tier.ID)
			if tier.Name != "" {
				fmt.Fprintf(&buf, " (%s)", tier.Name)
			}
		}
	}
	if inst.XclrDomainID != "" {
		fmt.Fprintf(&buf, " XclrDomain:%s", inst.XclrDomainID)
		if inst.XclrSubDomainID != "" {
			fmt.Fprintf(&buf, " XclrSubDomain:%s", inst.XclrSubDomainID)
		}
	}

	return buf.String()
}

func NewClusterTopology() *ClusterTopology {
	return &ClusterTopology{Instances: []*InstanceTopology{}}
}

func (c *ClusterTopology) Append(inst *InstanceTopology) {
	if klog.V(4).Enabled() {
		klog.V(4).Infof("Adding instance topology %s", inst.String())
	}
	c.Instances = append(c.Instances, inst)
}

func (c *ClusterTopology) Len() int {
	return len(c.Instances)
}

func (c *ClusterTopology) ToGraph(provider string, cis []ComputeInstances, trimTiers int, normalize bool) *Graph {
	i2n := make(map[string]string)
	for _, ci := range cis {
		maps.Copy(i2n, ci.Instances)
	}

	forest := make(map[string]*Vertex)
	type tierKey struct {
		level int
		id    string
	}
	nodes := make(map[tierKey]*Vertex)
	domainMap := NewDomainMap()

	if normalize {
		c.Normalize()
	}

	instances := make(map[string]Instance)
	for _, inst := range c.Instances {
		nodeName, ok := i2n[inst.InstanceID]
		if !ok {
			continue
		}

		klog.V(4).InfoS("Found", "node", nodeName, "instance", inst.InstanceID)
		delete(i2n, inst.InstanceID)

		instance := &Vertex{
			Name: nodeName,
			ID:   inst.InstanceID,
		}

		if inst.XclrDomainID != "" {
			domainMap.AddHostInfo(&HostInfo{
				Domain:     inst.XclrDomainID,
				SubDomain:  inst.XclrSubDomainID,
				InstanceID: inst.InstanceID,
				HostName:   nodeName,
			})
		}
		if inst.Instance != nil {
			instances[inst.InstanceID] = inst.toInstance(trimTiers)
		}

		for level, tier := range trimmedTiers(inst, trimTiers) {
			swID := tier.ID
			if len(swID) == 0 {
				continue
			}

			key := tierKey{level: level, id: swID}
			sw, ok := nodes[key]
			if !ok {
				sw = &Vertex{
					ID:       swID,
					Name:     tier.Name,
					Vertices: make(map[string]*Vertex),
				}
				nodes[key] = sw
			}
			sw.Vertices[instance.ID] = instance
			instance = sw
		}
		if root, ok := forest[instance.ID]; ok {
			mergeVertices(root, instance)
		} else {
			forest[instance.ID] = instance
		}
	}

	if len(i2n) != 0 {
		klog.V(4).Infof("Adding nodes w/o topology: %v", i2n)

		sw := &Vertex{
			ID:       NoTopology,
			Vertices: make(map[string]*Vertex),
		}
		for instanceID, nodeName := range i2n {
			sw.Vertices[instanceID] = &Vertex{
				Name: nodeName,
				ID:   instanceID,
			}
			metrics.SetMissingTopology(provider, nodeName)
		}
		forest[NoTopology] = sw
	}

	treeRoot := &Vertex{
		Vertices: make(map[string]*Vertex),
	}
	maps.Copy(treeRoot.Vertices, forest)

	graph := &Graph{Tiers: treeRoot}
	if len(domainMap) != 0 {
		graph.Domains = domainMap
	}
	if len(instances) != 0 {
		graph.Instances = instances
	}

	return graph
}

// mergeVertices preserves branches that use the same switch ID at different
// depths by recursively merging their children.
func mergeVertices(dst, src *Vertex) {
	if dst == src {
		return
	}
	if dst.Name == "" {
		dst.Name = src.Name
	}
	if dst.Vertices == nil {
		dst.Vertices = make(map[string]*Vertex)
	}
	for id, child := range src.Vertices {
		if existing, ok := dst.Vertices[id]; ok {
			mergeVertices(existing, child)
			continue
		}
		dst.Vertices[id] = child
	}
}

func (c *ClusterTopology) AttachInstances(instances map[string]Instance) {
	for _, topo := range c.Instances {
		instance, ok := instances[topo.InstanceID]
		if !ok {
			continue
		}
		clone := instance
		topo.Instance = &clone
	}
}

func (c *ClusterTopology) Normalize() {
	// sort by network hierarchy
	sort.Slice(c.Instances, func(i, j int) bool {
		a, b := c.Instances[i].FabricTiers, c.Instances[j].FabricTiers
		for level := max(len(a), len(b)) - 1; level >= 0; level-- {
			var aID, bID string
			if level < len(a) {
				aID = a[level].ID
			}
			if level < len(b) {
				bID = b[level].ID
			}
			if aID != bID {
				return aID < bID
			}
		}

		return c.Instances[i].InstanceID < c.Instances[j].InstanceID
	})

	// normalize switch names
	levelCounts := make(map[int]int)
	switches := make(map[int]map[string]string)
	for i, inst := range c.Instances {
		for level, tier := range inst.FabricTiers {
			if tier.ID == "" {
				continue
			}
			if switches[level] == nil {
				switches[level] = make(map[string]string)
			}
			name, ok := switches[level][tier.ID]
			if !ok {
				levelCounts[level]++
				name = fmt.Sprintf("switch.%d.%d", level+1, levelCounts[level])
				switches[level][tier.ID] = name
			}
			c.Instances[i].FabricTiers[level].Name = name
		}
	}
}

func trimmedTiers(inst *InstanceTopology, trimTiers int) []FabricTier {
	trim := min(max(0, trimTiers), len(inst.FabricTiers))
	keep := len(inst.FabricTiers) - trim
	tiers := make([]FabricTier, keep)
	copy(tiers, inst.FabricTiers[:keep])
	return tiers
}

func (inst *InstanceTopology) toInstance(trimTiers int) Instance {
	instance := *inst.Instance
	if instance.ID == "" {
		instance.ID = inst.InstanceID
	}
	instance.NetworkLayers = inst.networkLayers(trimTiers)
	if instance.Labels[KeyTopologyXclrDomain] == "" && inst.XclrDomainID != "" {
		if instance.Labels == nil {
			instance.Labels = make(map[string]string)
		}
		instance.Labels[KeyTopologyXclrDomain] = inst.XclrDomainID
	}
	if instance.Labels[KeyTopologyXclrDomain] != "" &&
		instance.Labels[KeyTopologyXclrSubDomain] == "" &&
		inst.XclrSubDomainID != "" {
		instance.Labels[KeyTopologyXclrSubDomain] = inst.XclrSubDomainID
	}

	return instance
}

func (inst *InstanceTopology) networkLayers(trimTiers int) []string {
	layers := []string{}
	for _, tier := range trimmedTiers(inst, trimTiers) {
		if tier.ID == "" {
			continue
		}
		if tier.Name != "" {
			layers = append(layers, tier.Name)
			continue
		}
		layers = append(layers, tier.ID)
	}
	return layers
}
