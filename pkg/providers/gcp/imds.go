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

package gcp

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/dsx-ai-factory/topograph/internal/exec"
	"github.com/dsx-ai-factory/topograph/pkg/providers"
	"github.com/dsx-ai-factory/topograph/pkg/topology"
)

const (
	IMDSURL         = "http://metadata.google.internal/computeMetadata/v1"
	IMDSInstanceURL = IMDSURL + "/instance/id"
	IMDSRegionURL   = IMDSURL + "/instance/zone"
	IMDSHeaderKey   = "Metadata-Flavor"
	IMDSHeaderVal   = "Google"
	IMDSHeader      = IMDSHeaderKey + ": " + IMDSHeaderVal
)

func instanceToNodeMap(ctx context.Context, nodes []string) (map[string]string, error) {
	stdout, err := exec.Pdsh(ctx, pdshCmd(IMDSInstanceURL), nodes)
	if err != nil {
		return nil, err
	}

	return providers.ParseInstanceOutput(stdout)
}

func getRegions(ctx context.Context, nodes []string) (map[string]string, error) {
	stdout, err := exec.Pdsh(ctx, pdshCmd(IMDSRegionURL), nodes)
	if err != nil {
		return nil, err
	}

	res, err := providers.ParsePdshOutput(stdout, true)
	if err != nil {
		return nil, err
	}

	for key, val := range res {
		res[key] = convertRegion(strings.TrimSpace(val))
	}

	return res, nil
}

func pdshCmd(url string) string {
	return fmt.Sprintf("echo $(curl -s -H %q %s)", IMDSHeader, url)
}

func convertRegion(region string) string {
	// convert "projects/<project id>/zones/<region>" to "<region>"
	indx := strings.LastIndex(region, "/")
	return region[indx+1:]
}

func GetNodeAnnotations(ctx context.Context) (map[string]string, error) {
	header := map[string]string{IMDSHeaderKey: IMDSHeaderVal}
	instance, err := providers.HttpReq(ctx, http.MethodGet, IMDSInstanceURL, header)
	if err != nil {
		return nil, fmt.Errorf("failed to execute instance-id IMDS request: %v", err)
	}

	region, err := providers.HttpReq(ctx, http.MethodGet, IMDSRegionURL, header)
	if err != nil {
		return nil, fmt.Errorf("failed to execute region IMDS request: %v", err)
	}

	return map[string]string{
		topology.KeyNodeInstance: instance,
		topology.KeyNodeRegion:   convertRegion(region),
	}, nil
}
