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

package providers

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"

	"k8s.io/klog/v2"

	"github.com/dsx-ai-factory/topograph/internal/component"
	"github.com/dsx-ai-factory/topograph/internal/httperr"
	"github.com/dsx-ai-factory/topograph/internal/httpreq"
	"github.com/dsx-ai-factory/topograph/pkg/topology"
)

type Provider interface {
	GenerateTopologyConfig(ctx context.Context, pageSize *int, instances []topology.ComputeInstances) (*topology.Graph, *httperr.Error)
}

type Config struct {
	Creds  map[string]any
	Params map[string]any
}
type NamedLoader = component.NamedLoader[Provider, Config]
type Loader = component.Loader[Provider, Config]
type Registry component.Registry[Provider, Config]

func NewRegistry(namedLoaders ...NamedLoader) Registry {
	return Registry(component.NewRegistry(namedLoaders...))
}

func (r Registry) Get(name string) (Loader, *httperr.Error) {
	loader, ok := r[name]
	if !ok {
		return nil, httperr.NewError(http.StatusBadRequest, fmt.Sprintf("unsupported provider %q", name))
	}

	return loader, nil
}

func HttpReq(ctx context.Context, method, url string, headers map[string]string) (string, error) {
	reqFunc := httpreq.GetRequestFunc(ctx, method, headers, nil, nil, url)
	data, err := httpreq.DoRequestWithRetries(reqFunc, false)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

func ParseInstanceOutput(buff *bytes.Buffer) (map[string]string, error) {
	return ParsePdshOutput(buff, false)
}

func ParsePdshOutput(buff *bytes.Buffer, direct bool) (map[string]string, error) {
	res := make(map[string]string)
	scanner := bufio.NewScanner(buff)
	for scanner.Scan() {
		arr := strings.Split(scanner.Text(), ": ")
		if len(arr) == 2 {
			node, attr := arr[0], arr[1]
			klog.V(4).Infof("Node name: %q Attribute: %q", node, attr)
			if direct {
				res[node] = attr
			} else {
				res[attr] = node
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, err
	}

	return res, nil
}

func ReadFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("failed to read %s: %v", path, err)
	}

	return string(data), nil
}

func GetTrimTiers(params map[string]any) (int, error) {
	v, ok := params[topology.KeyTrimTiers]
	if !ok || v == nil {
		return 0, nil
	}

	var trimTiers int
	switch val := v.(type) {
	case int:
		trimTiers = val
	case float64:
		trimTiers = int(val)
	default:
		return 0, fmt.Errorf("invalid '%s' value '%v': unsupported type %T", topology.KeyTrimTiers, v, v)
	}

	// support up to 2 trimmed tiers: core and spine
	if trimTiers < 0 || trimTiers > 2 {
		return 0, fmt.Errorf("invalid '%s' value '%v': must be an integer between 0 and 2", topology.KeyTrimTiers, v)
	}

	return trimTiers, nil
}

// GetIMDSURL returns the optional provider-level override for the Instance
// Metadata Service URL. It returns an empty string when unset, leaving the
// caller to fall back to its provider-specific default.
func GetIMDSURL(params map[string]any) (string, error) {
	v, ok := params[topology.KeyIMDSURL]
	if !ok || v == nil {
		return "", nil
	}

	url, ok := v.(string)
	if !ok {
		return "", fmt.Errorf("invalid '%s' value '%v': unsupported type %T", topology.KeyIMDSURL, v, v)
	}

	return url, nil
}
