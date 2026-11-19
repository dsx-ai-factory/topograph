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

package netq

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dsx-ai-factory/topograph/pkg/topology"
)

func TestAuthenticationFlow(t *testing.T) {
	graphData, err := os.ReadFile("../../../tests/output/netq/topologyGraph.json")
	require.NoError(t, err)
	var paths []string
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		switch r.URL.Path {
		case "/" + LoginURL:
			assert.Equal(t, http.MethodPost, r.Method)
			assert.Empty(t, r.Header.Get("Authorization"))
			assert.Equal(t, "application/json", r.Header.Get("Content-Type"))
			var body map[string]string
			assert.NoError(t, json.NewDecoder(r.Body).Decode(&body))
			assert.Equal(t, map[string]string{"username": "fake-user", "password": "fake-password"}, body)
			_, _ = io.WriteString(w, `{"access_token":"fake-login-token","premises":[{"config_key_viewed":false,"opid":9},{"config_key_viewed":true,"opid":7}]}`)
		case "/" + OpIdURL + "/7":
			assert.Equal(t, http.MethodGet, r.Method)
			assert.Equal(t, "Bearer fake-login-token", r.Header.Get("Authorization"))
			_, _ = io.WriteString(w, `{"access_token":"fake-premises-token"}`)
		case "/" + TopologyURL:
			assert.Equal(t, http.MethodPost, r.Method)
			assert.Equal(t, "Bearer fake-premises-token", r.Header.Get("Authorization"))
			assert.Equal(t, "0", r.URL.Query().Get("timestamp"))
			_, _ = w.Write(graphData)
		case "/" + ComputeURL:
			assert.Equal(t, http.MethodGet, r.Method)
			user, password, ok := r.BasicAuth()
			assert.True(t, ok)
			assert.Equal(t, "fake-user", user)
			assert.Equal(t, "fake-password", password)
			_, _ = io.WriteString(w, `[{"Name":"dgx-01","DomainUUID":"domain-1"}]`)
		default:
			t.Errorf("unexpected request: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	p := &Provider{params: &ProviderParams{ApiURL: server.URL}, creds: &Credentials{Username: "fake-user", Password: "fake-password"}}
	graph, httpErr := p.GenerateTopologyConfig(context.Background(), nil,
		[]topology.ComputeInstances{{Instances: map[string]string{"node21": "dgx-01"}}})
	require.Nil(t, httpErr)
	require.NotEmpty(t, graph.Tiers.Vertices)
	require.NotEmpty(t, graph.Domains)
	require.Equal(t, []string{"/" + LoginURL, "/" + OpIdURL + "/7", "/" + TopologyURL, "/" + ComputeURL}, paths)
}

func TestAuthenticationErrorStatusPreserved(t *testing.T) {
	calls := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		assert.Equal(t, "/"+LoginURL, r.URL.Path)
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, "login rejected")
	}))
	defer server.Close()
	p := &Provider{params: &ProviderParams{ApiURL: server.URL}, creds: &Credentials{Username: "fake-user", Password: "fake-password"}}
	_, err := p.getNetworkTree(context.Background(), nil)
	require.Equal(t, http.StatusUnauthorized, err.Code())
	require.EqualError(t, err, "HTTP request failed")
	require.Equal(t, 1, calls)
}

func TestSharedTokenAcquisitionFailures(t *testing.T) {
	for _, tc := range []struct {
		name      string
		selection bool
		status    int
		body      string
		expected  int
	}{
		{"empty login", false, 200, "", 502},
		{"malformed login", false, 200, "invalid", 502},
		{"missing token", false, 200, `{}`, 502},
		{"invalid premises", false, 200, `{"access_token":"fake-token","premises":{}}`, 502},
		{"blank token", false, 200, `{"access_token":" "}`, 502},
		{"header injection", false, 200, `{"access_token":"fake\r\nheader"}`, 502},
		{"login outage", false, 503, "unavailable", 503},
		{"empty selection", true, 200, "", 502},
		{"malformed selection", true, 200, "invalid", 502},
		{"selection unauthorized", true, 401, "selection rejected", 401},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if tc.selection && r.URL.Path == "/"+LoginURL {
					_, _ = io.WriteString(w, `{"access_token":"fake-login-token","premises":[{"config_key_viewed":true,"opid":7}]}`)
					return
				}
				expectedPath := "/" + LoginURL
				if tc.selection {
					expectedPath = "/" + OpIdURL + "/7"
					assert.Equal(t, "Bearer fake-login-token", r.Header.Get("Authorization"))
				}
				assert.Equal(t, expectedPath, r.URL.Path)
				w.Header().Set("Retry-After", "0")
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer server.Close()
			p := &Provider{params: &ProviderParams{ApiURL: server.URL}, creds: &Credentials{Username: "fake-user", Password: "fake-password"}}
			_, err := p.getNetworkTree(context.Background(), nil)
			require.NotNil(t, err)
			require.Equal(t, tc.expected, err.Code())
			expectedCalls := 1
			if tc.status == 503 {
				expectedCalls = 5
			}
			if tc.selection {
				expectedCalls++
			}
			require.Equal(t, expectedCalls, calls, "authentication failures must stop discovery after the shared HTTP retry policy")
		})
	}
}
