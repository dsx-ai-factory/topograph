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

package dsx

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"time"

	"k8s.io/klog/v2"

	"github.com/dsx-ai-factory/topograph/internal/httperr"
	"github.com/dsx-ai-factory/topograph/pkg/topology"
)

// totalGenerationTimeout is the wall-clock budget for one complete topology
// fetch across all pages. It is the primary bound on how long a single
// topology generation can run: even an API that returns an endless stream of
// unique tokens cannot keep generation active beyond this deadline.
//
// At the default page size of 100 on a healthy server (< 1 s/page), 10 min
// comfortably covers clusters up to 60 000 nodes with margin for retries.
// It is a var so tests can lower it without sleeping for minutes.
var totalGenerationTimeout = 10 * time.Minute

// maxPaginationPages is a secondary defence: it terminates runaway pagination
// independently of wall-clock time. The client enforces minPageSize=100, so
// 10 000 pages × 100 entries/page covers 1 000 000 entries — well beyond any
// realistic cluster, even with switch-only pages inflating the count.
// It is a var so tests can lower it without allocating thousands of responses.
var maxPaginationPages = 10_000

func (p *baseProvider) generateInstanceTopology(ctx context.Context, pageSize *int, cis []topology.ComputeInstances) (*topology.ClusterTopology, *httperr.Error) {
	client, err := p.clientFactory()
	if err != nil {
		return nil, httperr.NewError(http.StatusBadGateway, fmt.Sprintf("failed to get client: %v", err))
	}

	// Build want set and nodeIDs once. Deduplicate IDs that appear in more than
	// one ComputeInstances group, and sort for deterministic API requests.
	want := make(map[string]struct{})
	var nodeIDs []string
	for _, ci := range cis {
		for instanceID := range ci.Instances {
			if instanceID == "" {
				klog.Warningf("skipping empty instance ID in ComputeInstances")
				continue
			}
			if _, exists := want[instanceID]; !exists {
				want[instanceID] = struct{}{}
				nodeIDs = append(nodeIDs, instanceID)
			}
		}
	}
	sort.Strings(nodeIDs)

	if len(nodeIDs) == 0 {
		return topology.NewClusterTopology(), nil
	}

	pageSizeVal := 0
	if pageSize != nil {
		pageSizeVal = *pageSize
	}

	// Phase 1: accumulate all switch entries across pages before resolving topology.
	// Building parentOf page-by-page would lose cross-page ancestry (e.g. spine on
	// page 1, leaves on page 2).
	//
	// genCtx provides a single wall-clock deadline for the entire multi-page fetch.
	// This is the primary termination bound: no matter how many unique tokens the
	// API returns, generation cannot run past totalGenerationTimeout.
	//
	// seenTokens and the page counter are secondary defences against token cycles
	// and runaway page counts within the deadline.
	genCtx, cancel := context.WithTimeout(ctx, totalGenerationTimeout)
	defer cancel()

	seenTokens := map[string]struct{}{"": {}}
	var allSwitches []map[string]SwitchAdjacency
	var pageToken string
	for page := 1; ; page++ {
		if page > maxPaginationPages {
			// Deterministic: retrying would hit the same limit.
			return nil, httperr.NewError(http.StatusUnprocessableEntity,
				fmt.Sprintf("DSX API exceeded maximum page limit (%d)", maxPaginationPages))
		}
		response, apiErr := client.GetTopology(genCtx, "", nodeIDs, pageSizeVal, pageToken)
		if apiErr != nil {
			if ctx.Err() != nil {
				// The caller's context was cancelled — report that, not our deadline.
				return nil, httperr.NewError(http.StatusUnprocessableEntity,
					fmt.Sprintf("context cancelled: %v", apiErr))
			}
			if genCtx.Err() != nil {
				// Our own total-generation deadline fired. A retry would restart
				// with a fresh deadline, repeating the full wait — not retryable.
				return nil, httperr.NewError(http.StatusUnprocessableEntity,
					fmt.Sprintf("DSX topology generation deadline exceeded: %v", apiErr))
			}
			return nil, httperr.NewError(http.StatusBadGateway, fmt.Sprintf("API error: %v", apiErr))
		}
		if response == nil {
			return nil, httperr.NewError(http.StatusBadGateway, "DSX API returned nil response without error")
		}
		allSwitches = append(allSwitches, response.Switches...)
		if response.NextPageToken == "" {
			break
		}
		if _, seen := seenTokens[response.NextPageToken]; seen {
			// Deterministic: retrying would encounter the same cycle.
			return nil, httperr.NewError(http.StatusUnprocessableEntity, "DSX API returned a page token cycle")
		}
		seenTokens[response.NextPageToken] = struct{}{}
		pageToken = response.NextPageToken
	}

	// Phase 2: resolve topology from the complete cross-page switch set.
	return buildClusterTopology(allSwitches, want), nil
}

// buildClusterTopology translates the complete ordered switch list into per-instance
// topology records. It must be called with the full set of switches across all pages
// so that cross-page parent-child relationships are resolved correctly.
func buildClusterTopology(switches []map[string]SwitchAdjacency, want map[string]struct{}) *topology.ClusterTopology {
	// sortedEntryKeys returns the switch names in an entry sorted so that
	// multi-key entries (malformed per spec) are processed deterministically.
	sortedEntryKeys := func(entry map[string]SwitchAdjacency) []string {
		names := make([]string, 0, len(entry))
		for k := range entry {
			names = append(names, k)
		}
		sort.Strings(names)
		return names
	}

	// First pass: build the parent map from the complete switch list.
	parentOf := make(map[string]string)
	for _, entry := range switches {
		if len(entry) > 1 {
			klog.Warningf("DSX API returned a SwitchEntry with %d keys; expected 1 (ordering non-deterministic)", len(entry))
		}
		for _, swName := range sortedEntryKeys(entry) {
			for _, child := range entry[swName].Switches {
				parentOf[child] = swName
			}
		}
	}

	// Second pass: emit an InstanceTopology for each node attached to a leaf switch.
	// emitted guards against duplicate node IDs in malformed API responses.
	emitted := make(map[string]struct{})
	topo := topology.NewClusterTopology()
	for _, entry := range switches {
		for _, swName := range sortedEntryKeys(entry) {
			adj := entry[swName]
			for _, n := range adj.Nodes {
				if _, ok := want[n.NodeID]; !ok {
					continue
				}
				if _, dup := emitted[n.NodeID]; dup {
					klog.Warningf("DSX API returned duplicate node_id %q; ignoring", n.NodeID)
					continue
				}
				emitted[n.NodeID] = struct{}{}

				// Walk up the parentOf map to collect all fabric tiers closest-first.
				// The seen set guards against cycles in malformed API responses.
				var tierIDs []string
				seen := make(map[string]struct{})
				for id := swName; id != ""; id = parentOf[id] {
					if _, cycle := seen[id]; cycle {
						klog.Warningf("DSX parentOf map contains a cycle at switch %q; truncating fabric tiers", id)
						break
					}
					seen[id] = struct{}{}
					tierIDs = append(tierIDs, id)
				}

				inst := &topology.InstanceTopology{
					InstanceID:  n.NodeID,
					FabricTiers: topology.ClosestFirstFabricTiers(tierIDs...),
				}
				if n.AcceleratedNetworkID != "" {
					inst.XclrDomainID = n.AcceleratedNetworkID
				}
				klog.V(4).Infof("Adding instance topology %s", inst.String())
				topo.Append(inst)
			}
		}
	}

	return topo
}
