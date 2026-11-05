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
	"os"
	"path/filepath"
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

func none(string) []string { return nil }

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

func TestSelectDataDirectIsAuthoritative(t *testing.T) {
	inv, _ := ParseInventory(inventory)
	m, _ := ParseMatrix(ddMatrix)
	got, err := Select([]Binding{{Interface: "roce_vf_r0", RailID: "rail-0", PCI: "0000:3b:00.0"}}, none, inv, m)
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
	got, err := Select([]Binding{{Interface: "eth1", RailID: "r", PCI: "0000:01:00.0"}}, none, inv, m)
	require.NoError(t, err)
	require.Equal(t, "SYS", got["0000:01:00.0"].BestPath)
	require.Equal(t, ModeDataDirect, got["0000:01:00.0"].MappingMode)
}

func TestSelectNICRowTies(t *testing.T) {
	inv, _ := ParseInventory(inventory)
	m, _ := ParseMatrix(ddMatrix)
	// The matrix without DD entries falls back to the NIC row: GPU0 and GPU1 tie at NODE.
	m.DDs = map[string]string{}
	got, err := Select([]Binding{{Interface: "roce_vf_r0", RailID: "rail-0", PCI: "0000:3b:00.0"}}, none, inv, m)
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
	got, err := Select([]Binding{{Interface: "eth9", RailID: "x", PCI: "0000:5e:00.0"}}, rdma, inv, m)
	require.NoError(t, err)
	require.Equal(t, "NIC0", got["0000:5e:00.0"].TopologyTarget)
}

func TestSelectNoMatrixAndNoPath(t *testing.T) {
	b := []Binding{{Interface: "eth7", RailID: "r", PCI: "0000:01:00.0"}}
	got, err := Select(b, none, nil, nil)
	require.NoError(t, err)
	require.Equal(t, NICMapping{Interface: "eth7", RailID: "r", MappingMode: ModeNone, BestGPUs: []GPU{}}, got["0000:01:00.0"])

	inv, _ := ParseInventory(inventory)
	m, _ := ParseMatrix(ddMatrix)
	got, err = Select(b, none, inv, m) // not in the legend
	require.NoError(t, err)
	require.Equal(t, ModeNone, got["0000:01:00.0"].MappingMode)
}

func TestSelectAmbiguousAndUnknown(t *testing.T) {
	inv, _ := ParseInventory(inventory)
	m, _ := ParseMatrix(ddMatrix)
	m.NICs["NIC9"] = "roce_vf_r0"
	_, err := Select([]Binding{{Interface: "roce_vf_r0", RailID: "r", PCI: "0000:01:00.0"}}, none, inv, m)
	require.ErrorContains(t, err, "ambiguous")

	m, _ = ParseMatrix(ddMatrix)
	delete(inv, 1)
	_, err = Select([]Binding{{Interface: "roce_vf_r0", RailID: "r", PCI: "0000:01:00.0"}}, none, inv, m)
	require.ErrorContains(t, err, "inventory")

	inv, _ = ParseInventory(inventory)
	m.Cells["DD0"]["GPU0"] = "WAT"
	_, err = Select([]Binding{{Interface: "roce_vf_r0", RailID: "r", PCI: "0000:01:00.0"}}, none, inv, m)
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
