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

package dsx

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"k8s.io/klog/v2"

	"github.com/dsx-ai-factory/topograph/internal/httperr"
	"github.com/dsx-ai-factory/topograph/internal/httpreq"
)

const (
	pathNodes = "/v1/topology/nodes"

	// requestTimeout bounds each individual page fetch — connection, response
	// headers, and full body — including up to httpreq.maxRetries (5) retries
	// with httpreq.maxRetryAfter (5 min) Retry-After back-off. The 30-minute
	// ceiling allows legitimate rate-limit retry sequences to complete while
	// still terminating a permanently stalled connection.
	requestTimeout = 30 * time.Minute

	// minPageSize is the floor applied to caller-supplied page sizes. The DSX
	// API defaults to 100; values below this minimum create excessive pages,
	// which can exhaust maxPaginationPages before a finite response completes.
	minPageSize = 100
)

type httpClient struct {
	baseURL string
	token   string
}

// NewHTTPClient returns a Client that calls the DSX Topology API.
// If token is empty the Authorization header is omitted and the Envoy sidecar
// is expected to supply SVID-based authentication transparently.
func NewHTTPClient(baseURL, token string) *httpClient {
	return &httpClient{baseURL: baseURL, token: token}
}

func (c *httpClient) GetTopology(ctx context.Context, vpcID string, nodeIDs []string, pageSize int, pageToken string) (*TopologyResponse, error) {
	// Derive callCtx from context.Background() so requestTimeout is the true
	// per-page deadline, independent of any total-generation deadline already
	// set on ctx. context.AfterFunc propagates ctx cancellation so that a
	// caller-side abort (e.g. genCtx expiry) still terminates the page fetch.
	callCtx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()
	stopPropagation := context.AfterFunc(ctx, cancel)
	defer stopPropagation()

	path := pathNodes
	if vpcID != "" {
		path = "/v1/topology/vpcs/" + vpcID + "/nodes"
	}

	headers := map[string]string{}
	if c.token != "" {
		headers["Authorization"] = "Bearer " + c.token
	}

	if pageSize > 0 && pageSize < minPageSize {
		klog.Warningf("DSX page size %d is below minimum %d; clamping to avoid excessive pagination", pageSize, minPageSize)
		pageSize = minPageSize
	}

	query := map[string]string{}
	if len(nodeIDs) > 0 {
		query["node_ids"] = strings.Join(nodeIDs, ",")
	}
	if pageSize > 0 {
		query["page_size"] = strconv.Itoa(pageSize)
	}
	if pageToken != "" {
		query["page_token"] = pageToken
	}

	f := httpreq.GetRequestFunc(callCtx, http.MethodGet, headers, query, nil, c.baseURL, path)
	body, httpErr := httpreq.DoRequestWithRetries(f, false)
	if httpErr != nil {
		return nil, httpErr
	}

	var resp TopologyResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, httperr.NewError(http.StatusBadGateway, err.Error())
	}

	return &resp, nil
}
