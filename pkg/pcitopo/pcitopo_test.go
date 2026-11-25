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

package pcitopo_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/dsx-ai-factory/topograph/pkg/pcitopo"
	"github.com/dsx-ai-factory/topograph/pkg/pcitopo/pcitopotest"
)

const (
	gpuClass = 0x030200
	nicClass = 0x020000
)

func gpu(hb string, numa int, chain ...string) pcitopotest.Function {
	return pcitopotest.Function{HostBridge: hb, Chain: chain, Vendor: pcitopo.VendorNVIDIA, Device: 0x2330, Class: gpuClass, NUMANode: numa, Driver: "nvidia"}
}

func nic(hb string, numa int, rdma string, chain ...string) pcitopotest.Function {
	return pcitopotest.Function{HostBridge: hb, Chain: chain, Vendor: pcitopo.VendorMellanox, Device: 0x1021, Class: nicClass, NUMANode: numa, RDMA: []string{rdma}}
}

// pathFixture covers every category:
//
//	pci0000:10 (NUMA 0)
//	  RP 10:01.0 - USP 11:00.0 - DSP 12:00.0 - NIC 13:00.0, NIC 13:00.1
//	                           - DSP 12:01.0 - GPU 14:00.0
//	                           - DSP 12:02.0 - USP 15:00.0 - DSP 16:00.0 - GPU 17:00.0
//	  RP 10:02.0 - GPU 18:00.0
//	pci0000:20 (NUMA 0)
//	  RP 20:01.0 - GPU 21:00.0
//	pci0000:30 (NUMA 1)
//	  RP 30:01.0 - GPU 31:00.0
//	pci0000:40 (NUMA unknown)
//	  RP 40:01.0 - GPU 41:00.0
func pathFixture(t *testing.T) *pcitopo.Topology {
	s := pcitopotest.New(t).
		Add(nic("pci0000:10", 0, "mlx5_0", "0000:10:01.0", "0000:11:00.0", "0000:12:00.0", "0000:13:00.0")).
		Add(nic("pci0000:10", 0, "mlx5_1", "0000:10:01.0", "0000:11:00.0", "0000:12:00.0", "0000:13:00.1")).
		Add(gpu("pci0000:10", 0, "0000:10:01.0", "0000:11:00.0", "0000:12:01.0", "0000:14:00.0")).
		Add(gpu("pci0000:10", 0, "0000:10:01.0", "0000:11:00.0", "0000:12:02.0", "0000:15:00.0", "0000:16:00.0", "0000:17:00.0")).
		Add(gpu("pci0000:10", 0, "0000:10:02.0", "0000:18:00.0")).
		Add(gpu("pci0000:20", 0, "0000:20:01.0", "0000:21:00.0")).
		Add(gpu("pci0000:30", 1, "0000:30:01.0", "0000:31:00.0")).
		Add(gpu("pci0000:40", -1, "0000:40:01.0", "0000:41:00.0"))
	topo, err := pcitopo.Load(s.Root)
	require.NoError(t, err)
	return topo
}

func TestPath(t *testing.T) {
	topo := pathFixture(t)
	for _, tc := range []struct {
		name, a, b, want string
	}{
		{"functions of one device under a switch port", "0000:13:00.0", "0000:13:00.1", pcitopo.PathPIX},
		{"siblings behind one switch", "0000:13:00.0", "0000:14:00.0", pcitopo.PathPIX},
		{"nested switch", "0000:13:00.0", "0000:17:00.0", pcitopo.PathPXB},
		{"nested switch is symmetric", "0000:17:00.0", "0000:13:00.0", pcitopo.PathPXB},
		{"different root ports of one host bridge", "0000:13:00.0", "0000:18:00.0", pcitopo.PathPHB},
		{"different host bridges, same NUMA node", "0000:13:00.0", "0000:21:00.0", pcitopo.PathNODE},
		{"different NUMA nodes", "0000:13:00.0", "0000:31:00.0", pcitopo.PathSYS},
		{"unknown NUMA node never claims locality", "0000:21:00.0", "0000:41:00.0", pcitopo.PathSYS},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := topo.Path(tc.a, tc.b)
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
	_, err := topo.Path("0000:13:00.0", "0000:99:00.0")
	require.Error(t, err)
}

func TestLoad(t *testing.T) {
	topo := pathFixture(t)
	d := topo.Devices["0000:17:00.0"]
	require.NotNil(t, d)
	require.Equal(t, "pci0000:10", d.HostBridge)
	require.Equal(t, []string{"0000:10:01.0", "0000:11:00.0", "0000:12:02.0", "0000:15:00.0", "0000:16:00.0", "0000:17:00.0"}, d.Chain)
	require.True(t, d.IsNVIDIAGPU())
	require.Equal(t, "nvidia", d.Driver)
	require.Equal(t, 0, d.NUMANode)
	require.Equal(t, -1, topo.Devices["0000:41:00.0"].NUMANode)
	require.Equal(t, []string{"mlx5_0"}, topo.Devices["0000:13:00.0"].RDMA)
	// Bridges are loaded but are neither GPUs nor Data Direct functions.
	require.False(t, topo.Devices["0000:11:00.0"].IsNVIDIAGPU())
	require.Len(t, topo.Sorted((*pcitopo.Device).IsNVIDIAGPU), 6)
}

func TestLoadRejectsUnrecognizedPath(t *testing.T) {
	s := pcitopotest.New(t)
	dir := filepath.Join(s.Root, "devices", "platform", "0000:01:00.0")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.Symlink("../../../devices/platform/0000:01:00.0", filepath.Join(s.Root, "bus", "pci", "devices", "0000:01:00.0")))
	_, err := pcitopo.Load(s.Root)
	require.ErrorContains(t, err, "unrecognized sysfs device path")
}

func TestLoadVMDDomain(t *testing.T) {
	// A VMD controller exposes a nested root bus behind a function of the
	// outer hierarchy; devices behind it belong to the nested root bus.
	s := pcitopotest.New(t).
		Add(gpu("pci0000:00", 0, "0000:00:0e.0", "pci10000:e0", "10000:e0:06.0", "10000:e1:00.0"))
	topo, err := pcitopo.Load(s.Root)
	require.NoError(t, err)
	d := topo.Devices["10000:e1:00.0"]
	require.NotNil(t, d)
	require.Equal(t, "pci10000:e0", d.HostBridge)
	require.Equal(t, []string{"10000:e0:06.0", "10000:e1:00.0"}, d.Chain)
}

func TestParseVPD(t *testing.T) {
	v := pcitopo.ParseVPD(pcitopotest.VPD("ConnectX-8 ", "PN", "900-9X81", "SN", "MT2500X00001", "V3", "uuid-a\x00"))
	require.Equal(t, &pcitopo.VPD{Name: "ConnectX-8", SN: "MT2500X00001", V3: "uuid-a"}, v)

	// A truncated resource ends parsing; earlier fields are kept.
	full := pcitopotest.VPD("CX", "V3", "uuid-a")
	v = pcitopo.ParseVPD(full[:len(full)-4])
	require.Equal(t, "CX", v.Name)
	require.Empty(t, v.V3)

	require.Equal(t, &pcitopo.VPD{}, pcitopo.ParseVPD(nil))
}

func dd(hb string, numa int, v3 string, chain ...string) pcitopotest.Function {
	return pcitopotest.Function{HostBridge: hb, Chain: chain, Vendor: pcitopo.VendorMellanox, Device: pcitopo.DeviceCX8DataDirect, Class: 0x020700, NUMANode: numa, Driver: "mlx5_data_direct", VPD: pcitopotest.VPD("CX8", "V3", v3)}
}

func TestDataDirectPairs(t *testing.T) {
	pf := nic("pci0000:30", 0, "mlx5_0", "0000:30:01.0", "0000:31:00.0")
	pf.VPD = pcitopotest.VPD("CX8", "V3", "uuid-a")
	vf := nic("pci0000:30", 0, "roce_vf_r0", "0000:30:01.0", "0000:31:00.2")
	vf.PhysFn = "0000:31:00.0"
	other := nic("pci0000:40", 0, "mlx5_1", "0000:40:01.0", "0000:41:00.0")
	other.VPD = pcitopotest.VPD("CX8", "V3", "uuid-b")

	s := pcitopotest.New(t).Add(pf).Add(vf).Add(other).
		Add(dd("pci0000:10", 0, "uuid-a", "0000:10:01.0", "0000:11:00.0", "0000:12:00.0", "0000:13:00.0"))
	topo, err := pcitopo.Load(s.Root)
	require.NoError(t, err)
	pairs, err := topo.DataDirectPairs()
	require.NoError(t, err)
	require.Equal(t, map[string][]string{"0000:13:00.0": {"0000:31:00.0", "0000:31:00.2"}}, pairs)
}

func TestDataDirectPairsNone(t *testing.T) {
	pairs, err := pathFixture(t).DataDirectPairs()
	require.NoError(t, err)
	require.Nil(t, pairs)
}

func TestDataDirectPairsFailures(t *testing.T) {
	pf := nic("pci0000:30", 0, "mlx5_0", "0000:30:01.0", "0000:31:00.0")
	pf.VPD = pcitopotest.VPD("CX8", "V3", "uuid-a")

	t.Run("no matching network function", func(t *testing.T) {
		s := pcitopotest.New(t).Add(pf).Add(dd("pci0000:10", 0, "uuid-z", "0000:10:01.0", "0000:13:00.0"))
		topo, err := pcitopo.Load(s.Root)
		require.NoError(t, err)
		_, err = topo.DataDirectPairs()
		require.ErrorContains(t, err, `no network function shares VPD V3 UUID "uuid-z"`)
	})
	t.Run("no VPD", func(t *testing.T) {
		f := dd("pci0000:10", 0, "", "0000:10:01.0", "0000:13:00.0")
		f.VPD = nil
		s := pcitopotest.New(t).Add(pf).Add(f)
		topo, err := pcitopo.Load(s.Root)
		require.NoError(t, err)
		_, err = topo.DataDirectPairs()
		require.ErrorContains(t, err, "has no VPD V3 UUID")
	})
	t.Run("unreadable VPD", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root ignores file permissions")
		}
		s := pcitopotest.New(t).Add(pf).Add(dd("pci0000:10", 0, "uuid-a", "0000:10:01.0", "0000:13:00.0"))
		topo, err := pcitopo.Load(s.Root)
		require.NoError(t, err)
		require.NoError(t, os.Chmod(filepath.Join(s.Root, "devices", "pci0000:10", "0000:10:01.0", "0000:13:00.0", "vpd"), 0))
		_, err = topo.DataDirectPairs()
		require.ErrorContains(t, err, "reading VPD of 0000:13:00.0")
	})
}

func TestVPDCache(t *testing.T) {
	pf := nic("pci0000:30", 0, "mlx5_0", "0000:30:01.0", "0000:31:00.0")
	pf.VPD = pcitopotest.VPD("CX8", "V3", "uuid-a", "SN", "MT0001")
	vf := nic("pci0000:30", 0, "roce_vf_r0", "0000:30:01.0", "0000:31:00.2")
	vf.PhysFn = "0000:31:00.0"
	vf.VPD = pcitopotest.VPD("must not be read")
	s := pcitopotest.New(t).Add(pf).Add(vf).
		Add(gpu("pci0000:10", 0, "0000:10:01.0", "0000:11:00.0", "0000:12:01.0", "0000:14:00.0")).
		Add(dd("pci0000:10", 0, "uuid-a", "0000:10:01.0", "0000:11:00.0", "0000:12:00.0", "0000:13:00.0"))

	privileged, err := pcitopo.Load(s.Root)
	require.NoError(t, err)
	cache := privileged.ReadVPDCache()
	// Only Data Direct functions and Mellanox physical functions are read.
	require.Equal(t, &pcitopo.VPDCache{
		Functions: map[string]*pcitopo.VPD{
			"0000:13:00.0": {Name: "CX8", V3: "uuid-a"},
			"0000:31:00.0": {Name: "CX8", V3: "uuid-a", SN: "MT0001"},
		},
		Errors: map[string]string{},
	}, cache)

	// The broker pairs from the cache once sysfs VPD is no longer readable.
	b, err := json.Marshal(cache)
	require.NoError(t, err)
	require.NoError(t, os.Remove(filepath.Join(s.Root, "devices", "pci0000:10", "0000:10:01.0", "0000:11:00.0", "0000:12:00.0", "0000:13:00.0", "vpd")))
	broker, err := pcitopo.Load(s.Root)
	require.NoError(t, err)
	broker.VPDCache = &pcitopo.VPDCache{}
	require.NoError(t, json.Unmarshal(b, broker.VPDCache))
	pairs, err := broker.DataDirectPairs()
	require.NoError(t, err)
	require.Equal(t, map[string][]string{"0000:13:00.0": {"0000:31:00.0", "0000:31:00.2"}}, pairs)

	// A read error recorded by the cache surfaces only where it is needed.
	broker.VPDCache = &pcitopo.VPDCache{Functions: cache.Functions, Errors: map[string]string{"0000:13:00.0": "permission denied"}}
	_, err = broker.DataDirectPairs()
	require.ErrorContains(t, err, "VPD of 0000:13:00.0 was not readable: permission denied")
}

func TestReadVPDCacheRecordsErrors(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores file permissions")
	}
	s := pcitopotest.New(t).Add(dd("pci0000:10", 0, "uuid-a", "0000:10:01.0", "0000:13:00.0"))
	require.NoError(t, os.Chmod(filepath.Join(s.Root, "devices", "pci0000:10", "0000:10:01.0", "0000:13:00.0", "vpd"), 0))
	topo, err := pcitopo.Load(s.Root)
	require.NoError(t, err)
	cache := topo.ReadVPDCache()
	require.Empty(t, cache.Functions)
	require.Contains(t, cache.Errors["0000:13:00.0"], "permission denied")
}
