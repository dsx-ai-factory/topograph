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
)

// Select computes the mapping of every bound NIC.
//
// rdma returns the RDMA device names of an interface; the legend names a NIC
// by interface name or by RDMA device name, and either joins. Without a
// matrix (a node with no GPUs) every NIC is published with mode none.
func Select(bindings []Binding, rdma func(iface string) []string, inv Inventory, m *Matrix) (map[string]NICMapping, error) {
	out := make(map[string]NICMapping, len(bindings))
	for _, b := range bindings {
		nm := NICMapping{Interface: b.Interface, RailID: b.RailID, MappingMode: ModeNone, BestGPUs: []GPU{}}
		if m != nil {
			names := append([]string{b.Interface}, rdma(b.Interface)...)
			nic, err := legendAlias(m.NICs, names)
			if err != nil {
				return nil, fmt.Errorf("interface %q: %w", b.Interface, err)
			}
			dd, err := legendAlias(m.DDs, names)
			if err != nil {
				return nil, fmt.Errorf("interface %q: %w", b.Interface, err)
			}
			target, mode := nic, ModeNIC
			if dd != "" {
				// A matching Data Direct function carries the GPU traffic, so its
				// row is authoritative however its ranks compare with the NIC row.
				target, mode = dd, ModeDataDirect
			}
			if target != "" {
				path, gpus, err := bestGPUs(m, target, inv)
				if err != nil {
					return nil, fmt.Errorf("interface %q: %w", b.Interface, err)
				}
				if len(gpus) != 0 {
					nm.TopologyTarget, nm.MappingMode, nm.BestPath, nm.BestGPUs = target, mode, path, gpus
				}
			}
		}
		out[b.PCI] = nm
	}
	return out, nil
}

// legendAlias returns the single alias whose legend name is one of names.
func legendAlias(legend map[string]string, names []string) (string, error) {
	var found []string
	for alias, name := range legend {
		if slices.Contains(names, name) {
			found = append(found, alias)
		}
	}
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
