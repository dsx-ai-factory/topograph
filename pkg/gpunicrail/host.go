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
// Only the symlink at <root>/class/net/<if>/device is read, never followed
// out of the mount, so the result is the PCI function the interface is bound
// to; bonds, bridges and representors have no such link and are rejected.
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
