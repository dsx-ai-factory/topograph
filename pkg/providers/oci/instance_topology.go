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

package oci

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/oracle/oci-go-sdk/v65/core"
	"k8s.io/klog/v2"

	"github.com/dsx-ai-factory/topograph/pkg/topology"
)

func getComputeHostSummary(ctx context.Context, client Client, availabilityDomain *string, topo *topology.ClusterTopology, instMap map[string]string) error {
	req := core.ListComputeHostsRequest{
		CompartmentId:      client.TenantID(),
		AvailabilityDomain: availabilityDomain,
		Limit:              client.Limit(),
	}
	seenPages := make(map[string]struct{})

	for {
		klog.V(4).InfoS("ListComputeHosts", "request", req.String())
		start := time.Now()
		resp, err := client.ListComputeHosts(ctx, req)
		reportLatency(resp.HTTPResponse(), start, "ListComputeHosts")
		if err != nil {
			return err
		}
		if resp.OpcNextPage != nil {
			nextPage := *resp.OpcNextPage
			if _, ok := seenPages[nextPage]; ok {
				return fmt.Errorf("ListComputeHosts returned repeated page token %q", nextPage)
			}
			seenPages[nextPage] = struct{}{}
		}
		nRec := len(resp.Items)
		klog.V(4).Infof("received %d records", nRec)
		instances := make([]*topology.InstanceTopology, nRec)
		getErrors := make([]error, nRec)
		var wg sync.WaitGroup
		for i := range resp.Items {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()

				hostSummary := &resp.Items[i]
				if hostSummary.Id == nil {
					klog.Warning("missing Id in ComputeHostSummary")
					return
				}

				if hostSummary.InstanceId == nil {
					klog.Warning("missing InstanceId in ComputeHostSummary")
					return
				}

				if _, ok := instMap[*hostSummary.InstanceId]; !ok {
					klog.V(4).Infof("Skipping instance %s", *hostSummary.InstanceId)
					return
				}

				getReq := core.GetComputeHostRequest{
					ComputeHostId: hostSummary.Id,
				}
				klog.V(4).InfoS("GetComputeHost", "request", getReq.String())
				getResp, err := client.GetComputeHost(ctx, getReq)
				if err != nil {
					getErrors[i] = err
					return
				}

				inst, err := convertComputeHost(&getResp.ComputeHost)
				if err != nil {
					klog.Warning(err.Error())
					return
				}
				klog.V(4).Infof("Adding host %s", getResp.ComputeHost.String())
				instances[i] = inst
			}(i)
		}
		wg.Wait()
		klog.V(4).Infof("processed %d records", nRec)

		for i, inst := range instances {
			if getErrors[i] != nil {
				return getErrors[i]
			}
			if inst != nil {
				topo.Append(inst)
			}
		}

		if resp.OpcNextPage == nil {
			return nil
		}
		req.Page = resp.OpcNextPage
	}
}

func convert(host *core.ComputeHostSummary) (*topology.InstanceTopology, error) {
	return convertHost(
		host.InstanceId,
		host.LocalBlockId,
		host.NetworkBlockId,
		host.HpcIslandId,
		host.GpuMemoryFabricId,
		"",
	)
}

func convertComputeHost(host *core.ComputeHost) (*topology.InstanceTopology, error) {
	locationDetails, ok := host.AdditionalData["locationDetails"].(map[string]any)
	var rack string
	if ok {
		if v, ok := locationDetails["rack"].(string); ok {
			rack = v
		}
	}

	return convertHost(
		host.InstanceId,
		host.LocalBlockId,
		host.NetworkBlockId,
		host.HpcIslandId,
		host.GpuMemoryFabricId,
		rack,
	)
}

func convertHost(instanceID, localBlockID, networkBlockID, hpcIslandID, gpuMemoryFabricID *string, rack string) (*topology.InstanceTopology, error) {
	if instanceID == nil {
		return nil, fmt.Errorf("missing InstanceId in ComputeHostSummary")
	}

	if localBlockID == nil {
		missingHostData.WithLabelValues("localBlock", *instanceID).Add(float64(1))
		return nil, fmt.Errorf("missing LocalBlockId for instance %q", *instanceID)
	}

	if networkBlockID == nil {
		missingHostData.WithLabelValues("networkBlock", *instanceID).Add(float64(1))
		return nil, fmt.Errorf("missing NetworkBlockId for instance %q", *instanceID)
	}

	if hpcIslandID == nil {
		missingHostData.WithLabelValues("hpcIsland", *instanceID).Add(float64(1))
		return nil, fmt.Errorf("missing HpcIslandId for instance %q", *instanceID)
	}

	topo := &topology.InstanceTopology{
		InstanceID: *instanceID,
		FabricTiers: topology.ClosestFirstFabricTiers(
			*localBlockID, *networkBlockID, *hpcIslandID,
		),
	}

	if gpuMemoryFabricID != nil {
		topo.XclrDomainID = *gpuMemoryFabricID

		if rack != "" {
			topo.XclrSubDomainID = topo.XclrDomainID + "." + rack
		}
	}

	return topo, nil
}

func reportLatency(resp *http.Response, since time.Time, method string) {
	duration := time.Since(since).Seconds()
	if resp != nil {
		requestLatency.WithLabelValues(method, resp.Status).Observe(duration)
	} else {
		requestLatency.WithLabelValues(method, "Fatal").Observe(duration)
	}
}
