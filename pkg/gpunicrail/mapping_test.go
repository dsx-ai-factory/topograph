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

package gpunicrail

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const ddMatrix = `        GPU0  GPU1  GPU2  GPU3  NIC0  NIC1  NIC2  NIC3  NIC4  NIC5  DD0   DD1   DD2   DD3   CPU Affinity  NUMA Affinity
GPU0    X     NV18  NV18  NV18  SYS   SYS   NODE  NODE  SYS   SYS   NODE  PXB   SYS   SYS   0-47  0
GPU1    NV18  X     NV18  NV18  SYS   SYS   NODE  NODE  SYS   SYS   PXB   NODE  SYS   SYS   0-47  0
GPU2    NV18  NV18  X     NV18  NODE  NODE  SYS   SYS   NODE  NODE  SYS   SYS   NODE  PXB   48-95 1
GPU3    NV18  NV18  NV18  X     NODE  NODE  SYS   SYS   NODE  NODE  SYS   SYS   PXB   NODE  48-95 1
NIC0    SYS   SYS   NODE  NODE  X     PIX   SYS   SYS   NODE  NODE  SYS   SYS   NODE  NODE
NIC1    SYS   SYS   NODE  NODE  PIX   X     SYS   SYS   NODE  NODE  SYS   SYS   NODE  NODE
NIC2    NODE  NODE  SYS   SYS   SYS   SYS   X     NODE  SYS   SYS   NODE  NODE  SYS   SYS
NIC3    NODE  NODE  SYS   SYS   SYS   SYS   NODE  X     SYS   SYS   NODE  NODE  SYS   SYS
NIC4    SYS   SYS   NODE  NODE  NODE  NODE  SYS   SYS   X     NODE  SYS   SYS   NODE  NODE
NIC5    SYS   SYS   NODE  NODE  NODE  NODE  SYS   SYS   NODE  X     SYS   SYS   NODE  NODE
DD0     NODE  PXB   SYS   SYS   SYS   SYS   NODE  NODE  SYS   SYS   X     NODE  SYS   SYS
DD1     PXB   NODE  SYS   SYS   SYS   SYS   NODE  NODE  SYS   SYS   NODE  X     SYS   SYS
DD2     SYS   SYS   NODE  PXB   NODE  NODE  SYS   SYS   NODE  NODE  SYS   SYS   X     NODE
DD3     SYS   SYS   PXB   NODE  NODE  NODE  SYS   SYS   NODE  NODE  SYS   SYS   NODE  X

Legend:

  X    = Self
  NV#  = Connection traversing a bonded set of # NVLinks

NIC Legend:

  NIC0: mlx5_8
  NIC1: mlx5_9
  NIC2: roce_vf_r0
  NIC3: roce_vf_r1
  NIC4: roce_vf_r2
  NIC5: roce_vf_r3
  DD0:  roce_vf_r0 data-direct function
  DD1:  roce_vf_r1 data-direct function
  DD2:  roce_vf_r2 data-direct function
  DD3:  roce_vf_r3 data-direct function
`

const inventory = `0, GPU-bbb0, 00000000:16:00.0
1, GPU-aaa1, 00000000:17:00.0
2, GPU-ccc2, 00000000:98:00.0
3, GPU-ddd3, 00000000:99:00.0
`

type testNICResolver struct {
	rdma    func(string) []string
	devices map[string]string
	parents map[string]string
}

func (r testNICResolver) RDMADevices(name string) []string {
	if r.rdma != nil {
		return r.rdma(name)
	}
	return nil
}
func (r testNICResolver) DevicePCI(name string) (string, error)       { return r.devices[name], nil }
func (r testNICResolver) PhysicalFunction(pci string) (string, error) { return r.parents[pci], nil }

func TestPCI(t *testing.T) {
	for in, want := range map[string]string{
		"0000:3B:00.0":     "0000:3b:00.0",
		"00000000:17:00.0": "0000:17:00.0",
	} {
		got, err := NormalizePCI(in)
		require.NoError(t, err)
		require.Equal(t, want, got)
	}
	for _, bad := range []string{"", "3b:00.0", "00010000:17:00.0", "0000:3b:00.9", "eth0"} {
		_, err := NormalizePCI(bad)
		require.Error(t, err, bad)
	}
}

func TestParseInventory(t *testing.T) {
	inv, err := ParseInventory(inventory)
	require.NoError(t, err)
	require.Equal(t, GPU{UUID: "GPU-aaa1", PCIAddress: "0000:17:00.0"}, inv[1])
	_, err = ParseInventory("0, GPU-a, 0000:17:00.0\n1, GPU-a, 0000:18:00.0\n")
	require.Error(t, err)
}

func TestParseMatrix(t *testing.T) {
	m, err := ParseMatrix(ddMatrix)
	require.NoError(t, err)
	require.Len(t, m.Cols, 14)
	require.Equal(t, "PXB", m.Cells["DD0"]["GPU1"])
	require.Equal(t, "roce_vf_r0", m.NICs["NIC2"])
	require.Equal(t, "roce_vf_r0", m.DDs["DD0"])
	_, err = ParseMatrix("nothing here")
	require.Error(t, err)
}

func TestParseMatrixANSI(t *testing.T) {
	want, err := ParseMatrix(ddMatrix)
	require.NoError(t, err)
	header, rows, ok := strings.Cut(ddMatrix, "\n")
	require.True(t, ok)
	underlined := "\x1b[4m" + header + "\x1b[0m\n" + rows
	for name, out := range map[string]string{
		"underlined header":           underlined,
		"underlined header with CRLF": strings.ReplaceAll(underlined, "\n", "\r\n"),
		"colored path cells":          strings.ReplaceAll(ddMatrix, "PXB", "\x1b[1;32mPXB\x1b[0m"),
		"formatted legend":            strings.ReplaceAll(ddMatrix, "NIC2: roce_vf_r0", "\x1b[4mNIC2: roce_vf_r0\x1b[m"),
	} {
		t.Run(name, func(t *testing.T) {
			got, err := ParseMatrix(out)
			require.NoError(t, err)
			require.Equal(t, want, got)
		})
	}
}

func TestSelectDataDirectIsAuthoritative(t *testing.T) {
	inv, _ := ParseInventory(inventory)
	m, _ := ParseMatrix(ddMatrix)
	got, err := Select([]Binding{{Interface: "roce_vf_r0", RailID: "rail-0", PCI: "0000:3b:00.0"}}, testNICResolver{}, inv, m)
	require.NoError(t, err)
	require.Equal(t, NICMapping{
		Interface: "roce_vf_r0", RailID: "rail-0", TopologyTarget: "DD0", MappingMode: ModeDataDirect, BestPath: "PXB",
		BestGPUs: []GPU{{UUID: "GPU-aaa1", PCIAddress: "0000:17:00.0"}},
	}, got["0000:3b:00.0"])
}

func TestSelectDDWinsEvenWhenWorse(t *testing.T) {
	inv, _ := ParseInventory("0, GPU-a, 0000:17:00.0\n")
	m, err := ParseMatrix(`      GPU0  NIC0  DD0
GPU0  X     PIX   SYS
NIC0  PIX   X     SYS
DD0   SYS   SYS   X

NIC Legend:
  NIC0: eth1
  DD0:  eth1 data-direct function
`)
	require.NoError(t, err)
	got, err := Select([]Binding{{Interface: "eth1", RailID: "r", PCI: "0000:01:00.0"}}, testNICResolver{}, inv, m)
	require.NoError(t, err)
	require.Equal(t, "SYS", got["0000:01:00.0"].BestPath)
	require.Equal(t, ModeDataDirect, got["0000:01:00.0"].MappingMode)
}

func TestSelectNICRowTies(t *testing.T) {
	inv, _ := ParseInventory(inventory)
	m, _ := ParseMatrix(ddMatrix)
	// The matrix without DD entries falls back to the NIC row: GPU0 and GPU1 tie at NODE.
	m.DDs = map[string]string{}
	got, err := Select([]Binding{{Interface: "roce_vf_r0", RailID: "rail-0", PCI: "0000:3b:00.0"}}, testNICResolver{}, inv, m)
	require.NoError(t, err)
	n := got["0000:3b:00.0"]
	require.Equal(t, ModeNIC, n.MappingMode)
	require.Equal(t, "NODE", n.BestPath)
	require.Equal(t, []GPU{{"GPU-aaa1", "0000:17:00.0"}, {"GPU-bbb0", "0000:16:00.0"}}, n.BestGPUs, "sorted by UUID")
}

func TestSelectJoinsThroughRDMADevice(t *testing.T) {
	inv, _ := ParseInventory(inventory)
	m, _ := ParseMatrix(ddMatrix)
	rdma := func(i string) []string {
		if i == "eth9" {
			return []string{"mlx5_8"}
		}
		return nil
	}
	got, err := Select([]Binding{{Interface: "eth9", RailID: "x", PCI: "0000:5e:00.0"}}, testNICResolver{rdma: rdma}, inv, m)
	require.NoError(t, err)
	require.Equal(t, "NIC0", got["0000:5e:00.0"].TopologyTarget)
}

func TestSelectNoMatrixAndNoPath(t *testing.T) {
	b := []Binding{{Interface: "eth7", RailID: "r", PCI: "0000:01:00.0"}}
	got, err := Select(b, testNICResolver{}, nil, nil)
	require.NoError(t, err)
	require.Equal(t, NICMapping{Interface: "eth7", RailID: "r", MappingMode: ModeNone, BestGPUs: []GPU{}}, got["0000:01:00.0"])

	inv, _ := ParseInventory(inventory)
	m, _ := ParseMatrix(ddMatrix)
	got, err = Select(b, testNICResolver{}, inv, m) // not in the legend
	require.NoError(t, err)
	require.Equal(t, ModeNone, got["0000:01:00.0"].MappingMode)
}

func TestSelectAmbiguousAndUnknown(t *testing.T) {
	inv, _ := ParseInventory(inventory)
	m, _ := ParseMatrix(ddMatrix)
	m.NICs["NIC9"] = "roce_vf_r0"
	m.DDs = nil // Exercise NIC ambiguity without an authoritative DD match.
	_, err := Select([]Binding{{Interface: "roce_vf_r0", RailID: "r", PCI: "0000:01:00.0"}}, testNICResolver{}, inv, m)
	require.ErrorContains(t, err, "ambiguous")

	m, _ = ParseMatrix(ddMatrix)
	delete(inv, 1)
	_, err = Select([]Binding{{Interface: "roce_vf_r0", RailID: "r", PCI: "0000:01:00.0"}}, testNICResolver{}, inv, m)
	require.ErrorContains(t, err, "inventory")

	inv, _ = ParseInventory(inventory)
	m.Cells["DD0"]["GPU0"] = "WAT"
	_, err = Select([]Binding{{Interface: "roce_vf_r0", RailID: "r", PCI: "0000:01:00.0"}}, testNICResolver{}, inv, m)
	require.ErrorContains(t, err, "unknown path")
}

const lldpJSON = `{"lldp":{"interface":[
 {"roce_vf_r0":{"via":"LLDP","chassis":{"leaf-7":{"id":{"type":"mac","value":"aa:bb"}}},"port":{"id":{"type":"ifname","value":"p1"}}}},
 {"roce_vf_r1":{"via":"LLDP","chassis":{"leaf-8":{}},"port":{"id":{"value":"p2"}}}},
 {"eth0":{"via":"LLDP","chassis":null,"port":{}}}
]}}`

func TestParseLLDP(t *testing.T) {
	names, err := ParseLLDP([]byte(lldpJSON))
	require.NoError(t, err)
	require.Equal(t, []string{"roce_vf_r0", "roce_vf_r1"}, names)

	// A single neighbor is rendered as an object, and none as an empty document.
	names, err = ParseLLDP([]byte(`{"lldp":{"interface":{"eth1":{"chassis":{"a":{}},"port":{"id":{}}}}}}`))
	require.NoError(t, err)
	require.Equal(t, []string{"eth1"}, names)
	names, err = ParseLLDP([]byte(`{"lldp":{}}`))
	require.NoError(t, err)
	require.Empty(t, names)
	_, err = ParseLLDP([]byte(`oops`))
	require.Error(t, err)
}

func TestMatcherAndBindRails(t *testing.T) {
	m, err := NewMatcher(RailConfig{InterfaceRegex: `^roce_vf_r([0-9]+)$`, RailIDFormat: "rail-$1"})
	require.NoError(t, err)
	pci := func(i string) (string, error) { return "0000:3b:0" + i[len(i)-1:] + ".0", nil }

	b, err := BindRails([]string{"roce_vf_r0", "roce_vf_r1"}, m, pci)
	require.NoError(t, err)
	require.Equal(t, "rail-1", b[1].RailID)

	resolveRail := func(name string) (string, error) {
		require.Contains(t, []string{"roce_vf_r0", "roce_vf_r1"}, name, "unmatched interfaces must not reach PCI resolution")
		return pci(name)
	}
	filtered, err := BindRails([]string{"enx4cbb47f01eff", "roce_vf_r0", "eth0", "roce_vf_r1"}, m, resolveRail)
	require.NoError(t, err)
	require.Equal(t, b, filtered)

	filtered, err = BindRails([]string{"enx4cbb47f01eff", "eth0"}, m, resolveRail)
	require.NoError(t, err)
	require.Empty(t, filtered)

	collide, _ := NewMatcher(RailConfig{InterfaceRegex: `^roce_vf_r([0-9]+)$`, RailIDFormat: "rail"})
	_, err = BindRails([]string{"roce_vf_r0", "roce_vf_r1"}, collide, pci)
	require.ErrorContains(t, err, "same rail")

	_, err = BindRails([]string{"roce_vf_r0"}, m, func(string) (string, error) { return "", os.ErrNotExist })
	require.Error(t, err)
	same := func(string) (string, error) { return "0000:3b:00.0", nil }
	_, err = BindRails([]string{"roce_vf_r0", "roce_vf_r1"}, m, same)
	require.ErrorContains(t, err, "same PCI")

	for _, bad := range []RailConfig{
		{}, {InterfaceRegex: "(", RailIDFormat: "x"},
		{InterfaceRegex: `^r(\d)$`, RailIDFormat: "rail-$2"}, {InterfaceRegex: `^r(\d)$`, RailIDFormat: "${nope}"},
	} {
		_, err := NewMatcher(bad)
		require.Error(t, err, "%+v", bad)
	}
	empty, _ := NewMatcher(RailConfig{InterfaceRegex: `^r(\d?)$`, RailIDFormat: "$1"})
	_, err = BindRails([]string{"r"}, empty, pci)
	require.ErrorContains(t, err, "empty")
}

func TestSysfs(t *testing.T) {
	root := t.TempDir()
	dev := filepath.Join(root, "devices", "0000:3b:00.0")
	require.NoError(t, os.MkdirAll(filepath.Join(dev, "infiniband", "mlx5_2"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(root, "class", "net", "eth1"), 0o755))
	require.NoError(t, os.Symlink(dev, filepath.Join(root, "class", "net", "eth1", "device")))
	require.NoError(t, os.MkdirAll(filepath.Join(root, "class", "net", "bond0"), 0o755))

	s := Sysfs{Root: root}
	pci, err := s.PCIAddress("eth1")
	require.NoError(t, err)
	require.Equal(t, "0000:3b:00.0", pci)
	require.Equal(t, []string{"mlx5_2"}, s.RDMADevices("eth1"))
	_, err = s.PCIAddress("bond0")
	require.Error(t, err)
}

type fakeRunner map[string]string

func (f fakeRunner) Run(_ context.Context, cmd []string) (string, error) {
	if out, ok := f[cmd[0]+" "+cmd[1]]; ok {
		return out, nil
	}
	return "", os.ErrNotExist
}

func TestCollect(t *testing.T) {
	root := t.TempDir()
	for iface, pci := range map[string]string{"roce_vf_r0": "0000:3b:00.0", "roce_vf_r1": "0000:5e:00.0"} {
		require.NoError(t, os.MkdirAll(filepath.Join(root, "class", "net", iface), 0o755))
		require.NoError(t, os.Symlink(filepath.Join("..", "..", pci), filepath.Join(root, "class", "net", iface, "device")))
	}
	matcher, err := NewMatcher(RailConfig{InterfaceRegex: `^roce_vf_r([0-9]+)$`, RailIDFormat: "rail-$1"})
	require.NoError(t, err)
	c := &Collector{
		LLDP:    fakeRunner{"lldpctl -f": lldpJSON},
		GPU:     fakeRunner{"nvidia-smi --query-gpu=index,uuid,pci.bus_id": inventory, "nvidia-smi topo": ddMatrix},
		Matcher: matcher,
		Sysfs:   Sysfs{Root: root},
		Now:     func() time.Time { return time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC) },
	}
	st, err := c.Collect(context.Background())
	require.NoError(t, err)
	require.Equal(t, "2026-01-02T03:04:05Z", st.Sources.LLDP)
	require.Equal(t, ModeDataDirect, st.NICs["0000:3b:00.0"].MappingMode)
	require.Equal(t, "GPU-aaa1", st.NICs["0000:3b:00.0"].BestGPUs[0].UUID)
	require.Equal(t, "DD1", st.NICs["0000:5e:00.0"].TopologyTarget)

	// A missing GPU source fails rather than publishing a NIC-only mapping.
	c.GPU = fakeRunner{}
	st, err = c.Collect(context.Background())
	require.ErrorContains(t, err, "GPU inventory")
	require.Nil(t, st)

	// Inventory alone is insufficient: topology discovery must also succeed.
	c.GPU = fakeRunner{"nvidia-smi --query-gpu=index,uuid,pci.bus_id": inventory}
	st, err = c.Collect(context.Background())
	require.ErrorContains(t, err, "GPU topology")
	require.Nil(t, st)

	// No selected rail interface: no record.
	c.LLDP = fakeRunner{"lldpctl -f": `{"lldp":{"interface":{"eth0":{"chassis":{"switch":{}},"port":{"id":{}}}}}}`}
	st, err = c.Collect(context.Background())
	require.NoError(t, err)
	require.Nil(t, st)
}

func TestStatusValidate(t *testing.T) {
	ok := func() Status {
		return Status{NICs: map[string]NICMapping{
			"0000:3b:00.0": {Interface: "a", RailID: "r0", MappingMode: ModeNone, BestGPUs: []GPU{}},
		}}
	}
	s := ok()
	require.NoError(t, s.Validate())

	s = ok()
	s.NICs["0000:3b:01.0"] = NICMapping{Interface: "b", RailID: "r0", MappingMode: ModeNone}
	require.ErrorContains(t, s.Validate(), "railID")

	s = ok()
	n := s.NICs["0000:3b:00.0"]
	n.BestPath = "PIX"
	s.NICs["0000:3b:00.0"] = n
	require.Error(t, s.Validate())

	s = ok()
	s.NICs["0000:3B:00.0"] = s.NICs["0000:3b:00.0"]
	delete(s.NICs, "0000:3b:00.0")
	require.Error(t, s.Validate())

	s = Status{NICs: map[string]NICMapping{"0000:3b:00.0": {
		Interface: "a", RailID: "r", MappingMode: ModeNIC, TopologyTarget: "NIC0", BestPath: "PIX",
		BestGPUs: []GPU{{"GPU-b", "0000:01:00.0"}, {"GPU-a", "0000:02:00.0"}},
	}}}
	require.ErrorContains(t, s.Validate(), "sorted")
}

// These identities mirror the reported deployment: LLDP sees a PF while the
// NVIDIA matrix names its VF. The output must retain the collected identity.
func TestSelectPCIIdentity(t *testing.T) {
	const pf = "0001:03:00.0"
	const vf = "0001:03:00.4"
	const sibling = "0001:03:00.5"
	inv := Inventory{0: {UUID: "GPU-a", PCIAddress: "0000:17:00.0"}, 1: {UUID: "GPU-b", PCIAddress: "0000:18:00.0"}}
	for _, tc := range []struct {
		name      string
		bound     string
		resolver  testNICResolver
		legend    map[string]string
		dd        map[string]string
		want      string
		ambiguous bool
	}{
		{name: "ordinary NIC exact PCI", bound: pf, resolver: testNICResolver{devices: map[string]string{"mlx5_0": pf}}, legend: map[string]string{"NIC0": "mlx5_0"}, want: "NIC0"},
		{name: "PF uses VF row", bound: pf, resolver: testNICResolver{devices: map[string]string{"mlx5_0": vf}, parents: map[string]string{vf: pf}}, legend: map[string]string{"NIC0": "mlx5_0"}, want: "NIC0"},
		{name: "VF uses PF row", bound: vf, resolver: testNICResolver{devices: map[string]string{"roce_r0_n0": pf}, parents: map[string]string{vf: pf}}, legend: map[string]string{"NIC0": "roce_r0_n0"}, want: "NIC0"},
		{name: "unrelated device", bound: pf, resolver: testNICResolver{devices: map[string]string{"mlx5_0": vf}}, legend: map[string]string{"NIC0": "mlx5_0"}},
		{name: "sibling VFs do not match", bound: sibling, resolver: testNICResolver{devices: map[string]string{"mlx5_0": vf}, parents: map[string]string{vf: pf, sibling: pf}}, legend: map[string]string{"NIC0": "mlx5_0"}},
		{name: "multiple VFs are ambiguous", bound: pf, resolver: testNICResolver{devices: map[string]string{"mlx5_0": vf, "mlx5_1": sibling}, parents: map[string]string{vf: pf, sibling: pf}}, legend: map[string]string{"NIC0": "mlx5_0", "NIC1": "mlx5_1"}, ambiguous: true},
		{name: "exact PCI beats related VF", bound: pf, resolver: testNICResolver{devices: map[string]string{"mlx5_0": pf, "mlx5_1": vf}, parents: map[string]string{vf: pf}}, legend: map[string]string{"NIC0": "mlx5_0", "NIC1": "mlx5_1"}, want: "NIC0"},
		{name: "duplicate exact PCI is ambiguous", bound: pf, resolver: testNICResolver{devices: map[string]string{"mlx5_0": pf, "mlx5_1": pf}}, legend: map[string]string{"NIC0": "mlx5_0", "NIC1": "mlx5_1"}, ambiguous: true},
		{name: "name beats PCI ambiguity", bound: pf, resolver: testNICResolver{devices: map[string]string{"mlx5_0": pf, "mlx5_1": pf}}, legend: map[string]string{"NIC0": "eth_r0_p0", "NIC1": "mlx5_0", "NIC2": "mlx5_1"}, want: "NIC0"},
		{name: "related DD overrides direct NIC", bound: pf, resolver: testNICResolver{devices: map[string]string{"mlx5_0": vf}, parents: map[string]string{vf: pf}}, legend: map[string]string{"NIC0": "eth_r0_p0"}, dd: map[string]string{"DD0": "mlx5_0"}, want: "DD0"},
		{name: "DD avoids unused NIC ambiguity", bound: pf, legend: map[string]string{"NIC0": "eth_r0_p0", "NIC1": "eth_r0_p0"}, dd: map[string]string{"DD0": "eth_r0_p0"}, want: "DD0"},
		{name: "ambiguous DD cannot fall back to NIC", bound: pf, resolver: testNICResolver{devices: map[string]string{"mlx5_0": vf, "mlx5_1": sibling}, parents: map[string]string{vf: pf, sibling: pf}}, legend: map[string]string{"NIC0": "eth_r0_p0"}, dd: map[string]string{"DD0": "mlx5_0", "DD1": "mlx5_1"}, ambiguous: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := &Matrix{Cols: []string{"GPU0", "GPU1"}, NICs: tc.legend, DDs: tc.dd, Cells: map[string]map[string]string{}}
			for alias := range tc.legend {
				m.Cells[alias] = map[string]string{"GPU0": "NODE", "GPU1": "NODE"}
			}
			for alias := range tc.dd {
				m.Cells[alias] = map[string]string{"GPU0": "SYS", "GPU1": "SYS"}
			}
			got, err := Select([]Binding{{Interface: "eth_r0_p0", PCI: tc.bound, RailID: "rail-0-port-0"}}, tc.resolver, inv, m)
			if tc.ambiguous {
				require.ErrorContains(t, err, "ambiguous topology legend entries")
				require.Nil(t, got)
				return
			}
			require.NoError(t, err)
			require.Len(t, got, 1)
			n := got[tc.bound]
			require.Equal(t, "eth_r0_p0", n.Interface)
			require.Equal(t, "rail-0-port-0", n.RailID)
			require.Equal(t, tc.want, n.TopologyTarget)
			if tc.want == "" {
				require.Equal(t, ModeNone, n.MappingMode)
				require.Empty(t, n.BestGPUs)
				return
			}
			require.Equal(t, []GPU{inv[0], inv[1]}, n.BestGPUs)
			if tc.want == "DD0" {
				require.Equal(t, ModeDataDirect, n.MappingMode)
				require.Equal(t, "SYS", n.BestPath)
			} else {
				require.Equal(t, ModeNIC, n.MappingMode)
				require.Equal(t, "NODE", n.BestPath)
			}
		})
	}
}

// Device links are relative, as on the host, and all lookups must stay under
// the relocated sysfs root rather than consulting the machine running tests.
func sysfsLink(t *testing.T, root, path, target string) {
	t.Helper()
	path = filepath.Join(root, path)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.Symlink(target, path))
}

func TestSysfsPCIIdentity(t *testing.T) {
	root := t.TempDir()
	s := Sysfs{Root: root}
	sysfsLink(t, root, "class/infiniband/mlx5_0/device", "../../../0001:03:00.4")
	sysfsLink(t, root, "class/net/eth_r0_p0/device", "../../../0001:03:00.0")
	sysfsLink(t, root, "bus/pci/devices/0001:03:00.4/physfn", "../0001:03:00.0")
	for name, want := range map[string]string{"mlx5_0": "0001:03:00.4", "eth_r0_p0": "0001:03:00.0", "missing": ""} {
		got, err := s.DevicePCI(name)
		require.NoError(t, err)
		require.Equal(t, want, got)
	}
	parent, err := s.PhysicalFunction("0001:03:00.4")
	require.NoError(t, err)
	require.Equal(t, "0001:03:00.0", parent)
	parent, err = s.PhysicalFunction("0001:03:00.0")
	require.NoError(t, err)
	require.Empty(t, parent, "a PF has no physfn link")
	_, err = s.PhysicalFunction("not-pci")
	require.Error(t, err)
	_, err = s.DevicePCI("../mlx5_0")
	require.Error(t, err)

	// Never swallow malformed identities or unexpected filesystem entries.
	sysfsLink(t, root, "class/infiniband/bad/device", "../../../not-pci")
	_, err = s.DevicePCI("bad")
	require.ErrorContains(t, err, "reading PCI identity")
	sysfsLink(t, root, "bus/pci/devices/0001:03:00.5/physfn", "../not-pci")
	_, err = s.PhysicalFunction("0001:03:00.5")
	require.ErrorContains(t, err, "reading PCI identity")
	require.NoError(t, os.MkdirAll(filepath.Join(root, "class/net/broken/device"), 0o755))
	_, err = s.DevicePCI("broken")
	require.ErrorContains(t, err, "reading PCI identity")
	sysfsLink(t, root, "class/net/mlx5_0/device", "../../../0002:03:00.0")
	_, err = s.DevicePCI("mlx5_0")
	require.ErrorContains(t, err, "identifies both")
}

func TestCollectPFUsingVFTopology(t *testing.T) {
	root := t.TempDir()
	sysfsLink(t, root, "class/net/eth_r0_p0/device", "../../../bus/pci/devices/0001:03:00.0")
	require.NoError(t, os.MkdirAll(filepath.Join(root, "bus/pci/devices/0001:03:00.0/infiniband/roce_r0_n0"), 0o755))
	sysfsLink(t, root, "class/infiniband/mlx5_0/device", "../../../bus/pci/devices/0001:03:00.4")
	sysfsLink(t, root, "bus/pci/devices/0001:03:00.4/physfn", "../0001:03:00.0")
	matcher, err := NewMatcher(RailConfig{InterfaceRegex: `^eth_r([0-9]+)_p([0-9]+)$`, RailIDFormat: "rail-${1}-port-${2}"})
	require.NoError(t, err)
	c := &Collector{
		LLDP: fakeRunner{"lldpctl -f": `{"lldp":{"interface":{"eth_r0_p0":{"chassis":{"switch":{}},"port":{"id":{}}}}}}`},
		GPU: fakeRunner{
			"nvidia-smi --query-gpu=index,uuid,pci.bus_id": inventory,
			"nvidia-smi topo": `GPU0 GPU1 NIC0
GPU0 X NODE NODE
GPU1 NODE X NODE
NIC0 NODE NODE X
NIC Legend:
NIC0: mlx5_0 enP1p3s0f0v0 0001:03:00.4 N/A
`,
		},
		Matcher: matcher, Sysfs: Sysfs{Root: root},
	}
	st, err := c.Collect(context.Background())
	require.NoError(t, err)
	require.Len(t, st.NICs, 1)
	n := st.NICs["0001:03:00.0"]
	require.Equal(t, "eth_r0_p0", n.Interface)
	require.Equal(t, "rail-0-port-0", n.RailID)
	require.Equal(t, "NIC0", n.TopologyTarget)
	require.Equal(t, ModeNIC, n.MappingMode)
	require.Equal(t, "NODE", n.BestPath)
	require.Equal(t, []GPU{{UUID: "GPU-aaa1", PCIAddress: "0000:17:00.0"}, {UUID: "GPU-bbb0", PCIAddress: "0000:16:00.0"}}, n.BestGPUs)

	// A malformed parent must fail the whole refresh, not silently publish none.
	parentLink := filepath.Join(root, "bus/pci/devices/0001:03:00.4/physfn")
	require.NoError(t, os.Remove(parentLink))
	require.NoError(t, os.Symlink("../not-pci", parentLink))
	st, err = c.Collect(context.Background())
	require.ErrorContains(t, err, "reading PCI identity")
	require.Nil(t, st)
}

// A command-aware runner verifies that compatibility retries change the actual
// arguments; fakeRunner deliberately ignores arguments after the subcommand.
type topologyRunner struct {
	t     *testing.T
	calls int
	steps []topologyStep
}

type topologyStep struct {
	command []string
	out     string
	err     error
}

func (r *topologyRunner) Run(_ context.Context, command []string) (string, error) {
	r.t.Helper()
	require.Less(r.t, r.calls, len(r.steps), "unexpected command: %v", command)
	step := r.steps[r.calls]
	r.calls++
	require.Equal(r.t, step.command, command)
	return step.out, step.err
}

func TestTopologyCompatibility(t *testing.T) {
	const legacyMatrix = `GPU0 NIC0
GPU0 X NODE
NIC0 NODE X
NIC Legend:
NIC0: mlx5_0
`
	failure := errors.New("command terminated with exit code 2")
	for _, tc := range []struct {
		name    string
		steps   []topologyStep
		wantErr error
	}{
		{name: "complete matrix preserves DD", steps: []topologyStep{{command: TopologyCommand, out: ddMatrix}}},
		{name: "unsupported flag on stdout", steps: []topologyStep{
			{command: TopologyCommand, out: `Option "-all" is not recognized.`, err: failure},
			{command: []string{"nvidia-smi", "topo", "-m"}, out: legacyMatrix},
		}},
		{name: "unsupported flag on stderr", steps: []topologyStep{
			{command: TopologyCommand, err: errors.New(`Option "-all" is not recognized.: command terminated with exit code 2`)},
			{command: []string{"nvidia-smi", "topo", "-m"}, out: legacyMatrix},
		}},
		{name: "other argument error must not downgrade", steps: []topologyStep{{command: TopologyCommand, err: failure}}, wantErr: failure},
		{name: "transport error must not downgrade", steps: []topologyStep{{command: TopologyCommand, err: os.ErrPermission}}, wantErr: os.ErrPermission},
		{name: "fallback error is returned", steps: []topologyStep{
			{command: TopologyCommand, out: `Option "-all" is not recognized.`, err: failure},
			{command: []string{"nvidia-smi", "topo", "-m"}, err: os.ErrPermission},
		}, wantErr: os.ErrPermission},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &topologyRunner{t: t, steps: tc.steps}
			c := &Collector{GPU: r}
			out, err := c.topology(context.Background())
			require.Equal(t, len(tc.steps), r.calls)
			if tc.wantErr != nil {
				require.ErrorIs(t, err, tc.wantErr)
				return
			}
			require.NoError(t, err)
			m, err := ParseMatrix(out)
			require.NoError(t, err)
			if len(tc.steps) == 1 {
				require.Equal(t, "roce_vf_r0", m.DDs["DD0"], "supported versions preserve the complete matrix")
			} else {
				require.Equal(t, "mlx5_0", m.NICs["NIC0"])
				require.Empty(t, m.DDs, "legacy output need not expose DD entries")
			}
		})
	}
}

func TestTopologyCompatibilityCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := &topologyRunner{t: t, steps: []topologyStep{{command: TopologyCommand,
		out: `Option "-all" is not recognized.`, err: errors.New("exit code 2")}}}
	c := &Collector{GPU: r}
	_, err := c.topology(ctx)
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, 1, r.calls, "cancellation must prevent a compatibility retry")
}
