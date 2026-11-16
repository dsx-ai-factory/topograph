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

package akamai

import (
	"context"
	"crypto/sha256"
	"encoding/base32"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"

	"github.com/dsx-ai-factory/topograph/internal/httperr"
	"github.com/dsx-ai-factory/topograph/pkg/topology"
)

const (
	maxTopologyPages    = 1000
	maxTopologySwitches = 100000
	maxTopologyDepth    = 256
	// Count traversal steps and host-path vertices across all regions of a request.
	maxTopologyWork = 1000000
)

type switchInfo struct {
	parents map[string]bool
	hosts   map[string]bool
}

func (p *Provider) regionSwitches(ctx context.Context, region string, size int) ([]Switch, *httperr.Error) {
	var switches []Switch
	pages, results := 0, 0
	for page := 1; ; page++ {
		response, err := p.client.Topology(ctx, region, size, page)
		if err != nil {
			return nil, err
		}
		if response == nil || response.Region != region || response.Data == nil || response.Page != page || response.Pages < 0 || response.Results < 0 {
			return nil, httperr.NewError(http.StatusBadGateway, "invalid Akamai topology response or pagination metadata")
		}
		if response.Pages > maxTopologyPages || response.Results > maxTopologySwitches {
			return nil, httperr.NewError(http.StatusBadGateway, "Akamai topology exceeds pagination limits")
		}
		if page == 1 {
			pages, results = response.Pages, response.Results
		}
		if response.Pages != pages || response.Results != results || (pages < page && (page != 1 || pages != 0 || results != 0)) {
			return nil, httperr.NewError(http.StatusBadGateway, "inconsistent Akamai pagination metadata")
		}
		if len(response.Data) == 0 && page < pages {
			return nil, httperr.NewError(http.StatusBadGateway, "empty Akamai topology page before final page")
		}
		if len(response.Data) > results-len(switches) {
			return nil, httperr.NewError(http.StatusBadGateway, "Akamai topology result count exceeds results")
		}
		switches = append(switches, response.Data...)
		if page >= pages {
			break
		}
	}
	if len(switches) != results {
		return nil, httperr.NewError(http.StatusBadGateway, "Akamai topology result count does not match results")
	}
	return switches, nil
}

// indexSwitches retains repeated switch records and host attachments. Repeated
// records with different uplinks supply multiple parents. Connections describe
// downstream links and are not used to guess additional parents.
func indexSwitches(records []Switch) (map[string]*switchInfo, error) {
	index := map[string]*switchInfo{}
	for _, record := range records {
		if strings.TrimSpace(record.Name) == "" {
			return nil, fmt.Errorf("akamai switch has no name")
		}
		sw := index[record.Name]
		if sw == nil {
			sw = &switchInfo{parents: map[string]bool{}, hosts: map[string]bool{}}
			index[record.Name] = sw
		}
		if record.Uplink != nil {
			if strings.TrimSpace(record.Uplink.Device) == "" {
				return nil, fmt.Errorf("akamai switch %q has an empty uplink device", record.Name)
			}
			sw.parents[record.Uplink.Device] = true
		}
		for _, host := range record.Hosts {
			if strings.TrimSpace(host.UUID) == "" {
				return nil, fmt.Errorf("akamai switch %q has an empty host UUID", record.Name)
			}
			sw.hosts[host.UUID] = true
		}
	}
	return index, nil
}

// walkPaths follows only uplink.device, enumerating each leaf-to-root path.
// The active set detects cycles without rejecting a shared ancestor in a DAG.
func walkPaths(ctx context.Context, index map[string]*switchInfo, name string, path []string, active map[string]bool, remaining *int, visit func([]string) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if *remaining <= 0 || len(path) >= maxTopologyDepth {
		return fmt.Errorf("akamai topology exceeds discovery work or depth limit")
	}
	*remaining -= 1
	if active[name] {
		return fmt.Errorf("cycle in Akamai uplinks at switch %q", name)
	}
	sw := index[name]
	if sw == nil {
		return fmt.Errorf("akamai uplink references unknown switch %q", name)
	}
	active[name] = true
	defer delete(active, name)
	path = append(path, name)
	if len(sw.parents) == 0 {
		return visit(path)
	}
	for _, parent := range slices.Sorted(maps.Keys(sw.parents)) {
		if err := walkPaths(ctx, index, parent, path, active, remaining, visit); err != nil {
			return err
		}
	}
	return nil
}

func switchID(region, name string) string {
	// Hash an unambiguous pair into a Kubernetes-label-safe, region-scoped ID.
	sum := sha256.Sum256([]byte(fmt.Sprintf("%d:%s%s", len(region), region, name)))
	return "sw-" + strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(sum[:]))
}

// combineBranches combines trees built by ClusterTopology.ToGraph without
// discarding repeated host IDs. It preserves every attachment for a later merge.
func combineBranches(dst, src *topology.Vertex) {
	for id, child := range src.Vertices {
		if existing := dst.Vertices[id]; existing != nil {
			combineBranches(existing, child)
		} else {
			dst.Vertices[id] = child
		}
	}
}

func groupRequests(instances []topology.ComputeInstances) (map[string]map[string]string, error) {
	regions := map[string]map[string]string{}
	owners := map[string]string{}
	for _, ci := range instances {
		if len(ci.Instances) == 0 {
			continue
		}
		if strings.TrimSpace(ci.Region) == "" {
			return nil, fmt.Errorf("must specify region")
		}
		if regions[ci.Region] == nil {
			regions[ci.Region] = map[string]string{}
		}
		for id, name := range ci.Instances {
			if strings.TrimSpace(id) == "" {
				return nil, fmt.Errorf("akamai host UUID must not be empty")
			}
			if owner, exists := owners[id]; exists && (owner != ci.Region || regions[ci.Region][id] != name) {
				return nil, fmt.Errorf("akamai host UUID has conflicting region or node mappings")
			}
			owners[id] = ci.Region
			regions[ci.Region][id] = name
		}
	}
	return regions, nil
}

func (p *Provider) appendPath(graph *topology.Graph, provider, region string, hosts map[string]string, hostIDs, path []string) {
	tiers := topology.ClosestFirstFabricTiers(path...)
	for i, name := range path {
		tiers[i].ID = switchID(region, name)
		tiers[i].Name = tiers[i].ID
	}
	cluster := topology.NewClusterTopology()
	for _, id := range hostIDs {
		instance := topology.Instance{ID: id}
		if metadata, ok := p.metadata[id]; ok {
			instance = metadata
		}
		cluster.Append(&topology.InstanceTopology{InstanceID: id, FabricTiers: tiers, Instance: &instance})
	}
	branch := cluster.ToGraph(provider, []topology.ComputeInstances{{Region: region, Instances: hosts}}, p.params.TrimTiers, false)
	combineBranches(graph.Tiers, branch.Tiers)
	if graph.Instances == nil {
		graph.Instances = map[string]topology.Instance{}
	}
	// Instance output has one path; retain the first sorted attachment.
	for id, instance := range branch.Instances {
		// Scheduler names are scoped; instance output retains the API switch names.
		instance.NetworkLayers = append([]string{}, path[:max(0, len(path)-p.params.TrimTiers)]...)
		if _, exists := graph.Instances[id]; !exists {
			graph.Instances[id] = instance
		}
	}
}

func (p *Provider) appendRegion(ctx context.Context, graph *topology.Graph, provider, region string, requested map[string]string, index map[string]*switchInfo, remaining *int) (map[string]string, *httperr.Error) {
	absent := maps.Clone(requested)
	for _, leaf := range slices.Sorted(maps.Keys(index)) {
		hosts := map[string]string{}
		for id := range index[leaf].hosts {
			if name, ok := requested[id]; ok {
				hosts[id] = name
			}
		}
		if len(hosts) == 0 {
			continue
		}
		hostIDs := slices.Sorted(maps.Keys(hosts))
		pathErr := walkPaths(ctx, index, leaf, nil, map[string]bool{}, remaining, func(path []string) error {
			if len(hostIDs) > *remaining/(len(path)+1) {
				return fmt.Errorf("akamai topology exceeds discovery work limit")
			}
			*remaining -= len(hostIDs) * (len(path) + 1)
			p.appendPath(graph, provider, region, hosts, hostIDs, path)
			return nil
		})
		if pathErr != nil {
			return nil, httperr.NewError(http.StatusBadGateway, pathErr.Error())
		}
		for id := range hosts {
			delete(absent, id)
		}
	}
	return absent, nil
}

func (p *Provider) generateGraph(ctx context.Context, provider string, pageSize *int, instances []topology.ComputeInstances) (*topology.Graph, *httperr.Error) {
	size := defaultPageSize
	if pageSize != nil {
		if *pageSize <= 0 {
			return nil, httperr.NewError(http.StatusBadRequest, "pageSize must be positive")
		}
		size = *pageSize
	}
	regions, requestErr := groupRequests(instances)
	if requestErr != nil {
		return nil, httperr.NewError(http.StatusBadRequest, requestErr.Error())
	}
	graph := topology.NewClusterTopology().ToGraph(provider, nil, p.params.TrimTiers, false)
	if len(regions) == 0 {
		return graph, nil
	}
	remaining := maxTopologyWork
	missing := []topology.ComputeInstances{}
	for _, region := range slices.Sorted(maps.Keys(regions)) {
		records, err := p.regionSwitches(ctx, region, size)
		if err != nil {
			return nil, err
		}
		index, indexErr := indexSwitches(records)
		if indexErr != nil {
			return nil, httperr.NewError(http.StatusBadGateway, indexErr.Error())
		}
		absent, err := p.appendRegion(ctx, graph, provider, region, regions[region], index, &remaining)
		if err != nil {
			return nil, err
		}
		for id := range absent {
			if graph.Instances == nil {
				graph.Instances = map[string]topology.Instance{}
			}
			instance := p.metadata[id]
			instance.ID = id
			instance.NetworkLayers = []string{}
			graph.Instances[id] = instance
		}
		missing = append(missing, topology.ComputeInstances{Region: region, Instances: absent})
	}
	unknown := topology.NewClusterTopology().ToGraph(provider, missing, 0, false)
	combineBranches(graph.Tiers, unknown.Tiers)
	return graph, nil
}
