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
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"k8s.io/klog/v2"

	"github.com/dsx-ai-factory/topograph/internal/httperr"
	"github.com/dsx-ai-factory/topograph/pkg/providers"
)

func TestClientCredentials(t *testing.T) {
	// The handlers run on server goroutines, so mu guards what they record.
	var (
		mu                            sync.Mutex
		tokenCalls                    int
		gotPath, gotAuth, gotPageSize string
	)

	idp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			w.Write([]byte(`{"token_endpoint":"http://` + r.Host + `/token"}`))
		case "/token":
			mu.Lock()
			tokenCalls++
			mu.Unlock()
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
		mu.Lock()
		gotPath, gotAuth, gotPageSize = r.URL.EscapedPath(), r.Header.Get("Authorization"), r.URL.Query().Get("page_size")
		mu.Unlock()
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
	mu.Lock()
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
	mu.Unlock()

	// The token endpoint rejects the client: the failure is an upstream one, so 502.
	p, herr = Loader(context.Background(), providersConfig(map[string]any{
		"base_url": api.URL, "vpc_id": "vpc-1",
	}, map[string]any{"client_id": "id", "client_secret": "wrong", "issuer_url": idp.URL}))
	if herr != nil {
		t.Fatal(herr)
	}
	prov = p.(*Provider)
	c, _ := prov.clientFactory()
	_, err := c.GetTopology(context.Background(), prov.vpcID, 0, "")
	var he *httperr.Error
	if !errors.As(err, &he) || he.Code() != http.StatusBadGateway {
		t.Errorf("rejected token request: error = %v, want an httperr.Error with status %d", err, http.StatusBadGateway)
	}
}

func TestLoaderCredentialValidation(t *testing.T) {
	base := map[string]any{"base_url": "https://x", "vpc_id": "vpc-1"}
	// Each case uses an https issuer, so issuer validation cannot make it pass by accident.
	for name, tc := range map[string]struct {
		creds map[string]any
		want  string
	}{
		"id without secret": {
			creds: map[string]any{"client_id": "a", "issuer_url": "https://i"},
			want:  "client ID and client secret are required",
		},
		"bad scopes type": {
			creds: map[string]any{"client_id": "a", "client_secret": "b", "issuer_url": "https://i", "scopes": 5},
			want:  "scopes a list or a string",
		},
	} {
		if _, err := Loader(context.Background(), providersConfig(base, tc.creds)); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: expected %q, got %v", name, tc.want, err)
		}
	}
	if _, err := Loader(context.Background(), providersConfig(map[string]any{"base_url": "https://x", "vpc_id": "vpc-1"},
		map[string]any{"client_id": "a", "client_secret": "b"})); err == nil || !strings.Contains(err.Error(), "issuer URL") {
		t.Errorf("expected issuer_url error, got %v", err)
	}

	// An https issuer, so URL validation cannot mask the mutual-exclusion check.
	_, err := Loader(context.Background(), providersConfig(base,
		map[string]any{"token": "t", "client_id": "a", "client_secret": "b", "issuer_url": "https://i"}))
	if err == nil || err.Code() != http.StatusBadRequest ||
		!strings.Contains(err.Error(), "provide either 'token' or the OAuth2 client credentials, not both") {
		t.Errorf("expected token/client conflict error, got %v", err)
	}
}

func providersConfig(params, creds map[string]any) providers.Config {
	return providers.Config{Params: params, Creds: creds}
}

func TestLoaderVPCID(t *testing.T) {
	for _, bad := range []string{"", "a/b", ".", "..", "a?b", "a%2fb", "-a", " a", "a b", "a\n", "a\nb"} {
		params := map[string]any{"base_url": "https://x"}
		if bad != "" {
			params["vpc_id"] = bad
		}
		_, err := Loader(context.Background(), providersConfig(params, nil))
		if err == nil || err.Code() != http.StatusBadRequest {
			t.Errorf("vpc_id %q: error = %v, want status %d", bad, err, http.StatusBadRequest)
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
	const missingClient = "client ID and client secret are required"
	for name, tc := range map[string]struct {
		creds map[string]any
		want  string
	}{
		"issuer without client": {map[string]any{"issuer_url": "https://i"}, missingClient},
		"scopes without client": {map[string]any{"scopes": []any{"a"}}, missingClient},
		"token with issuer": {
			map[string]any{"token": "t", "issuer_url": "https://i"},
			"provide either 'token' or the OAuth2 client credentials, not both",
		},
	} {
		_, err := Loader(context.Background(), providersConfig(base, tc.creds))
		if err == nil || err.Code() != http.StatusBadRequest || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: error = %v, want status %d containing %q", name, err, http.StatusBadRequest, tc.want)
		}
	}
}

func TestMissingNodesWarningAndMultiParent(t *testing.T) {
	// A switch listed under two parents keeps the last one.
	sw := []map[string]SwitchAdjacency{
		{"s1": {Switches: []string{"leaf"}}},
		{"s2": {Switches: []string{"leaf"}}},
		{"leaf": {Nodes: []NodeInfo{{NodeID: "n1"}}}},
	}
	want := map[string]struct{}{"n1": {}, "missing": {}}

	var buf bytes.Buffer
	klog.LogToStderr(false)
	klog.SetOutput(&buf)
	defer klog.LogToStderr(true)
	warnMissingNodes(sw, want)
	klog.Flush()
	if out := buf.String(); !strings.Contains(out, "no topology for 1 of 2 requested nodes") || !strings.Contains(out, "missing") {
		t.Errorf("missing-node warning not logged: %q", out)
	}

	topo := buildClusterTopology(sw, want)
	if topo.Len() != 1 {
		t.Fatalf("Len = %d, want 1", topo.Len())
	}
	inst := topo.Instances[0]
	if inst.InstanceID != "n1" {
		t.Errorf("InstanceID = %q, want n1", inst.InstanceID)
	}
	var tiers []string
	for _, ft := range inst.FabricTiers {
		tiers = append(tiers, ft.ID)
	}
	if !reflect.DeepEqual(tiers, []string{"leaf", "s2"}) {
		t.Errorf("FabricTiers = %v, want [leaf s2]", tiers)
	}
}

func TestTokenFailureSurfacesAsBadGateway(t *testing.T) {
	failing := func(context.Context) (string, error) { return "", errors.New("invalid_client") }
	c := NewHTTPClientWithTokenSource("http://127.0.0.1:1", failing)
	_, err := c.GetTopology(context.Background(), "vpc-1", 0, "")
	if err == nil || !strings.Contains(err.Error(), "access token") {
		t.Fatalf("expected access token error, got %v", err)
	}
	var herr *httperr.Error
	if !errors.As(err, &herr) || herr.Code() != http.StatusBadGateway {
		t.Errorf("error = %v, want an httperr.Error with status %d", err, http.StatusBadGateway)
	}
}

// TestTokenSourceCancellation checks that cancelling the caller's context ends a
// call that is waiting for a token: GetTopology returns promptly, the token
// source runs once, and no request reaches the API.
func TestTokenSourceCancellation(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1) }))
	defer srv.Close()

	var calls atomic.Int32
	waiting := make(chan struct{})
	tokenFn := func(ctx context.Context) (string, error) {
		calls.Add(1)
		close(waiting)
		<-ctx.Done() // hold until the caller's cancellation reaches the token source
		return "", ctx.Err()
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := NewHTTPClientWithTokenSource(srv.URL, tokenFn).GetTopology(ctx, "vpc-1", 0, "")
		done <- err
	}()

	<-waiting // the token source is blocked; now cancel the caller's context
	cancel()

	select {
	case err := <-done:
		var herr *httperr.Error
		if !errors.As(err, &herr) || herr.Code() != http.StatusBadGateway {
			t.Errorf("error = %v, want an httperr.Error with status %d", err, http.StatusBadGateway)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("GetTopology did not return promptly after context cancellation")
	}
	if n := calls.Load(); n != 1 {
		t.Errorf("token source called %d times, want 1", n)
	}
	if n := hits.Load(); n != 0 {
		t.Errorf("API received %d requests, want 0", n)
	}
}

func TestLoaderBaseURLValidation(t *testing.T) {
	for _, bad := range []string{"", "topology.example.com", "ftp://x", "https://"} {
		_, err := Loader(context.Background(), providersConfig(map[string]any{"base_url": bad, "vpc_id": "vpc-1"}, nil))
		if err == nil || err.Code() != http.StatusBadRequest {
			t.Errorf("base_url %q: error = %v, want status %d", bad, err, http.StatusBadRequest)
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
		_, err := Loader(context.Background(), providersConfig(map[string]any{"base_url": "http://topology.svc:8080", "vpc_id": "vpc-1"}, creds))
		if err == nil || err.Code() != http.StatusBadRequest {
			t.Errorf("http with %s credentials: error = %v, want status %d", name, err, http.StatusBadRequest)
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
