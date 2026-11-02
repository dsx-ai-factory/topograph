// SPDX-FileCopyrightText: Copyright (c) 2024 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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

package topology

import (
	"fmt"
	"maps"
	"sort"
	"strings"

	"k8s.io/klog/v2"
)

type HostInfo struct {
	Domain     string
	InstanceID string
	HostName   string
	SubDomain  string // optional: sub-domain name this host belongs to within its accelerator domain
}

// BlockVertex augments a Vertex with domain-tree-specific metadata. The
// general-purpose Vertex type is kept unmodified; BlockVertex does NOT use the
// inherited Vertex.Vertices field for its own child storage — instead it carries
// a type-safe Children map so the compiler enforces that only *BlockVertex values
// are inserted.
type BlockVertex struct {
	Vertex
	ActualNodeCount   int
	MaxChildNodeCount int                     // maximum ActualNodeCount among this vertex's direct children
	Hosts             map[string]*HostInfo    // non-nil only for leaf vertices
	Children          map[string]*BlockVertex // type-safe children (interior vertices only)
}

// ChildAt returns the child BlockVertex keyed by name, or nil if absent.
func (bv *BlockVertex) ChildAt(name string) *BlockVertex {
	if bv == nil {
		return nil
	}
	return bv.Children[name]
}

// DomainMap maps accelerator domain name to host metadata.
type DomainMap map[string]map[string]*HostInfo

func NewDomainMap() DomainMap {
	return make(DomainMap)
}

func (m DomainMap) AddHost(domain, instance, host string) {
	m.AddHostInfo(&HostInfo{Domain: domain, InstanceID: instance, HostName: host})
}

func (m DomainMap) String() string {
	var str strings.Builder
	str.WriteString("DomainMap:\n")
	for name, nodes := range m {
		fmt.Fprintf(&str, " %s : %v\n", name, nodes)
	}
	return str.String()
}

func (m DomainMap) AddHostInfo(hostInfo *HostInfo) {
	if hostInfo == nil {
		return
	}
	if hostInfo.Domain == "" {
		klog.Warningf("skipping topology domain with empty name for host %q (instance %q)", hostInfo.HostName, hostInfo.InstanceID)
		return
	}

	if hosts, ok := m[hostInfo.Domain]; ok {
		hosts[hostInfo.HostName] = hostInfo
	} else {
		m[hostInfo.Domain] = map[string]*HostInfo{hostInfo.HostName: hostInfo}
	}
}

// InferTwoLevelBlockSizes derives a blockSizes slice for two-level domain maps
// (i.e. at least one host carries a SubDomain). It returns
// [maxSubDomainSize, aggregateSize], where aggregateSize is the smallest
// power-of-2 multiple of maxSubDomainSize that is >= maxDomainSize — matching
// the slot-capacity formula used by buildBlockTree. Returns nil for single-level
// maps (no SubDomain present) and for empty maps, so callers fall back to
// getBlockSizes for those cases.
func (m DomainMap) InferTwoLevelBlockSizes() []int {
	maxDomainSize := 0
	maxSubDomainSize := 0
	hasSubDomains := false

	for _, hosts := range m {
		subSizes := make(map[string]int)
		for _, hi := range hosts {
			if hi.SubDomain != "" {
				hasSubDomains = true
				subSizes[hi.SubDomain]++
			}
		}
		if n := len(hosts); n > maxDomainSize {
			maxDomainSize = n
		}
		for _, size := range subSizes {
			if size > maxSubDomainSize {
				maxSubDomainSize = size
			}
		}
	}

	if !hasSubDomains {
		return nil
	}
	aggregateSize := maxSubDomainSize
	for aggregateSize < maxDomainSize {
		aggregateSize *= 2
	}
	if aggregateSize == maxSubDomainSize {
		return []int{maxSubDomainSize}
	}
	return []int{maxSubDomainSize, aggregateSize}
}

// GetDomainTree builds a flat BlockVertex tree from the DomainMap and populates
// ActualNodeCount and MaxChildNodeCount on every vertex.
//
//   - When no host in a domain has a SubDomain, the domain vertex is a leaf
//     that holds its hosts directly (one level below root).
//   - When hosts carry a SubDomain, the domain vertex has one child per distinct
//     SubDomain value, and each sub-domain vertex holds the hosts belonging to it
//     (two levels below root). A host with an empty SubDomain in an otherwise
//     dual-level domain is placed in a fallback sub-domain vertex keyed by the
//     accelerator domain name, so the host is always emitted.
//
// After the tree is built, MaxChildNodeCount on each interior vertex is set to the
// maximum ActualNodeCount among its direct children. The translate layer uses this
// to size base-block slots without a separate BFS pass.
func (m DomainMap) GetDomainTree() *BlockVertex {
	root := &BlockVertex{Children: make(map[string]*BlockVertex)}

	for domain, hosts := range m {
		domainBV := &BlockVertex{Vertex: Vertex{ID: domain}}
		root.Children[domain] = domainBV

		hasSubDomain := false
		for _, host := range hosts {
			if host.SubDomain != "" {
				hasSubDomain = true
				break
			}
		}

		if !hasSubDomain {
			// One-level: domain vertex is the leaf; hosts live here directly.
			hostMap := make(map[string]*HostInfo, len(hosts))
			maps.Copy(hostMap, hosts)
			domainBV.Hosts = hostMap
			domainBV.ActualNodeCount = len(hosts)
		} else {
			// Two-level: one sub-domain vertex per distinct SubDomain under the domain.
			// Every host is placed: those with a SubDomain go into their named sub-domain
			// vertex; those without one go into a fallback sub-domain keyed by the domain
			// name. placed tracks the total so domainBV.ActualNodeCount reflects all hosts.
			domainBV.Children = make(map[string]*BlockVertex)
			placed := 0
			var fallbackHosts []string
			for _, host := range hosts {
				gn := host.SubDomain
				if gn == "" {
					// A host with no SubDomain in a domain where other hosts carry one
					// indicates a partially-configured provider (e.g. OCI missing rack
					// info). Place it in a fallback sub-domain vertex keyed by the
					// accelerator domain name so the host is always emitted rather than
					// silently dropped.
					fallbackHosts = append(fallbackHosts, host.HostName)
					gn = domain
				}
				sub := domainBV.ChildAt(gn)
				if sub == nil {
					sub = &BlockVertex{Vertex: Vertex{ID: gn}, Hosts: make(map[string]*HostInfo)}
					domainBV.Children[gn] = sub
				}
				sub.Hosts[host.HostName] = host
				sub.ActualNodeCount++
				placed++
			}
			domainBV.ActualNodeCount = placed
			if len(fallbackHosts) > 0 {
				sort.Strings(fallbackHosts)
				klog.Warningf("domain %q: hosts %v have no SubDomain; placed in fallback sub-domain %q", domain, fallbackHosts, domain)
			}
			// MaxChildNodeCount = largest sub-domain host count within this domain.
			for _, sub := range domainBV.Children {
				if sub.ActualNodeCount > domainBV.MaxChildNodeCount {
					domainBV.MaxChildNodeCount = sub.ActualNodeCount
				}
			}
		}
		// MaxChildNodeCount on root = largest domain host count across all domains.
		if domainBV.ActualNodeCount > root.MaxChildNodeCount {
			root.MaxChildNodeCount = domainBV.ActualNodeCount
		}
	}

	return root
}
