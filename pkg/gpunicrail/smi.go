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
	"encoding/csv"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Inventory maps a GPU matrix index to its stable identity.
type Inventory map[int]GPU

// ParseInventory parses `nvidia-smi --query-gpu=index,uuid,pci.bus_id --format=csv,noheader`.
func ParseInventory(out string) (Inventory, error) {
	r := csv.NewReader(strings.NewReader(out))
	r.TrimLeadingSpace = true
	r.FieldsPerRecord = 3
	recs, err := r.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("parsing GPU inventory: %w", err)
	}
	inv := Inventory{}
	uuids := map[string]bool{}
	for _, rec := range recs {
		idx, err := strconv.Atoi(strings.TrimSpace(rec[0]))
		if err != nil {
			return nil, fmt.Errorf("invalid GPU index %q", rec[0])
		}
		uuid := strings.TrimSpace(rec[1])
		pci, err := NormalizePCI(rec[2])
		if err != nil {
			return nil, err
		}
		if uuid == "" || uuids[uuid] || inv[idx].UUID != "" {
			return nil, fmt.Errorf("GPU inventory has an empty or duplicate entry for index %d", idx)
		}
		uuids[uuid] = true
		inv[idx] = GPU{UUID: uuid, PCIAddress: pci}
	}
	return inv, nil
}

// Matrix is the parsed `nvidia-smi topo -all -m` output.
type Matrix struct {
	Cols []string
	// Cells[row][col] is the path category between two matrix devices.
	Cells map[string]map[string]string
	// NICs and DDs map a matrix alias (NIC2, DD0) to the legend's device name.
	NICs map[string]string
	DDs  map[string]string
}

var (
	// NVIDIA CLI versions may emit ANSI formatting even without a terminal.
	ansiRE   = regexp.MustCompile(`\x1b\[[0-?]*[ -/]*[@-~]`)
	aliasRE  = regexp.MustCompile(`^(GPU|NIC|DD|NVME|NVMe)\d+$`)
	legendRE = regexp.MustCompile(`^\s*(NIC|DD)(\d+):\s+(\S+)`)
	gpuRE    = regexp.MustCompile(`^GPU(\d+)$`)
)

// ParseMatrix parses the topology matrix and its legend. NVMe rows and
// columns are ignored.
func ParseMatrix(out string) (*Matrix, error) {
	out = ansiRE.ReplaceAllString(out, "")
	m := &Matrix{Cells: map[string]map[string]string{}, NICs: map[string]string{}, DDs: map[string]string{}}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if lm := legendRE.FindStringSubmatch(line); lm != nil {
			alias := lm[1] + lm[2]
			if lm[1] == "NIC" {
				m.NICs[alias] = lm[3]
			} else {
				m.DDs[alias] = lm[3]
			}
			continue
		}
		if m.Cols == nil {
			// The header is the first line that starts with a device alias.
			if !aliasRE.MatchString(fields[0]) {
				continue
			}
			for _, f := range fields {
				if !aliasRE.MatchString(f) {
					break
				}
				m.Cols = append(m.Cols, f)
			}
			continue
		}
		if aliasRE.MatchString(fields[0]) && len(fields)-1 >= len(m.Cols) && !strings.HasSuffix(fields[0], ":") {
			if _, dup := m.Cells[fields[0]]; dup {
				continue
			}
			row := make(map[string]string, len(m.Cols))
			for i, col := range m.Cols {
				row[col] = fields[1+i]
			}
			m.Cells[fields[0]] = row
		}
	}
	if len(m.Cols) == 0 {
		return nil, fmt.Errorf("no topology matrix found in nvidia-smi output")
	}
	for _, col := range m.Cols {
		if _, ok := m.Cells[col]; !ok && !strings.HasPrefix(strings.ToUpper(col), "NVME") {
			return nil, fmt.Errorf("topology matrix has no row for %s", col)
		}
	}
	return m, nil
}

func gpuIndex(alias string) (int, bool) {
	g := gpuRE.FindStringSubmatch(alias)
	if g == nil {
		return 0, false
	}
	i, _ := strconv.Atoi(g[1])
	return i, true
}
