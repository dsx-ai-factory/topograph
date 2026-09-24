/*
 * Copyright 2026 NVIDIA CORPORATION
 * SPDX-License-Identifier: Apache-2.0
 */

package nfd

import (
	"context"
	"net/http"

	"github.com/dsx-ai-factory/topograph/internal/httperr"
	internalk8s "github.com/dsx-ai-factory/topograph/internal/k8s"
	"github.com/dsx-ai-factory/topograph/pkg/topology"
)

func (eng *NfdEngine) ResolveComputeInstances(ctx context.Context, instances []topology.ComputeInstances, _ any) ([]topology.ComputeInstances, *httperr.Error) {
	nodes, err := internalk8s.GetNodes(ctx, eng.client, eng.params.nodeListOpt)
	if err != nil {
		return nil, httperr.NewError(http.StatusBadGateway, err.Error())
	}
	eng.cachedNodes = nodes
	if len(instances) != 0 {
		return instances, nil
	}
	return internalk8s.GetComputeInstances(nodes), nil
}
