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
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGetDomainTreeUnrackedHostFallback(t *testing.T) {
	const domain = "nvl-a"
	const rackedSubDomain = "nvl-a.rack-1"

	dm := NewDomainMap()
	// 8 racked hosts
	for i := 1; i <= 8; i++ {
		dm.AddHostInfo(&HostInfo{
			Domain:    domain,
			HostName:  fmt.Sprintf("node%04d", i),
			SubDomain: rackedSubDomain,
		})
	}
	// 1 rackless host — SubDomain intentionally empty, as OCI sets it when rack is absent
	dm.AddHostInfo(&HostInfo{
		Domain:   domain,
		HostName: "node0009",
	})

	root := dm.GetDomainTree()
	require.NotNil(t, root)

	domainBV := root.ChildAt(domain)
	require.NotNil(t, domainBV, "domain vertex must exist")
	require.Equal(t, 9, domainBV.ActualNodeCount, "all 9 hosts must be placed (none dropped)")

	// Fallback vertex is keyed by the domain name itself.
	fallback := domainBV.ChildAt(domain)
	require.NotNil(t, fallback, "fallback sub-domain vertex keyed by domain name must exist")
	require.Len(t, fallback.Hosts, 1, "fallback vertex must hold the one rackless host")
	require.NotNil(t, fallback.Hosts["node0009"])

	// Racked vertex still holds all 8 racked hosts.
	racked := domainBV.ChildAt(rackedSubDomain)
	require.NotNil(t, racked, "racked sub-domain vertex must exist")
	require.Len(t, racked.Hosts, 8)
}

func TestDomainMapAddHost(t *testing.T) {
	domainMap := NewDomainMap()

	domainMap.AddHost("domain1", "instance1", "host1")
	domainMap.AddHost("domain1", "instance2", "host2")
	domainMap.AddHost("domain2", "instance3", "host3")
	domainMap.AddHost("", "instance4", "host4")

	require.Equal(t, DomainMap{
		"domain1": map[string]*HostInfo{
			"host1": {Domain: "domain1", InstanceID: "instance1", HostName: "host1"},
			"host2": {Domain: "domain1", InstanceID: "instance2", HostName: "host2"},
		},
		"domain2": map[string]*HostInfo{
			"host3": {Domain: "domain2", InstanceID: "instance3", HostName: "host3"},
		},
	}, domainMap)
}

func TestInferTwoLevelBlockSizes(t *testing.T) {
	addHosts := func(dm DomainMap, domain, subDomain string, count int) {
		key := domain
		if subDomain != "" {
			key = subDomain
		}
		for i := 1; i <= count; i++ {
			dm.AddHostInfo(&HostInfo{
				Domain:    domain,
				SubDomain: subDomain,
				HostName:  fmt.Sprintf("%s-node%d", key, i),
			})
		}
	}

	t.Run("empty map returns nil", func(t *testing.T) {
		require.Nil(t, NewDomainMap().InferTwoLevelBlockSizes())
	})

	t.Run("single-level returns nil", func(t *testing.T) {
		dm := NewDomainMap()
		addHosts(dm, "d1", "", 8)
		addHosts(dm, "d2", "", 6)
		require.Nil(t, dm.InferTwoLevelBlockSizes())
	})

	t.Run("two-level returns maxSubDomainSize and aggregateSize", func(t *testing.T) {
		dm := NewDomainMap()
		addHosts(dm, "fabric-1", "fabric-1.rack-a", 8)
		addHosts(dm, "fabric-1", "fabric-1.rack-b", 8)
		require.Equal(t, []int{8, 16}, dm.InferTwoLevelBlockSizes())
	})

	t.Run("two-level single sub-domain collapses to one size", func(t *testing.T) {
		dm := NewDomainMap()
		addHosts(dm, "fabric-1", "fabric-1.rack-a", 8)
		require.Equal(t, []int{8}, dm.InferTwoLevelBlockSizes())
	})

	t.Run("two-level unequal sub-domains rounds aggregate to power-of-2 multiple", func(t *testing.T) {
		// maxSubDomainSize=8, maxDomainSize=12 → aggregateSize=16 (first 2^n*8 >= 12)
		dm := NewDomainMap()
		addHosts(dm, "fabric-1", "fabric-1.rack-a", 8)
		addHosts(dm, "fabric-1", "fabric-1.rack-b", 4)
		require.Equal(t, []int{8, 16}, dm.InferTwoLevelBlockSizes())
	})
}
