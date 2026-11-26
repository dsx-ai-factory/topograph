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

package http

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"k8s.io/klog/v2"

	"github.com/dsx-ai-factory/topograph/internal/httperr"
	"github.com/dsx-ai-factory/topograph/pkg/topology"
)

// maxListedMissing bounds the node names included in logs and errors.
const maxListedMissing = 10

type requestedInstance struct {
	node   string
	region string
}

type requestedSet struct {
	byInstance map[string]requestedInstance
}

// newRequestedSet validates the requested instance-to-node mapping: instance
// IDs must be nonempty and unique across regions, and each node name must map
// to only one instance.
func newRequestedSet(cis []topology.ComputeInstances) (*requestedSet, *httperr.Error) {
	set := &requestedSet{byInstance: make(map[string]requestedInstance)}
	nodes := make(map[string]string)
	for _, ci := range cis {
		for id, node := range ci.Instances {
			switch {
			case strings.TrimSpace(id) == "":
				return nil, badRequest("requested instance IDs must not be empty")
			case id == topology.NoTopology:
				return nil, badRequest("requested instance ID %q is reserved", id)
			case strings.TrimSpace(node) == "":
				return nil, badRequest("requested instance %q has an empty node name", id)
			}
			if _, dup := set.byInstance[id]; dup {
				return nil, badRequest("requested instance ID %q appears in more than one region", id)
			}
			if other, dup := nodes[node]; dup {
				return nil, badRequest("node %q is mapped to both instance %q and %q", node, other, id)
			}
			set.byInstance[id] = requestedInstance{node: node, region: ci.Region}
			nodes[node] = id
		}
	}
	return set, nil
}

// match validates matched records together and appends them to cluster.
// Unrelated inventory is ignored after its identity has been validated.
func (s *requestedSet) match(records []*record, cluster *topology.ClusterTopology, missingNodes string) *httperr.Error {
	matched := make(map[string]*record)
	for _, rec := range records {
		req, ok := s.byInstance[rec.identity]
		if !ok {
			continue
		}
		if prev, dup := matched[rec.identity]; dup {
			return badGateway("instance %q appears more than once in the response (page %d item %d and page %d item %d)",
				rec.identity, prev.page, prev.item, rec.page, rec.item)
		}
		if rec.err != "" {
			return badGateway("%s", rec.err)
		}
		if rec.hasRegion && rec.region != req.region {
			return badGateway("instance %q: response region %q does not match requested region %q", rec.identity, rec.region, req.region)
		}
		if len(rec.networkPath) == 0 && rec.acceleratorDomain == "" {
			return badGateway("instance %q has neither a network path nor an accelerator domain", rec.identity)
		}
		matched[rec.identity] = rec
	}

	if err := s.validateTopology(matched); err != nil {
		return err
	}

	if missing := s.missing(matched); len(missing) != 0 {
		listed := missing
		if len(listed) > maxListedMissing {
			listed = listed[:maxListedMissing]
		}
		summary := fmt.Sprintf("%d requested node(s) have no topology record: %s", len(missing), strings.Join(listed, ", "))
		if len(missing) > len(listed) {
			summary += ", ..."
		}
		if missingNodes == missingNodesError {
			return badGateway("%s", summary)
		}
		klog.Warningf("http provider: %s", summary)
	}

	// Deterministic order keeps logs and graph construction stable.
	ids := make([]string, 0, len(matched))
	for id := range matched {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		rec := matched[id]
		cluster.Append(&topology.InstanceTopology{
			InstanceID:   id,
			FabricTiers:  topology.ClosestFirstFabricTiers(rec.networkPath...),
			XclrDomainID: rec.acceleratorDomain,
		})
	}
	return nil
}

// validateTopology enforces that switch and accelerator domain IDs identify
// one resource across the matched inventory.
func (s *requestedSet) validateTopology(matched map[string]*record) *httperr.Error {
	type switchInfo struct {
		parent   string
		region   string
		instance string
	}
	switches := make(map[string]switchInfo)
	domainRegion := make(map[string]string)

	ids := make([]string, 0, len(matched))
	for id := range matched {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	for _, id := range ids {
		rec := matched[id]
		region := s.byInstance[id].region
		path := rec.networkPath
		for i, sw := range path {
			switch {
			case sw == topology.NoTopology:
				return badGateway("instance %q: switch ID %q is reserved", id, sw)
			case slices.Contains(path[:i], sw):
				return badGateway("instance %q: switch %q appears more than once in its network path", id, sw)
			}
			if _, isInstance := s.byInstance[sw]; isInstance {
				return badGateway("instance %q: switch ID %q is also a requested instance ID", id, sw)
			}
			parent := ""
			if i+1 < len(path) {
				parent = path[i+1]
			}
			prev, seen := switches[sw]
			if !seen {
				switches[sw] = switchInfo{parent: parent, region: region, instance: id}
				continue
			}
			if prev.region != region {
				return badGateway("switch %q is used in regions %q and %q", sw, prev.region, region)
			}
			if prev.parent != parent {
				return badGateway("switch %q has conflicting paths toward the fabric root (instances %q and %q)", sw, prev.instance, id)
			}
		}
		if d := rec.acceleratorDomain; d != "" {
			if prev, seen := domainRegion[d]; seen && prev != region {
				return badGateway("accelerator domain %q is used in regions %q and %q", d, prev, region)
			}
			domainRegion[d] = region
		}
	}
	return nil
}

func (s *requestedSet) missing(matched map[string]*record) []string {
	var nodes []string
	for id, req := range s.byInstance {
		if _, ok := matched[id]; !ok {
			nodes = append(nodes, req.node)
		}
	}
	sort.Strings(nodes)
	return nodes
}
