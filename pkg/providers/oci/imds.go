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
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"k8s.io/klog/v2"

	"github.com/dsx-ai-factory/topograph/internal/exec"
	"github.com/dsx-ai-factory/topograph/pkg/providers"
	"github.com/dsx-ai-factory/topograph/pkg/topology"
)

const (
	IMDSURL         = "http://169.254.169.254/opc/v2"
	IMDSInstanceURL = IMDSURL + "/instance/id"
	IMDSRegionURL   = IMDSURL + "/instance/region"
	IMDSTopologyURL = IMDSURL + "/host/rdmaTopologyData"
	IMDSHeaderKey   = "Authorization"
	IMDSHeaderVal   = "Bearer Oracle"
	IMDSHeader      = IMDSHeaderKey + ": " + IMDSHeaderVal
)

type topologyData struct {
	GpuMemoryFabric string `json:"customerGpuMemoryFabric"`
	HPCIslandId     string `json:"customerHPCIslandId"`
	LocalBlock      string `json:"customerLocalBlock"`
	NetworkBlock    string `json:"customerNetworkBlock"`
}

func instanceToNodeMap(ctx context.Context, nodes []string) (map[string]string, error) {
	stdout, err := exec.Pdsh(ctx, pdshCmd(IMDSInstanceURL), nodes)
	if err != nil {
		return nil, err
	}

	return providers.ParseInstanceOutput(stdout)
}

func getHostTopology(ctx context.Context, nodes []string) (map[string]*topologyData, error) {
	stdout, err := exec.Pdsh(ctx, pdshCmd(IMDSTopologyURL), nodes)
	if err != nil {
		return nil, err
	}

	return parseTopologyOutput(stdout)
}

func parseTopologyOutput(buff *bytes.Buffer) (map[string]*topologyData, error) {
	topoMap := map[string]*topologyData{}
	scanner := bufio.NewScanner(buff)
	for scanner.Scan() {
		str := scanner.Text()
		indx := strings.Index(str, ": ")
		if indx != -1 {
			node, data := str[:indx], str[indx+2:]
			klog.V(4).Info("Node name: ", node, " Topology: ", data)
			nodeTopology := &topologyData{}
			err := json.Unmarshal([]byte(data), nodeTopology)
			if err != nil {
				klog.Warningf("Failed to parse topology data for node %s: %v", node, err)
			} else {
				topoMap[node] = nodeTopology
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, err
	}

	return topoMap, nil
}

func getRegions(ctx context.Context, nodes []string) (map[string]string, error) {
	stdout, err := exec.Pdsh(ctx, pdshCmd(IMDSRegionURL), nodes)
	if err != nil {
		return nil, err
	}

	return providers.ParsePdshOutput(stdout, true)
}

func pdshCmd(url string) string {
	return fmt.Sprintf("echo $(curl -s -H %q -L %s)", IMDSHeader, url)
}

func GetNodeAnnotations(ctx context.Context) (map[string]string, error) {
	header := map[string]string{IMDSHeaderKey: IMDSHeaderVal}
	instance, err := providers.HttpReq(ctx, http.MethodGet, IMDSInstanceURL, header)
	if err != nil {
		return nil, fmt.Errorf("failed to execute instance/id IMDS request: %v", err)
	}

	region, err := providers.HttpReq(ctx, http.MethodGet, IMDSRegionURL, header)
	if err != nil {
		return nil, fmt.Errorf("failed to execute instance/region IMDS request: %v", err)
	}

	return map[string]string{
		topology.KeyNodeInstance: instance,
		topology.KeyNodeRegion:   region,
	}, nil
}
