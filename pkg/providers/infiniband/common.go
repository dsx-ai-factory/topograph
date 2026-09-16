/*
 * Copyright 2025 NVIDIA CORPORATION
 * SPDX-License-Identifier: Apache-2.0
 */

package infiniband

import (
	"bytes"
	"context"
	"fmt"
	"strings"

	"k8s.io/klog/v2"

	"github.com/dsx-ai-factory/topograph/pkg/ib"
	"github.com/dsx-ai-factory/topograph/pkg/topology"
)

type IBNetDiscover interface {
	Run(context.Context, string) (*bytes.Buffer, error)
}

func getIbTree(ctx context.Context, cis []topology.ComputeInstances, ibnetdiscover IBNetDiscover) (*topology.Vertex, error) {
	nodeVisited := make(map[string]bool)
	rootMap := make(map[string]*topology.Vertex)

	for _, node := range topology.GetNodeNameList(cis) {
		if _, exists := nodeVisited[node]; !exists {
			stdout, err := ibnetdiscover.Run(ctx, node)
			if err != nil {
				klog.Warningf("failed to run ibnetdiscover: %v", err)
				continue
			}
			if strings.Contains(stdout.String(), "Topology file:") {
				ibRoots, hca, err := ib.GenerateTopologyConfig(stdout.Bytes(), cis)
				if err != nil {
					return nil, fmt.Errorf("IB GenerateTopologyConfig failed: %v", err)
				}
				// mark the visited nodes
				for _, nodeName := range hca {
					nodeVisited[nodeName] = true
				}
				for _, v := range ibRoots {
					rootMap[v.ID] = v
				}
			} else {
				klog.Warningf("Missing ibnetdiscover output for node %q", node)
			}
		}
	}

	roots := make([]*topology.Vertex, 0, len(rootMap))
	for _, v := range rootMap {
		roots = append(roots, v)
	}

	merger := topology.NewMerger(roots)
	treeRoot := &topology.Vertex{
		Vertices: make(map[string]*topology.Vertex),
	}
	for _, v := range merger.TopTier() {
		treeRoot.Vertices[v.ID] = v
	}

	return treeRoot, nil
}
