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

package gpunic

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
	got, err := Select([]Binding{{Interface: "roce_vf_r0", SwitchName: "rail-0", PCI: "0000:3b:00.0"}}, testNICResolver{}, inv, m)
	require.NoError(t, err)
	require.Equal(t, NICMapping{
		Interface: "roce_vf_r0", SwitchName: "rail-0", TopologyTarget: "DD0", MappingMode: ModeDataDirect, BestPath: "PXB",
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
	got, err := Select([]Binding{{Interface: "eth1", SwitchName: "r", PCI: "0000:01:00.0"}}, testNICResolver{}, inv, m)
	require.NoError(t, err)
	require.Equal(t, "SYS", got["0000:01:00.0"].BestPath)
	require.Equal(t, ModeDataDirect, got["0000:01:00.0"].MappingMode)
}

func TestSelectNICRowTies(t *testing.T) {
	inv, _ := ParseInventory(inventory)
	m, _ := ParseMatrix(ddMatrix)
	// The matrix without DD entries falls back to the NIC row: GPU0 and GPU1 tie at NODE.
	m.DDs = map[string]string{}
	got, err := Select([]Binding{{Interface: "roce_vf_r0", SwitchName: "rail-0", PCI: "0000:3b:00.0"}}, testNICResolver{}, inv, m)
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
	got, err := Select([]Binding{{Interface: "eth9", SwitchName: "x", PCI: "0000:5e:00.0"}}, testNICResolver{rdma: rdma}, inv, m)
	require.NoError(t, err)
	require.Equal(t, "NIC0", got["0000:5e:00.0"].TopologyTarget)
}

func TestSelectNoMatrixAndNoPath(t *testing.T) {
	b := []Binding{{Interface: "eth7", SwitchName: "r", PCI: "0000:01:00.0"}}
	got, err := Select(b, testNICResolver{}, nil, nil)
	require.NoError(t, err)
	require.Equal(t, NICMapping{Interface: "eth7", SwitchName: "r", MappingMode: ModeNone, BestGPUs: []GPU{}}, got["0000:01:00.0"])

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
	_, err := Select([]Binding{{Interface: "roce_vf_r0", SwitchName: "r", PCI: "0000:01:00.0"}}, testNICResolver{}, inv, m)
	require.ErrorContains(t, err, "ambiguous")

	m, _ = ParseMatrix(ddMatrix)
	delete(inv, 1)
	_, err = Select([]Binding{{Interface: "roce_vf_r0", SwitchName: "r", PCI: "0000:01:00.0"}}, testNICResolver{}, inv, m)
	require.ErrorContains(t, err, "inventory")

	inv, _ = ParseInventory(inventory)
	m.Cells["DD0"]["GPU0"] = "WAT"
	_, err = Select([]Binding{{Interface: "roce_vf_r0", SwitchName: "r", PCI: "0000:01:00.0"}}, testNICResolver{}, inv, m)
	require.ErrorContains(t, err, "unknown path")
}

const lldpJSON = `{"lldp":[{"interface":[
 {"name":"roce_vf_r0","via":"LLDP","chassis":[{"id":[{"type":"mac","value":"aa:bb"}],"name":[{"value":"leaf-7"}]}],"port":[{"id":[{"value":"p1"}]}]},
 {"name":"roce_vf_r1","chassis":[{"name":[{"value":"leaf-8"}]}]},
 {"name":"eth0","chassis":[{"id":[{"value":"not-a-switch-name"}]}]}
]}]}`

func TestParseLLDP(t *testing.T) {
	names, err := ParseLLDP([]byte(lldpJSON))
	require.NoError(t, err)
	require.Equal(t, map[string]string{"roce_vf_r0": "leaf-7", "roce_vf_r1": "leaf-8", "eth0": ""}, names)
	for _, data := range []string{`{"lldp":[]}`, `{"lldp":[{}]}`, `{"lldp":[{"interface":[{"name":"eth0"}]}]}`} {
		names, err = ParseLLDP([]byte(data))
		require.NoError(t, err)
		require.Empty(t, names)
	}
	// Repeated reports and multiple NICs connected to the same switch are valid.
	names, err = ParseLLDP([]byte(`{"lldp":[{"interface":[
 {"name":"eth0","chassis":[{"name":[{"value":"leaf"},{"value":"leaf"}]}]},
 {"name":"eth0","chassis":[{"name":[{"value":"leaf"}]}]},
 {"name":"arbitrary-name","chassis":[{"name":[{"value":"leaf"}]}]}
 ]}]}`))
	require.NoError(t, err)
	require.Equal(t, map[string]string{"eth0": "leaf", "arbitrary-name": "leaf"}, names)
	for _, data := range []string{`oops`, `{}`, `null`, `{"lldp":{}}`,
		`{"lldp":[{"interface":[{}]}]}`,
		`{"lldp":[{"interface":[{"name":"eth0","chassis":[{"name":[{"value":"a"},{"value":"b"}]}]}]}]}`,
		`{"lldp":[{"interface":[{"name":"eth0","chassis":[{"name":"bad-shape"}]}]}]}`,
	} {
		_, err = ParseLLDP([]byte(data))
		require.Error(t, err, data)
	}
}

func TestBindSwitches(t *testing.T) {
	nics := []Binding{{Interface: "no-neighbor", PCI: "0000:04:00.0"}, {Interface: "eth9", PCI: "0000:02:00.0"}, {Interface: "enp1s0", PCI: "0000:01:00.0"}, {Interface: "unnamed-neighbor", PCI: "0000:03:00.0"}}
	got, err := BindSwitches(nics, map[string]string{"eth9": "leaf", "enp1s0": "leaf", "unnamed-neighbor": "", "lo": "ignored"})
	require.NoError(t, err)
	require.Equal(t, []Binding{{Interface: "enp1s0", PCI: "0000:01:00.0", SwitchName: "leaf"}, {Interface: "eth9", PCI: "0000:02:00.0", SwitchName: "leaf"}, {Interface: "unnamed-neighbor", PCI: "0000:03:00.0"}}, got)
	_, err = BindSwitches([]Binding{{Interface: "a", PCI: "0000:01:00.0"}, {Interface: "b", PCI: "0000:01:00.0"}}, map[string]string{"a": "", "b": ""})
	require.ErrorContains(t, err, "same PCI")
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
	nics, err := s.NICs()
	require.NoError(t, err)
	require.Equal(t, []Binding{{Interface: "eth1", PCI: "0000:3b:00.0"}}, nics)

	// USB NICs have a device link too, but are outside PCI locality discovery.
	usb := filepath.Join(root, "devices", "usb1")
	require.NoError(t, os.MkdirAll(usb, 0o755))
	require.NoError(t, os.Symlink("../../bus/usb", filepath.Join(usb, "subsystem")))
	sysfsLink(t, root, "class/net/usb0/device", "../../../devices/usb1")
	nics, err = s.NICs()
	require.NoError(t, err)
	require.Len(t, nics, 1)

	// Do not turn unexpected filesystem entries into successful empty discovery.
	require.NoError(t, os.MkdirAll(filepath.Join(root, "class/net/broken/device"), 0o755))
	_, err = s.NICs()
	require.ErrorContains(t, err, "reading PCI identity")
	_, err = (Sysfs{Root: t.TempDir()}).NICs()
	require.ErrorContains(t, err, "enumerating host NICs")
}

// staticTopology is a GPUTopology that returns fixed results.
type staticTopology struct {
	inv Inventory
	m   *Matrix
	err error
}

func (s staticTopology) Collect() (Inventory, *Matrix, error) { return s.inv, s.m, s.err }

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
	c := &Collector{
		LLDP: NewLLDPRunner(func(_ context.Context, exe string, args []string) (string, error) {
			require.Equal(t, "lldpcli", exe)
			require.Equal(t, []string{"-u", "/host/lldpd/lldpd.socket", "-f", "json0", "show", "neighbors", "details"}, args)
			return lldpJSON, nil
		}, "/host/lldpd/lldpd.socket"),
		GPU:   staticTopology{inv: mustInventory(t, inventory), m: mustMatrix(t, ddMatrix)},
		Sysfs: Sysfs{Root: root},
		Now:   func() time.Time { return time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC) },
	}
	st, err := c.Collect(context.Background())
	require.NoError(t, err)
	require.Equal(t, "2026-01-02T03:04:05Z", st.Sources.LLDP)
	require.Equal(t, "2026-01-02T03:04:05Z", st.Sources.PCITopology)
	require.Equal(t, ModeDataDirect, st.NICs["0000:3b:00.0"].MappingMode)
	require.Equal(t, "GPU-aaa1", st.NICs["0000:3b:00.0"].BestGPUs[0].UUID)
	require.Equal(t, "DD1", st.NICs["0000:5e:00.0"].TopologyTarget)
	require.Equal(t, "leaf-7", st.NICs["0000:3b:00.0"].SwitchName)

	// A failed GPU topology fails rather than publishing a NIC-only mapping.
	gpu := c.GPU
	c.GPU = staticTopology{err: errors.New("no NVIDIA GPUs bound to the nvidia driver")}
	st, err = c.Collect(context.Background())
	require.ErrorContains(t, err, "GPU topology: no NVIDIA GPUs")
	require.Nil(t, st)

	// LLDP neighbors without chassis names must preserve both GPU mappings.
	c.GPU = gpu
	c.LLDP = fakeRunner{"lldpcli -f": strings.ReplaceAll(strings.ReplaceAll(lldpJSON, "leaf-7", ""), "leaf-8", "")}
	st, err = c.Collect(context.Background())
	require.NoError(t, err)
	require.Len(t, st.NICs, 2)
	require.Empty(t, st.NICs["0000:3b:00.0"].SwitchName)
	require.Equal(t, ModeDataDirect, st.NICs["0000:3b:00.0"].MappingMode)
	require.Equal(t, "GPU-aaa1", st.NICs["0000:3b:00.0"].BestGPUs[0].UUID)
	// Errors must not be mistaken for absent neighbors.
	c.LLDP = fakeRunner{}
	st, err = c.Collect(context.Background())
	require.ErrorContains(t, err, "lldp")
	require.Nil(t, st)
	c.LLDP = fakeRunner{"lldpcli -f": `malformed`}
	st, err = c.Collect(context.Background())
	require.ErrorContains(t, err, "JSON0")
	require.Nil(t, st)
	// A successful LLDP refresh with no neighbors removes the record, even
	// when PCI-backed NICs still exist.
	c.LLDP = fakeRunner{"lldpcli -f": `{"lldp":[]}`}
	st, err = c.Collect(context.Background())
	require.NoError(t, err)
	require.Nil(t, st)

	// Losing one neighbor excludes only that NIC and preserves other mappings.
	c.LLDP = fakeRunner{"lldpcli -f": `{"lldp":[{"interface":[{"name":"roce_vf_r0","chassis":[{"name":[{"value":"leaf"}]}]}]}]}`}
	st, err = c.Collect(context.Background())
	require.NoError(t, err)
	require.Len(t, st.NICs, 1)
	require.Equal(t, "GPU-aaa1", st.NICs["0000:3b:00.0"].BestGPUs[0].UUID)
}

func TestStatusValidate(t *testing.T) {
	ok := func() Status {
		return Status{NICs: map[string]NICMapping{
			"0000:3b:00.0": {Interface: "a", SwitchName: "r0", MappingMode: ModeNone, BestGPUs: []GPU{}},
		}}
	}
	s := ok()
	require.NoError(t, s.Validate())

	s = ok()
	s.NICs["0000:3b:01.0"] = NICMapping{Interface: "b", SwitchName: "r0", MappingMode: ModeNone}
	require.NoError(t, s.Validate(), "NICs may share a switch name")

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
		Interface: "a", SwitchName: "r", MappingMode: ModeNIC, TopologyTarget: "NIC0", BestPath: "PIX",
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
			got, err := Select([]Binding{{Interface: "eth_r0_p0", PCI: tc.bound, SwitchName: "rail-0-port-0"}}, tc.resolver, inv, m)
			if tc.ambiguous {
				require.ErrorContains(t, err, "ambiguous topology legend entries")
				require.Nil(t, got)
				return
			}
			require.NoError(t, err)
			require.Len(t, got, 1)
			n := got[tc.bound]
			require.Equal(t, "eth_r0_p0", n.Interface)
			require.Equal(t, "rail-0-port-0", n.SwitchName)
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
	c := &Collector{
		LLDP: fakeRunner{"lldpcli -f": `{"lldp":[{"interface":[{"name":"eth_r0_p0","chassis":[{"name":[{"value":"leaf-01"}]}]}]}]}`},
		GPU: staticTopology{inv: mustInventory(t, inventory), m: mustMatrix(t, `GPU0 GPU1 NIC0
GPU0 X NODE NODE
GPU1 NODE X NODE
NIC0 NODE NODE X
NIC Legend:
NIC0: mlx5_0 enP1p3s0f0v0 0001:03:00.4 N/A
`)},
		Sysfs: Sysfs{Root: root},
	}
	st, err := c.Collect(context.Background())
	require.NoError(t, err)
	require.Len(t, st.NICs, 1)
	n := st.NICs["0001:03:00.0"]
	require.Equal(t, "eth_r0_p0", n.Interface)
	require.Equal(t, "leaf-01", n.SwitchName)
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
