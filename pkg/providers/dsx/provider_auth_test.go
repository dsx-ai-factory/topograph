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
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/topograph/pkg/providers"
)

func TestClientCredentials(t *testing.T) {
	var tokenCalls int
	var gotPath, gotAuth, gotPageSize string

	idp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			w.Write([]byte(`{"token_endpoint":"http://` + r.Host + `/token"}`))
		case "/token":
			tokenCalls++
			user, pass, _ := r.BasicAuth()
			if user != "id" || pass != "secret" {
				http.Error(w, `{"error":"invalid_client"}`, http.StatusUnauthorized)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"access_token":"tok","token_type":"bearer","expires_in":900}`))
		}
	}))
	defer idp.Close()

	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAuth, gotPageSize = r.URL.EscapedPath(), r.Header.Get("Authorization"), r.URL.Query().Get("page_size")
		w.Write([]byte(`{"switches":[{"leaf1":{"nodes":[{"node_id":"n1"}]}}]}`))
	}))
	defer api.Close()

	p, herr := Loader(context.Background(), providersConfig(map[string]any{
		"base_url": api.URL, "vpc_id": "vpc-1",
	}, map[string]any{"client_id": "id", "client_secret": "secret", "issuer_url": idp.URL, "scopes": []any{"a", "b"}}))
	if herr != nil {
		t.Fatal(herr)
	}
	prov := p.(*Provider)
	for i := 0; i < 2; i++ {
		c, _ := prov.clientFactory()
		if _, err := c.GetTopology(context.Background(), prov.vpcID, 5000, ""); err != nil {
			t.Fatal(err)
		}
	}
	if gotPath != "/v1/topology/vpcs/vpc-1/nodes" {
		t.Errorf("path = %q", gotPath)
	}
	if gotAuth != "Bearer tok" {
		t.Errorf("auth header = %q", gotAuth)
	}
	if gotPageSize != "1000" {
		t.Errorf("page_size = %q, want clamp to 1000", gotPageSize)
	}
	if tokenCalls != 1 {
		t.Errorf("token endpoint called %d times, want 1 (cached)", tokenCalls)
	}
}

func TestLoaderCredentialValidation(t *testing.T) {
	base := map[string]any{"base_url": "https://x", "vpc_id": "vpc-1"}
	for name, creds := range map[string]map[string]any{
		"id without secret": {"client_id": "a", "issuer_url": "http://i"},
		"token and client":  {"token": "t", "client_id": "a", "client_secret": "b", "issuer_url": "http://i"},
		"bad scopes type":   {"client_id": "a", "client_secret": "b", "issuer_url": "http://i", "scopes": 5},
	} {
		if _, err := Loader(context.Background(), providersConfig(base, creds)); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
	if _, err := Loader(context.Background(), providersConfig(map[string]any{"base_url": "https://x", "vpc_id": "vpc-1"},
		map[string]any{"client_id": "a", "client_secret": "b"})); err == nil || !strings.Contains(err.Error(), "issuer URL") {
		t.Errorf("expected issuer_url error, got %v", err)
	}
}

func providersConfig(params, creds map[string]any) providers.Config {
	return providers.Config{Params: params, Creds: creds}
}

func TestLoaderVPCID(t *testing.T) {
	for _, bad := range []string{"", "a/b", "..", "a?b", "a%2fb"} {
		params := map[string]any{"base_url": "https://x"}
		if bad != "" {
			params["vpc_id"] = bad
		}
		if _, err := Loader(context.Background(), providersConfig(params, nil)); err == nil {
			t.Errorf("vpc_id %q: expected error", bad)
		}
	}
	if _, err := Loader(context.Background(), providersConfig(map[string]any{"base_url": "https://x", "vpc_id": "vpc-1.a_b"}, nil)); err != nil {
		t.Errorf("valid vpc_id rejected: %v", err)
	}
}

func TestBaseURLPathPrefixPreserved(t *testing.T) {
	var gotPath string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Write([]byte(`{}`))
	}))
	defer api.Close()

	c := NewHTTPClient(api.URL+"/topology", "")
	if _, err := c.GetTopology(context.Background(), "vpc-1", 0, ""); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/topology/v1/topology/vpcs/vpc-1/nodes" {
		t.Errorf("path = %q", gotPath)
	}
}

func TestLoaderRejectsUnusedAuthSettings(t *testing.T) {
	base := map[string]any{"base_url": "https://x", "vpc_id": "vpc-1"}
	for name, creds := range map[string]map[string]any{
		"issuer without client": {"issuer_url": "https://i"},
		"scopes without client": {"scopes": []any{"a"}},
		"token with issuer":     {"token": "t", "issuer_url": "https://i"},
	} {
		if _, err := Loader(context.Background(), providersConfig(base, creds)); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}

func TestMissingNodesWarningAndMultiParent(t *testing.T) {
	// Smoke test: neither helper may panic and the multi-parent child keeps the last parent.
	sw := []map[string]SwitchAdjacency{
		{"s1": {Switches: []string{"leaf"}}},
		{"s2": {Switches: []string{"leaf"}}},
		{"leaf": {Nodes: []NodeInfo{{NodeID: "n1"}}}},
	}
	want := map[string]struct{}{"n1": {}, "missing": {}}
	warnMissingNodes(sw, want)
	topo := buildClusterTopology(sw, want)
	if topo.Len() != 1 {
		t.Fatalf("Len = %d, want 1", topo.Len())
	}
}

func TestTokenFailureSurfacesAsBadGateway(t *testing.T) {
	failing := func(context.Context) (string, error) { return "", errors.New("invalid_client") }
	c := NewHTTPClientWithTokenSource("http://127.0.0.1:1", failing)
	_, err := c.GetTopology(context.Background(), "vpc-1", 0, "")
	if err == nil || !strings.Contains(err.Error(), "access token") {
		t.Errorf("expected access token error, got %v", err)
	}
}

func TestLoaderBaseURLValidation(t *testing.T) {
	for _, bad := range []string{"", "topology.example.com", "ftp://x", "https://"} {
		if _, err := Loader(context.Background(), providersConfig(map[string]any{"base_url": bad, "vpc_id": "vpc-1"}, nil)); err == nil {
			t.Errorf("base_url %q: expected error", bad)
		}
	}

	// Plain http is fine without credentials (in-cluster, Envoy supplies auth)...
	if _, err := Loader(context.Background(), providersConfig(map[string]any{"base_url": "http://topology.svc:8080", "vpc_id": "vpc-1"}, nil)); err != nil {
		t.Errorf("http without credentials rejected: %v", err)
	}
	// ...but never with a token or OAuth client credentials.
	for name, creds := range map[string]map[string]any{
		"token": {"token": "t"},
		"oauth": {"client_id": "a", "client_secret": "b", "issuer_url": "https://i"},
	} {
		if _, err := Loader(context.Background(), providersConfig(map[string]any{"base_url": "http://topology.svc:8080", "vpc_id": "vpc-1"}, creds)); err == nil {
			t.Errorf("http with %s credentials: expected error", name)
		}
	}
}

// TestLoaderBindsServerCredentialsToConfiguredBaseURL checks that a request
// without credentials cannot send the server's credentials to another endpoint,
// while its other parameters stay free to change.
func TestLoaderBindsServerCredentialsToConfiguredBaseURL(t *testing.T) {
	creds := map[string]any{"token": "server-token"}
	serverParams := map[string]any{"base_url": "https://dsx.example/topology", "vpc_id": "vpc-server"}

	for _, tc := range []struct {
		name        string
		serverCreds bool
		params      map[string]any
		wantCode    int
	}{
		{"same base_url", true, map[string]any{"base_url": "https://dsx.example/topology", "vpc_id": "vpc-1"}, 0},
		{"same base_url with trailing slash", true, map[string]any{"base_url": "https://dsx.example/topology/", "vpc_id": "vpc-1"}, 0},
		{"other vpc_id and trimTiers are accepted", true, map[string]any{"base_url": "https://dsx.example/topology", "vpc_id": "vpc-2", "trimTiers": 1}, 0},
		{"different base_url", true, map[string]any{"base_url": "https://attacker.example", "vpc_id": "vpc-1"}, http.StatusForbidden},
		{"different path prefix", true, map[string]any{"base_url": "https://dsx.example", "vpc_id": "vpc-1"}, http.StatusForbidden},
		{"request credentials may choose their own base_url", false, map[string]any{"base_url": "https://other.example", "vpc_id": "vpc-1"}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, herr := Loader(context.Background(), providers.Config{
				Params: tc.params, Creds: creds, ServerCreds: tc.serverCreds, ServerParams: serverParams,
			})
			if tc.wantCode == 0 {
				if herr != nil {
					t.Fatalf("unexpected error: %v", herr)
				}
				return
			}
			if herr == nil || herr.Code() != tc.wantCode {
				t.Fatalf("error = %v, want code %d", herr, tc.wantCode)
			}
		})
	}

	// With no base_url in the server configuration there is nothing to bind to.
	_, herr := Loader(context.Background(), providers.Config{
		Params: map[string]any{"base_url": "https://dsx.example", "vpc_id": "vpc-1"}, Creds: creds, ServerCreds: true,
	})
	if herr == nil || herr.Code() != http.StatusForbidden {
		t.Fatalf("error = %v, want 403", herr)
	}
}
