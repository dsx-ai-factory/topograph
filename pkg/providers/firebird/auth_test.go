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

package firebird

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/dsx-ai-factory/topograph/pkg/providers"
	"github.com/dsx-ai-factory/topograph/pkg/topology"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCredentialsRequired(t *testing.T) {
	for _, creds := range []map[string]any{
		nil, {"token": "fake-legacy-token"}, {"clientId": "fake-client-id"},
		{"clientSecret": "fake-client-secret"}, {"clientId": " ", "clientSecret": "fake-client-secret"},
		{"clientId": 12, "clientSecret": "fake-client-secret"},
	} {
		_, err := Loader(context.Background(), providers.Config{Params: map[string]any{"apiUrl": "https://example.com"}, Creds: creds})
		require.NotNil(t, err)
		require.Equal(t, http.StatusBadRequest, err.Code())
		require.NotContains(t, err.Error(), "fake-client-secret")
	}
}

func TestTokenPerGenerationRequest(t *testing.T) {
	for _, lifetime := range []string{`,"expiresIn":3600`, `,"expiresIn":0`, ""} {
		t.Run(lifetime, func(t *testing.T) {
			var authCalls atomic.Int32
			var pages sync.Map
			server := newTrustedTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/auth/token" {
					assert.Equal(t, http.MethodPost, r.Method)
					assert.Equal(t, "application/json", r.Header.Get("Content-Type"))
					assert.Equal(t, "application/json", r.Header.Get("Accept"))
					assert.Empty(t, r.Header.Get("Authorization"))
					var credentials map[string]string
					assert.NoError(t, json.NewDecoder(r.Body).Decode(&credentials))
					assert.Equal(t, map[string]string{"grantType": "client_credentials", "clientId": "fake-client-id", "clientSecret": "fake-client-secret"}, credentials)
					token := fmt.Sprintf("fake-token-%d", authCalls.Add(1))
					pages.Store("Bearer "+token, new(atomic.Int32))
					_, _ = fmt.Fprintf(w, `{"accessToken":%q,"tokenType":"Bearer"%s}`, token, lifetime)
					return
				}
				assert.Equal(t, "/api/topology/nodes", r.URL.Path)
				count, ok := pages.Load(r.Header.Get("Authorization"))
				if !assert.True(t, ok, "page must use a token issued for this request") {
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
				switch count.(*atomic.Int32).Add(1) {
				case 1:
					assert.Empty(t, r.URL.Query().Get("pageToken"))
					_, _ = w.Write([]byte(`{"nodes":[],"nextPageToken":"last"}`))
				case 2:
					assert.Equal(t, "last", r.URL.Query().Get("pageToken"))
					_, _ = w.Write([]byte(`{"nodes":[{"nodeId":"n1","networkPath":["leaf"]}]}`))
				default:
					t.Error("token reused across generation requests")
					w.WriteHeader(http.StatusUnauthorized)
				}
			}))
			p, err := Loader(context.Background(), providers.Config{Params: map[string]any{"apiUrl": server.URL + "/api"}, Creds: fakeCredentials()})
			require.Nil(t, err)
			_, err = p.GenerateTopologyConfig(context.Background(), nil, nil)
			require.Nil(t, err)
			require.Zero(t, authCalls.Load(), "empty requests must not authenticate")
			generate := func() {
				graph, err := p.GenerateTopologyConfig(context.Background(), nil, []topology.ComputeInstances{{Instances: map[string]string{"n1": "worker"}}})
				if assert.Nil(t, err) && assert.NotNil(t, graph) {
					assert.Equal(t, []string{"leaf"}, graph.Instances["n1"].NetworkLayers)
				}
			}
			// Sequential and concurrent calls on one provider must each obtain a token.
			generate()
			generate()
			var wg sync.WaitGroup
			for range 4 {
				wg.Go(generate)
			}
			wg.Wait()
			require.EqualValues(t, 6, authCalls.Load())
			pages.Range(func(_, count any) bool {
				assert.EqualValues(t, 2, count.(*atomic.Int32).Load())
				return true
			})
		})
	}
}

func TestAuthFailures(t *testing.T) {
	for _, status := range []int{400, 401, 403, 422, 503} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			calls := 0
			server := newTrustedTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				assert.Equal(t, "/auth/token", r.URL.Path)
				w.Header().Set("Retry-After", "0")
				w.WriteHeader(status)
				_, _ = w.Write([]byte(`{"error":{"code":"AUTH_FAILED","message":"request rejected","details":{"tenant":"unverified"}}}`))
			}))
			p, err := Loader(context.Background(), providers.Config{Params: map[string]any{"apiUrl": server.URL}, Creds: fakeCredentials()})
			require.Nil(t, err)
			graph, err := p.GenerateTopologyConfig(context.Background(), nil, []topology.ComputeInstances{{Instances: map[string]string{"n1": "worker"}}})
			require.Nil(t, graph)
			require.Equal(t, status, err.Code())
			require.Equal(t, `Firebird authentication request failed: AUTH_FAILED: request rejected (details: {"tenant":"unverified"})`, err.Error())
			expectedCalls := 1
			if status == http.StatusServiceUnavailable {
				expectedCalls = 5
			}
			require.Equal(t, expectedCalls, calls)
		})
	}
}

func TestInvalidTokenResponses(t *testing.T) {
	for _, body := range []string{
		`invalid`, `null`, `{}`, `{"accessToken":""}`, `{"accessToken":"fake","expiresIn":-1}`,
		`{"accessToken":"fake","expiresIn":"bad"}`,
		`{"accessToken":"fake","tokenType":"Basic"}`, `{"accessToken":"fake\r\nheader"}`,
	} {
		t.Run(body, func(t *testing.T) {
			server := newTrustedTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(body)) }))
			p, err := Loader(context.Background(), providers.Config{Params: map[string]any{"apiUrl": server.URL}, Creds: fakeCredentials()})
			require.Nil(t, err)
			_, err = p.(*Provider).accessToken(context.Background())
			require.Equal(t, http.StatusBadGateway, err.Code())
			require.Equal(t, "invalid Firebird authentication response", err.Error())
		})
	}
}

func TestTopologyUnauthorizedDoesNotReauthenticate(t *testing.T) {
	authCalls, topologyCalls := 0, 0
	server := newTrustedTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/auth/token" {
			authCalls++
			_, _ = w.Write([]byte(`{"accessToken":"fake-token","expiresIn":3600}`))
			return
		}
		assert.Equal(t, "/topology/nodes", r.URL.Path)
		assert.Equal(t, "Bearer fake-token", r.Header.Get("Authorization"))
		topologyCalls++
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"code":"TOKEN_INVALID","message":"invalid token"}}`))
	}))
	p, err := Loader(context.Background(), providers.Config{Params: map[string]any{"apiUrl": server.URL}, Creds: fakeCredentials()})
	require.Nil(t, err)
	graph, err := p.GenerateTopologyConfig(context.Background(), nil, []topology.ComputeInstances{{Instances: map[string]string{"n1": "worker"}}})
	require.Nil(t, graph)
	require.NotNil(t, err)
	require.Equal(t, http.StatusUnauthorized, err.Code())
	require.Equal(t, 1, authCalls)
	require.Equal(t, 1, topologyCalls)
}

func TestAuthenticationCanCancel(t *testing.T) {
	var calls atomic.Int32
	server := newTrustedTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	p, err := Loader(context.Background(), providers.Config{Params: map[string]any{"apiUrl": server.URL}, Creds: fakeCredentials()})
	require.Nil(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = p.(*Provider).accessToken(ctx)
	require.NotNil(t, err)
	require.Equal(t, http.StatusBadGateway, err.Code())
	require.ErrorContains(t, err, "canceled")
	require.Zero(t, calls.Load())
}
