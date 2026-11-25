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

// Package pcitopotest builds fake sysfs PCI hierarchies for tests.
package pcitopotest

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// Function describes one PCI function of a fake sysfs tree.
type Function struct {
	HostBridge string
	// Chain lists the functions from the root port down to and including this
	// one. Missing intermediate bridges are created automatically.
	Chain    []string
	Vendor   uint16
	Device   uint16
	Class    uint32
	NUMANode int
	PhysFn   string
	Driver   string
	RDMA     []string
	Net      []string
	VPD      []byte
}

// Sysfs is a fake sysfs mount rooted at Root.
type Sysfs struct {
	t    testing.TB
	Root string
}

// New returns an empty fake sysfs in a test temporary directory.
func New(t testing.TB) *Sysfs {
	t.Helper()
	root := filepath.Join(t.TempDir(), "sys")
	if err := os.MkdirAll(filepath.Join(root, "bus", "pci", "devices"), 0o755); err != nil {
		t.Fatal(err)
	}
	return &Sysfs{t: t, Root: root}
}

// Add creates fn and any of its bridges that do not exist yet. Bridges take
// the NUMA node of the first function created below them.
func (s *Sysfs) Add(fn Function) *Sysfs {
	s.t.Helper()
	for i := 1; i < len(fn.Chain); i++ {
		addr := fn.Chain[i-1]
		if _, err := os.Lstat(filepath.Join(s.Root, "bus", "pci", "devices", addr)); err == nil {
			continue
		}
		s.create(Function{HostBridge: fn.HostBridge, Chain: fn.Chain[:i], Vendor: 0x10b5, Device: 0xc010, Class: 0x060400, NUMANode: fn.NUMANode})
	}
	s.create(fn)
	return s
}

func (s *Sysfs) create(fn Function) {
	s.t.Helper()
	addr := fn.Chain[len(fn.Chain)-1]
	rel := filepath.Join(append([]string{"devices", fn.HostBridge}, fn.Chain...)...)
	dir := filepath.Join(s.Root, rel)
	s.mkdir(dir)
	s.write(filepath.Join(dir, "vendor"), fmt.Sprintf("0x%04x\n", fn.Vendor))
	s.write(filepath.Join(dir, "device"), fmt.Sprintf("0x%04x\n", fn.Device))
	s.write(filepath.Join(dir, "class"), fmt.Sprintf("0x%06x\n", fn.Class))
	s.write(filepath.Join(dir, "numa_node"), fmt.Sprintf("%d\n", fn.NUMANode))
	if fn.PhysFn != "" {
		s.symlink("../"+fn.PhysFn, filepath.Join(dir, "physfn"))
	}
	if fn.Driver != "" {
		s.symlink("../../../../bus/pci/drivers/"+fn.Driver, filepath.Join(dir, "driver"))
	}
	for _, n := range fn.RDMA {
		s.mkdir(filepath.Join(dir, "infiniband", n))
	}
	for _, n := range fn.Net {
		s.mkdir(filepath.Join(dir, "net", n))
	}
	if fn.VPD != nil {
		if err := os.WriteFile(filepath.Join(dir, "vpd"), fn.VPD, 0o600); err != nil {
			s.t.Fatal(err)
		}
	}
	s.symlink(filepath.Join("..", "..", "..", rel), filepath.Join(s.Root, "bus", "pci", "devices", addr))
}

func (s *Sysfs) mkdir(dir string) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		s.t.Fatal(err)
	}
}

func (s *Sysfs) write(path, content string) {
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		s.t.Fatal(err)
	}
}

func (s *Sysfs) symlink(target, path string) {
	if err := os.Symlink(target, path); err != nil {
		s.t.Fatal(err)
	}
}

// VPD encodes an identifier string and read-only keywords, given as
// alternating keyword and value strings, followed by the end tag.
func VPD(name string, keywords ...string) []byte {
	var ro []byte
	for i := 0; i+1 < len(keywords); i += 2 {
		ro = append(ro, keywords[i][0], keywords[i][1], byte(len(keywords[i+1])))
		ro = append(ro, keywords[i+1]...)
	}
	out := []byte{0x82, byte(len(name)), byte(len(name) >> 8)}
	out = append(out, name...)
	out = append(out, 0x90, byte(len(ro)), byte(len(ro)>>8))
	out = append(out, ro...)
	return append(out, 0x78)
}
