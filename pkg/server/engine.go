/*
 * Copyright (c) 2024, NVIDIA CORPORATION.  All rights reserved.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package server

import (
	"context"
	"net/http"
	"time"

	"k8s.io/klog/v2"

	"github.com/dsx-ai-factory/topograph/internal/httperr"
	"github.com/dsx-ai-factory/topograph/internal/httpreq"
	"github.com/dsx-ai-factory/topograph/pkg/engines"
	"github.com/dsx-ai-factory/topograph/pkg/metrics"
	"github.com/dsx-ai-factory/topograph/pkg/providers"
	"github.com/dsx-ai-factory/topograph/pkg/registry"
	"github.com/dsx-ai-factory/topograph/pkg/topology"
)

const (
	maxRetries     = 5
	defaultBackOff = 2 * time.Second
)

var backOff time.Duration

type computeInstancesProvider interface {
	GetComputeInstances(ctx context.Context) ([]topology.ComputeInstances, *httperr.Error)
}

func init() {
	backOff = defaultBackOff
}

func processRequest(item any) (any, *httperr.Error) {
	return processRequestWithRetries(item.(*topology.Request), processTopologyRequest)
}

func processRequestWithRetries(tr *topology.Request, f func(*topology.Request) ([]byte, *httperr.Error)) ([]byte, *httperr.Error) {
	attempt := 0
	for {
		var code int
		attempt++
		start := time.Now()

		ret, err := f(tr)
		if err != nil {
			code = err.Code()
		} else {
			code = http.StatusOK
		}
		metrics.AddTopologyRequest(tr.Provider.Name, tr.Engine.Name, code, time.Since(start))

		if !httpreq.ShouldRetry(code) || attempt == maxRetries {
			return ret, err
		}

		wait := httpreq.GetNextBackoff(nil, backOff, attempt-1)
		klog.Infof("Attempt %d failed with error: %v. Retrying in %s", attempt, err, wait.String())
		time.Sleep(wait)
	}
}

func processTopologyRequest(tr *topology.Request) ([]byte, *httperr.Error) {
	klog.InfoS("Creating topology config", "provider", tr.Provider.Name, "engine", tr.Engine.Name)
	defer klog.Info("Topology request completed")

	engLoader, err := registry.Engines.Get(tr.Engine.Name)
	if err != nil {
		return nil, err
	}

	prvLoader, err := registry.Providers.Get(tr.Provider.Name)
	if err != nil {
		return nil, err
	}

	ctx := context.Background()

	eng, err := engLoader(ctx, tr.Engine.Params)
	if err != nil {
		return nil, err
	}

	prv, err := prvLoader(ctx, providers.Config{
		Creds:  checkCredentials(tr.Provider.Creds, srv.cfg.Credentials),
		Params: tr.Provider.Params,
	})
	if err != nil {
		return nil, err
	}

	// if the instance/node mapping is not provided in the payload, get the mapping from the provider
	computeInstances, err := resolveComputeInstances(ctx, eng, prv, tr.Nodes)
	if err != nil {
		return nil, err
	}

	graph, err := prv.GenerateTopologyConfig(ctx, srv.cfg.PageSize, computeInstances)
	if err != nil {
		return nil, err
	}

	return eng.GenerateOutput(ctx, graph, tr.Engine.Params)
}

func checkCredentials(payloadCreds, cfgCreds map[string]any) map[string]any {
	if len(payloadCreds) != 0 {
		return payloadCreds
	}
	return cfgCreds
}

func resolveComputeInstances(ctx context.Context, eng engines.Engine, prv providers.Provider, requested []topology.ComputeInstances) ([]topology.ComputeInstances, *httperr.Error) {
	if len(requested) != 0 {
		return eng.ResolveComputeInstances(ctx, requested, prv)
	}

	if p, ok := prv.(computeInstancesProvider); ok {
		instances, err := p.GetComputeInstances(ctx)
		if err != nil {
			return nil, err
		}
		if len(instances) == 0 {
			return instances, nil
		}
		return eng.ResolveComputeInstances(ctx, instances, prv)
	}

	return eng.ResolveComputeInstances(ctx, nil, prv)
}
