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
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
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
	if _, err := discoverTokenEndpoint(context.Background(), idp.URL); err == nil {
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
