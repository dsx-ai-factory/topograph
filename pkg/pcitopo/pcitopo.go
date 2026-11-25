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

// Package pcitopo reads the host PCI hierarchy from sysfs and classifies the
// path between two PCI functions with the categories that `nvidia-smi topo -m`
// prints (PIX, PXB, PHB, NODE, SYS).
//
// The package only reads sysfs. It does not load a vendor library, run a
// command, or read PCI configuration space, so it works without cgo and
// without privileges, except for VPD, which the kernel exposes to root only.
// VPD is needed only to pair a ConnectX Data Direct function with its NIC.
package pcitopo

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Path categories, closest first, as defined by the `nvidia-smi topo -m` legend.
const (
	// PathPIX traverses at most a single PCIe bridge or switch.
	PathPIX = "PIX"
	// PathPXB traverses multiple PCIe bridges or switches, without the host bridge.
	PathPXB = "PXB"
	// PathPHB traverses a PCIe host bridge.
	PathPHB = "PHB"
	// PathNODE traverses the interconnect between host bridges within a NUMA node.
	PathNODE = "NODE"
	// PathSYS traverses the SMP interconnect between NUMA nodes.
	PathSYS = "SYS"
)

// PCI identifiers.
const (
	VendorNVIDIA   = 0x10de
	VendorMellanox = 0x15b3

	// DeviceCX8DataDirect is the ConnectX-8 DMA (Data Direct) function. The
	// value matches the `cx_dma` entry of rdma-core's rdma_topo.
	DeviceCX8DataDirect = 0x2100

	// classDisplay is the PCI base class of display controllers, which
	// includes NVIDIA VGA (0x0300) and 3D (0x0302) controllers.
	classDisplay = 0x03
)

var (
	// bdfRE matches a sysfs PCI function name. VMD domains have five or more
	// hex digits.
	bdfRE = regexp.MustCompile(`^[0-9a-f]{4,8}:[0-9a-f]{2}:[0-9a-f]{2}\.[0-7]$`)
	// hostBridgeRE matches a sysfs PCI root bus directory, such as pci0000:17.
	hostBridgeRE = regexp.MustCompile(`^pci[0-9a-f]{4,8}:[0-9a-f]{2}$`)
)

// Device is one PCI function.
type Device struct {
	// Address is the sysfs name of the function, for example 0000:17:00.0.
	Address  string
	Vendor   uint16
	DeviceID uint16
	// Class is the 24-bit class code: base class, subclass, programming interface.
	Class uint32
	// NUMANode is the node the kernel reports, or -1 when it is unknown.
	NUMANode int
	// HostBridge is the PCI root bus the function sits under, for example pci0000:17.
	HostBridge string
	// Chain lists the functions from the root port down to and including this
	// one. Every entry but the last is a bridge.
	Chain []string
	// PhysFn is the physical function of an SR-IOV virtual function, or empty.
	PhysFn string
	// Driver is the name of the bound kernel driver, or empty.
	Driver string
	// RDMA and Net are the RDMA and network devices of the function, sorted.
	RDMA []string
	Net  []string

	dir string
}

// IsNVIDIAGPU reports whether d is an NVIDIA display controller.
func (d *Device) IsNVIDIAGPU() bool {
	return d.Vendor == VendorNVIDIA && d.Class>>16 == classDisplay
}

// IsDataDirect reports whether d is a ConnectX Data Direct function.
func (d *Device) IsDataDirect() bool {
	return d.Vendor == VendorMellanox && d.DeviceID == DeviceCX8DataDirect
}

// Topology is the PCI hierarchy of one host.
type Topology struct {
	// Devices maps a PCI address to its function.
	Devices map[string]*Device
	// VPDCache, when set, replaces sysfs as the source of VPD.
	VPDCache *VPDCache
}

// Load reads every PCI function under sysRoot/bus/pci/devices. sysRoot is the
// sysfs mount, for example /sys, or /host/sys inside a container.
//
// Device links are read, not followed, to locate a function in the
// hierarchy; attribute reads go through the relative link and so stay within
// sysRoot. A missing optional attribute is not an error, an unreadable one is.
func Load(sysRoot string) (*Topology, error) {
	base := filepath.Join(sysRoot, "bus", "pci", "devices")
	entries, err := os.ReadDir(base)
	if err != nil {
		return nil, fmt.Errorf("enumerating PCI devices: %w", err)
	}
	t := &Topology{Devices: make(map[string]*Device, len(entries))}
	for _, e := range entries {
		addr := e.Name()
		if !bdfRE.MatchString(addr) {
			continue
		}
		d, err := loadDevice(filepath.Join(base, addr), addr)
		if err != nil {
			return nil, fmt.Errorf("PCI device %s: %w", addr, err)
		}
		t.Devices[addr] = d
	}
	return t, nil
}

func loadDevice(dir, addr string) (*Device, error) {
	link, err := os.Readlink(dir)
	if err != nil {
		return nil, err
	}
	hb, chain, err := parseDevicePath(link)
	if err != nil {
		return nil, err
	}
	if chain[len(chain)-1] != addr {
		return nil, fmt.Errorf("sysfs link %q does not end in the device address", link)
	}
	d := &Device{Address: addr, HostBridge: hb, Chain: chain, NUMANode: -1, dir: dir}

	vendor, err := readHex(dir, "vendor", 16)
	if err != nil {
		return nil, err
	}
	device, err := readHex(dir, "device", 16)
	if err != nil {
		return nil, err
	}
	class, err := readHex(dir, "class", 24)
	if err != nil {
		return nil, err
	}
	d.Vendor, d.DeviceID, d.Class = uint16(vendor), uint16(device), uint32(class)

	switch b, err := os.ReadFile(filepath.Join(dir, "numa_node")); {
	case err == nil:
		n, err := strconv.Atoi(strings.TrimSpace(string(b)))
		if err != nil {
			return nil, fmt.Errorf("invalid numa_node %q", strings.TrimSpace(string(b)))
		}
		if n >= 0 {
			d.NUMANode = n
		}
	case !errors.Is(err, fs.ErrNotExist):
		return nil, err
	}

	if d.PhysFn, err = readLinkBase(filepath.Join(dir, "physfn")); err != nil {
		return nil, err
	}
	if d.Driver, err = readLinkBase(filepath.Join(dir, "driver")); err != nil {
		return nil, err
	}
	if d.RDMA, err = readNames(filepath.Join(dir, "infiniband")); err != nil {
		return nil, err
	}
	if d.Net, err = readNames(filepath.Join(dir, "net")); err != nil {
		return nil, err
	}
	return d, nil
}

// parseDevicePath splits a sysfs device link such as
// ../../../devices/pci0000:17/0000:17:01.0/0000:18:00.0 into the host bridge
// and the chain of functions below it. The host bridge is the last root bus
// before the final run of functions, so a VMD domain nested behind another
// function resolves to the VMD root bus.
func parseDevicePath(link string) (string, []string, error) {
	parts := strings.Split(filepath.ToSlash(link), "/")
	end := len(parts)
	start := end
	for start > 0 && bdfRE.MatchString(parts[start-1]) {
		start--
	}
	if start == end || start == 0 || !hostBridgeRE.MatchString(parts[start-1]) {
		return "", nil, fmt.Errorf("unrecognized sysfs device path %q", link)
	}
	return parts[start-1], parts[start:end], nil
}

// Path classifies the path between the functions at addresses a and b.
func (t *Topology) Path(a, b string) (string, error) {
	da, ok := t.Devices[a]
	if !ok {
		return "", fmt.Errorf("unknown PCI device %s", a)
	}
	db, ok := t.Devices[b]
	if !ok {
		return "", fmt.Errorf("unknown PCI device %s", b)
	}
	return classify(da, db), nil
}

// classify implements the nvidia-smi categories on the sysfs hierarchy:
//
//   - Functions under different host bridges are NODE when the kernel places
//     both on the same NUMA node and SYS otherwise, including when either
//     NUMA node is unknown, so that an unknown node never claims locality.
//   - Functions under the same host bridge whose chains share no function
//     meet only at the host bridge: PHB.
//   - Otherwise the path runs through their deepest common function. A PCIe
//     switch is one upstream port and its downstream ports; it counts as one
//     bridge, as does a root port. One bridge is PIX; more than one is PXB.
func classify(a, b *Device) string {
	if a.HostBridge != b.HostBridge {
		if a.NUMANode >= 0 && a.NUMANode == b.NUMANode {
			return PathNODE
		}
		return PathSYS
	}
	common := 0
	for common < len(a.Chain) && common < len(b.Chain) && a.Chain[common] == b.Chain[common] {
		common++
	}
	if common == 0 {
		return PathPHB
	}
	bridges := map[string]bool{}
	add := func(chain []string, from, to int) {
		for i := from; i < to; i++ {
			bridges[bridgeKey(chain, i)] = true
		}
	}
	// The deepest common function, unless it is one of the endpoints.
	lca := common - 1
	if lca < len(a.Chain)-1 && lca < len(b.Chain)-1 {
		add(a.Chain, lca, lca+1)
	}
	add(a.Chain, common, len(a.Chain)-1)
	add(b.Chain, common, len(b.Chain)-1)
	if len(bridges) <= 1 {
		return PathPIX
	}
	return PathPXB
}

// bridgeKey groups the bridge at chain[i] with the bridges of the same
// device. Below a root port (index 0), bridges alternate between a switch
// upstream port (odd index) and one of its downstream ports (even index), so
// a downstream port belongs to the upstream port directly above it.
func bridgeKey(chain []string, i int) string {
	if i > 0 && i%2 == 0 {
		return chain[i-1]
	}
	return chain[i]
}

// Sorted returns the functions that match keep, ordered by PCI address.
func (t *Topology) Sorted(keep func(*Device) bool) []*Device {
	var out []*Device
	for _, d := range t.Devices {
		if keep(d) {
			out = append(out, d)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Address < out[j].Address })
	return out
}

func readHex(dir, name string, bits int) (uint64, error) {
	b, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		return 0, err
	}
	s := strings.TrimSpace(string(b))
	v, err := strconv.ParseUint(strings.TrimPrefix(s, "0x"), 16, bits)
	if err != nil {
		return 0, fmt.Errorf("invalid %s %q", name, s)
	}
	return v, nil
}

// readLinkBase returns the final component of a symlink, or empty when the
// link does not exist.
func readLinkBase(path string) (string, error) {
	link, err := os.Readlink(path)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return filepath.Base(link), nil
}

// readNames returns the sorted entry names of dir, or nil when it does not exist.
func readNames(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names, nil
}
