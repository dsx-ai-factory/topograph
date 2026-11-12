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
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// Sysfs resolves host interfaces through a (possibly relocated) sysfs mount.
// PCI identities come from device and physfn symlink basenames; they are
// never inferred from interface naming or PCI function numbering. All lookups
// use Root, including when host sysfs is mounted at /host/sys.
type Sysfs struct{ Root string }

// PCIAddress returns the normalized PCI address of the function behind iface.
func (s Sysfs) PCIAddress(iface string) (string, error) {
	link, err := os.Readlink(filepath.Join(s.Root, "class", "net", iface, "device"))
	if err != nil {
		return "", fmt.Errorf("interface %q has no backing PCI device: %w", iface, err)
	}
	return NormalizePCI(filepath.Base(link))
}

// RDMADevices returns the RDMA device names of iface, which is how the
// nvidia-smi legend names a NIC.
func (s Sysfs) RDMADevices(iface string) []string {
	entries, err := os.ReadDir(filepath.Join(s.Root, "class", "net", iface, "device", "infiniband"))
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names
}

// DevicePCI resolves either an RDMA name or a netdev from an NVIDIA legend.
// A device absent from host sysfs cannot supply a fallback match. Permission
// errors and malformed links are different: they must fail collection.
func (s Sysfs) DevicePCI(name string) (string, error) {
	if name == "." || name == ".." || filepath.Base(name) != name {
		return "", fmt.Errorf("invalid topology device name %q", name)
	}
	var pci string
	for _, class := range []string{"infiniband", "net"} {
		path := filepath.Join(s.Root, "class", class, name, "device")
		device, err := readPCILink(path)
		if err != nil {
			return "", err
		}
		if device == "" {
			continue
		}
		if pci != "" && pci != device {
			return "", fmt.Errorf("topology device %q identifies both %s and %s", name, pci, device)
		}
		pci = device
	}
	return pci, nil
}

// PhysicalFunction returns a VF's parent PF, or an empty address when physfn
// does not exist (normal for a non-VF). Read the actual symlink: resolving a
// nonexistent path with readlink -f can otherwise look like a valid parent.
func (s Sysfs) PhysicalFunction(pci string) (string, error) {
	normalized, err := NormalizePCI(pci)
	if err != nil {
		return "", err
	}
	return readPCILink(filepath.Join(s.Root, "bus", "pci", "devices", normalized, "physfn"))
}

// readPCILink reads only the final identity component. Sysfs device links are
// relative and their intermediate directories may not exist in a test mount;
// following an absolute link could also escape a relocated host sysfs mount.
func readPCILink(path string) (string, error) {
	link, err := os.Readlink(path)
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("reading PCI identity at %s: %w", path, err)
	}
	pci, err := NormalizePCI(filepath.Base(link))
	if err != nil {
		return "", fmt.Errorf("reading PCI identity at %s: %w", path, err)
	}
	return pci, nil
}
