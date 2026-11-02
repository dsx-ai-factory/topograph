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

package crusoe

import (
	"strings"

	"k8s.io/klog/v2"
)

const (
	// Crusoe Cloud publishes the InfiniBand topology of every VM as Kubernetes
	// Node labels. The control plane owns them, so a guest VM does not need
	// fabric access to learn where it sits.
	labelPartitionID = "crusoe.ai/ib.partition.id" // InfiniBand partition, the outer fabric tier
	labelPodID       = "crusoe.ai/pod.id"          // rail-optimized pod, the inner fabric tier

	// labelGPUClique carries the NVLink clique of an MNNVL node such as GB200
	// and GB300 NVL72. Its value is "<clusterUUID>.<cliqueID>".
	//
	// Either NVIDIA GPU Feature Discovery or the NVIDIA DRA driver's
	// compute-domain kubelet plugin publishes it, depending on the cluster. Both
	// read the same NVML fabric info and write the same value, so the source
	// does not matter here.
	//
	// Group on the whole value, which is the NVL Partition: the nodes Fabric
	// Manager placed in one IMEX domain. That is the granularity a job must
	// stay inside to get NVLink bandwidth. It can be finer than the physical
	// NVL Domain named by the <clusterUUID> prefix alone, because one rack can
	// be split into several cliques. A clique never spans racks.
	labelGPUClique = "nvidia.com/gpu.clique"

	// rootTier names the synthetic tier above every partition. Slurm needs one
	// common root so a job can span partitions and reach nodes that have no
	// InfiniBand at all.
	rootTier = "crusoe"

	// Fabric tier names used for nodes that carry no InfiniBand labels.
	cpuPartition = "cpu-partition"
	cpuPod       = "cpu-pod"
)

// fabricLabels holds the InfiniBand locality read from one Node.
type fabricLabels struct {
	PartitionID string
	PodID       string
}

// readFabricLabels extracts the InfiniBand locality of a Node. It reports false
// when either label is absent, which is the normal state of a CPU-only node and
// of a GPU SKU that ships without InfiniBand.
func readFabricLabels(labels map[string]string) (fabricLabels, bool) {
	partitionID := strings.TrimSpace(labels[labelPartitionID])
	podID := strings.TrimSpace(labels[labelPodID])

	if partitionID == "" || podID == "" {
		klog.V(4).Infof("No InfiniBand labels (%s=%q %s=%q)",
			labelPartitionID, partitionID, labelPodID, podID)
		return fabricLabels{}, false
	}

	return fabricLabels{PartitionID: partitionID, PodID: podID}, true
}

// readNVLinkDomain returns the NVLink domain of a Node, or "" when the node is
// not on an NVLink fabric.
//
// The value is the clique verbatim. Providers that derive a domain from a clique
// publish the two identically, so accelerator.topograph.run/domain can be
// compared directly against nvidia.com/gpu.clique on the node.
func readNVLinkDomain(labels map[string]string) string {
	clique := strings.TrimSpace(labels[labelGPUClique])
	if clique == "" {
		return ""
	}

	klog.V(4).Infof("NVLink domain from %s=%q", labelGPUClique, clique)
	return clique
}
