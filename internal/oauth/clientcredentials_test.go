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

package oauth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestValidateAuthURL(t *testing.T) {
	for url, ok := range map[string]bool{
		"https://idp.example.com": true,
		"http://127.0.0.1:8080":   true,
		"http://localhost:8080":   true,
		"http://idp.example.com":  false,
		"ftp://idp.example.com":   false,
		"idp.example.com":         false,
		"https://":                false,
	} {
		if err := ValidateURL(url); (err == nil) != ok {
			t.Errorf("ValidateURL(%q) = %v, want ok=%v", url, err, ok)
		}
	}
}

func TestDiscoveryRejectsInsecureTokenEndpoint(t *testing.T) {
	idp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"token_endpoint":"http://idp.example.com/token"}`))
	}))
	defer idp.Close()
	if _, err := discoverTokenEndpoint(context.Background(), newHTTPClient(nil), idp.URL); err == nil {
		t.Error("expected error for non-https token_endpoint")
	}
}

func TestClientCredentialsCachesToken(t *testing.T) {
	var calls atomic.Int32
	idp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			calls.Add(1)
			user, pass, _ := r.BasicAuth()
			if user != "id" || pass != "secret" {
				http.Error(w, `{"error":"invalid_client"}`, http.StatusUnauthorized)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"access_token":"tok","token_type":"bearer","expires_in":900}`))
			return
		}
		w.Write([]byte(`{"token_endpoint":"http://` + r.Host + `/token"}`))
	}))
	defer idp.Close()

	fn, err := NewClientCredentialsSource(Config{ClientID: "id", ClientSecret: "secret", IssuerURL: idp.URL, Scopes: []string{"a"}})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		tok, err := fn(context.Background())
		if err != nil || tok != "tok" {
			t.Fatalf("token = %q, err = %v", tok, err)
		}
	}
	if calls.Load() != 1 {
		t.Errorf("token endpoint called %d times, want 1", calls.Load())
	}
}

func TestClientCredentialsFailure(t *testing.T) {
	idp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			http.Error(w, `{"error":"invalid_client"}`, http.StatusUnauthorized)
			return
		}
		w.Write([]byte(`{"token_endpoint":"http://` + r.Host + `/token"}`))
	}))
	defer idp.Close()

	fn, err := NewClientCredentialsSource(Config{ClientID: "id", ClientSecret: "bad", IssuerURL: idp.URL})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fn(context.Background()); err == nil {
		t.Fatal("expected token error")
	}
}

func TestTokenURLSkipsDiscovery(t *testing.T) {
	var discovery, token atomic.Int32
	idp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			token.Add(1)
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"access_token":"tok","token_type":"bearer","expires_in":900}`))
			return
		}
		discovery.Add(1)
	}))
	defer idp.Close()

	// IssuerURL is deliberately unreachable: TokenURL must win.
	fn, err := NewClientCredentialsSource(Config{ClientID: "id", ClientSecret: "s", IssuerURL: "https://127.0.0.1:1", TokenURL: idp.URL + "/token"})
	if err != nil {
		t.Fatal(err)
	}
	if tok, err := fn(context.Background()); err != nil || tok != "tok" {
		t.Fatalf("token = %q, err = %v", tok, err)
	}
	if discovery.Load() != 0 || token.Load() != 1 {
		t.Errorf("discovery=%d token=%d, want 0 and 1", discovery.Load(), token.Load())
	}
}

func TestConfigValidate(t *testing.T) {
	for name, c := range map[string]Config{
		"no secret":       {ClientID: "id", IssuerURL: "https://i"},
		"no id":           {ClientSecret: "s", IssuerURL: "https://i"},
		"no endpoint":     {ClientID: "id", ClientSecret: "s"},
		"insecure issuer": {ClientID: "id", ClientSecret: "s", IssuerURL: "http://i.example.com"},
		"insecure token":  {ClientID: "id", ClientSecret: "s", TokenURL: "http://i.example.com/token"},
	} {
		if err := c.Validate(); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
	for name, c := range map[string]Config{
		"issuer": {ClientID: "id", ClientSecret: "s", IssuerURL: "https://i"},
		"token":  {ClientID: "id", ClientSecret: "s", TokenURL: "https://i/token"},
	} {
		if err := c.Validate(); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestDecodeConfig(t *testing.T) {
	cfg, err := DecodeConfig(map[string]any{
		"client_id": "id", "client_secret": "s", "issuer_url": "https://i", "token": "ignored",
		"scopes": []any{"a", "b"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ClientID != "id" || cfg.ClientSecret != "s" || cfg.IssuerURL != "https://i" || len(cfg.Scopes) != 2 {
		t.Errorf("unexpected config: %+v", cfg)
	}

	cfg, err = DecodeConfig(map[string]any{"scopes": "a b  c", "token_url": "https://i/token"})
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Scopes) != 3 || cfg.TokenURL != "https://i/token" {
		t.Errorf("unexpected config: %+v", cfg)
	}

	if _, err := DecodeConfig(map[string]any{"scopes": 5}); err == nil {
		t.Error("expected error for non-string scopes")
	}
	if _, err := DecodeConfig(map[string]any{"client_id": 5}); err == nil {
		t.Error("expected error for non-string client_id")
	}

	empty, err := DecodeConfig(nil)
	if err != nil || empty.Configured() {
		t.Errorf("empty creds: cfg=%+v err=%v", empty, err)
	}
}

func TestDiscoveryFailures(t *testing.T) {
	for name, handler := range map[string]http.HandlerFunc{
		"non-200":           func(w http.ResponseWriter, r *http.Request) { http.Error(w, "boom", http.StatusInternalServerError) },
		"malformed JSON":    func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{not json`)) },
		"no token_endpoint": func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{}`)) },
	} {
		t.Run(name, func(t *testing.T) {
			idp := httptest.NewServer(handler)
			defer idp.Close()
			if _, err := discoverTokenEndpoint(context.Background(), newHTTPClient(nil), idp.URL); err == nil {
				t.Error("expected error")
			}
		})
	}
}

func TestFailedDiscoveryIsRetried(t *testing.T) {
	var discovery atomic.Int32
	idp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"access_token":"tok","token_type":"bearer","expires_in":900}`))
		default:
			if discovery.Add(1) == 1 {
				http.Error(w, "not ready", http.StatusServiceUnavailable)
				return
			}
			w.Write([]byte(`{"token_endpoint":"http://` + r.Host + `/token"}`))
		}
	}))
	defer idp.Close()

	fn, err := NewClientCredentialsSource(Config{ClientID: "id", ClientSecret: "s", IssuerURL: idp.URL})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fn(context.Background()); err == nil {
		t.Fatal("first call: expected discovery error")
	}
	if tok, err := fn(context.Background()); err != nil || tok != "tok" {
		t.Fatalf("second call: token = %q, err = %v", tok, err)
	}
}

func TestConcurrentCallersShareDiscoveryAndToken(t *testing.T) {
	var discovery, token atomic.Int32
	idp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			token.Add(1)
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"access_token":"tok","token_type":"bearer","expires_in":900}`))
			return
		}
		discovery.Add(1)
		w.Write([]byte(`{"token_endpoint":"http://` + r.Host + `/token"}`))
	}))
	defer idp.Close()

	fn, err := NewClientCredentialsSource(Config{ClientID: "id", ClientSecret: "s", IssuerURL: idp.URL})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if tok, err := fn(context.Background()); err != nil || tok != "tok" {
				t.Errorf("token = %q, err = %v", tok, err)
			}
		}()
	}
	wg.Wait()
	if discovery.Load() != 1 || token.Load() != 1 {
		t.Errorf("discovery=%d token=%d, want 1 and 1", discovery.Load(), token.Load())
	}
}

func TestRedirectGuard(t *testing.T) {
	check := newHTTPClient(nil).CheckRedirect
	mk := func(u string) *http.Request {
		r, _ := http.NewRequest(http.MethodGet, u, nil)
		return r
	}
	via := []*http.Request{mk("https://idp.example.com/a")}

	if err := check(mk("https://idp.example.com/b"), via); err != nil {
		t.Errorf("https redirect refused: %v", err)
	}
	if err := check(mk("http://idp.example.com/b"), via); err == nil {
		t.Error("https to http redirect must be refused")
	}
	if err := check(mk("http://127.0.0.1:8080/b"), via); err == nil {
		t.Error("https to loopback http redirect must be refused")
	}
	if err := check(mk("http://evil.example.com/b"), via); err == nil {
		t.Error("redirect to a remote http host must be refused")
	}
	tooMany := make([]*http.Request, maxRedirects)
	if err := check(mk("https://idp.example.com/b"), tooMany); err == nil {
		t.Error("redirect limit not enforced")
	}
}

// TestTokenRequestRefusesDowngradeRedirect drives the client built by
// NewClientCredentialsSource: an https token endpoint redirects to a plain http
// server, and neither the request nor the client secret may reach it.
func TestTokenRequestRefusesDowngradeRedirect(t *testing.T) {
	var plainHits atomic.Int32
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		plainHits.Add(1)
	}))
	defer plain.Close()

	tlsSrv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, plain.URL+"/token", http.StatusTemporaryRedirect)
	}))
	defer tlsSrv.Close()

	fn, err := NewClientCredentialsSource(Config{
		ClientID: "id", ClientSecret: "s", TokenURL: tlsSrv.URL + "/token",
		transport: tlsSrv.Client().Transport,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = fn(context.Background()); err == nil || !strings.Contains(err.Error(), "redirect refused") {
		t.Fatalf("expected redirect refusal, got %v", err)
	}
	if plainHits.Load() != 0 {
		t.Errorf("plain http target was contacted %d times", plainHits.Load())
	}
}

// TestDiscoveryRefusesDowngradeRedirect covers the same guard on the OIDC
// discovery request.
func TestDiscoveryRefusesDowngradeRedirect(t *testing.T) {
	var plainHits atomic.Int32
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		plainHits.Add(1)
	}))
	defer plain.Close()

	tlsSrv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, plain.URL, http.StatusTemporaryRedirect)
	}))
	defer tlsSrv.Close()

	_, err := discoverTokenEndpoint(context.Background(), newHTTPClient(tlsSrv.Client().Transport), tlsSrv.URL)
	if err == nil || !strings.Contains(err.Error(), "redirect refused") {
		t.Fatalf("expected redirect refusal, got %v", err)
	}
	if plainHits.Load() != 0 {
		t.Errorf("plain http target was contacted %d times", plainHits.Load())
	}
}

// namedString has string kind but is not string, so a plain type assertion on it panics.
type namedString string

func TestSplitFieldsAcceptsNamedStringTypes(t *testing.T) {
	cfg, err := DecodeConfig(map[string]any{"scopes": namedString("a  b")})
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Scopes) != 2 || cfg.Scopes[0] != "a" || cfg.Scopes[1] != "b" {
		t.Errorf("scopes = %v, want [a b]", cfg.Scopes)
	}
}

// TestHTTPSIssuerRejectsHTTPTokenEndpoint covers a discovery document that
// moves the client secret from an https issuer to a plain-http loopback server,
// which ValidateURL alone would accept.
func TestHTTPSIssuerRejectsHTTPTokenEndpoint(t *testing.T) {
	var plainHits atomic.Int32
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		plainHits.Add(1)
	}))
	defer plain.Close()

	tlsSrv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"token_endpoint":"` + plain.URL + `/token"}`))
	}))
	defer tlsSrv.Close()

	_, err := discoverTokenEndpoint(context.Background(), newHTTPClient(tlsSrv.Client().Transport), tlsSrv.URL)
	if err == nil || !strings.Contains(err.Error(), "must use https because the issuer does") {
		t.Fatalf("expected https-issuer rejection, got %v", err)
	}
	if plainHits.Load() != 0 {
		t.Errorf("plain http target was contacted %d times", plainHits.Load())
	}
}

// TestTokenFuncReturnsOnCallerDeadline checks that a caller whose context ends
// stops waiting at once, while the detached fetch is left to finish.
func TestTokenFuncReturnsOnCallerDeadline(t *testing.T) {
	release := make(chan struct{})
	idp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"access_token":"tok","token_type":"bearer","expires_in":900}`))
	}))
	defer idp.Close()
	defer close(release)

	fn, err := NewClientCredentialsSource(Config{ClientID: "id", ClientSecret: "s", TokenURL: idp.URL + "/token"})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err = fn(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected deadline exceeded, got %v", err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("caller waited %s for the blocked token endpoint", d)
	}

	// The fetch kept running, so a later caller still gets the token.
	release <- struct{}{}
	tok, err := fn(context.Background())
	if err != nil || tok != "tok" {
		t.Fatalf("token = %q, err = %v", tok, err)
	}
}

// TestDecodeConfigErrorOmitsValues checks that a mistyped secret is not echoed
// back in the error.
func TestDecodeConfigErrorOmitsValues(t *testing.T) {
	_, err := DecodeConfig(map[string]any{"client_secret": 987654321})
	if err == nil {
		t.Fatal("expected an error for a numeric client_secret")
	}
	if strings.Contains(err.Error(), "987654321") {
		t.Errorf("error leaks the secret value: %v", err)
	}
}
