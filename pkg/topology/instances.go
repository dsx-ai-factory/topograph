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
package topology

import (
	"fmt"
	"strconv"
)

// Instances is the top-level JSON envelope for instance-oriented topology export.
type Instances struct {
	Instances []Instance `json:"instances"`
}

// Instance describes one compute instance and its discovered network / accelerator context.
// YAML and JSON use the same field names for this exported shape.
type Instance struct {
	ID            string            `json:"id" yaml:"id,omitempty"`
	NetworkLayers []string          `json:"network_layers" yaml:"network_layers,omitempty"`
	Labels        map[string]string `json:"labels,omitempty" yaml:"labels,omitempty"`
	NetLayers     []string          `yaml:"-" json:"-"`
}

func FabricTierKey(tier int) string {
	return KeyFabricTierPrefix + strconv.Itoa(tier)
}

// String summarizes an instance for logging (simulation / derived fields).
func (inst *Instance) String() string {
	return fmt.Sprintf("Instance: %s Labels: %v NetLayers: %v",
		inst.ID, inst.Labels, inst.NetLayers)
}

// CloneForTopology returns a copy suitable for instance export: network layers
// are filled and simulation-only fields are cleared.
func (inst *Instance) CloneForTopology() Instance {
	return Instance{
		ID:            inst.ID,
		NetworkLayers: append([]string(nil), inst.NetLayers...),
		Labels:        cloneStringMap(inst.Labels),
	}
}

func cloneStringMap(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}
	clone := make(map[string]string, len(values))
	for k, v := range values {
		clone[k] = v
	}
	return clone
}
