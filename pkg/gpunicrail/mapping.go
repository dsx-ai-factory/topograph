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
	"slices"
	"sort"

	"k8s.io/klog/v2"
)

// NICResolver supplies host identities for topology legend names. Empty PCI
// addresses mean a device or optional physfn link is absent; other lookup
// failures must be returned so collection cannot publish a guessed mapping.
type NICResolver interface {
	RDMADevices(iface string) []string
	DevicePCI(name string) (string, error)
	PhysicalFunction(pci string) (string, error)
}

// Select computes the mapping of every bound NIC. Name matches take precedence
// over exact PCI matches, then explicit PF/VF relationships. The original bound
// PCI address remains the output key even when another function supplies locality.
// Without a matrix every NIC is published with mode none.
func Select(bindings []Binding, resolver NICResolver, inv Inventory, m *Matrix) (map[string]NICMapping, error) {
	out := make(map[string]NICMapping, len(bindings))
	for _, b := range bindings {
		nm := NICMapping{Interface: b.Interface, RailID: b.RailID, MappingMode: ModeNone, BestGPUs: []GPU{}}
		reason := "no topology matrix"
		if m != nil {
			reason = "no matching topology device"
			names := append([]string{b.Interface}, resolver.RDMADevices(b.Interface)...)
			// A matching Data Direct function carries GPU traffic. Resolve it
			// first, including PCI fallbacks, rather than substituting a NIC row
			// with a better rank or failing on unused NIC candidates.
			target, err := resolveLegend(m.DDs, names, b.PCI, resolver)
			if err != nil {
				return nil, fmt.Errorf("interface %q: %w", b.Interface, err)
			}
			mode := ModeDataDirect
			if target == "" {
				target, err = resolveLegend(m.NICs, names, b.PCI, resolver)
				if err != nil {
					return nil, fmt.Errorf("interface %q: %w", b.Interface, err)
				}
				mode = ModeNIC
			}
			if target != "" {
				reason = "no usable GPU paths"
				path, gpus, err := bestGPUs(m, target, inv)
				if err != nil {
					return nil, fmt.Errorf("interface %q: %w", b.Interface, err)
				}
				if len(gpus) != 0 {
					nm.TopologyTarget, nm.MappingMode, nm.BestPath, nm.BestGPUs = target, mode, path, gpus
				}
			}
		}
		// Log the selection rather than publication: the refresh can still fail
		// on a later NIC, and unchanged mappings need not trigger a CR update.
		if nm.MappingMode == ModeNone {
			klog.InfoS("No GPU mapping for NIC", "interface", b.Interface, "pciAddress", b.PCI, "railID", b.RailID, "reason", reason)
		} else {
			klog.InfoS("Selected GPUs for NIC", "interface", b.Interface, "pciAddress", b.PCI, "railID", b.RailID,
				"topologyTarget", nm.TopologyTarget, "mappingMode", nm.MappingMode, "bestPath", nm.BestPath, "bestGpus", nm.BestGPUs)
		}
		out[b.PCI] = nm
	}
	return out, nil
}

// resolveLegend tries successively weaker identity evidence. Never merge all
// candidates into one set: an exact match must beat related VF/PF candidates.
// In particular, sibling VFs are not interchangeable just because they share a PF.
func resolveLegend(legend map[string]string, names []string, pci string, resolver NICResolver) (string, error) {
	var named []string
	for alias, name := range legend {
		if slices.Contains(names, name) {
			named = append(named, alias)
		}
	}
	if len(named) != 0 {
		alias, err := uniqueAlias(named)
		if err == nil {
			klog.V(4).InfoS("Matched NIC topology by name", "pciAddress", pci, "topologyTarget", alias, "device", legend[alias])
		}
		return alias, err
	}

	// Sort before reading sysfs as well as before reporting ambiguity so that
	// diagnostics are deterministic when several devices fail or match.
	aliases := make([]string, 0, len(legend))
	for alias := range legend {
		aliases = append(aliases, alias)
	}
	sort.Strings(aliases)
	devices := make(map[string]string, len(legend))
	var exact []string
	for _, alias := range aliases {
		device, err := resolver.DevicePCI(legend[alias])
		if err != nil {
			return "", fmt.Errorf("resolving topology device %s (%s): %w", alias, legend[alias], err)
		}
		devices[alias] = device
		if device == pci {
			exact = append(exact, alias)
		}
	}
	if len(exact) != 0 {
		alias, err := uniqueAlias(exact)
		if err == nil {
			klog.V(4).InfoS("Matched NIC topology by PCI address", "pciAddress", pci, "topologyTarget", alias, "device", legend[alias])
		}
		return alias, err
	}
	if len(aliases) == 0 {
		return "", nil
	}

	parent, err := resolver.PhysicalFunction(pci)
	if err != nil {
		return "", err
	}
	var related []string
	for _, alias := range aliases {
		device := devices[alias]
		if device == "" {
			continue
		}
		if parent != "" && device == parent {
			// The collected interface is a VF and the matrix lists its PF.
			related = append(related, alias)
			continue
		}
		deviceParent, err := resolver.PhysicalFunction(device)
		if err != nil {
			return "", fmt.Errorf("resolving topology device %s (%s): %w", alias, legend[alias], err)
		}
		if deviceParent == pci {
			// The collected interface is a PF and the matrix lists its VF.
			related = append(related, alias)
		}
	}
	alias, err := uniqueAlias(related)
	if err == nil && alias != "" {
		direction := "pf-to-vf"
		if parent != "" && devices[alias] == parent {
			direction = "vf-to-pf"
		}
		klog.V(4).InfoS("Matched NIC topology by PF/VF relationship", "pciAddress", pci, "topologyTarget", alias,
			"device", legend[alias], "devicePCIAddress", devices[alias], "direction", direction)
	}
	return alias, err
}

// uniqueAlias rejects multiple candidates even when their current best GPUs
// happen to agree. Picking an arbitrary VF would hide an ambiguous identity.
func uniqueAlias(found []string) (string, error) {
	sort.Strings(found)
	switch len(found) {
	case 0:
		return "", nil
	case 1:
		return found[0], nil
	}
	return "", fmt.Errorf("ambiguous topology legend entries %v", found)
}

func bestGPUs(m *Matrix, target string, inv Inventory) (string, []GPU, error) {
	row, ok := m.Cells[target]
	if !ok {
		return "", nil, fmt.Errorf("topology matrix has no row for %s", target)
	}
	best, path := len(pathRanks), ""
	var gpus []GPU
	for _, col := range m.Cols {
		idx, isGPU := gpuIndex(col)
		if !isGPU {
			continue
		}
		cell := row[col]
		rank, ranked := pathRanks[cell]
		if !ranked {
			if cell == "X" || isNVLink(cell) {
				continue
			}
			return "", nil, fmt.Errorf("unknown path category %q between %s and %s", cell, target, col)
		}
		gpu, ok := inv[idx]
		if !ok {
			return "", nil, fmt.Errorf("topology matrix lists %s but the GPU inventory has no index %d", col, idx)
		}
		switch {
		case rank < best:
			best, path, gpus = rank, cell, []GPU{gpu}
		case rank == best:
			gpus = append(gpus, gpu)
		}
	}
	sort.Slice(gpus, func(i, j int) bool { return gpus[i].UUID < gpus[j].UUID })
	return path, gpus, nil
}

func isNVLink(cell string) bool {
	return len(cell) > 2 && cell[:2] == "NV" && cell[2] >= '0' && cell[2] <= '9'
}
