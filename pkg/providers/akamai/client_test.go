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
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dsx-ai-factory/topograph/pkg/providers"
	"github.com/dsx-ai-factory/topograph/pkg/topology"
)

func TestClientFailures(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusTooManyRequests, http.StatusServiceUnavailable} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				assert.Equal(t, "/topology", r.URL.Path)
				assert.Equal(t, "Bearer fake-test-token", r.Header.Get("Authorization"))
				w.WriteHeader(status)
				_, _ = w.Write([]byte("private token details"))
			}))
			defer server.Close()
			p, err := Loader(context.Background(), providers.Config{Creds: fakeCredentials(), Params: map[string]any{"apiUrl": server.URL}})
			require.Nil(t, err)
			g, err := p.GenerateTopologyConfig(context.Background(), nil, []topology.ComputeInstances{{Region: "r", Instances: map[string]string{"n1": "worker"}}})
			require.Nil(t, g)
			require.Equal(t, status, err.Code())
			require.NotContains(t, err.Error(), "private")
			require.Equal(t, 1, calls, "rejected tokens do not trigger retries or authentication requests")
		})
	}
}

func TestInvalidResponses(t *testing.T) {
	for _, tt := range []struct{ name, body string }{
		{"malformed", "invalid"}, {"null", "null"},
		{"missing data", `{"region":"r","page":1,"pages":1}`},
		{"wrong region", `{"region":"other","data":[],"page":1,"pages":1}`},
		{"repeated page", `{"region":"r","data":[],"page":1,"pages":2}`},
		{"missing result records", `{"region":"r","data":[],"page":1,"pages":1,"results":3}`},
		{"negative pages", `{"region":"r","data":[],"page":1,"pages":-1}`},
		{"trailing data", `{"region":"r","data":[],"page":1,"pages":1} invalid`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(tt.body))
			}))
			defer server.Close()
			p, err := Loader(context.Background(), providers.Config{Creds: fakeCredentials(), Params: map[string]any{"apiUrl": server.URL}})
			require.Nil(t, err)
			g, err := p.GenerateTopologyConfig(context.Background(), nil, []topology.ComputeInstances{{Region: "r", Instances: map[string]string{"n1": "worker"}}})
			require.Nil(t, g)
			require.NotNil(t, err)
			require.Equal(t, 502, err.Code())
		})
	}
}

func TestLaterPageFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page") == "2" {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		assert.NoError(t, json.NewEncoder(w).Encode(TopologyResult{Region: "r", Page: 1, Pages: 2, Results: 2, Data: simpleRecords()[:1]}))
	}))
	defer server.Close()
	p, err := Loader(context.Background(), providers.Config{Creds: fakeCredentials(), Params: map[string]any{"apiUrl": server.URL}})
	require.Nil(t, err)
	g, err := p.GenerateTopologyConfig(context.Background(), nil, []topology.ComputeInstances{{Region: "r", Instances: map[string]string{"n1": "worker"}}})
	require.Nil(t, g)
	require.Equal(t, 503, err.Code())
}

func TestRedirectDoesNotForwardToken(t *testing.T) {
	forwarded := 0
	target := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) { forwarded++ }))
	defer target.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/topology", http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	p, err := Loader(context.Background(), providers.Config{Creds: fakeCredentials(), Params: map[string]any{"apiUrl": server.URL}})
	require.Nil(t, err)
	_, err = p.GenerateTopologyConfig(context.Background(), nil, []topology.ComputeInstances{{Region: "r", Instances: map[string]string{"n1": "worker"}}})
	require.Equal(t, 502, err.Code())
	require.Zero(t, forwarded)
}

func TestTransportFailureAndCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) { t.Error("canceled request reached server") }))
	defer server.Close()
	p, err := Loader(context.Background(), providers.Config{Creds: fakeCredentials(), Params: map[string]any{"apiUrl": server.URL}})
	require.Nil(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = p.GenerateTopologyConfig(ctx, nil, []topology.ComputeInstances{{Region: "r", Instances: map[string]string{"n1": "worker"}}})
	require.Equal(t, 502, err.Code())
	server.Close()
	_, err = p.GenerateTopologyConfig(context.Background(), nil, []topology.ComputeInstances{{Region: "r", Instances: map[string]string{"n1": "worker"}}})
	require.Equal(t, 502, err.Code())
}
