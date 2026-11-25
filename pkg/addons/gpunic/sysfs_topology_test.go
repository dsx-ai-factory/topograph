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
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/dsx-ai-factory/topograph/pkg/pcitopo"
	"github.com/dsx-ai-factory/topograph/pkg/pcitopo/pcitopotest"
)

// ddSysfs builds a host whose PCI hierarchy produces ddMatrix and whose GPUs
// match the inventory fixture. Each CX-8 adapter has a PF with the VPD V3
// UUID, an RDMA VF (roce_vf_rN), and a Data Direct function that shares a
// switch with one GPU behind a nested switch:
//
//	NUMA 0: pci0000:10  RP - USP - DSP - DD (r1)
//	                             - DSP - USP - DSP - GPU0 16:00.0
//	        pci0000:1a  RP - USP - DSP - DD (r0)
//	                             - DSP - USP - DSP - GPU1 17:00.0
//	        pci0000:30  RP - PF/VF r0
//	        pci0000:38  RP - PF/VF r1
//	NUMA 1: pci0000:90  same shape as pci0000:10, DD (r3) and GPU2 98:00.0
//	        pci0000:b0  same shape as pci0000:1a, DD (r2) and GPU3 99:00.0
//	        pci0000:a0  RP - USP - DSP - mlx5_8, mlx5_9 (two ports, one card)
//	        pci0000:c0  RP - PF/VF r2
//	        pci0000:d0  RP - PF/VF r3
func ddSysfs(t *testing.T) (sysRoot, procRoot string) {
	s := pcitopotest.New(t)
	procRoot = filepath.Join(t.TempDir(), "proc")

	complexWithGPU := func(hb string, bus byte, numa int, v3, gpuAddr, uuid string) {
		b := func(off byte, fn string) string { return pciAddr(bus+off, fn) }
		s.Add(pcitopotest.Function{
			HostBridge: hb, Chain: []string{b(0, "01.0"), b(1, "00.0"), b(2, "00.0"), b(3, "00.0")},
			Vendor: pcitopo.VendorMellanox, Device: pcitopo.DeviceCX8DataDirect, Class: 0x020700, NUMANode: numa,
			Driver: "mlx5_data_direct", VPD: pcitopotest.VPD("CX8", "V3", v3),
		})
		s.Add(pcitopotest.Function{
			HostBridge: hb, Chain: []string{b(0, "01.0"), b(1, "00.0"), b(2, "01.0"), b(4, "00.0"), b(5, "00.0"), gpuAddr},
			Vendor: pcitopo.VendorNVIDIA, Device: 0x2941, Class: 0x030200, NUMANode: numa, Driver: "nvidia",
		})
		dir := filepath.Join(procRoot, "driver", "nvidia", "gpus", gpuAddr)
		require.NoError(t, os.MkdirAll(dir, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "information"),
			[]byte("Model: \t\t NVIDIA GB300\nIRQ:   \t\t 123\nGPU UUID: \t "+uuid+"\nVideo BIOS: \t ??.??.??.??.??\n"), 0o644))
	}
	adapter := func(hb string, bus byte, numa int, v3, rdma string) {
		pf := pciAddr(bus+1, "00.0")
		s.Add(pcitopotest.Function{
			HostBridge: hb, Chain: []string{pciAddr(bus, "01.0"), pf},
			Vendor: pcitopo.VendorMellanox, Device: 0x1023, Class: 0x020000, NUMANode: numa,
			Net: []string{"pf_" + rdma}, VPD: pcitopotest.VPD("CX8", "V3", v3),
		})
		s.Add(pcitopotest.Function{
			HostBridge: hb, Chain: []string{pciAddr(bus, "01.0"), pciAddr(bus+1, "00.2")},
			Vendor: pcitopo.VendorMellanox, Device: 0x101e, Class: 0x020000, NUMANode: numa,
			PhysFn: pf, RDMA: []string{rdma}, Net: []string{rdma},
		})
	}

	complexWithGPU("pci0000:10", 0x10, 0, "uuid-r1", "0000:16:00.0", "GPU-bbb0")
	complexWithGPU("pci0000:1a", 0x1a, 0, "uuid-r0", "0000:17:00.0", "GPU-aaa1")
	complexWithGPU("pci0000:90", 0x90, 1, "uuid-r3", "0000:98:00.0", "GPU-ccc2")
	complexWithGPU("pci0000:b0", 0xb0, 1, "uuid-r2", "0000:99:00.0", "GPU-ddd3")
	adapter("pci0000:30", 0x30, 0, "uuid-r0", "roce_vf_r0")
	adapter("pci0000:38", 0x38, 0, "uuid-r1", "roce_vf_r1")
	adapter("pci0000:c0", 0xc0, 1, "uuid-r2", "roce_vf_r2")
	adapter("pci0000:d0", 0xd0, 1, "uuid-r3", "roce_vf_r3")
	for i, name := range []string{"mlx5_8", "mlx5_9"} {
		s.Add(pcitopotest.Function{
			HostBridge: "pci0000:a0", Chain: []string{"0000:a0:01.0", "0000:a1:00.0", "0000:a2:00.0", pciAddr(0xa3, "00."+string(rune('0'+i)))},
			Vendor: pcitopo.VendorMellanox, Device: 0xa2dc, Class: 0x020000, NUMANode: 1, RDMA: []string{name},
		})
	}
	return s.Root, procRoot
}

func pciAddr(bus byte, devfn string) string {
	return "0000:" + hex2(bus) + ":" + devfn
}

func hex2(b byte) string {
	const digits = "0123456789abcdef"
	return string([]byte{digits[b>>4], digits[b&0xf]})
}

// rowsByName keys the NIC and DD rows by legend name, restricted to GPU columns.
func rowsByName(t *testing.T, m *Matrix) (nics, dds map[string]map[string]string) {
	pick := func(legend map[string]string) map[string]map[string]string {
		out := map[string]map[string]string{}
		for alias, name := range legend {
			row := map[string]string{}
			for _, col := range m.Cols {
				if _, ok := gpuIndex(col); ok {
					row[col] = m.Cells[alias][col]
				}
			}
			require.NotContains(t, out, name)
			out[name] = row
		}
		return out
	}
	return pick(m.NICs), pick(m.DDs)
}

func TestSysfsTopologyMatchesNvidiaSMI(t *testing.T) {
	sysRoot, procRoot := ddSysfs(t)
	gotInv, got, err := SysfsTopology{SysRoot: sysRoot, ProcRoot: procRoot}.Collect()
	require.NoError(t, err)

	wantInv, err := ParseInventory(inventory)
	require.NoError(t, err)
	want, err := ParseMatrix(ddMatrix)
	require.NoError(t, err)

	require.Equal(t, wantInv, gotInv)
	gotNICs, gotDDs := rowsByName(t, got)
	wantNICs, wantDDs := rowsByName(t, want)
	require.Len(t, wantNICs, 6)
	require.Len(t, wantDDs, 4)
	require.Equal(t, wantNICs, gotNICs)
	require.Equal(t, wantDDs, gotDDs)

	// Select reaches the same decisions from either source. NIC aliases are
	// assigned in a different order, so targets are compared by legend name.
	rdma := map[string][]string{"eth_r0": {"roce_vf_r0"}, "eth_r1": {"roce_vf_r1"}, "eth_r2": {"roce_vf_r2"}, "eth_r3": {"roce_vf_r3"}, "eth8": {"mlx5_8"}}
	resolver := testNICResolver{rdma: func(iface string) []string { return rdma[iface] }}
	var bindings []Binding
	for i, iface := range []string{"eth_r0", "eth_r1", "eth_r2", "eth_r3", "eth8"} {
		bindings = append(bindings, Binding{Interface: iface, PCI: pciAddr(byte(0xe0+i), "00.0"), SwitchName: "rail"})
	}
	byName := func(inv Inventory, m *Matrix) map[string]NICMapping {
		out, err := Select(bindings, resolver, inv, m)
		require.NoError(t, err)
		for pci, nm := range out {
			if name, ok := m.DDs[nm.TopologyTarget]; ok {
				nm.TopologyTarget = name
			} else if name, ok := m.NICs[nm.TopologyTarget]; ok {
				nm.TopologyTarget = name
			}
			out[pci] = nm
		}
		return out
	}
	wantSel, gotSel := byName(wantInv, want), byName(gotInv, got)
	require.Equal(t, wantSel, gotSel)
	r0 := gotSel[pciAddr(0xe0, "00.0")]
	require.Equal(t, ModeDataDirect, r0.MappingMode)
	require.Equal(t, "PXB", r0.BestPath)
	require.Equal(t, []GPU{{UUID: "GPU-aaa1", PCIAddress: "0000:17:00.0"}}, r0.BestGPUs)
}

func TestSysfsTopologyErrors(t *testing.T) {
	t.Run("no GPUs", func(t *testing.T) {
		s := pcitopotest.New(t)
		_, _, err := SysfsTopology{SysRoot: s.Root, ProcRoot: t.TempDir()}.Collect()
		require.ErrorContains(t, err, "no NVIDIA GPUs")
	})
	t.Run("GPU not bound to nvidia is skipped", func(t *testing.T) {
		s := pcitopotest.New(t).Add(pcitopotest.Function{
			HostBridge: "pci0000:10", Chain: []string{"0000:10:01.0", "0000:11:00.0"},
			Vendor: pcitopo.VendorNVIDIA, Device: 0x2330, Class: 0x030200, Driver: "vfio-pci",
		})
		_, _, err := SysfsTopology{SysRoot: s.Root, ProcRoot: t.TempDir()}.Collect()
		require.ErrorContains(t, err, "no NVIDIA GPUs")
	})
	t.Run("missing UUID", func(t *testing.T) {
		sysRoot, _ := ddSysfs(t)
		_, _, err := SysfsTopology{SysRoot: sysRoot, ProcRoot: t.TempDir()}.Collect()
		require.ErrorContains(t, err, "reading GPU UUID")
	})
}

func TestSysfsTopologyFromVPDCache(t *testing.T) {
	sysRoot, procRoot := ddSysfs(t)
	direct, wantM, err := SysfsTopology{SysRoot: sysRoot, ProcRoot: procRoot}.Collect()
	require.NoError(t, err)

	// The init container reads VPD with root privileges...
	cachePath := filepath.Join(t.TempDir(), "vpd.json")
	cache, err := WriteVPDCache(sysRoot, cachePath)
	require.NoError(t, err)
	require.Len(t, cache.Functions, 8, "four Data Direct functions and four PFs")
	require.Empty(t, cache.Errors)
	info, err := os.Stat(cachePath)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o644), info.Mode().Perm())

	// ...and the unprivileged broker can no longer read it from sysfs.
	require.NoError(t, filepath.Walk(filepath.Join(sysRoot, "devices"), func(path string, fi os.FileInfo, err error) error {
		if err == nil && fi.Name() == "vpd" {
			return os.Remove(path)
		}
		return err
	}))
	_, _, err = SysfsTopology{SysRoot: sysRoot, ProcRoot: procRoot}.Collect()
	require.ErrorContains(t, err, "has no VPD V3 UUID")

	inv, m, err := SysfsTopology{SysRoot: sysRoot, ProcRoot: procRoot, VPDCachePath: cachePath}.Collect()
	require.NoError(t, err)
	require.Equal(t, direct, inv)
	require.Equal(t, wantM, m)

	// A host with Data Direct functions needs the cache once it is configured.
	_, _, err = SysfsTopology{SysRoot: sysRoot, ProcRoot: procRoot, VPDCachePath: filepath.Join(t.TempDir(), "missing")}.Collect()
	require.ErrorContains(t, err, "reading VPD cache")
}

func TestSysfsTopologyIgnoresMissingVPDCacheWithoutDataDirect(t *testing.T) {
	s := pcitopotest.New(t).Add(pcitopotest.Function{
		HostBridge: "pci0000:10", Chain: []string{"0000:10:01.0", "0000:11:00.0"},
		Vendor: pcitopo.VendorNVIDIA, Device: 0x2330, Class: 0x030200, Driver: "nvidia",
	})
	procRoot := t.TempDir()
	dir := filepath.Join(procRoot, "driver", "nvidia", "gpus", "0000:11:00.0")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "information"), []byte("GPU UUID: GPU-x\n"), 0o644))
	inv, _, err := SysfsTopology{SysRoot: s.Root, ProcRoot: procRoot, VPDCachePath: filepath.Join(t.TempDir(), "missing")}.Collect()
	require.NoError(t, err)
	require.Equal(t, Inventory{0: {UUID: "GPU-x", PCIAddress: "0000:11:00.0"}}, inv)
}
