/*
 * Copyright 2026 NVIDIA CORPORATION
 * SPDX-License-Identifier: Apache-2.0
 */

package translate

import (
	"fmt"
	"maps"
	"slices"
	"sort"

	"github.com/dsx-ai-factory/topograph/pkg/topology"
	"k8s.io/klog/v2"
)

// blockTreeNode is implemented by host, base, and aggregate block nodes.
type blockTreeNode interface {
	blockTreeNode()
	levelIdentifier() string
}

// hostNode is the lowermost tree level: a host slot or an empty placeholder (host == nil).
type hostNode struct {
	host *topology.HostInfo
}

func (*hostNode) blockTreeNode() {}
func (n *hostNode) levelIdentifier() string {
	if n.host == nil {
		return ""
	}
	return n.host.HostName
}

// baseBlockNode is the Slurm base block level. It always holds exactly baseBlockSize
// host nodes; missing positions or hosts are nil-host placeholders.
type baseBlockNode struct {
	id        string
	domain    string // primary domain ID, pre-computed from id at construction
	leaves    []*hostNode
	nodeCount int // live host count (leaves with non-empty HostName)
}

func (*baseBlockNode) blockTreeNode() {}

func (n *baseBlockNode) levelIdentifier() string { return n.domain }

// aggregateBlockNode groups base blocks or other aggregates. A domain with
// multiple base blocks is represented as an aggregate of baseBlockNode children.
type aggregateBlockNode struct {
	id        string
	children  []blockTreeNode
	nodeCount int // sum of nodeCount across all children
}

func (*aggregateBlockNode) blockTreeNode()            {}
func (n *aggregateBlockNode) levelIdentifier() string { return n.id }

// splitIntoBaseBlocks splits a sorted host list into one or more base blocks of at
// most baseBlockSize leaves each. Overflow blocks get a "#N" suffix on the ID.
func splitIntoBaseBlocks(id string, hosts []*topology.HostInfo, baseBlockSize int) []*baseBlockNode {
	blocks := make([]*baseBlockNode, 0, (len(hosts)+baseBlockSize-1)/baseBlockSize)
	for start := 0; start < len(hosts); start += baseBlockSize {
		end := start + baseBlockSize
		if end > len(hosts) {
			end = len(hosts)
		}
		blockID := id
		if len(blocks) > 0 {
			blockID = fmt.Sprintf("%s#%d", id, len(blocks)+1)
		}
		blocks = append(blocks, newBaseBlock(blockID, hosts[start:end], baseBlockSize))
	}
	return blocks
}

// hostsSorted returns hosts in deterministic alphabetical order so that block
// packing is reproducible across runs.
func hostsSorted(hosts map[string]*topology.HostInfo) []*topology.HostInfo {
	list := make([]*topology.HostInfo, 0, len(hosts))
	for _, h := range hosts {
		list = append(list, h)
	}
	sortHostsByName(list)
	return list
}

// collectBaseBlockSlots returns all base blocks in the tree via a left-to-right DFS.
func collectBaseBlockSlots(tree *aggregateBlockNode) []*baseBlockNode {
	var slots []*baseBlockNode
	var walk func(blockTreeNode)
	walk = func(n blockTreeNode) {
		switch c := n.(type) {
		case *baseBlockNode:
			slots = append(slots, c)
		case *aggregateBlockNode:
			for _, ch := range c.children {
				walk(ch)
			}
		}
	}
	walk(tree)
	return slots
}

// isEmptyBlock reports whether a block carries neither a name nor any nodes.
// A block with a name but no nodes is a valid placeholder — the domain is
// identified but no live hosts were assigned — and is not considered empty.
func isEmptyBlock(b *blockInfo) bool {
	return b == nil || (len(b.name) == 0 && len(b.nodes) == 0)
}

// baseBlockToBlockInfo resolves a base block to a blockInfo using a priority fallback
// chain, because not all blocks have live hosts attached to their leaves:
//  1. Host names directly in leaves (live hosts — normal case)
//  2. Domain IDs from leaves → byName lookup (placeholder hosts: Domain set, HostName empty)
//  3. Domain ID as display name with no nodes (domain known, host list missing entirely)
//  4. Empty blockInfo (tree slot was never filled)
func baseBlockToBlockInfo(bb *baseBlockNode, byName map[string]*blockInfo, seq int) *blockInfo {
	id := fmt.Sprintf("block%03d", seq)
	domainID := bb.levelIdentifier()
	nodes := hostNamesFromLeaves(bb.leaves)
	if len(nodes) > 0 {
		return &blockInfo{id: id, name: blockDisplayName(bb.id, domainID), nodes: nodes}
	}
	for _, domain := range domainIDsFromLeaves(bb.leaves) {
		if b := byName[domain]; b != nil {
			return &blockInfo{
				id:    id,
				name:  blockDisplayName(bb.id, domain),
				nodes: append([]string(nil), b.nodes...),
			}
		}
	}
	if domainID != "" {
		return &blockInfo{id: id, name: blockDisplayName(bb.id, domainID)}
	}
	return &blockInfo{id: id}
}

func blockDisplayName(blockID, primarydomain string) string {
	if primarydomain != "" {
		return primarydomain
	}
	return blockID
}

// domainIDsFromLeaves collects unique domainID values from leaf hosts.
// Sorted for determinism; used as a fallback key set in baseBlockToBlockInfo.
func domainIDsFromLeaves(leaves []*hostNode) []string {
	seen := make(map[string]struct{})
	var ids []string
	for _, leaf := range leaves {
		if leaf.host == nil || leaf.host.Domain == "" {
			continue
		}
		if _, ok := seen[leaf.host.Domain]; ok {
			continue
		}
		seen[leaf.host.Domain] = struct{}{}
		ids = append(ids, leaf.host.Domain)
	}
	sort.Strings(ids)
	return ids
}

func hostNamesFromLeaves(leaves []*hostNode) []string {
	nodes := make([]string, 0, len(leaves))
	for _, leaf := range leaves {
		if leaf.host == nil || leaf.host.HostName == "" {
			continue
		}
		nodes = append(nodes, leaf.host.HostName)
	}
	return nodes
}

// extractDomainID returns the primary domain ID from a possibly compound block ID.
// It strips everything from the first compound separator onward:
//
//	"acc-a+acc-b" → "acc-a"   (merged block; separator produced by combinedBlockID)
//	"acc/d0"      → "acc"     (domain-qualified path)
//	"acc#2"       → "acc"     (overflow block produced by splitIntoBaseBlocks)
func extractDomainID(id string) string {
	for i, r := range id {
		if r == '/' || r == '#' || r == '+' {
			return id[:i]
		}
	}
	return id
}

// newBaseBlock builds a baseBlockNode from a pre-sorted host list, filling slots
// left-to-right. Slots beyond the provided hosts remain empty placeholders.
func newBaseBlock(id string, hosts []*topology.HostInfo, baseBlockSize int) *baseBlockNode {
	leaves := make([]*hostNode, baseBlockSize)
	for i := range leaves {
		leaves[i] = &hostNode{}
	}
	nodeCount := 0
	for i, h := range hosts {
		if i >= baseBlockSize {
			break
		}
		leaves[i] = &hostNode{host: h}
		if h.HostName != "" {
			nodeCount++
		}
	}
	return &baseBlockNode{id: id, domain: extractDomainID(id), leaves: leaves, nodeCount: nodeCount}
}

func newEmptyBaseBlock(baseBlockSize int) *baseBlockNode {
	if baseBlockSize <= 0 {
		return &baseBlockNode{}
	}
	leaves := make([]*hostNode, baseBlockSize)
	for i := range leaves {
		leaves[i] = &hostNode{}
	}
	return &baseBlockNode{leaves: leaves}
}

func buildBlockTree(domains topology.DomainMap, blockSizes []int) *aggregateBlockNode {
	if len(blockSizes) == 0 || blockSizes[0] <= 0 {
		klog.V(4).Infof("buildBlockTree: skipping — invalid blockSizes %v", blockSizes)
		return nil
	}
	klog.V(4).Infof("buildBlockTree: building tree for %d domain(s) with blockSizes=%v", len(domains), blockSizes)
	root := domains.GetDomainTree()
	result := toRootAggregate(root, blockSizes)
	if result == nil {
		klog.V(4).Infof("buildBlockTree: result is empty (no domains or no hosts)")
	} else {
		klog.V(4).Infof("buildBlockTree: done, root nodeCount=%d", result.nodeCount)
	}
	return result
}

// newEmptyChildAggregate returns an aggregate whose slot capacity matches a
// real child (capacity/baseBlockSize empty base blocks, nodeCount = capacity).
func newEmptyChildAggregate(capacity, baseBlockSize int) *aggregateBlockNode {
	agg := &aggregateBlockNode{}
	for i := 0; i < capacity/baseBlockSize; i++ {
		agg.children = append(agg.children, newEmptyBaseBlock(baseBlockSize))
		agg.nodeCount += baseBlockSize
	}
	return agg
}

// isSingleLevelDomainTree reports whether every accelerator domain under root
// stores hosts directly rather than through sub-domain children. This is the
// legacy DomainMap shape and must retain the pre-dual-level block sizing rules.
func isSingleLevelDomainTree(root *topology.BlockVertex) bool {
	if root == nil || len(root.Children) == 0 {
		return false
	}
	for _, domain := range root.Children {
		if domain == nil || domain.Hosts == nil || len(domain.Children) != 0 {
			return false
		}
	}
	return true
}

// toSingleLevelDomainAggregate preserves the block allocation used before
// dual-level domains were introduced.
//
// With a single BlockSizes entry there is no aggregate tier, so a domain uses
// only ceil(ActualNodeCount/baseBlockSize) base blocks. With multiple entries,
// every domain receives the same power-of-two slot sized from the largest
// sibling domain. The latter reservation keeps domain positions stable when
// differently sized single-level domains share a topology.
func toSingleLevelDomainAggregate(src *topology.BlockVertex, maxSiblingNodes int, blockSizes []int) *aggregateBlockNode {
	if src == nil || len(blockSizes) == 0 {
		return nil
	}

	baseBlockSize := blockSizes[0]
	var numBaseBlocks int
	if len(blockSizes) == 1 {
		numBaseBlocks = (src.ActualNodeCount + baseBlockSize - 1) / baseBlockSize
	} else {
		numBaseBlocks = aggregateSlotCapacity(maxSiblingNodes, baseBlockSize) / baseBlockSize
	}
	numBaseBlocks = max(numBaseBlocks, 1)

	return packHostsIntoAggregate(src.ID, hostsSorted(src.Hosts), numBaseBlocks, baseBlockSize)
}

// toDomainAggregate converts a BlockVertex into a flat aggregateBlockNode whose
// slot capacity is determined by the maximum actualNodeCount among the vertex's
// siblings (maxSiblingNodes).
//
// Slot capacity differs by node type:
//
//	Leaf (src.Hosts != nil):
//	  Normal  (lastBlockSize ≥ maxSiblingNodes): numBaseBlocks = aggregateSlotCapacity(maxSiblingNodes, blockSizes[0]) / blockSizes[0]
//	  Oversized (lastBlockSize < maxSiblingNodes): numBaseBlocks = ceil(ActualNodeCount / blockSizes[0])
//	  (Power-of-2 rounding is skipped for oversized leaves to avoid spurious empty blocks.)
//
//	Interior (src.Hosts == nil):
//	  nodeCount    = aggregateSlotCapacity(min(lastBlockSize, maxSiblingNodes), blockSizes[0])
//	  numBaseBlocks = nodeCount / blockSizes[0]
//
//	  min() caps the slot at lastBlockSize when maxSiblingNodes exceeds it (under-configured
//	  blockSizes), avoiding inflated slots. When maxSiblingNodes ≤ lastBlockSize the result
//	  equals aggregateSlotCapacity(maxSiblingNodes, blockSizes[0]).
//
//	  numBaseBlocks is a minimum: if recurseChildrenIntoAggregate produces more slots due
//	  to many small sub-domains, the count is rounded to the nearest natural blockSizes
//	  level to guarantee the domain ends on a valid aggregate boundary (see
//	  recurseChildrenIntoAggregate).
//
// Base blocks are filled by one of two strategies:
//
//  1. Leaf (src.Hosts != nil): split hosts with splitIntoBaseBlocks and pad
//     to numBaseBlocks with empty base blocks (using the leaf slot formula above).
//
//  2. Interior: call toDomainAggregate recursively per child using src.MaxChildNodeCount
//     (the parent's max child node count) as maxSiblingNodes so all siblings are sized
//     uniformly, flatten the resulting base blocks into this aggregate, and pad
//     to numBaseBlocks.
func toDomainAggregate(src *topology.BlockVertex, maxSiblingNodes int, blockSizes []int) *aggregateBlockNode {
	if src == nil || len(blockSizes) == 0 {
		return nil
	}
	baseBlockSize := blockSizes[0]
	lastBlockSize := blockSizes[len(blockSizes)-1]

	// Strategy 1: leaf vertex — split hosts directly into base blocks.
	// Normal case (lastBlockSize >= maxSiblingNodes): all sibling domains get the same
	// power-of-2 slot width via aggregateSlotCapacity(maxSiblingNodes).
	// Oversized case (lastBlockSize < maxSiblingNodes): use ceil(ActualNodeCount/baseBlockSize)
	// to avoid inflating the slot with spurious empty placeholder blocks.
	if src.Hosts != nil {
		var numBaseBlocks int
		if lastBlockSize < maxSiblingNodes {
			numBaseBlocks = (src.ActualNodeCount + baseBlockSize - 1) / baseBlockSize
		} else {
			numBaseBlocks = aggregateSlotCapacity(maxSiblingNodes, baseBlockSize) / baseBlockSize
		}
		numBaseBlocks = max(numBaseBlocks, 1)
		return packHostsIntoAggregate(src.ID, hostsSorted(src.Hosts), numBaseBlocks, baseBlockSize)
	}

	// Strategy 2: interior vertex — recurse into each sub-domain child.
	// min(lastBlockSize, maxSiblingNodes) gives each sibling a uniform slot sized to
	// the largest sibling, capped at lastBlockSize so under-configured blockSizes do
	// not inflate every domain's slot beyond the configured hierarchy boundary.
	nodeCount := aggregateSlotCapacity(min(lastBlockSize, maxSiblingNodes), baseBlockSize)
	numBaseBlocks := max(nodeCount/baseBlockSize, 1)
	return recurseChildrenIntoAggregate(src, numBaseBlocks, blockSizes)
}

// toRootAggregate builds the root-level aggregateBlockNode by calling toDomainAggregate on
// each domain child of root and appending empty domain aggregates until the total
// nodeCount reaches a positive multiple of blockSizes[last].
//
// Entirely single-level trees use toSingleLevelDomainAggregate to preserve the
// pre-dual-level allocation rules. Dual-level and mixed trees use
// toDomainAggregate. Empty root-padding aggregates use the GCD of the resulting
// child capacities so padding can reach a valid root boundary even when child
// capacities differ.
func toRootAggregate(root *topology.BlockVertex, blockSizes []int) *aggregateBlockNode {
	if root == nil || len(blockSizes) == 0 || blockSizes[0] <= 0 {
		return nil
	}
	baseBlockSize := blockSizes[0]
	rootDesired := blockSizes[len(blockSizes)-1]
	singleLevel := isSingleLevelDomainTree(root)

	result := &aggregateBlockNode{id: root.ID}
	childCapacity := 0
	for _, name := range slices.Sorted(maps.Keys(root.Children)) {
		domain := root.ChildAt(name)
		var domainAgg *aggregateBlockNode
		if singleLevel {
			domainAgg = toSingleLevelDomainAggregate(domain, root.MaxChildNodeCount, blockSizes)
		} else {
			domainAgg = toDomainAggregate(domain, root.MaxChildNodeCount, blockSizes)
		}
		if domainAgg == nil {
			continue
		}
		result.children = append(result.children, domainAgg)
		result.nodeCount += domainAgg.nodeCount
		if childCapacity == 0 {
			childCapacity = domainAgg.nodeCount
		} else {
			childCapacity = blockTreeGCD(childCapacity, domainAgg.nodeCount)
		}
	}

	// Pad with empty domain aggregates to reach the nearest positive multiple of
	// blockSizes[last]. The LCM-based step ensures correctness when childCapacity
	// does not evenly divide rootDesired (non-power-of-2-aligned block size pairs).
	if result.nodeCount > 0 && childCapacity > 0 && result.nodeCount%rootDesired != 0 {
		g := blockTreeGCD(childCapacity, rootDesired)
		step := childCapacity / g * rootDesired // = lcm(childCapacity, rootDesired)
		targetCount := ((result.nodeCount + step - 1) / step) * step
		for result.nodeCount < targetCount {
			result.children = append(result.children, newEmptyChildAggregate(childCapacity, baseBlockSize))
			result.nodeCount += childCapacity
		}
		klog.V(4).Infof("toRootTree: padded to nodeCount=%d (rootDesired=%d)", result.nodeCount, rootDesired)
	}

	return result
}

// aggregateSlotCapacity returns the smallest power-of-2 multiple of baseBlockSize
// that is >= maxSiblingNodes, which is the slot nodeCount for a vertex sized by its
// sibling maximum. When maxSiblingNodes is zero or negative it falls back to one slot.
func aggregateSlotCapacity(maxSiblingNodes, baseBlockSize int) int {
	capacity := baseBlockSize
	for capacity < maxSiblingNodes {
		capacity *= 2
	}
	return capacity
}

// packHostsIntoAggregate splits sortedHosts into baseBlockSize-wide base blocks,
// pads the list to numBaseBlocks with empty base blocks, and wraps the result in an
// aggregateBlockNode. It is the leaf-packing routine used by strategy 1.
func packHostsIntoAggregate(id string, sortedHosts []*topology.HostInfo, numBaseBlocks, baseBlockSize int) *aggregateBlockNode {
	blocks := splitIntoBaseBlocks(id, sortedHosts, baseBlockSize)
	for len(blocks) < numBaseBlocks {
		blocks = append(blocks, newEmptyBaseBlock(baseBlockSize))
	}
	agg := &aggregateBlockNode{id: id}
	for _, b := range blocks {
		agg.children = append(agg.children, b)
		agg.nodeCount += baseBlockSize
	}
	return agg
}

// recurseChildrenIntoAggregate implements strategy 2: it calls toDomainAggregate on
// each child of src using src.MaxChildNodeCount as the sibling-max, collects all
// base blocks produced by those recursive calls (via collectBaseBlockSlots), and pads
// the flat result to at least numBaseBlocks with empty base blocks.
//
// numBaseBlocks is a lower bound only. When many small sub-domains collectively produce
// more base blocks than numBaseBlocks and blockSizes has more than one entry, the excess
// is rounded up to the nearest aggregate boundary so the domain slot does not end
// mid-aggregate. The boundary is the smallest blockSizes[k] (k ≥ 1) that can hold the
// actual node count; if none fits, aggregateSlotCapacity is used as a fallback. With a
// single blockSizes entry there is no higher aggregate level, so no rounding is applied.
func recurseChildrenIntoAggregate(src *topology.BlockVertex, numBaseBlocks int, blockSizes []int) *aggregateBlockNode {
	baseBlockSize := blockSizes[0]
	childMax := src.MaxChildNodeCount
	agg := &aggregateBlockNode{id: src.ID}
	for _, name := range slices.Sorted(maps.Keys(src.Children)) {
		child := src.ChildAt(name)
		childAgg := toDomainAggregate(child, childMax, blockSizes)
		if childAgg == nil {
			continue
		}
		for _, bb := range collectBaseBlockSlots(childAgg) {
			agg.children = append(agg.children, bb)
			agg.nodeCount += baseBlockSize
		}
	}
	// If children overflowed the pre-computed slot and there are multiple blockSizes
	// levels, round up to the nearest aggregate boundary to prevent the next domain
	// from starting mid-aggregate. Find the smallest blockSizes[k] (k>=1) that fits
	// the actual node count; if none fits, use aggregateSlotCapacity as fallback.
	// With a single blockSizes entry there is no higher aggregate level, so no rounding
	// is needed.
	if actual := agg.nodeCount / baseBlockSize; actual > numBaseBlocks && len(blockSizes) >= 2 {
		actualNodes := actual * baseBlockSize
		alignSize := actualNodes
		for _, bs := range blockSizes[1:] {
			if bs >= actualNodes {
				alignSize = bs
				break
			}
		}
		aggregateWidth := aggregateSlotCapacity(alignSize, baseBlockSize) / baseBlockSize
		numBaseBlocks = ((actual + aggregateWidth - 1) / aggregateWidth) * aggregateWidth
	}
	for agg.nodeCount/baseBlockSize < numBaseBlocks {
		agg.children = append(agg.children, newEmptyBaseBlock(baseBlockSize))
		agg.nodeCount += baseBlockSize
	}
	return agg
}

// blockTreeGCD returns the greatest common divisor of two non-negative integers.
// Returns b when a is zero (standard GCD identity), so callers receive a safe
// non-zero result when only one argument is zero.
func blockTreeGCD(a, b int) int {
	if a == 0 {
		return b
	}
	for b != 0 {
		a, b = b, a%b
	}
	return a
}

// sortHostsByName sorts hosts alphabetically by HostName for deterministic packing.
func sortHostsByName(hosts []*topology.HostInfo) {
	sort.Slice(hosts, func(i, j int) bool {
		return hosts[i].HostName < hosts[j].HostName
	})
}
