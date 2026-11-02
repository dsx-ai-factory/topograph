// SPDX-FileCopyrightText: Copyright (c) 2024-2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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
	"strings"
)

const (
	KeyEngine = "engine"

	KeyUID               = "uid"
	KeyNamespace         = "namespace"
	KeyPodSelector       = "podSelector"
	KeyNodeSelector      = "nodeSelector"
	KeyTopologies        = "topologies"
	KeyTopoConfigPath    = "topologyConfigPath"
	KeyTopoConfigmapName = "topologyConfigmapName"
	KeyBlockSizes        = "blockSizes"
	KeyBlockName         = "blockName"
	KeyNodeNameRegexp    = "nodeNameRegexp"
	KeyFormat            = "format"
	KeyTrimTiers         = "trimTiers"
	KeyIMDSURL           = "imdsUrl"

	KeyPlugin     = "plugin"
	TopologyTree  = "topology/tree"
	TopologyBlock = "topology/block"
	TopologyFlat  = "topology/flat"
	NoTopology    = "no-topology"

	KeyNodeInstance = "topograph.run/instance"
	KeyNodeRegion   = "topograph.run/region"
	KeyGpuClusterID = "topograph.run/cluster-id"

	// Topograph default node labels. Fabric tier zero is closest to the compute
	// node.
	KeyFabricTierPrefix      = "fabric.topograph.run/tier-"
	KeyTopologyXclrDomain    = "accelerator.topograph.run/domain"
	KeyTopologyXclrSubDomain = "accelerator.topograph.run/sub-domain"

	// ConfigMap annotation keys for metadata tracking
	KeyConfigMapEngine            = "topograph.run/engine"
	KeyConfigMapTopologyManagedBy = "topograph.run/topology-managed-by"
	KeyConfigMapLastUpdated       = "topograph.run/last-updated"
	KeyConfigMapPlugin            = "topograph.run/plugin"
	KeyConfigMapBlockSizes        = "topograph.run/block-sizes"
	KeyConfigMapNamespace         = "topograph.run/slurm-namespace"

	//Slinky specific annotations and labels
	KeySlinkyTopologySpec = "topology.slinky.slurm.net/spec"
	KeySlurmNodeName      = "slurm.node.name"
)

// Graph is the canonical scheduler-agnostic topology representation.
// Tiers is the root of the switch hierarchy. Domains maps accelerator/block
// domains to hosts and carries the finalized enumerated domain ID.
type Graph struct {
	Tiers   *Vertex
	Domains DomainMap
	// Instances optionally carries per-instance metadata keyed by instance ID.
	// Engines that do not need instance-oriented output ignore it.
	Instances map[string]Instance
}

// Vertex is a tree node, representing a compute node or a network switch, where
// - Name is a compute node name
// - ID is an CSP defined instance ID of switches and compute nodes
// - Vertices is a list of connected compute nodes or network switches
type Vertex struct {
	Name     string
	ID       string
	Vertices map[string]*Vertex
}

func (v *Vertex) String() string {
	vertices := []string{}
	for _, w := range v.Vertices {
		vertices = append(vertices, w.ID)
	}
	return fmt.Sprintf("ID:%q Name:%q Vertices: %s", v.ID, v.Name, strings.Join(vertices, ","))
}
