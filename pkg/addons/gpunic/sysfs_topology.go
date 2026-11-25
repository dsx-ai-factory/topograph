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
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/dsx-ai-factory/topograph/pkg/pcitopo"
)

// nvidiaDriver is the kernel driver a GPU must be bound to for NVML, and so
// nvidia-smi, to list it. A GPU bound to vfio-pci for passthrough is skipped.
const nvidiaDriver = "nvidia"

// SysfsTopology builds the GPU inventory and the topology matrix from host
// sysfs and the NVIDIA driver's procfs entries, without nvidia-smi.
//
// It reproduces only the part of `nvidia-smi topo -m` that Select consumes:
// one row per RDMA NIC function and per Data Direct function, against the
// GPU columns, and the NIC and DD legends. GPU-to-GPU (NVLink) cells and the
// CPU and NUMA affinity columns are not produced, so Cols lists GPUs only.
//
// Aliases are assigned in PCI address order for GPUs and NICs, and in the
// order of the paired NIC for Data Direct functions. nvidia-smi does not
// document its order, so an alias may differ from the one nvidia-smi prints
// for the same device; compare results by legend name, not by alias.
type SysfsTopology struct {
	// SysRoot is the host sysfs mount, for example /host/sys.
	SysRoot string
	// ProcRoot is a procfs mount that shows the NVIDIA driver's
	// driver/nvidia/gpus entries, for example /proc.
	ProcRoot string
	// VPDCachePath, when set, is a file written by WriteVPDCache. It replaces
	// sysfs as the source of VPD, which only root can read. The file is read
	// only on hosts with Data Direct functions, the only ones that need VPD.
	VPDCachePath string
}

// WriteVPDCache reads the VPD that Data Direct pairing needs from the host
// sysfs at sysRoot and writes it to path. It runs with the privileges that
// reading VPD requires, typically in an init container, so that the broker
// itself can run without them. Per-function read errors are recorded in the
// file and reported by the broker only if pairing needs that function.
func WriteVPDCache(sysRoot, path string) (*pcitopo.VPDCache, error) {
	topo, err := pcitopo.Load(sysRoot)
	if err != nil {
		return nil, err
	}
	cache := topo.ReadVPDCache()
	b, err := json.Marshal(cache)
	if err != nil {
		return nil, err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".vpd-*")
	if err != nil {
		return nil, fmt.Errorf("writing VPD cache: %w", err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		return nil, fmt.Errorf("writing VPD cache: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return nil, fmt.Errorf("writing VPD cache: %w", err)
	}
	// The broker runs as a different, unprivileged user.
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return nil, fmt.Errorf("writing VPD cache: %w", err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return nil, fmt.Errorf("writing VPD cache: %w", err)
	}
	return cache, nil
}

func readVPDCache(path string) (*pcitopo.VPDCache, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading VPD cache: %w", err)
	}
	cache := &pcitopo.VPDCache{}
	if err := json.Unmarshal(b, cache); err != nil {
		return nil, fmt.Errorf("reading VPD cache %s: %w", path, err)
	}
	return cache, nil
}

// Collect returns the inventory and matrix of the local host.
func (s SysfsTopology) Collect() (Inventory, *Matrix, error) {
	topo, err := pcitopo.Load(s.SysRoot)
	if err != nil {
		return nil, nil, err
	}
	if s.VPDCachePath != "" && len(topo.Sorted((*pcitopo.Device).IsDataDirect)) != 0 {
		if topo.VPDCache, err = readVPDCache(s.VPDCachePath); err != nil {
			return nil, nil, err
		}
	}
	gpus := topo.Sorted(func(d *pcitopo.Device) bool { return d.IsNVIDIAGPU() && d.Driver == nvidiaDriver })
	if len(gpus) == 0 {
		return nil, nil, fmt.Errorf("no NVIDIA GPUs bound to the %s driver", nvidiaDriver)
	}

	inv := make(Inventory, len(gpus))
	m := &Matrix{Cells: map[string]map[string]string{}, NICs: map[string]string{}, DDs: map[string]string{}}
	for i, g := range gpus {
		pci, err := NormalizePCI(g.Address)
		if err != nil {
			return nil, nil, err
		}
		uuid, err := s.gpuUUID(g.Address)
		if err != nil {
			return nil, nil, err
		}
		inv[i] = GPU{UUID: uuid, PCIAddress: pci}
		m.Cols = append(m.Cols, fmt.Sprintf("GPU%d", i))
	}

	row := func(alias, addr string) error {
		cells := make(map[string]string, len(gpus))
		for i, g := range gpus {
			path, err := topo.Path(addr, g.Address)
			if err != nil {
				return err
			}
			cells[m.Cols[i]] = path
		}
		m.Cells[alias] = cells
		return nil
	}

	// nvidia-smi lists RDMA devices, not every network interface.
	nics := topo.Sorted(func(d *pcitopo.Device) bool { return len(d.RDMA) > 0 && !d.IsDataDirect() })
	for i, n := range nics {
		alias := fmt.Sprintf("NIC%d", i)
		m.NICs[alias] = n.RDMA[0]
		if err := row(alias, n.Address); err != nil {
			return nil, nil, err
		}
	}

	pairs, err := topo.DataDirectPairs()
	if err != nil {
		return nil, nil, err
	}
	ddByFn := map[string][]string{}
	for dd, fns := range pairs {
		for _, fn := range fns {
			ddByFn[fn] = append(ddByFn[fn], dd)
		}
	}
	// As in the nvidia-smi legend, a Data Direct entry is named after the
	// NIC it serves. Every RDMA function of the adapter gets its own entry so
	// that each one resolves by name; the entries share the DMA function's row.
	k := 0
	for _, n := range nics {
		dds := ddByFn[n.Address]
		sort.Strings(dds)
		for _, dd := range dds {
			alias := fmt.Sprintf("DD%d", k)
			k++
			m.DDs[alias] = n.RDMA[0]
			if err := row(alias, dd); err != nil {
				return nil, nil, err
			}
		}
	}
	return inv, m, nil
}

// gpuUUID reads the UUID from driver/nvidia/gpus/<pci>/information, which
// has a line such as "GPU UUID:        GPU-5f9c...".
func (s SysfsTopology) gpuUUID(addr string) (string, error) {
	path := filepath.Join(s.ProcRoot, "driver", "nvidia", "gpus", addr, "information")
	b, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("reading GPU UUID: %w", err)
	}
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		if v, ok := strings.CutPrefix(sc.Text(), "GPU UUID:"); ok {
			if uuid := strings.TrimSpace(v); uuid != "" {
				return uuid, nil
			}
		}
	}
	return "", fmt.Errorf("no GPU UUID in %s", path)
}

// Inventory maps a GPU matrix index to its stable identity.
type Inventory map[int]GPU

// Matrix holds the path category between topology devices, in the shape of
// the `nvidia-smi topo -m` matrix: GPUn columns, NICn and DDn rows, and
// legends that name the device behind each NIC and DD alias.
type Matrix struct {
	Cols []string
	// Cells[row][col] is the path category between two matrix devices.
	Cells map[string]map[string]string
	// NICs and DDs map a matrix alias (NIC2, DD0) to the legend's device name.
	NICs map[string]string
	DDs  map[string]string
}

var gpuRE = regexp.MustCompile(`^GPU(\d+)$`)

func gpuIndex(alias string) (int, bool) {
	g := gpuRE.FindStringSubmatch(alias)
	if g == nil {
		return 0, false
	}
	i, _ := strconv.Atoi(g[1])
	return i, true
}
