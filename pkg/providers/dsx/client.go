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
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/dsx-ai-factory/topograph/internal/httperr"
	"github.com/dsx-ai-factory/topograph/internal/httpreq"
	"github.com/dsx-ai-factory/topograph/internal/oauth"
)

const (
	// pathVPCNodesFmt is the VPC-scoped topology endpoint.
	pathVPCNodesFmt = "/v1/topology/vpcs/%s/nodes"

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

	// maxPageSize is the API's documented ceiling for page_size.
	maxPageSize = 1000
)

type httpClient struct {
	baseURL string
	// tokenFn supplies the Bearer token for each request; nil means no
	// Authorization header, and the Envoy sidecar authenticates the caller.
	tokenFn oauth.TokenFunc
}

// NewHTTPClient returns a Client that sends a static Bearer token. An empty
// token omits the Authorization header.
func NewHTTPClient(baseURL, token string) *httpClient {
	c := &httpClient{baseURL: baseURL}
	if token != "" {
		c.tokenFn = func(context.Context) (string, error) { return token, nil }
	}
	return c
}

// NewHTTPClientWithTokenSource returns a Client that gets its Bearer token from
// tokenFn on every request, for example an OAuth2 source that caches and
// refreshes the token.
func NewHTTPClientWithTokenSource(baseURL string, tokenFn oauth.TokenFunc) *httpClient {
	return &httpClient{baseURL: baseURL, tokenFn: tokenFn}
}

// clampPageSize limits a positive page size to the API range; zero means "use
// the API default" and is left alone.
func clampPageSize(n int) int {
	if n <= 0 {
		return n
	}
	return min(max(n, minPageSize), maxPageSize)
}

func (c *httpClient) GetTopology(ctx context.Context, vpcID string, pageSize int, pageToken string) (*TopologyResponse, error) {
	// Derive callCtx from context.Background() so requestTimeout is the true
	// per-page deadline, independent of any total-generation deadline already
	// set on ctx. context.AfterFunc propagates ctx cancellation so that a
	// caller-side abort (e.g. genCtx expiry) still terminates the page fetch.
	callCtx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()
	stopPropagation := context.AfterFunc(ctx, cancel)
	defer stopPropagation()

	headers := map[string]string{}
	if c.tokenFn != nil {
		token, err := c.tokenFn(callCtx)
		if err != nil {
			return nil, httperr.NewError(http.StatusBadGateway, "failed to obtain access token: "+err.Error())
		}
		headers["Authorization"] = "Bearer " + token
	}

	pageSize = clampPageSize(pageSize)

	query := map[string]string{}
	if pageSize > 0 {
		query["page_size"] = strconv.Itoa(pageSize)
	}
	if pageToken != "" {
		query["page_token"] = pageToken
	}

	f := httpreq.GetRequestFunc(callCtx, http.MethodGet, headers, query, nil, c.baseURL, fmt.Sprintf(pathVPCNodesFmt, vpcID))
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
