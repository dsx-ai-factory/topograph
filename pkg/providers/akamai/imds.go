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

package akamai

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
	// IMDSURL is the node-local Akamai instance metadata service.
	IMDSURL         = "http://169.254.169.254/v1"
	IMDSInstanceURL = IMDSURL + "/instance"
	IMDSTokenURL    = IMDSURL + "/token"
	imdsTokenExpiry = "3600"

	// maxIMDSLineSize bounds the per-node line length parseIMDSOutput will scan.
	maxIMDSLineSize = 1 << 20
)

type imdsMetadata struct {
	HostUUID string `json:"host_uuid"`
	Region   string `json:"region"`
}

// GetNodeAnnotations obtains a node-local metadata token, then decodes host_uuid
// and region from IMDS JSON for the Node Data Broker's identity annotations.
// The instance and token endpoints are fixed under IMDSURL.
// The topology API's credentials are not used by IMDS.
func GetNodeAnnotations(ctx context.Context, _ map[string]any) (map[string]string, error) {
	token, err := providers.HttpReq(ctx, http.MethodPut, IMDSTokenURL, map[string]string{
		"Accept":                        "text/plain",
		"Metadata-Token-Expiry-Seconds": imdsTokenExpiry,
	})
	if err != nil {
		return nil, fmt.Errorf("akamai IMDS token request failed: %w", err)
	}
	if token == "" || strings.ContainsAny(token, "\r\n") {
		return nil, fmt.Errorf("invalid Akamai IMDS token response")
	}
	data, err := providers.HttpReq(ctx, http.MethodGet, IMDSInstanceURL, map[string]string{
		"Accept":         "application/json",
		"Metadata-Token": token,
	})
	if err != nil {
		return nil, fmt.Errorf("akamai IMDS instance request failed: %w", err)
	}
	metadata, err := parseIMDSMetadata(data)
	if err != nil {
		return nil, err
	}
	return map[string]string{
		topology.KeyNodeInstance: metadata.HostUUID,
		topology.KeyNodeRegion:   metadata.Region,
	}, nil
}

// parseIMDSMetadata decodes an instance IMDS response and rejects incomplete
// identity metadata.
func parseIMDSMetadata(data string) (*imdsMetadata, error) {
	metadata := &imdsMetadata{}
	if err := json.Unmarshal([]byte(data), metadata); err != nil {
		return nil, fmt.Errorf("failed to parse Akamai IMDS response: %w", err)
	}
	metadata.HostUUID = strings.TrimSpace(metadata.HostUUID)
	metadata.Region = strings.TrimSpace(metadata.Region)
	if metadata.HostUUID == "" {
		return nil, fmt.Errorf("missing or empty host_uuid in Akamai IMDS response")
	}
	if metadata.Region == "" {
		return nil, fmt.Errorf("missing or empty region in Akamai IMDS response")
	}
	return metadata, nil
}

// shellQuote wraps s in single quotes for safe embedding in a POSIX shell
// command, escaping any single quotes it contains.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// pdshCmd runs the same token exchange as getNodeAnnotations on each node and
// prints the instance JSON on a single line, as pdsh prefixes every line.
func pdshCmd() string {
	return fmt.Sprintf(
		"token=$(curl -fsS -X PUT -H 'Metadata-Token-Expiry-Seconds: %s' -- %s) && "+
			"res=$(curl -fsS -H \"Metadata-Token: $token\" -H 'Accept: application/json' -- %s) && "+
			"echo \"$res\" | tr -d '\\r\\n' && echo",
		imdsTokenExpiry, shellQuote(IMDSTokenURL), shellQuote(IMDSInstanceURL))
}

// parseIMDSOutput decodes pdsh output of "node: {json}" lines. Nodes with
// unparsable or incomplete metadata are skipped with a warning.
func parseIMDSOutput(buff *bytes.Buffer) (map[string]*imdsMetadata, error) {
	res := make(map[string]*imdsMetadata)
	scanner := bufio.NewScanner(buff)
	scanner.Buffer(make([]byte, 0, 64*1024), maxIMDSLineSize)
	for scanner.Scan() {
		node, data, ok := strings.Cut(scanner.Text(), ": ")
		if !ok {
			continue
		}
		metadata, err := parseIMDSMetadata(data)
		if err != nil {
			klog.Warningf("skipping node %s: %v", node, err)
			continue
		}
		res[node] = metadata
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("failed to scan IMDS output: %v", err)
	}
	return res, nil
}

// getNodeInstances groups the nodes' host_uuid values by region.
func getNodeInstances(nodeMeta map[string]*imdsMetadata) []topology.ComputeInstances {
	i2n := make(map[string]string, len(nodeMeta))
	regions := make(map[string]string, len(nodeMeta))
	for node, metadata := range nodeMeta {
		i2n[metadata.HostUUID] = node
		regions[node] = metadata.Region
	}
	return providers.GroupByRegion(i2n, regions)
}

// GetNodeInstances implements slurm.instanceMapper
func (p *Provider) GetNodeInstances(ctx context.Context, nodes []string) ([]topology.ComputeInstances, error) {
	stdout, err := exec.PdshTolerant(ctx, pdshCmd(), nodes)
	if err != nil {
		return nil, err
	}
	nodeMeta, err := parseIMDSOutput(stdout)
	if err != nil {
		return nil, err
	}
	return getNodeInstances(nodeMeta), nil
}
