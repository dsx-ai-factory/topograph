/*
 * Copyright 2024-2026 NVIDIA CORPORATION
 * SPDX-License-Identifier: Apache-2.0
 */

package k8s

import (
	"context"
	"fmt"
	"hash/fnv"
	"slices"

	internalk8s "github.com/dsx-ai-factory/topograph/internal/k8s"
	"github.com/dsx-ai-factory/topograph/pkg/topology"
)

type TopologyLabelKeys struct {
	Fabric        []string
	XclrDomain    string
	XclrSubDomain string
}

// NewTopologyLabelKeys creates an independent copy of the configured
// closest-first fabric label keys and XCLR label keys.
func NewTopologyLabelKeys(fabric []string, xclrDomain string) *TopologyLabelKeys {
	if xclrDomain == "" {
		xclrDomain = topology.KeyTopologyXclrDomain
	}
	return &TopologyLabelKeys{
		Fabric:        slices.Clone(fabric),
		XclrDomain:    xclrDomain,
		XclrSubDomain: topology.KeyTopologyXclrSubDomain,
	}
}

// Validate checks that configured keys are valid and unique across all label
// families.
func (keys *TopologyLabelKeys) Validate() error {
	seen := make(map[string]string)
	validate := func(location, key string) error {
		if err := internalk8s.ValidateLabelKey(location, key); err != nil {
			return err
		}
		if previous, ok := seen[key]; ok {
			return fmt.Errorf("topology label key %q is configured for both %s and %s", key, previous, location)
		}
		seen[key] = location
		return nil
	}
	for tier, key := range keys.Fabric {
		if err := validate(fmt.Sprintf("fabricLabels[%d]", tier), key); err != nil {
			return err
		}
	}
	if err := validate("acceleratorLabel", keys.XclrDomain); err != nil {
		return err
	}
	return validate("xclrSubDomainLabel", keys.XclrSubDomain)
}

// FabricKey returns the fabric key for tier. Defaults are used when no custom
// fabric keys were supplied; an empty result means the tier is omitted.
func (keys *TopologyLabelKeys) FabricKey(tier int) string {
	if len(keys.Fabric) == 0 {
		return topology.FabricTierKey(tier)
	}
	if tier >= 0 && tier < len(keys.Fabric) {
		return keys.Fabric[tier]
	}
	return ""
}

// XclrDomainKey returns the configured XCLR domain label key.
func (keys *TopologyLabelKeys) XclrDomainKey() string {
	return keys.XclrDomain
}

// XclrSubDomainKey returns the XCLR sub-domain label key.
func (keys *TopologyLabelKeys) XclrSubDomainKey() string {
	return keys.XclrSubDomain
}

// map nodename:[label name: label value]
type NodeLabelMap map[string]map[string]string

type Labeler interface {
	AddNodeLabels(context.Context, string, map[string]string) error
}

type topologyLabeler struct {
	mapper map[string]string
	keys   *TopologyLabelKeys
}

// NewTopologyLabeler creates a graph-to-label translator using the supplied
// topology label keys.
func NewTopologyLabeler(keys *TopologyLabelKeys) *topologyLabeler {
	return &topologyLabeler{
		mapper: make(map[string]string),
		keys:   keys,
	}
}

// ApplyNodeLabels builds the desired labels and delegates each node update to
// the supplied Labeler.
func (l *topologyLabeler) ApplyNodeLabels(ctx context.Context, graph *topology.Graph, labeler Labeler) error {
	nodeMap, err := l.BuildNodeLabels(graph)
	if err != nil {
		return err
	}

	for nodeName, labels := range nodeMap {
		if err := labeler.AddNodeLabels(ctx, nodeName, labels); err != nil {
			return err
		}
	}

	return nil
}

// BuildNodeLabels converts accelerator domains and fabric tiers into desired
// Kubernetes labels keyed by node name.
func (l *topologyLabeler) BuildNodeLabels(graph *topology.Graph) (NodeLabelMap, error) {
	nodeMap := make(NodeLabelMap)

	if graph == nil || (graph.Domains == nil && graph.Tiers == nil) {
		return nodeMap, nil
	}

	if graph.Domains != nil {
		if err := l.getDomainLabels(graph.Domains, nodeMap); err != nil {
			return nil, err
		}
	}

	if treeRoot := graph.Tiers; treeRoot != nil {
		layers := []string{}
		if len(treeRoot.ID) != 0 {
			layers = append(layers, treeRoot.ID)
		}
		if err := l.getTierLabels(treeRoot, nodeMap, layers); err != nil {
			return nil, err
		}
	}

	return nodeMap, nil
}

// getDomainLabels adds accelerator domain and optional sub-domain labels.
func (l *topologyLabeler) getDomainLabels(domains topology.DomainMap, nodeMap NodeLabelMap) error {
	domainKey := l.keys.XclrDomainKey()
	subDomainKey := l.keys.XclrSubDomainKey()
	for domainName, domain := range domains {
		for nodeName, hostInfo := range domain {
			if nodeName == "" {
				continue
			}
			labels, ok := nodeMap[nodeName]
			if !ok {
				labels = make(map[string]string)
				nodeMap[nodeName] = labels
			}
			if val, ok := labels[domainKey]; ok {
				return fmt.Errorf("multiple accelerator labels %s, %s for node %s", val, domainName, nodeName)
			}
			labels[domainKey] = l.checkLabel(domainName)
			if hostInfo != nil && hostInfo.SubDomain != "" {
				labels[subDomainKey] = l.checkLabel(hostInfo.SubDomain)
			}
		}
	}
	return nil
}

// getTierLabels walks the fabric tree and records closest-first switch labels
// when it reaches each compute-node vertex.
func (l *topologyLabeler) getTierLabels(v *topology.Vertex, nodeMap NodeLabelMap, layers []string) error {
	if len(v.Vertices) == 0 { // compute node
		if len(layers) != 0 {
			if v.ID != layers[0] {
				return fmt.Errorf("instance ID mismatch: expected %s, got %s", v.ID, layers[0])
			}
			nodeName := v.Name
			if nodeName == "" {
				return nil
			}
			labels, ok := nodeMap[nodeName]
			if !ok {
				labels = make(map[string]string)
				nodeMap[nodeName] = labels
			}
			for i, sw := range layers[1:] {
				labelKey := l.keys.FabricKey(i)
				if len(sw) == 0 || labelKey == "" {
					break
				}
				labels[labelKey] = l.checkLabel(sw)
			}
		}
		return nil
	}

	for _, w := range v.Vertices {
		if err := l.getTierLabels(w, nodeMap, append([]string{w.ID}, layers...)); err != nil {
			return err
		}
	}

	return nil
}

// checkLabel preserves valid-length values and hashes values that exceed the
// Kubernetes 63-character label-value limit.
func (l *topologyLabeler) checkLabel(val string) string {
	v, ok := l.mapper[val]
	if ok {
		return v
	}

	if len(val) <= 63 {
		v = val
	} else {
		h := fnv.New64a()
		h.Write([]byte(val))
		v = fmt.Sprintf("x%x", h.Sum64())
	}

	l.mapper[val] = v
	return v
}
