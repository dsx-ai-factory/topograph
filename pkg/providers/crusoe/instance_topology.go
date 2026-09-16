/*
 * Copyright 2026 NVIDIA CORPORATION
 * SPDX-License-Identifier: Apache-2.0
 */

package crusoe

import (
	"fmt"
	"net/http"

	"k8s.io/klog/v2"

	"github.com/dsx-ai-factory/topograph/internal/httperr"
	"github.com/dsx-ai-factory/topograph/pkg/topology"
)

// nodeMetadata is the subset of a Kubernetes Node the provider reads. The
// simulation provider supplies the same shape from a model file, so both run
// the same topology logic.
type nodeMetadata struct {
	// InstanceID identifies the node in the canonical graph. It must match the
	// key the engine uses in its ComputeInstances map.
	InstanceID string
	Labels     map[string]string
}

type extractionStats struct {
	infiniband int
	plain      int
	skipped    int
}

// buildClusterTopology converts selected nodes into the canonical cluster
// topology.
func buildClusterTopology(nodes []nodeMetadata, requested map[string]struct{}) (*topology.ClusterTopology, *httperr.Error) {
	topo := topology.NewClusterTopology()
	var stats extractionStats

	for _, node := range nodes {
		if skipNode(node.InstanceID, requested) {
			stats.skipped++
			continue
		}

		inst, hasFabric := buildInstanceTopology(node)
		if hasFabric {
			stats.infiniband++
		} else {
			stats.plain++
		}
		topo.Append(inst)
	}

	klog.Infof("Crusoe topology: %d InfiniBand nodes, %d nodes without InfiniBand, %d skipped",
		stats.infiniband, stats.plain, stats.skipped)

	nodesProcessed.WithLabelValues("infiniband").Add(float64(stats.infiniband))
	nodesProcessed.WithLabelValues("no_infiniband").Add(float64(stats.plain))
	nodesProcessed.WithLabelValues("skipped").Add(float64(stats.skipped))

	if topo.Len() == 0 {
		return nil, httperr.NewError(http.StatusNotFound,
			fmt.Sprintf("no requested nodes found among %d selected nodes", len(nodes)))
	}

	return topo, nil
}

// buildInstanceTopology maps one node's labels onto the canonical instance
// topology, and reports whether the node had InfiniBand labels.
//
// Fabric tiers are closest-first, so tier 0 is the rail-optimized pod, tier 1 is
// the InfiniBand partition and tier 2 is the common root. A node without labels
// gets placeholder tiers under the same root, which lets Slurm schedule across
// GPU and CPU nodes in one tree.
//
// The accelerator domain is set separately from the tiers, because it feeds
// block topology rather than the switch tree. Only an NVLink fabric produces
// one: the InfiniBand partition is deliberately not used as a fallback. A
// partition can span many racks while a clique never leaves one, so keying
// blocks on the partition would let Slurm spread a job across racks and quietly
// fall back to InfiniBand instead of MNNVL. Nodes with no NVLink fabric are
// left without a domain and are scheduled by the tree alone.
func buildInstanceTopology(node nodeMetadata) (*topology.InstanceTopology, bool) {
	inst := &topology.InstanceTopology{InstanceID: node.InstanceID}

	labels, ok := readFabricLabels(node.Labels)
	if ok {
		inst.FabricTiers = topology.ClosestFirstFabricTiers(labels.PodID, labels.PartitionID, rootTier)
	} else {
		inst.FabricTiers = topology.ClosestFirstFabricTiers(cpuPod, cpuPartition, rootTier)
	}

	// A GB200 node on the SKU that ships without InfiniBand has no fabric
	// labels but does have a clique, so it still contributes a block.
	inst.XclrDomainID = readNVLinkDomain(node.Labels)

	klog.V(4).Infof("Built %s", inst.String())
	return inst, ok
}

// requestedNodeIDs turns the request's instance list into a lookup set. An
// empty request means every selected node.
func requestedNodeIDs(instances []topology.ComputeInstances) (map[string]struct{}, *httperr.Error) {
	if len(instances) > 1 {
		return nil, httperr.NewError(http.StatusBadRequest,
			"crusoe does not support multi-region topology requests")
	}
	if len(instances) == 0 || len(instances[0].Instances) == 0 {
		return nil, nil
	}

	ids := make(map[string]struct{}, len(instances[0].Instances))
	for id := range instances[0].Instances {
		ids[id] = struct{}{}
	}
	return ids, nil
}

func skipNode(instanceID string, requested map[string]struct{}) bool {
	if requested == nil {
		return false
	}
	_, ok := requested[instanceID]
	return !ok
}
