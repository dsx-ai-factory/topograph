// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package nautobot

import (
	"fmt"
	"slices"
	"sort"

	"github.com/dsx-ai-factory/topograph/pkg/topology"
)

type inventory struct {
	switches map[string]device
	hosts    map[string]device
	links    map[string]map[string]bool
}

func (p *Provider) excluded(d device) bool {
	return d.Status != nil && slices.Contains(p.params.ExcludeStatuses, d.Status.Name)
}

func (p *Provider) inventory(devices []device) (*inventory, error) {
	inv := &inventory{switches: map[string]device{}, hosts: map[string]device{}, links: map[string]map[string]bool{}}
	known := map[string]device{}
	switchNames := map[string]string{}
	for _, d := range devices {
		if p.excluded(d) {
			continue
		}
		if d.ID == "" || d.Name == "" {
			return nil, fmt.Errorf("device returned by Nautobot has no ID or name")
		}
		if _, exists := known[d.ID]; exists {
			return nil, fmt.Errorf("duplicate device ID %q returned by Nautobot", d.ID)
		}
		switch {
		case hasRole(d, p.params.DeviceRole):
			inv.hosts[d.ID] = d
		case hasRole(d, p.params.SwitchRoles):
			if previousID, exists := switchNames[d.Name]; exists {
				return nil, fmt.Errorf("duplicate switch name %q for device IDs %q and %q returned by Nautobot", d.Name, previousID, d.ID)
			}
			switchNames[d.Name] = d.ID
			inv.switches[d.ID] = d
		default:
			continue
		}
		known[d.ID] = d
	}
	for _, d := range known {
		for _, iface := range d.Interfaces {
			if iface.Peer == nil || iface.Peer.Device == nil {
				continue
			}
			peer := *iface.Peer.Device
			if _, exists := known[peer.ID]; !exists || peer.ID == d.ID || p.excluded(peer) {
				continue
			}
			_, fromHost := inv.hosts[d.ID]
			_, toHost := inv.hosts[peer.ID]
			if fromHost && toHost {
				continue
			}
			for _, edge := range [][2]string{{d.ID, peer.ID}, {peer.ID, d.ID}} {
				if inv.links[edge[0]] == nil {
					inv.links[edge[0]] = map[string]bool{}
				}
				inv.links[edge[0]][edge[1]] = true
			}
		}
	}
	return inv, nil
}

// switchDepths computes distance from every inventoried compute endpoint, not
// just requested nodes, so requesting a subset never changes tier assignments.
func (inv *inventory) switchDepths() (map[string]int, [][]string) {
	depth := map[string]int{}
	layers := [][]string{}
	queue := []string{}
	for host := range inv.hosts {
		for sw := range inv.links[host] {
			if _, seen := depth[sw]; !seen {
				depth[sw] = 0
				queue = append(queue, sw)
			}
		}
	}
	for offset := 0; offset < len(queue); offset++ {
		id := queue[offset]
		d := depth[id]
		if len(layers) <= d {
			layers = append(layers, nil)
		}
		layers[d] = append(layers[d], id)
		for peer := range inv.links[id] {
			if _, isSwitch := inv.switches[peer]; !isSwitch {
				continue
			}
			if _, seen := depth[peer]; !seen {
				depth[peer] = d + 1
				queue = append(queue, peer)
			}
		}
	}
	for _, layer := range layers {
		sort.Strings(layer)
	}
	return depth, layers
}

// hierarchy reduces a layered fabric to a tree by merging same-tier switches
// with any shared child. Transitive overlap is merged too: every child must have
// exactly one parent in the scheduler tree. The lowest UUID names each group.
// Lateral (same-tier) links do not define parents and are ignored.
func (inv *inventory) hierarchy() (map[string]string, map[string]string) {
	depth, layers := inv.switchDepths()
	groups, parents := map[string]string{}, map[string]string{}
	for host := range inv.hosts {
		groups[host] = host
	}
	for level, layer := range layers {
		representative := map[string]string{}
		for _, id := range layer {
			representative[id] = id
		}
		var find func(string) string
		find = func(id string) string {
			if representative[id] != id {
				representative[id] = find(representative[id])
			}
			return representative[id]
		}
		children := map[string]string{}
		for _, id := range layer {
			for peer := range inv.links[id] {
				if _, host := inv.hosts[peer]; !host && depth[peer] != level-1 {
					continue
				}
				if _, host := inv.hosts[peer]; host && level != 0 {
					continue
				}
				child := groups[peer]
				if previous, ok := children[child]; ok {
					a, b := find(id), find(previous)
					if a > b {
						a, b = b, a
					}
					representative[b] = a
				} else {
					children[child] = id
				}
			}
		}
		for _, id := range layer {
			groups[id] = find(id)
		}
		for child, sw := range children {
			parents[child] = find(sw)
		}
	}
	return groups, parents
}

func (p *Provider) buildTopology(devices []device, requested []topology.ComputeInstances) (*topology.ClusterTopology, error) {
	inv, err := p.inventory(devices)
	if err != nil {
		return nil, err
	}
	_, parents := inv.hierarchy()
	// Accept UUID, name, or serial, but never guess when an alias is ambiguous.
	aliases := map[string]string{}
	for id, host := range inv.hosts {
		for _, alias := range []string{id, host.Name, host.Serial} {
			if alias == "" {
				continue
			}
			if previous, exists := aliases[alias]; exists && previous != id {
				aliases[alias] = ""
			} else if !exists {
				aliases[alias] = id
			}
		}
	}
	cluster := topology.NewClusterTopology()
	seen := map[string]bool{}
	for _, region := range requested {
		for instanceID := range region.Instances {
			if seen[instanceID] {
				continue
			}
			seen[instanceID] = true
			hostID := aliases[instanceID]
			if hostID == "" {
				continue
			}
			ids := []string{}
			for parent := parents[hostID]; parent != ""; parent = parents[parent] {
				ids = append(ids, parent)
			}
			if len(ids) == 0 {
				continue
			}
			tiers := topology.ClosestFirstFabricTiers(ids...)
			for index, id := range ids {
				tiers[index].Name = inv.switches[id].Name
			}
			cluster.Append(&topology.InstanceTopology{
				InstanceID: instanceID, FabricTiers: tiers,
				Instance: &topology.Instance{ID: instanceID},
			})
		}
	}
	return cluster, nil
}
