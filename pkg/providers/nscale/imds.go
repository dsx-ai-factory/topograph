/*
 * Copyright 2026 NVIDIA CORPORATION
 * SPDX-License-Identifier: Apache-2.0
 */

package nscale

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
	IMDSURL = "http://169.254.169.254/openstack/latest/meta_data.json"

	metaKeyServerID = "serverID"
	metaKeyRegionID = "regionID"
)

type imdsMetadata struct {
	Meta map[string]string `json:"meta"`
}

func pdshCmd(url string) string {
	return fmt.Sprintf("res=$(curl -fsS -- %s) && echo \"$res\"", shellQuote(url))
}

// shellQuote wraps s in single quotes for safe embedding in a POSIX shell
// command, escaping any single quotes it contains.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// maxIMDSLineSize bounds the per-node line length parseIMDSOutput will
// scan, so a pathological pdsh line does not silently truncate the output.
const maxIMDSLineSize = 1 << 20 // 1 MiB

func parseIMDSOutput(buff *bytes.Buffer) (map[string]*imdsMetadata, error) {
	res := make(map[string]*imdsMetadata)
	scanner := bufio.NewScanner(buff)
	scanner.Buffer(make([]byte, 0, 64*1024), maxIMDSLineSize)
	for scanner.Scan() {
		str := scanner.Text()
		idx := strings.Index(str, ": ")
		if idx == -1 {
			continue
		}
		node, data := str[:idx], str[idx+2:]

		metadata := &imdsMetadata{}
		if err := json.Unmarshal([]byte(data), metadata); err != nil {
			klog.Warningf("failed to parse IMDS response for node %s: %v", node, err)
			continue
		}
		res[node] = metadata
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("failed to scan IMDS output: %v", err)
	}

	return res, nil
}

func fetchIMDSMetadata(ctx context.Context, nodes []string, imdsURL string) (map[string]*imdsMetadata, error) {
	stdout, err := exec.PdshTolerant(ctx, pdshCmd(imdsURL), nodes)
	if err != nil {
		return nil, err
	}

	return parseIMDSOutput(stdout)
}

// filterByRegion drops nodes whose IMDS region does not match expectedRegion,
// logging a warning for each one. An empty expectedRegion disables filtering.
func filterByRegion(nodeMeta map[string]*imdsMetadata, expectedRegion string) map[string]*imdsMetadata {
	if len(expectedRegion) == 0 {
		return nodeMeta
	}

	filtered := make(map[string]*imdsMetadata, len(nodeMeta))
	for node, metadata := range nodeMeta {
		region := metadata.Meta[metaKeyRegionID]
		if region != expectedRegion {
			klog.Warningf("excluding node %s from topology query: IMDS region %q does not match configured region %q", node, region, expectedRegion)
			continue
		}
		filtered[node] = metadata
	}

	return filtered
}

func instanceToNodeMap(nodeMeta map[string]*imdsMetadata) map[string]string {
	i2n := make(map[string]string, len(nodeMeta))
	for node, metadata := range nodeMeta {
		serverID := metadata.Meta[metaKeyServerID]
		if len(serverID) == 0 {
			continue
		}
		i2n[serverID] = node
	}

	return i2n
}

func getRegions(nodeMeta map[string]*imdsMetadata) map[string]string {
	regions := make(map[string]string, len(nodeMeta))
	for node, metadata := range nodeMeta {
		if region := metadata.Meta[metaKeyRegionID]; len(region) > 0 {
			regions[node] = region
		}
	}

	return regions
}

// GetNodeAnnotations queries the local IMDS endpoint for this node's server
// ID and region. params is the provider's raw config parameters; an
// 'imdsUrl' entry overrides the default IMDS endpoint.
func GetNodeAnnotations(ctx context.Context, params map[string]any) (map[string]string, error) {
	imdsURL, err := providers.GetIMDSURL(params)
	if err != nil {
		return nil, err
	}
	if len(imdsURL) == 0 {
		imdsURL = IMDSURL
	}

	data, err := providers.HttpReq(ctx, http.MethodGet, imdsURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to execute IMDS request: %v", err)
	}

	metadata := &imdsMetadata{}
	if err := json.Unmarshal([]byte(data), metadata); err != nil {
		return nil, fmt.Errorf("failed to parse IMDS response: %v", err)
	}

	serverID := metadata.Meta[metaKeyServerID]
	if len(serverID) == 0 {
		return nil, fmt.Errorf("missing %q in IMDS response", metaKeyServerID)
	}

	annotations := map[string]string{
		topology.KeyNodeInstance: serverID,
	}

	if region := metadata.Meta[metaKeyRegionID]; len(region) > 0 {
		annotations[topology.KeyNodeRegion] = region
	}

	return annotations, nil
}
