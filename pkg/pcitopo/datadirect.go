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

package pcitopo

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
)

// PCI VPD resource tags (PCI Local Bus Specification, VPD).
const (
	vpdLargeResource = 0x80
	vpdEndSmall      = 0x78
	vpdEndLarge      = 0x79
	vpdIDString      = 0x82
	vpdReadOnly      = 0x90
)

// VPD is the subset of a function's Vital Product Data that pairing needs.
type VPD struct {
	Name string `json:"name,omitempty"`
	// V3 is the vendor-specific UUID that ConnectX writes into every function
	// of one adapter, including its Data Direct function.
	V3 string `json:"v3,omitempty"`
	SN string `json:"sn,omitempty"`
}

// VPDCache holds VPD read in advance, so that a process that cannot read
// the root-only sysfs vpd files can still pair Data Direct functions. VPD is
// fixed by the adapter's firmware, so reading it once at startup is enough.
type VPDCache struct {
	// Functions maps a PCI address to its VPD. A function that was read and
	// exposes no VPD is absent.
	Functions map[string]*VPD `json:"functions"`
	// Errors maps a PCI address to the error that reading its VPD returned.
	Errors map[string]string `json:"errors,omitempty"`
}

// vpdCandidate reports whether DataDirectPairs may need the VPD of d:
// ConnectX Data Direct functions and the physical network functions of
// Mellanox adapters. VFs carry no VPD of their own.
func vpdCandidate(d *Device) bool {
	if d.Vendor != VendorMellanox || d.PhysFn != "" {
		return false
	}
	return d.IsDataDirect() || len(d.RDMA)+len(d.Net) > 0
}

// ReadVPDCache reads the VPD of every function that DataDirectPairs may
// consult. A function whose VPD cannot be read is recorded in Errors rather
// than failing the whole read, so that one faulty function only affects the
// pairing that actually needs it.
func (t *Topology) ReadVPDCache() *VPDCache {
	c := &VPDCache{Functions: map[string]*VPD{}, Errors: map[string]string{}}
	for _, d := range t.Sorted(vpdCandidate) {
		v, err := t.readVPDFile(d)
		switch {
		case err != nil:
			c.Errors[d.Address] = err.Error()
		case v != nil:
			c.Functions[d.Address] = v
		}
	}
	return c
}

// ReadVPD returns the parsed VPD of the function at addr, from VPDCache when
// it is set and from sysfs otherwise. It returns nil without an error when
// the function exposes no VPD. Reading sysfs VPD requires root.
func (t *Topology) ReadVPD(addr string) (*VPD, error) {
	d, ok := t.Devices[addr]
	if !ok {
		return nil, fmt.Errorf("unknown PCI device %s", addr)
	}
	if t.VPDCache != nil {
		if msg, failed := t.VPDCache.Errors[addr]; failed {
			return nil, fmt.Errorf("VPD of %s was not readable: %s", addr, msg)
		}
		return t.VPDCache.Functions[addr], nil
	}
	return t.readVPDFile(d)
}

func (t *Topology) readVPDFile(d *Device) (*VPD, error) {
	addr := d.Address
	b, err := os.ReadFile(filepath.Join(d.dir, "vpd"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading VPD of %s: %w", addr, err)
	}
	return ParseVPD(b), nil
}

// ParseVPD extracts the identifier string and the V3 and SN read-only
// keywords. Parsing stops at the end tag or at the first malformed resource,
// as rdma-core's rdma_topo does; whatever was parsed before is returned.
func ParseVPD(data []byte) *VPD {
	v := &VPD{}
	for len(data) > 0 {
		tag := data[0]
		if tag == vpdEndSmall || tag == vpdEndLarge {
			break
		}
		var offset, length int
		if tag&vpdLargeResource != 0 {
			if len(data) < 3 {
				break
			}
			offset, length = 3, int(data[1])|int(data[2])<<8
		} else {
			offset, length = 1, int(tag&0x07)
		}
		if length > len(data)-offset {
			break
		}
		item := data[offset : offset+length]
		data = data[offset+length:]
		switch tag {
		case vpdIDString:
			v.Name = trimVPD(item)
		case vpdReadOnly:
			for len(item) >= 3 {
				kw, n := string(item[:2]), int(item[2])
				if n > len(item)-3 {
					break
				}
				switch kw {
				case "V3":
					v.V3 = trimVPD(item[3 : 3+n])
				case "SN":
					v.SN = trimVPD(item[3 : 3+n])
				}
				item = item[3+n:]
			}
		}
	}
	return v
}

func trimVPD(b []byte) string {
	for len(b) > 0 && (b[len(b)-1] == 0 || b[len(b)-1] == ' ') {
		b = b[:len(b)-1]
	}
	for len(b) > 0 && b[0] == ' ' {
		b = b[1:]
	}
	return string(b)
}

// DataDirectPairs maps the address of every Data Direct function to the
// addresses of the network functions of the same adapter: the physical
// functions that share its VPD V3 UUID, and their virtual functions, which
// carry no VPD of their own. This is how rdma-core's rdma_topo pairs them.
//
// A Data Direct function that cannot be paired is an error rather than an
// omission: without the pairing, GPU traffic for its NIC would silently be
// attributed to the NIC's own, usually more distant, function.
func (t *Topology) DataDirectPairs() (map[string][]string, error) {
	dds := t.Sorted((*Device).IsDataDirect)
	if len(dds) == 0 {
		return nil, nil
	}
	byV3 := map[string][]string{}
	for _, d := range t.Sorted(func(d *Device) bool { return vpdCandidate(d) && !d.IsDataDirect() }) {
		vpd, err := t.ReadVPD(d.Address)
		if err != nil {
			return nil, err
		}
		if vpd != nil && vpd.V3 != "" {
			byV3[vpd.V3] = append(byV3[vpd.V3], d.Address)
		}
	}
	vfs := map[string][]string{}
	for _, d := range t.Sorted(func(d *Device) bool { return d.PhysFn != "" }) {
		vfs[d.PhysFn] = append(vfs[d.PhysFn], d.Address)
	}

	out := make(map[string][]string, len(dds))
	for _, dd := range dds {
		vpd, err := t.ReadVPD(dd.Address)
		if err != nil {
			return nil, err
		}
		if vpd == nil || vpd.V3 == "" {
			return nil, fmt.Errorf("data direct function %s has no VPD V3 UUID", dd.Address)
		}
		pfs := byV3[vpd.V3]
		if len(pfs) == 0 {
			return nil, fmt.Errorf("data direct function %s: no network function shares VPD V3 UUID %q", dd.Address, vpd.V3)
		}
		var fns []string
		for _, pf := range pfs {
			fns = append(fns, pf)
			fns = append(fns, vfs[pf]...)
		}
		sort.Strings(fns)
		out[dd.Address] = fns
	}
	return out, nil
}
