/*
 * Copyright 2026 NVIDIA CORPORATION
 * SPDX-License-Identifier: Apache-2.0
 */

package firebird

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/topograph/internal/httpreq"
	"github.com/dsx-ai-factory/topograph/pkg/providers"
	"github.com/dsx-ai-factory/topograph/pkg/topology"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The shared request helper uses http.DefaultTransport. Trust only this
// server's certificate for each sequential test, then restore the transport.
func newTLSTestServer(t *testing.T, handler http.Handler) *httptest.Server {
	t.Helper()
	return newTrustedTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.NotNil(t, r.TLS)
		if strings.HasSuffix(r.URL.Path, "/auth/token") {
			assert.Equal(t, http.MethodPost, r.Method)
			assert.Equal(t, "application/json", r.Header.Get("Content-Type"))
			assert.Equal(t, "application/json", r.Header.Get("Accept"))
			assert.Empty(t, r.Header.Get("Authorization"))
			var creds map[string]string
			if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&creds)) {
				http.Error(w, "invalid test authentication request", http.StatusBadRequest)
				return
			}
			assert.Equal(t, map[string]string{"grantType": "client_credentials", "clientId": "fake-client-id", "clientSecret": "fake-client-secret"}, creds)
			_, _ = w.Write([]byte(`{"accessToken":"fake-test-token","expiresIn":3600,"tokenType":"Bearer"}`))
			return
		}
		handler.ServeHTTP(w, r)
	}))
}

func newTrustedTLSServer(t *testing.T, handler http.Handler) *httptest.Server {
	t.Helper()
	server := httptest.NewTLSServer(handler)
	transport := http.DefaultTransport
	http.DefaultTransport = server.Client().Transport
	t.Cleanup(func() {
		http.DefaultTransport = transport
		server.Close()
	})
	return server
}

func TestLoader(t *testing.T) {
	name, loader := NamedLoader()
	require.Equal(t, "firebird", name)
	for _, params := range []map[string]any{
		nil,
		{"apiUrl": "relative"},
		{"apiUrl": "ftp://example.com"},
		{"apiUrl": "http://example.com"},
		{"apiUrl": "https://user:password@example.com"},
		{"apiUrl": "https://example.com?token=fake"},
		{"apiUrl": "https://example.com", "trimTiers": 3},
		{"apiUrl": "https://example.com", "trimTiers": "invalid"},
		{"apiUrl": "https://example.com", "trimTiers": []int{1}},
	} {
		_, err := loader(context.Background(), providers.Config{Creds: fakeCredentials(), Params: params})
		require.NotNil(t, err, "%v", params)
		require.Equal(t, http.StatusBadRequest, err.Code())
	}
	p, err := loader(context.Background(), providers.Config{Creds: fakeCredentials(), Params: map[string]any{"apiUrl": "https://example.com"}})
	require.Nil(t, err)
	require.NotNil(t, p)
}

func TestGenerateTopologyConfigPagination(t *testing.T) {
	calls := 0
	server := newTLSTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, "/api/topology/nodes", r.URL.Path)
		assert.Equal(t, "application/json", r.Header.Get("Accept"))
		assert.NotContains(t, r.URL.Query(), "page_size")
		assert.NotContains(t, r.URL.Query(), "page_token")
		assert.Equal(t, "Bearer fake-test-token", r.Header.Get("Authorization"))
		assert.Equal(t, "2", r.URL.Query().Get("pageSize"))
		switch calls {
		case 1:
			assert.Empty(t, r.URL.Query().Get("pageToken"))
			_, _ = w.Write([]byte(`{"nextPageToken":"opaque +/&=", "nodes":[{"nodeId":"n1", "networkPath":["leaf1","spine","core"], "nodeKind":"compute", "observedAt":"2026-10-14T21:50:39.380Z", "tiers":{"unordered":{"id":"leaf1","name":"Leaf"}},"topologyStatus":"ready"},{"nodeId":"other","networkPath":["other-core"]}]}`))
		case 2:
			assert.Equal(t, "opaque +/&=", r.URL.Query().Get("pageToken"))
			// Empty pages with a continuation token must not end discovery.
			_, _ = w.Write([]byte(`{"nextPageToken":"last", "nodes":[]}`))
		case 3:
			assert.Equal(t, "last", r.URL.Query().Get("pageToken"))
			_, _ = w.Write([]byte(`{"nodes":[{"nodeId":"n2","networkPath":["leaf2","spine","core"]},{"nodeId":"no-path"}]}`))
		default:
			t.Error("unexpected request")
		}
	}))
	p, err := Loader(context.Background(), providers.Config{Creds: fakeCredentials(), Params: map[string]any{"apiUrl": server.URL + "/api"}})
	require.Nil(t, err)
	size := 2
	graph, err := p.GenerateTopologyConfig(context.Background(), &size, []topology.ComputeInstances{{Instances: map[string]string{"n1": "host1", "n2": "host2", "missing": "host3", "no-path": "host4"}}})
	require.Nil(t, err)
	require.Equal(t, 3, calls)
	require.Nil(t, graph.Domains)
	core := graph.Tiers.Vertices["core"]
	require.NotNil(t, core)
	require.Equal(t, "host1", core.Vertices["spine"].Vertices["leaf1"].Vertices["n1"].Name)
	require.Equal(t, "host2", core.Vertices["spine"].Vertices["leaf2"].Vertices["n2"].Name)
	require.Len(t, graph.Instances, 2)
	require.Equal(t, []string{"leaf1", "spine", "core"}, graph.Instances["n1"].NetworkLayers)
	require.Len(t, graph.Tiers.Vertices[topology.NoTopology].Vertices, 2)
}

func TestLeafFirstPathAndTrim(t *testing.T) {
	server := newTLSTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "Bearer fake-test-token", r.Header.Get("Authorization"))
		assert.Equal(t, "100", r.URL.Query().Get("pageSize"))
		_, _ = w.Write([]byte(`{"nodes":[{"nodeId":"n1","networkPath":["leaf","spine","core"]}]}`))
	}))
	p, err := Loader(context.Background(), providers.Config{Creds: fakeCredentials(), Params: map[string]any{"apiUrl": server.URL, "trimTiers": 1}})
	require.Nil(t, err)
	size := 0
	graph, err := p.GenerateTopologyConfig(context.Background(), &size, []topology.ComputeInstances{{Instances: map[string]string{"n1": "host"}}})
	require.Nil(t, err)
	require.Equal(t, []string{"leaf", "spine"}, graph.Instances["n1"].NetworkLayers)
	require.Equal(t, "host", graph.Tiers.Vertices["spine"].Vertices["leaf"].Vertices["n1"].Name)
}

func TestUpstreamErrors(t *testing.T) {
	for _, body := range []string{
		`invalid`, `null`, `{}`, `{"nodes":null}`, `{"nodes":"bad"}`,
		`{"nodes":[{"nodeId":"n1","networkPath":[""]}]}`,
		`{"nodes":[{"nodeId":"n1","networkPath":["leaf"]},{"nodeId":"n1","networkPath":["leaf"]}]}`,
		`{"nodes":[],"nextPageToken":"repeat"}`,
	} {
		t.Run(body, func(t *testing.T) {
			server := newTLSTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(body)) }))
			p, err := Loader(context.Background(), providers.Config{Creds: fakeCredentials(), Params: map[string]any{"apiUrl": server.URL}})
			require.Nil(t, err)
			graph, err := p.GenerateTopologyConfig(context.Background(), nil, []topology.ComputeInstances{{Instances: map[string]string{"n1": "host"}}})
			require.Nil(t, graph)
			require.NotNil(t, err)
			require.Equal(t, http.StatusBadGateway, err.Code())
		})
	}
}

func TestRequestFailuresAndEmptyInput(t *testing.T) {
	calls := 0
	server := newTLSTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte("private upstream detail"))
	}))
	p, err := Loader(context.Background(), providers.Config{Creds: fakeCredentials(), Params: map[string]any{"apiUrl": server.URL}})
	require.Nil(t, err)
	graph, err := p.GenerateTopologyConfig(context.Background(), nil, nil)
	require.Nil(t, err)
	require.Empty(t, graph.Tiers.Vertices)
	require.Zero(t, calls)
	_, err = p.GenerateTopologyConfig(context.Background(), nil, []topology.ComputeInstances{{Instances: map[string]string{"": "host"}}})
	require.Equal(t, http.StatusBadRequest, err.Code())
	require.Zero(t, calls)
	requested := []topology.ComputeInstances{{Instances: map[string]string{"n1": "host"}}}
	_, err = p.GenerateTopologyConfig(context.Background(), nil, requested)
	require.Equal(t, http.StatusUnauthorized, err.Code())
	require.NotContains(t, err.Error(), "private upstream detail")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = p.GenerateTopologyConfig(ctx, nil, requested)
	require.Equal(t, http.StatusBadGateway, err.Code())
	require.Equal(t, 1, calls)
}

func TestFirebirdIgnoresTiers(t *testing.T) {
	const nodeID = "bm.01M48NJ1NSSMZ9RR72K5K3EERG"
	for _, trim := range []int{0, 1} {
		t.Run(string(rune('0'+trim)), func(t *testing.T) {
			server := newTLSTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(`{"nodes":[{
     "nodeId":"bm.01M48NJ1NSSMZ9RR72K5K3EERG",
     "nodeKind":"bm","topologyStatus":"complete",
     "networkPath":["sw.leaf-pod3-su1","sw.spine-pod3","sw.fabric"],
     "tiers":{
      "core":{"id":"sw.fabric","name":"fabric"},
      "leaf":{"id":"sw.leaf-pod3-su1","name":"pod3/su1"},
      "spine":{"id":"sw.spine-pod3","name":"pod3"},
      "unrelated":{"id":"sw.other","name":"ignored"},
      "conflicting":{"id":"sw.leaf-pod3-su1","name":"different"}
     },
     "observedAt":"2026-10-14T22:06:32Z"
    }]}`))
			}))
			p, err := Loader(context.Background(), providers.Config{Creds: fakeCredentials(), Params: map[string]any{"apiUrl": server.URL, "trimTiers": trim}})
			require.Nil(t, err)
			graph, err := p.GenerateTopologyConfig(context.Background(), nil, []topology.ComputeInstances{{Instances: map[string]string{nodeID: "worker"}}})
			require.Nil(t, err)
			names := []string{"sw.leaf-pod3-su1", "sw.spine-pod3", "sw.fabric"}
			require.Equal(t, names[:len(names)-trim], graph.Instances[nodeID].NetworkLayers)
			spine := graph.Tiers.Vertices["sw.spine-pod3"]
			if trim == 0 {
				core := graph.Tiers.Vertices["sw.fabric"]
				require.NotNil(t, core)
				require.Empty(t, core.Name)
				spine = core.Vertices["sw.spine-pod3"]
			}
			require.NotNil(t, spine)
			require.Empty(t, spine.Name)
			leaf := spine.Vertices["sw.leaf-pod3-su1"]
			require.Empty(t, leaf.Name)
			require.Equal(t, "worker", leaf.Vertices[nodeID].Name)
			require.Nil(t, graph.Domains)
		})
	}
}

func TestFirebirdAPIErrors(t *testing.T) {
	for _, status := range []int{400, 401, 403, 404, 409, 422, 429, 500, 502, 503, 504} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			calls := 0
			server := newTLSTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls++
				w.Header().Set("Retry-After", "0")
				w.WriteHeader(status)
				_, _ = w.Write([]byte(`{"error":{"code":"INVALID_REQUEST","message":"request rejected","details":{"pageToken":"invalid","pageSize":"too large"}}}`))
			}))
			p, err := Loader(context.Background(), providers.Config{Creds: fakeCredentials(), Params: map[string]any{"apiUrl": server.URL}})
			require.Nil(t, err)
			graph, err := p.GenerateTopologyConfig(context.Background(), nil, []topology.ComputeInstances{{Instances: map[string]string{"n1": "worker"}}})
			require.Nil(t, graph)
			require.NotNil(t, err)
			require.Equal(t, status, err.Code())
			require.Equal(t, `Firebird topology request failed: INVALID_REQUEST: request rejected (details: {"pageSize":"too large","pageToken":"invalid"})`, err.Error())
			expectedCalls := 1
			if httpreq.ShouldRetry(status) {
				expectedCalls = 5
			}
			require.Equal(t, expectedCalls, calls)
		})
	}
}

func TestFirebirdRateLimitRecovery(t *testing.T) {
	calls := 0
	server := newTLSTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		if calls == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"code":"RATE_LIMITED","message":"try again later"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"nodes":[{"nodeId":"n1","networkPath":["leaf"]}]}`))
	}))
	p, err := Loader(context.Background(), providers.Config{Creds: fakeCredentials(), Params: map[string]any{"apiUrl": server.URL}})
	require.Nil(t, err)
	graph, err := p.GenerateTopologyConfig(context.Background(), nil, []topology.ComputeInstances{{Instances: map[string]string{"n1": "worker"}}})
	require.Nil(t, err)
	require.Len(t, graph.Instances, 1)
	require.Equal(t, 2, calls)
}

func TestFirebirdErrorDiagnostics(t *testing.T) {
	for _, body := range []string{"not JSON", `null`, `{}`, `{"error":null}`, `{"error":{"details":{"pageSize":123}}}`} {
		err := apiError(http.StatusBadRequest, []byte(body))
		require.Equal(t, http.StatusBadRequest, err.Code())
		require.Equal(t, "Firebird topology request failed", err.Error())
	}
	err := apiError(http.StatusServiceUnavailable, []byte(`{"error":{"code":"UNAVAILABLE","message":"try later"}}`))
	require.Equal(t, http.StatusServiceUnavailable, err.Code())
	require.Equal(t, "Firebird topology request failed: UNAVAILABLE: try later", err.Error())
}

func fakeCredentials() map[string]any {
	return map[string]any{"clientId": "fake-client-id", "clientSecret": "fake-client-secret"}
}
