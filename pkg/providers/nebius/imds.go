/*
 * Copyright 2025 NVIDIA CORPORATION
 * SPDX-License-Identifier: Apache-2.0
 */

package nebius

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/dsx-ai-factory/topograph/internal/exec"
	"github.com/dsx-ai-factory/topograph/pkg/providers"
	"github.com/dsx-ai-factory/topograph/pkg/topology"
)

const (
	IMDSURL           = "http://metadata.nebius.internal/v1"
	IMDSInstanceURL   = IMDSURL + "/instance-data"
	IMDSInstanceIDURL = IMDSInstanceURL + "/id"
	IMDSParentIDURL   = IMDSInstanceURL + "/parent_id"
	IMDSRegionURL     = IMDSInstanceURL + "/region"
	IMDSTokenURL      = IMDSURL + "/iam/sa/token/access_token"
	IMDSHeaderKey     = "Metadata"
	IMDSHeaderVal     = "true"
	IMDSHeader        = IMDSHeaderKey + ": " + IMDSHeaderVal
)

type instanceData struct {
	ID     string `json:"id"`
	Region string `json:"region"`
}

func instanceToNodeMap(ctx context.Context, nodes []string) (map[string]string, error) {
	stdout, err := exec.Pdsh(ctx, imdsCmd(IMDSInstanceIDURL), nodes)
	if err != nil {
		return nil, err
	}

	return providers.ParseInstanceOutput(stdout)
}

func getRegions(ctx context.Context, nodes []string) (map[string]string, error) {
	stdout, err := exec.Pdsh(ctx, imdsCmd(IMDSRegionURL), nodes)
	if err != nil {
		return nil, err
	}

	return providers.ParsePdshOutput(stdout, true)
}

func imdsCmd(url string) string {
	return fmt.Sprintf("v=$(curl -fsS -H %q %s) && printf '%%s\\n' \"$v\"", IMDSHeader, url)
}

func getParentID(ctx context.Context) (string, error) {
	return providers.HttpReq(ctx, http.MethodGet, IMDSParentIDURL, map[string]string{IMDSHeaderKey: IMDSHeaderVal})
}

func getAccessToken(ctx context.Context) (string, error) {
	return providers.HttpReq(ctx, http.MethodGet, IMDSTokenURL, map[string]string{IMDSHeaderKey: IMDSHeaderVal})
}

func GetNodeAnnotations(ctx context.Context) (map[string]string, error) {
	data, err := providers.HttpReq(ctx, http.MethodGet, IMDSInstanceURL, map[string]string{IMDSHeaderKey: IMDSHeaderVal})
	if err != nil {
		return nil, err
	}

	metadata := &instanceData{}
	if err := json.Unmarshal([]byte(data), metadata); err != nil {
		return nil, fmt.Errorf("failed to parse instance data IMDS response: %v", err)
	}

	return map[string]string{
		topology.KeyNodeInstance: strings.TrimSpace(metadata.ID),
		topology.KeyNodeRegion:   strings.TrimSpace(metadata.Region),
	}, nil
}
