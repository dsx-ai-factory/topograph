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

// Package gpunicrail implements the gpu-nic-rail-mapping node-metadata plugin.
//
// The plugin joins two node-local sources, LLDP neighbors (which NIC sits on
// which rail) and the nvidia-smi topology matrix (which GPU is closest to each
// NIC), and publishes the result as one NodeGPUNICRailMapping custom resource per
// Node. It is independent of the topology provider and of topology.Graph.
package gpunicrail

import (
	"fmt"
	"slices"

	"k8s.io/apimachinery/pkg/runtime/schema"
)

const (
	// PluginName is the value of the PluginLabel on every resource the plugin manages.
	PluginName = "gpu-nic-rail-mapping"

	Group    = "topograph.run"
	Version  = "v1alpha1"
	Kind     = "NodeGPUNICRailMapping"
	Resource = "nodegpunicrailmappings"

	PluginLabel      = "topograph.run/node-metadata-plugin"
	NodeUIDLabel     = "topograph.run/node-uid"
	NodeNameAnnot    = "topograph.run/node-name"
	InstanceLabel    = "app.kubernetes.io/instance"
	ManagedByLabel   = "app.kubernetes.io/managed-by"
	ManagedByValue   = "topograph"
	resourceNamePref = "node-"
)

// GVR identifies the NodeGPUNICRailMapping resource.
var GVR = schema.GroupVersionResource{Group: Group, Version: Version, Resource: Resource}

// Mapping modes.
const (
	ModeDataDirect = "data-direct"
	ModeNIC        = "nic"
	ModeNone       = "none"
)

// ResourceName returns the name of the mapping resource of the Node with the given UID.
func ResourceName(nodeUID string) string { return resourceNamePref + nodeUID }

// Path ranks, closest first. NV# links are GPU-to-GPU only and never rank a NIC.
var pathRanks = map[string]int{"PIX": 0, "PXB": 1, "PHB": 2, "NODE": 3, "SYS": 4}

// ValidPathRank reports whether p is a NIC-to-GPU path category.
func ValidPathRank(p string) bool { _, ok := pathRanks[p]; return ok }

// GPU is a stable GPU identity.
type GPU struct {
	UUID       string `json:"uuid"`
	PCIAddress string `json:"pciAddress"`
}

// NICMapping is the mapping record of one NIC.
type NICMapping struct {
	Interface      string `json:"interface"`
	RailID         string `json:"railID"`
	TopologyTarget string `json:"topologyTarget,omitempty"`
	MappingMode    string `json:"mappingMode"`
	BestPath       string `json:"bestPath,omitempty"`
	BestGPUs       []GPU  `json:"bestGpus"`
}

// Sources records when each input was collected (RFC 3339).
type Sources struct {
	LLDP      string `json:"lldp"`
	NvidiaSMI string `json:"nvidiaSmi,omitempty"`
}

// Status is the observed mapping of one Node. NICs is keyed by normalized host
// PCI address.
type Status struct {
	Sources Sources               `json:"sources"`
	NICs    map[string]NICMapping `json:"nics"`
}

// Validate enforces the cross-field invariants that the CRD schema cannot.
func (s *Status) Validate() error {
	rails := make(map[string]string, len(s.NICs))
	for pci, n := range s.NICs {
		norm, err := NormalizePCI(pci)
		if err != nil || norm != pci {
			return fmt.Errorf("nics key %q is not a normalized PCI address", pci)
		}
		if n.Interface == "" || n.RailID == "" {
			return fmt.Errorf("nic %s: interface and railID are required", pci)
		}
		if other, dup := rails[n.RailID]; dup {
			return fmt.Errorf("railID %q is used by both %s and %s", n.RailID, other, pci)
		}
		rails[n.RailID] = pci
		switch n.MappingMode {
		case ModeNone:
			if n.TopologyTarget != "" || n.BestPath != "" || len(n.BestGPUs) != 0 {
				return fmt.Errorf("nic %s: mappingMode none must not carry a target, path or GPUs", pci)
			}
		case ModeNIC, ModeDataDirect:
			if n.TopologyTarget == "" || !ValidPathRank(n.BestPath) || len(n.BestGPUs) == 0 {
				return fmt.Errorf("nic %s: mappingMode %s requires topologyTarget, a valid bestPath and bestGpus", pci, n.MappingMode)
			}
		default:
			return fmt.Errorf("nic %s: unknown mappingMode %q", pci, n.MappingMode)
		}
		if !slices.IsSortedFunc(n.BestGPUs, func(a, b GPU) int { return compareStr(a.UUID, b.UUID) }) {
			return fmt.Errorf("nic %s: bestGpus must be sorted by UUID", pci)
		}
		for _, g := range n.BestGPUs {
			if g.UUID == "" || g.PCIAddress == "" {
				return fmt.Errorf("nic %s: bestGpus entries need uuid and pciAddress", pci)
			}
		}
	}
	return nil
}

// sameMapping reports whether two statuses carry the same mapping, ignoring
// the collection timestamps.
func sameMapping(a, b Status) bool {
	if len(a.NICs) != len(b.NICs) {
		return false
	}
	for k, av := range a.NICs {
		bv, ok := b.NICs[k]
		if !ok || av.Interface != bv.Interface || av.RailID != bv.RailID ||
			av.TopologyTarget != bv.TopologyTarget || av.MappingMode != bv.MappingMode ||
			av.BestPath != bv.BestPath || !slices.Equal(av.BestGPUs, bv.BestGPUs) {
			return false
		}
	}
	return true
}

func compareStr(a, b string) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}
