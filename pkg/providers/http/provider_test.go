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

package http

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	nethttp "net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/dsx-ai-factory/topograph/internal/httperr"
	"github.com/dsx-ai-factory/topograph/pkg/providers"
	"github.com/dsx-ai-factory/topograph/pkg/topology"
)

// Placeholder credentials that cannot authenticate against anything real.
const (
	fakeClientID     = "fake-client-id"
	fakeClientSecret = "fake-client-secret"
	fakeAPIKey       = "fake-api-key"
)

type fixture struct {
	t      *testing.T
	server *httptest.Server
	mux    *nethttp.ServeMux
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	mux := nethttp.NewServeMux()
	server := httptest.NewTLSServer(mux)
	t.Cleanup(server.Close)
	return &fixture{t: t, server: server, mux: mux}
}

func (f *fixture) url(path string) string { return f.server.URL + path }

func (f *fixture) client() *nethttp.Client {
	c := f.server.Client()
	c.CheckRedirect = func(*nethttp.Request, []*nethttp.Request) error { return nethttp.ErrUseLastResponse }
	return c
}

func (f *fixture) provider(params map[string]any, creds map[string]any) *Provider {
	f.t.Helper()
	p, err := newProvider(providers.Config{Params: params, Creds: creds}, f.client())
	require.Nil(f.t, err)
	p.backoff = func(*nethttp.Response, int) time.Duration { return time.Millisecond }
	return p
}

func writeJSON(w nethttp.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func item(id string, path []string, domain string) map[string]any {
	m := map[string]any{"instance_id": id}
	if path != nil {
		m["network_path"] = path
	}
	if domain != "" {
		m["accelerator_domain"] = domain
	}
	return m
}

func instances(region string, pairs ...string) []topology.ComputeInstances {
	m := map[string]string{}
	for i := 0; i < len(pairs); i += 2 {
		m[pairs[i]] = pairs[i+1]
	}
	return []topology.ComputeInstances{{Region: region, Instances: m}}
}

// leafNodes returns node names under the given closest-first switch path.
func leafNodes(t *testing.T, g *topology.Graph, rootFirst ...string) []string {
	t.Helper()
	v := g.Tiers
	for _, id := range rootFirst {
		next, ok := v.Vertices[id]
		require.True(t, ok, "switch %q not found", id)
		v = next
	}
	var names []string
	for _, child := range v.Vertices {
		names = append(names, child.Name)
	}
	return names
}

func TestDefaultParserNoPagination(t *testing.T) {
	f := newFixture(t)
	f.mux.HandleFunc("/v1/nodes", func(w nethttp.ResponseWriter, r *nethttp.Request) {
		require.Equal(t, "active", r.URL.Query().Get("status"))
		require.Equal(t, "application/json", r.Header.Get("Accept"))
		require.Empty(t, r.Header.Get("Authorization"))
		writeJSON(w, map[string]any{"items": []any{
			item("i-1", []string{"leaf-1", "spine-1"}, "nvl-1"),
			item("i-2", []string{"leaf-1", "spine-1"}, "nvl-1"),
			item("i-3", []string{"leaf-2", "spine-1"}, ""),
			item("i-4", nil, "nvl-2"),
			item("unrelated", []string{"leaf-9"}, ""),
		}})
	})
	p := f.provider(map[string]any{"request": map[string]any{
		"url": f.url("/v1/nodes"), "query": map[string]any{"status": "active"},
	}}, nil)

	g, err := p.GenerateTopologyConfig(context.Background(), nil,
		instances("r1", "i-1", "n1", "i-2", "n2", "i-3", "n3", "i-4", "n4"))
	require.Nil(t, err)
	require.ElementsMatch(t, []string{"n1", "n2"}, leafNodes(t, g, "spine-1", "leaf-1"))
	require.ElementsMatch(t, []string{"n3"}, leafNodes(t, g, "spine-1", "leaf-2"))
	require.Len(t, g.Domains["nvl-1"], 2)
	require.Len(t, g.Domains["nvl-2"], 1)
	require.NotContains(t, g.Tiers.Vertices, "leaf-9")
}

func TestEmptyRequestDoesNoIO(t *testing.T) {
	f := newFixture(t)
	var calls atomic.Int32
	f.mux.HandleFunc("/", func(nethttp.ResponseWriter, *nethttp.Request) { calls.Add(1) })
	p := f.provider(map[string]any{"request": map[string]any{"url": f.url("/")}}, nil)
	g, err := p.GenerateTopologyConfig(context.Background(), nil, nil)
	require.Nil(t, err)
	require.NotNil(t, g)
	require.Zero(t, calls.Load())
}

func TestCursorPagination(t *testing.T) {
	f := newFixture(t)
	f.mux.HandleFunc("/nodes", func(w nethttp.ResponseWriter, r *nethttp.Request) {
		require.Equal(t, "2", r.URL.Query().Get("limit"))
		switch r.URL.Query().Get("cursor") {
		case "":
			writeJSON(w, map[string]any{"items": []any{item("i-1", []string{"leaf-1"}, "")}, "next_cursor": "c1"})
		case "c1":
			writeJSON(w, map[string]any{"items": []any{}, "next_cursor": "c2"})
		case "c2":
			writeJSON(w, map[string]any{"items": []any{item("i-2", []string{"leaf-2"}, "")}, "next_cursor": nil})
		default:
			t.Fatalf("unexpected cursor")
		}
	})
	p := f.provider(map[string]any{
		"request":    map[string]any{"url": f.url("/nodes")},
		"pagination": map[string]any{"mode": "cursor", "sizeParam": "limit"},
	}, nil)
	size := 2
	g, err := p.GenerateTopologyConfig(context.Background(), &size, instances("", "i-1", "n1", "i-2", "n2"))
	require.Nil(t, err)
	require.Equal(t, []string{"n1"}, leafNodes(t, g, "leaf-1"))
	require.Equal(t, []string{"n2"}, leafNodes(t, g, "leaf-2"))
}

func TestCursorRepeatedFails(t *testing.T) {
	f := newFixture(t)
	f.mux.HandleFunc("/nodes", func(w nethttp.ResponseWriter, r *nethttp.Request) {
		writeJSON(w, map[string]any{"items": []any{}, "next_cursor": "same"})
	})
	p := f.provider(map[string]any{
		"request":    map[string]any{"url": f.url("/nodes")},
		"pagination": map[string]any{"mode": "cursor"},
	}, nil)
	_, err := p.GenerateTopologyConfig(context.Background(), nil, instances("", "i-1", "n1"))
	require.NotNil(t, err)
	require.Equal(t, nethttp.StatusBadGateway, err.Code())
}

// mappedServer serves a custom response layout with page or offset pagination.
func mappedServer(f *fixture, all []map[string]any, param string) {
	f.mux.HandleFunc("/api", func(w nethttp.ResponseWriter, r *nethttp.Request) {
		size, _ := strconv.Atoi(r.URL.Query().Get("per_page"))
		pos, _ := strconv.Atoi(r.URL.Query().Get(param))
		start := pos
		if param == "pg" {
			start = pos * size
		}
		end := min(start+size, len(all))
		if start > len(all) {
			start = end
		}
		writeJSON(w, map[string]any{"data": map[string]any{"machines": all[start:end]}})
	})
}

func mappedNodes(n int) []map[string]any {
	var all []map[string]any
	for i := range n {
		all = append(all, map[string]any{
			"hw":  map[string]any{"id": fmt.Sprintf("i-%d", i), "zone": "r1"},
			"net": map[string]any{"uplinks": []any{fmt.Sprintf("leaf-%d", i/2), "spine-0"}},
			"gpu": map[string]any{"nvl": "nvl-0"},
		})
	}
	return all
}

func mappingParsing() map[string]any {
	return map[string]any{
		"type":         "mapping",
		"itemsPointer": "/data/machines",
		"fields": map[string]any{
			"identity":          "/hw/id",
			"region":            "/hw/zone",
			"networkPath":       "/net/uplinks",
			"acceleratorDomain": "/gpu/nvl",
		},
	}
}

func TestPageAndOffsetPaginationWithMapping(t *testing.T) {
	for _, tc := range []struct {
		name       string
		pagination map[string]any
		param      string
	}{
		{"page", map[string]any{"mode": "page", "pageParam": "pg", "initialPage": 0, "sizeParam": "per_page"}, "pg"},
		{"offset", map[string]any{"mode": "offset", "offsetParam": "skip", "sizeParam": "per_page"}, "skip"},
		{"page-short", map[string]any{"mode": "page", "pageParam": "pg", "initialPage": 0, "sizeParam": "per_page", "completion": "short"}, "pg"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			mappedServer(f, mappedNodes(5), tc.param)
			p := f.provider(map[string]any{
				"request":    map[string]any{"url": f.url("/api")},
				"pagination": tc.pagination,
				"parsing":    mappingParsing(),
			}, nil)
			size := 2
			g, err := p.GenerateTopologyConfig(context.Background(), &size,
				instances("r1", "i-0", "n0", "i-1", "n1", "i-4", "n4"))
			require.Nil(t, err)
			require.ElementsMatch(t, []string{"n0", "n1"}, leafNodes(t, g, "spine-0", "leaf-0"))
			require.ElementsMatch(t, []string{"n4"}, leafNodes(t, g, "spine-0", "leaf-2"))
			require.Len(t, g.Domains["nvl-0"], 3)
		})
	}
}

func TestMaxPages(t *testing.T) {
	f := newFixture(t)
	f.mux.HandleFunc("/nodes", func(w nethttp.ResponseWriter, r *nethttp.Request) {
		writeJSON(w, map[string]any{"items": []any{item("x"+r.URL.Query().Get("page"), []string{"l"}, "")}})
	})
	p := f.provider(map[string]any{
		"request":    map[string]any{"url": f.url("/nodes")},
		"pagination": map[string]any{"mode": "page", "maxPages": 3},
	}, nil)
	_, err := p.GenerateTopologyConfig(context.Background(), nil, instances("", "i-1", "n1"))
	require.NotNil(t, err)
	require.Equal(t, nethttp.StatusBadGateway, err.Code())
	require.Contains(t, err.Error(), "maxPages")
}

func TestStaticAuth(t *testing.T) {
	for _, tc := range []struct {
		name   string
		auth   map[string]any
		creds  map[string]any
		header string
		want   string
	}{
		{"basic", map[string]any{"type": "basic"}, map[string]any{"username": "u", "password": "p"},
			"Authorization", "Basic dTpw"},
		{"apiKey", map[string]any{"type": "apiKey", "headerName": "X-API-Key"}, map[string]any{"apiKey": fakeAPIKey},
			"X-Api-Key", fakeAPIKey},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			f.mux.HandleFunc("/nodes", func(w nethttp.ResponseWriter, r *nethttp.Request) {
				require.Equal(t, tc.want, r.Header.Get(tc.header))
				writeJSON(w, map[string]any{"items": []any{item("i-1", []string{"leaf"}, "")}})
			})
			p := f.provider(map[string]any{"request": map[string]any{"url": f.url("/nodes")}, "auth": tc.auth}, tc.creds)
			_, err := p.GenerateTopologyConfig(context.Background(), nil, instances("", "i-1", "n1"))
			require.Nil(t, err)
		})
	}
}

func TestOAuth2TokenReusedAcrossPagesAndRetries(t *testing.T) {
	f := newFixture(t)
	var tokens, pages atomic.Int32
	f.mux.HandleFunc("/token", func(w nethttp.ResponseWriter, r *nethttp.Request) {
		user, pass, ok := r.BasicAuth()
		require.True(t, ok)
		require.Equal(t, fakeClientID, user)
		require.Equal(t, fakeClientSecret, pass)
		n := tokens.Add(1)
		writeJSON(w, map[string]any{"access_token": fmt.Sprintf("tok-%d", n), "token_type": "Bearer", "expires_in": 3600})
	})
	f.mux.HandleFunc("/nodes", func(w nethttp.ResponseWriter, r *nethttp.Request) {
		require.Equal(t, "Bearer tok-1", r.Header.Get("Authorization"))
		n := pages.Add(1)
		if n == 1 {
			w.WriteHeader(nethttp.StatusServiceUnavailable)
			return
		}
		if r.URL.Query().Get("page") == "1" {
			writeJSON(w, map[string]any{"items": []any{item("i-1", []string{"leaf"}, "")}})
			return
		}
		writeJSON(w, map[string]any{"items": []any{}})
	})
	p := f.provider(map[string]any{
		"request":    map[string]any{"url": f.url("/nodes")},
		"pagination": map[string]any{"mode": "page"},
		"auth": map[string]any{"type": "oauth2", "oauth2": map[string]any{
			"tokenUrl": f.url("/token"), "scopes": []any{"topology.read"},
		}},
	}, map[string]any{"clientId": fakeClientID, "clientSecret": fakeClientSecret})
	_, err := p.GenerateTopologyConfig(context.Background(), nil, instances("", "i-1", "n1"))
	require.Nil(t, err)
	require.EqualValues(t, 1, tokens.Load())
	require.EqualValues(t, 3, pages.Load())
}

func TestBearerLoginOncePerInvocation(t *testing.T) {
	f := newFixture(t)
	var logins atomic.Int32
	f.mux.HandleFunc("/login", func(w nethttp.ResponseWriter, r *nethttp.Request) {
		require.Equal(t, nethttp.MethodPost, r.Method)
		var body map[string]string
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		require.Equal(t, map[string]string{"user": "svc", "secret": "fake"}, body)
		n := logins.Add(1)
		writeJSON(w, map[string]any{"data": map[string]any{"jwt": fmt.Sprintf("jwt-%d", n)}})
	})
	var expect atomic.Value
	f.mux.HandleFunc("/nodes", func(w nethttp.ResponseWriter, r *nethttp.Request) {
		require.Equal(t, "Bearer "+expect.Load().(string), r.Header.Get("Authorization"))
		if r.URL.Query().Get("cursor") == "" {
			writeJSON(w, map[string]any{"items": []any{item("i-1", []string{"leaf"}, "")}, "next_cursor": "c"})
			return
		}
		writeJSON(w, map[string]any{"items": []any{}})
	})
	p := f.provider(map[string]any{
		"request":    map[string]any{"url": f.url("/nodes")},
		"pagination": map[string]any{"mode": "cursor"},
		"auth": map[string]any{"type": "bearer", "bearer": map[string]any{
			"url": f.url("/login"), "tokenPointer": "/data/jwt",
		}},
	}, map[string]any{"user": "svc", "secret": "fake"})

	expect.Store("jwt-1")
	_, err := p.GenerateTopologyConfig(context.Background(), nil, instances("", "i-1", "n1"))
	require.Nil(t, err)
	expect.Store("jwt-2")
	_, err = p.GenerateTopologyConfig(context.Background(), nil, instances("", "i-1", "n1"))
	require.Nil(t, err)
	require.EqualValues(t, 2, logins.Load())
}

func TestAuthFailurePreservesStatus(t *testing.T) {
	f := newFixture(t)
	var nodes atomic.Int32
	f.mux.HandleFunc("/token", func(w nethttp.ResponseWriter, _ *nethttp.Request) {
		w.WriteHeader(nethttp.StatusUnauthorized)
	})
	f.mux.HandleFunc("/nodes", func(nethttp.ResponseWriter, *nethttp.Request) { nodes.Add(1) })
	p := f.provider(map[string]any{
		"request": map[string]any{"url": f.url("/nodes")},
		"auth":    map[string]any{"type": "oauth2", "oauth2": map[string]any{"tokenUrl": f.url("/token")}},
	}, map[string]any{"clientId": fakeClientID, "clientSecret": fakeClientSecret})
	_, err := p.GenerateTopologyConfig(context.Background(), nil, instances("", "i-1", "n1"))
	require.NotNil(t, err)
	require.Equal(t, nethttp.StatusUnauthorized, err.Code())
	require.NotContains(t, err.Error(), fakeClientSecret)
	require.Zero(t, nodes.Load())
}

func TestUpstreamFailures(t *testing.T) {
	for _, tc := range []struct {
		name     string
		handler  func(calls int32, w nethttp.ResponseWriter)
		wantCode int
		wantCall int32
	}{
		{"retry then succeed", func(calls int32, w nethttp.ResponseWriter) {
			if calls < 3 {
				w.Header().Set("Retry-After", "0")
				w.WriteHeader(nethttp.StatusTooManyRequests)
				return
			}
			writeJSON(w, map[string]any{"items": []any{item("i-1", []string{"leaf"}, "")}})
		}, 0, 3},
		{"no retry on 404", func(_ int32, w nethttp.ResponseWriter) { w.WriteHeader(nethttp.StatusNotFound) },
			nethttp.StatusBadGateway, 1},
		{"no retry on 500", func(_ int32, w nethttp.ResponseWriter) { w.WriteHeader(nethttp.StatusInternalServerError) },
			nethttp.StatusBadGateway, 1},
		{"retries exhausted", func(_ int32, w nethttp.ResponseWriter) { w.WriteHeader(nethttp.StatusBadGateway) },
			nethttp.StatusBadGateway, maxAttempts},
		{"redirect rejected", func(_ int32, w nethttp.ResponseWriter) {
			w.Header().Set("Location", "https://elsewhere.invalid/")
			w.WriteHeader(nethttp.StatusFound)
		}, nethttp.StatusBadGateway, 1},
		{"invalid JSON", func(_ int32, w nethttp.ResponseWriter) { _, _ = io.WriteString(w, "not json") },
			nethttp.StatusBadGateway, 1},
		{"items not array", func(_ int32, w nethttp.ResponseWriter) { writeJSON(w, map[string]any{"items": "x"}) },
			nethttp.StatusBadGateway, 1},
		{"too large", func(_ int32, w nethttp.ResponseWriter) {
			_, _ = io.WriteString(w, `{"items":[`+strings.Repeat(" ", 2048)+`]}`)
		}, nethttp.StatusBadGateway, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			var calls atomic.Int32
			f.mux.HandleFunc("/nodes", func(w nethttp.ResponseWriter, _ *nethttp.Request) {
				tc.handler(calls.Add(1), w)
			})
			p := f.provider(map[string]any{"request": map[string]any{"url": f.url("/nodes")}, "maxResponseBytes": 1024}, nil)
			_, err := p.GenerateTopologyConfig(context.Background(), nil, instances("", "i-1", "n1"))
			if tc.wantCode == 0 {
				require.Nil(t, err)
			} else {
				require.NotNil(t, err)
				require.Equal(t, tc.wantCode, err.Code())
				require.NotContains(t, err.Error(), f.server.URL)
			}
			require.Equal(t, tc.wantCall, calls.Load())
		})
	}
}

func TestCancellationAndTimeout(t *testing.T) {
	f := newFixture(t)
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	f.mux.HandleFunc("/nodes", func(_ nethttp.ResponseWriter, r *nethttp.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	})

	p := f.provider(map[string]any{"request": map[string]any{"url": f.url("/nodes")}, "timeout": "50ms"}, nil)
	_, err := p.GenerateTopologyConfig(context.Background(), nil, instances("", "i-1", "n1"))
	require.NotNil(t, err)
	require.Equal(t, nethttp.StatusGatewayTimeout, err.Code())

	p = f.provider(map[string]any{"request": map[string]any{"url": f.url("/nodes")}}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(50*time.Millisecond, cancel)
	_, err = p.GenerateTopologyConfig(ctx, nil, instances("", "i-1", "n1"))
	require.NotNil(t, err)
	require.Equal(t, nethttp.StatusRequestTimeout, err.Code())
}

func TestMissingNodes(t *testing.T) {
	f := newFixture(t)
	f.mux.HandleFunc("/nodes", func(w nethttp.ResponseWriter, _ *nethttp.Request) {
		writeJSON(w, map[string]any{"items": []any{item("i-1", []string{"leaf"}, "")}})
	})
	cis := instances("", "i-1", "n1", "i-2", "n2")

	p := f.provider(map[string]any{"request": map[string]any{"url": f.url("/nodes")}}, nil)
	_, err := p.GenerateTopologyConfig(context.Background(), nil, cis)
	require.NotNil(t, err)
	require.Equal(t, nethttp.StatusBadGateway, err.Code())
	require.Contains(t, err.Error(), "n2")

	p = f.provider(map[string]any{"request": map[string]any{"url": f.url("/nodes")}, "missingNodes": "warn"}, nil)
	g, err := p.GenerateTopologyConfig(context.Background(), nil, cis)
	require.Nil(t, err)
	require.Equal(t, []string{"n2"}, leafNodes(t, g, topology.NoTopology))
}

func TestMatchingAndTopologyValidation(t *testing.T) {
	for _, tc := range []struct {
		name  string
		items []any
		cis   []topology.ComputeInstances
		want  string // empty means success
	}{
		{"duplicate identical", []any{item("i-1", []string{"l"}, ""), item("i-1", []string{"l"}, "")},
			instances("", "i-1", "n1"), "more than once"},
		{"duplicate unrelated ok", []any{item("x", []string{"l"}, ""), item("x", []string{"l"}, ""), item("i-1", []string{"l"}, "")},
			instances("", "i-1", "n1"), ""},
		{"invalid unrelated field ok", []any{map[string]any{"instance_id": "x", "network_path": "bad"}, item("i-1", []string{"l"}, "")},
			instances("", "i-1", "n1"), ""},
		{"invalid matched field", []any{map[string]any{"instance_id": "i-1", "network_path": []any{"l", 3}}},
			instances("", "i-1", "n1"), "element 1"},
		{"invalid identity anywhere", []any{map[string]any{"instance_id": 7}, item("i-1", []string{"l"}, "")},
			instances("", "i-1", "n1"), "identity"},
		{"no topology", []any{item("i-1", nil, "")}, instances("", "i-1", "n1"), "neither"},
		{"region mismatch", []any{map[string]any{"instance_id": "i-1", "region": "r2", "network_path": []any{"l"}}},
			instances("r1", "i-1", "n1"), "does not match"},
		{"region match", []any{map[string]any{"instance_id": "i-1", "region": "r1", "network_path": []any{"l"}}},
			instances("r1", "i-1", "n1"), ""},
		{"conflicting parents", []any{item("i-1", []string{"leaf", "spine-a"}, ""), item("i-2", []string{"leaf", "spine-b"}, "")},
			instances("", "i-1", "n1", "i-2", "n2"), "conflicting paths"},
		{"root vs non-root", []any{item("i-1", []string{"leaf"}, ""), item("i-2", []string{"leaf", "spine"}, "")},
			instances("", "i-1", "n1", "i-2", "n2"), "conflicting paths"},
		{"different lengths consistent", []any{item("i-1", []string{"leaf", "spine", "core"}, ""), item("i-2", []string{"spine", "core"}, "")},
			instances("", "i-1", "n1", "i-2", "n2"), ""},
		{"repeated switch", []any{item("i-1", []string{"a", "b", "a"}, "")}, instances("", "i-1", "n1"), "more than once"},
		{"switch is instance", []any{item("i-1", []string{"i-2"}, ""), item("i-2", []string{"x"}, "")},
			instances("", "i-1", "n1", "i-2", "n2"), "requested instance ID"},
		{"reserved switch", []any{item("i-1", []string{topology.NoTopology}, "")}, instances("", "i-1", "n1"), "reserved"},
		{"switch across regions", []any{item("i-1", []string{"leaf"}, ""), item("i-2", []string{"leaf"}, "")},
			append(instances("r1", "i-1", "n1"), instances("r2", "i-2", "n2")...), "regions"},
		{"domain across regions", []any{item("i-1", nil, "d"), item("i-2", nil, "d")},
			append(instances("r1", "i-1", "n1"), instances("r2", "i-2", "n2")...), "regions"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			f.mux.HandleFunc("/nodes", func(w nethttp.ResponseWriter, _ *nethttp.Request) {
				writeJSON(w, map[string]any{"items": tc.items})
			})
			p := f.provider(map[string]any{"request": map[string]any{"url": f.url("/nodes")}}, nil)
			_, err := p.GenerateTopologyConfig(context.Background(), nil, tc.cis)
			if tc.want == "" {
				require.Nil(t, err)
				return
			}
			require.NotNil(t, err)
			require.Equal(t, nethttp.StatusBadGateway, err.Code())
			require.Contains(t, err.Error(), tc.want)
		})
	}
}

func TestRequestedInstanceValidation(t *testing.T) {
	f := newFixture(t)
	p := f.provider(map[string]any{"request": map[string]any{"url": f.url("/nodes")}}, nil)
	for name, cis := range map[string][]topology.ComputeInstances{
		"empty id":         instances("", "", "n1"),
		"reserved id":      instances("", topology.NoTopology, "n1"),
		"id across region": append(instances("r1", "i-1", "n1"), instances("r2", "i-1", "n2")...),
		"node twice":       instances("", "i-1", "n1", "i-2", "n1"),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := p.GenerateTopologyConfig(context.Background(), nil, cis)
			require.NotNil(t, err)
			require.Equal(t, nethttp.StatusBadRequest, err.Code())
		})
	}
}

func TestLoaderValidation(t *testing.T) {
	const u = "https://topology.example.invalid/v1/nodes"
	req := map[string]any{"url": u}
	oauth := map[string]any{"type": "oauth2", "oauth2": map[string]any{"tokenUrl": "https://id.example.invalid/token"}}
	oauthCreds := map[string]any{"clientId": fakeClientID, "clientSecret": fakeClientSecret}

	for _, tc := range []struct {
		name   string
		params map[string]any
		creds  map[string]any
		want   string
	}{
		{"valid minimal", map[string]any{"request": req}, nil, ""},
		{"valid oauth2", map[string]any{"request": req, "auth": oauth}, oauthCreds, ""},
		{"missing url", map[string]any{}, nil, "request.url"},
		{"http url", map[string]any{"request": map[string]any{"url": "http://x.invalid/"}}, nil, "HTTPS"},
		{"url with query", map[string]any{"request": map[string]any{"url": u + "?a=b"}}, nil, "query"},
		{"unknown field", map[string]any{"request": req, "bogus": 1}, nil, "bogus"},
		{"post", map[string]any{"request": map[string]any{"url": u, "method": "POST"}}, nil, "GET"},
		{"body", map[string]any{"request": map[string]any{"url": u, "body": "{}"}}, nil, "body"},
		{"static authorization", map[string]any{"request": map[string]any{"url": u, "headers": map[string]any{"authorization": "x"}}}, nil, "credentials"},
		{"static api key header", map[string]any{
			"request": map[string]any{"url": u, "headers": map[string]any{"x-api-key": "x"}},
			"auth":    map[string]any{"type": "apiKey", "headerName": "X-API-Key"},
		}, map[string]any{"apiKey": fakeAPIKey}, "conflicts"},
		{"query conflicts with pagination", map[string]any{
			"request":    map[string]any{"url": u, "query": map[string]any{"cursor": "x"}},
			"pagination": map[string]any{"mode": "cursor"},
		}, nil, "conflicts"},
		{"bad mode", map[string]any{"request": req, "pagination": map[string]any{"mode": "magic"}}, nil, "mode"},
		{"cursorParam in page mode", map[string]any{"request": req, "pagination": map[string]any{"mode": "page", "cursorParam": "c"}}, nil, "cursorParam"},
		{"sizeParam with none", map[string]any{"request": req, "pagination": map[string]any{"sizeParam": "limit"}}, nil, "sizeParam"},
		{"initialPage 2", map[string]any{"request": req, "pagination": map[string]any{"mode": "page", "initialPage": 2}}, nil, "initialPage"},
		{"short without size", map[string]any{"request": req, "pagination": map[string]any{"mode": "page", "completion": "short"}}, nil, "sizeParam"},
		{"default parser with fields", map[string]any{"request": req, "parsing": map[string]any{"fields": map[string]any{"identity": "/id"}}}, nil, "mapping"},
		{"mapping without identity", map[string]any{"request": req, "parsing": map[string]any{
			"type": "mapping", "itemsPointer": "/items", "fields": map[string]any{"networkPath": "/p"},
		}}, nil, "identity"},
		{"mapping bad pointer", map[string]any{"request": req, "parsing": map[string]any{
			"type": "mapping", "itemsPointer": "items", "fields": map[string]any{"identity": "/id", "networkPath": "/p"},
		}}, nil, "itemsPointer"},
		{"mapping cursor missing nextCursor", map[string]any{"request": req, "pagination": map[string]any{"mode": "cursor"}, "parsing": map[string]any{
			"type": "mapping", "itemsPointer": "", "fields": map[string]any{"identity": "/id", "networkPath": "/p"},
		}}, nil, "nextCursor"},
		{"nextCursor without cursor mode", map[string]any{"request": req, "parsing": map[string]any{
			"type": "mapping", "itemsPointer": "", "fields": map[string]any{"identity": "/id", "networkPath": "/p", "nextCursor": "/n"},
		}}, nil, "nextCursor"},
		{"missingNodes bad", map[string]any{"request": req, "missingNodes": "ignore"}, nil, "missingNodes"},
		{"timeout bad", map[string]any{"request": req, "timeout": "soon"}, nil, "timeout"},
		{"trimTiers bad", map[string]any{"request": req, "trimTiers": 5}, nil, "trimTiers"},
		{"auth without type", map[string]any{"request": req, "auth": map[string]any{"headerName": "X"}}, nil, "auth.type"},
		{"auth unknown type", map[string]any{"request": req, "auth": map[string]any{"type": "kerberos"}}, nil, "unsupported"},
		{"auth unknown field", map[string]any{"request": req, "auth": map[string]any{"type": "none", "clientSecret": "x"}}, nil, "clientSecret"},
		{"auth mismatched block", map[string]any{"request": req, "auth": map[string]any{
			"type": "basic", "oauth2": map[string]any{"tokenUrl": "https://x.invalid/"},
		}}, map[string]any{"username": "u", "password": "p"}, "oauth2"},
		{"oauth2 missing secret", map[string]any{"request": req, "auth": oauth}, map[string]any{"clientId": fakeClientID}, "clientSecret"},
		{"basic missing password", map[string]any{"request": req, "auth": map[string]any{"type": "basic"}}, map[string]any{"username": "u"}, "password"},
		{"apiKey missing header", map[string]any{"request": req, "auth": map[string]any{"type": "apiKey"}}, map[string]any{"apiKey": fakeAPIKey}, "headerName"},
		{"bearer missing pointer", map[string]any{"request": req, "auth": map[string]any{
			"type": "bearer", "bearer": map[string]any{"url": "https://x.invalid/login"},
		}}, map[string]any{"user": "u"}, "tokenPointer"},
		{"bearer empty creds", map[string]any{"request": req, "auth": map[string]any{
			"type": "bearer", "bearer": map[string]any{"url": "https://x.invalid/login", "tokenPointer": "/t"},
		}}, nil, "credentials"},
		{"non-string credential", map[string]any{"request": req}, map[string]any{"apiKey": 5}, "apiKey"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Loader(context.Background(), providers.Config{Params: tc.params, Creds: tc.creds})
			if tc.want == "" {
				require.Nil(t, err)
				return
			}
			require.NotNil(t, err)
			require.Equal(t, nethttp.StatusBadRequest, err.Code(), err.Error())
			require.Contains(t, err.Error(), tc.want)
			assertNoSecrets(t, err)
		})
	}
}

func assertNoSecrets(t *testing.T, err *httperr.Error) {
	t.Helper()
	for _, secret := range []string{fakeClientSecret, fakeAPIKey} {
		require.NotContains(t, err.Error(), secret)
	}
}

// Params arrive as JSON (node observer, API requests) or YAML (config file).
func TestLoaderAcceptsJSONAndYAMLParams(t *testing.T) {
	const jsonParams = `{
	  "request": {"url": "https://topology.example.invalid/v1/nodes", "headers": {"Accept": "application/json"}},
	  "pagination": {"mode": "page", "initialPage": 0, "sizeParam": "limit", "maxPages": 50},
	  "parsing": {"type": "mapping", "itemsPointer": "/data", "fields": {"identity": "/id", "networkPath": "/path"}},
	  "maxResponseBytes": 1048576,
	  "trimTiers": 1
	}`
	var params map[string]any
	require.NoError(t, json.Unmarshal([]byte(jsonParams), &params))
	p, err := Loader(context.Background(), providers.Config{Params: params})
	require.Nil(t, err)
	cfg := p.(*Provider).cfg
	require.Equal(t, 0, cfg.pagination.initialPage)
	require.Equal(t, 50, cfg.pagination.maxPages)
	require.EqualValues(t, 1048576, cfg.maxResponseBytes)
	require.Equal(t, 1, cfg.trimTiers)

	const yamlParams = `
request:
  url: https://topology.example.invalid/v1/nodes
pagination:
  mode: offset
  initialOffset: 0
  sizeParam: limit
auth:
  type: apiKey
  headerName: X-API-Key
`
	params = nil
	require.NoError(t, yaml.Unmarshal([]byte(yamlParams), &params))
	_, err = Loader(context.Background(), providers.Config{Params: params, Creds: map[string]any{"apiKey": fakeAPIKey}})
	require.Nil(t, err)
}
