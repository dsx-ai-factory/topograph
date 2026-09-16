/*
 * Copyright 2026 NVIDIA CORPORATION
 * SPDX-License-Identifier: Apache-2.0
 */

package translate

import (
	"github.com/dsx-ai-factory/topograph/pkg/topology"
	"k8s.io/klog/v2"
)

// complementBlocks builds a block tree shaped by BlockSizes, packs domain hosts into
// it, and returns the flat block list derived from low-level tree nodes.
//
// Only domains for accelerators present in blocks are used so per-partition YAML
// complementing is not masked by domains owned by other partitions in nt.domains.
//
// buildBlockTree calls GetDomainTree to produce a flat one- or two-level
// BlockVertex tree (one level when no host carries a SubDomain, two levels
// otherwise). GetDomainTree computes ActualNodeCount and MaxChildNodeCount inline
// as it builds the tree. buildBlockTree then converts the result into an
// aggregateBlockNode tree with empty placeholder slots for absent groups or
// domains. The flat base-block slot list is then numbered sequentially.
//
// When BlockSizes is not configured, DomainMap.InferTwoLevelBlockSizes is tried
// first: for two-level maps (SubDomain present) it yields [maxSubDomainSize,
// aggregateSize] where aggregateSize is the smallest power-of-2 multiple of
// maxSubDomainSize that is >= maxDomainSize. Single-level maps return nil and
// fall through to getBlockSizes, which derives a doubling sequence from the
// live block list.
func (nt *NetworkTopology) complementBlocks(blocks []*blockInfo, blockSizes []int) ([]*blockInfo, []int) {
	if nt.domains == nil {
		return blocks, blockSizes
	}

	domains := domainsForBlocks(nt.domains, blocks)
	if len(domains) == 0 {
		return blocks, blockSizes
	}

	if len(blockSizes) < 1 {
		blockSizes = domains.InferTwoLevelBlockSizes()
		if len(blockSizes) == 0 {
			return blocks, blockSizes
		}
	}

	klog.Infof("Complementing %d blocks with %d domains into tree shape %v", len(blocks), len(domains), blockSizes)
	byName := blocksByName(blocks)

	actualTree := buildBlockTree(domains, blockSizes)
	if actualTree == nil {
		return blocks, blockSizes
	}
	allSlots := collectBaseBlockSlots(actualTree)

	out := make([]*blockInfo, 0, len(allSlots))
	for i, bb := range allSlots {
		out = append(out, baseBlockToBlockInfo(bb, byName, i+1))
	}
	return out, blockSizes
}

// domainsForBlocks returns a subset of the cluster domain map containing only the
// hosts that belong to the given partition-local blocks. For each block it intersects
// the global domain with the block's own node list, so that nodes owned by another
// partition in the same accelerator domain are never included.
//
// Contract: b.name must match a DomainMap key (the accelerator domain name, e.g.
// "domain-01"). In the dual-level case the outer DomainMap is keyed by accelerator
// domain so this holds; a provider that uses SubDomain names as block names will
// silently receive an empty domain map here and hosts will be dropped.
func domainsForBlocks(all topology.DomainMap, blocks []*blockInfo) topology.DomainMap {
	if all == nil {
		return nil
	}
	local := topology.NewDomainMap()
	for _, b := range blocks {
		if b == nil || b.name == "" {
			continue
		}
		hosts, ok := all[b.name]
		if !ok {
			continue
		}
		// Restrict to nodes the partition actually owns; a domain may span multiple
		// partitions and the global map holds all of them.
		partitionNodes := make(map[string]struct{}, len(b.nodes))
		for _, n := range b.nodes {
			partitionNodes[n] = struct{}{}
		}
		for _, hi := range hosts {
			if _, owned := partitionNodes[hi.HostName]; !owned {
				continue
			}
			copy := *hi
			local.AddHostInfo(&copy)
		}
	}
	return local
}

// blocksByName builds an accelerator-ID → blockInfo index used by baseBlockToBlockInfo
// to look up node lists when a block's leaves carry only an accelerator reference.
// Nil entries and blocks without a name are skipped, matching the guard in domainsForBlocks.
func blocksByName(blocks []*blockInfo) map[string]*blockInfo {
	m := make(map[string]*blockInfo, len(blocks))
	for _, b := range blocks {
		if b == nil || b.name == "" {
			continue
		}
		m[b.name] = b
	}
	return m
}
