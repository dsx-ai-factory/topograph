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
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// ParseLLDP reads neighbor-bearing interfaces from lldpcli's JSON0 format.
// A value is empty when a neighbor advertises no chassis system name.
// Conflicting names are ambiguous; chassis IDs and port IDs never replace names.
func ParseLLDP(data []byte) (map[string]string, error) {
	var doc struct {
		LLDP []struct {
			Interfaces []struct {
				Name    string            `json:"name"`
				Ports   []json.RawMessage `json:"port"`
				Chassis []struct {
					Names []struct {
						Value string `json:"value"`
					} `json:"name"`
				} `json:"chassis"`
			} `json:"interface"`
		} `json:"lldp"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parsing LLDP JSON0 output: %w", err)
	}
	if doc.LLDP == nil {
		return nil, fmt.Errorf("parsing LLDP JSON0 output: missing lldp array")
	}
	names := map[string]string{}
	for _, lldp := range doc.LLDP {
		for _, iface := range lldp.Interfaces {
			if iface.Name == "" {
				return nil, fmt.Errorf("LLDP interface has no name")
			}
			if len(iface.Chassis) == 0 && len(iface.Ports) == 0 {
				continue
			}
			if _, seen := names[iface.Name]; !seen {
				names[iface.Name] = ""
			}
			for _, chassis := range iface.Chassis {
				for _, name := range chassis.Names {
					value := strings.TrimSpace(name.Value)
					if value == "" {
						continue
					}
					if previous := names[iface.Name]; previous != "" && previous != value {
						return nil, fmt.Errorf("interface %q has conflicting LLDP switch names %q and %q", iface.Name, previous, value)
					}
					names[iface.Name] = value
				}
			}
		}
	}
	return names, nil
}

// Binding ties a host interface to its NIC PCI address and optional switch name.
type Binding struct {
	Interface  string
	SwitchName string
	PCI        string
}

// BindSwitches keeps PCI-backed NICs with LLDP neighbors and attaches optional
// switch names. Multiple NICs can share a name, but cannot claim the same PCI key.
func BindSwitches(nics []Binding, switches map[string]string) ([]Binding, error) {
	var out []Binding
	pcis := map[string]string{}
	for _, nic := range nics {
		name, neighbor := switches[nic.Interface]
		if !neighbor {
			continue
		}
		if other, dup := pcis[nic.PCI]; dup {
			return nil, fmt.Errorf("interfaces %q and %q resolve to the same PCI function %s", other, nic.Interface, nic.PCI)
		}
		pcis[nic.PCI] = nic.Interface
		nic.SwitchName = name
		out = append(out, nic)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Interface < out[j].Interface })
	return out, nil
}
