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

package httpauth

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dsx-ai-factory/topograph/internal/httperr"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func fakeCredentials() Credentials {
	return Credentials{"clientId": "fake-client", "clientSecret": "fake-secret"}
}

func newTestSource(t *testing.T, server *httptest.Server, style string) *TokenSource {
	t.Helper()
	source, err := NewClientCredentialsSource(AuthConfig{Type: "oauth2", TokenRequest: &TokenRequestConfig{
		TokenURL: server.URL, Scopes: []string{"api.read", "topology.read"}, AuthStyle: style,
	}}, fakeCredentials(), server.Client())
	require.Nil(t, err)
	return source
}

func TestClientCredentialsGrant(t *testing.T) {
	for _, style := range []string{"", "basic", "form"} {
		t.Run(fmt.Sprint(style), func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				assert.Equal(t, http.MethodPost, r.Method)
				assert.Equal(t, "application/x-www-form-urlencoded", r.Header.Get("Content-Type"))
				assert.NoError(t, r.ParseForm())
				expected := url.Values{"grant_type": {"client_credentials"}, "scope": {"api.read topology.read"}}
				if style != "form" {
					user, password, ok := r.BasicAuth()
					assert.True(t, ok)
					assert.Equal(t, "fake-client", user)
					assert.Equal(t, "fake-secret", password)
				} else {
					assert.Empty(t, r.Header.Get("Authorization"))
					expected.Set("client_id", "fake-client")
					expected.Set("client_secret", "fake-secret")
				}
				assert.Equal(t, expected, r.PostForm)
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"access_token":"fake.jwt.token","token_type":"Bearer","expires_in":3600,"refresh_token":"fake-refresh","private_extra":"fake-private"}`)
			}))
			defer server.Close()
			source := newTestSource(t, server, style)
			var wg sync.WaitGroup
			for range 8 {
				wg.Go(func() {
					token, err := source.Token(context.Background())
					if assert.Nil(t, err) {
						assert.Equal(t, "fake.jwt.token", token.AccessToken)
						assert.Empty(t, token.RefreshToken)
						assert.Nil(t, token.Extra("private_extra"))
						// Returned values cannot change the cached token.
						token.AccessToken = "mutated"
					}
				})
			}
			wg.Wait()
			require.EqualValues(t, 1, calls.Load())
		})
	}
}

func TestTokenRenewalAndIsolation(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := calls.Add(1)
		assert.NoError(t, r.ParseForm())
		assert.Equal(t, "client_credentials", r.Form.Get("grant_type"))
		assert.Empty(t, r.Form.Get("refresh_token"))
		expiry := 3600
		if call == 1 {
			expiry = 1
		} // Within the OAuth2 expiry safety margin.
		user, _, _ := r.BasicAuth()
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"access_token":%q,"expires_in":%d,"refresh_token":"fake-refresh"}`, user, expiry)
	}))
	defer server.Close()
	first := newTestSource(t, server, "basic")
	_, err := first.Token(context.Background())
	require.Nil(t, err)
	_, err = first.Token(context.Background())
	require.Nil(t, err)
	_, err = first.Token(context.Background())
	require.Nil(t, err)
	require.EqualValues(t, 2, calls.Load())
	second, err := NewClientCredentialsSource(AuthConfig{Type: "oauth2", TokenRequest: &TokenRequestConfig{TokenURL: server.URL}},
		Credentials{"clientId": "other-fake-client", "clientSecret": "other-fake-secret"}, server.Client())
	require.Nil(t, err)
	token, err := second.Token(context.Background())
	require.Nil(t, err)
	require.Equal(t, "other-fake-client", token.AccessToken)
	require.EqualValues(t, 3, calls.Load())
	// Even identical credentials in another invocation get a separate cache.
	_, err = newTestSource(t, server, "basic").Token(context.Background())
	require.Nil(t, err)
	require.EqualValues(t, 4, calls.Load())
}

func TestTokenWithoutExpiry(t *testing.T) {
	calls := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"access_token":"fake-token"}`)
	}))
	defer server.Close()
	source := newTestSource(t, server, "basic")
	for range 2 {
		token, err := source.Token(context.Background())
		require.Nil(t, err)
		require.True(t, token.Expiry.IsZero())
	}
	require.Equal(t, 1, calls)
}

func TestTokenErrorsAreRedactedAndNotRetried(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{"upstream", 503, `fake-client fake-secret fake-token`},
		{"unauthorized", 401, `fake-secret`},
		{"bad request", 400, `fake-secret`},
		{"forbidden", 403, `fake-secret`},
		{"rate limited", 429, `fake-secret`},
		{"OAuth2 error in success response", 200, `{"error":"invalid_client","error_description":"fake-secret"}`},
		{"malformed", 200, `fake-secret`},
		{"missing token", 200, `{}`},
		{"empty token", 200, `{"access_token":""}`},
		{"invalid type", 200, `{"access_token":"fake-token","token_type":"Basic"}`},
		{"header injection", 200, `{"access_token":"fake-token\r\n"}`},
		{"expired", 200, `{"access_token":"fake-token","expires_in":-10}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls++
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer server.Close()
			token, err := newTestSource(t, server, "basic").Token(context.Background())
			require.NotNil(t, err)
			require.Nil(t, token)
			expectedStatus := http.StatusBadGateway
			if tc.status >= 400 {
				expectedStatus = tc.status
			}
			require.Equal(t, expectedStatus, err.Code())
			for _, secret := range []string{"fake-client", "fake-secret", "fake-token", server.URL} {
				require.NotContains(t, err.Error(), secret)
			}
			require.Equal(t, 1, calls)
		})
	}
}

func TestTokenRedirectAndTransportErrors(t *testing.T) {
	var targetCalls atomic.Int32
	target := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { targetCalls.Add(1) }))
	defer target.Close()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	source := newTestSource(t, server, "basic")
	_, err := source.Token(context.Background())
	require.EqualError(t, err, "OAuth2 token acquisition failed")
	require.Equal(t, http.StatusBadGateway, err.Code())
	require.Zero(t, targetCalls.Load())
	require.Nil(t, server.Client().CheckRedirect, "caller-owned client must not be changed")
	server.Close()
	_, err = source.Token(context.Background())
	require.EqualError(t, err, "OAuth2 token acquisition failed")
	require.Equal(t, http.StatusBadGateway, err.Code())
}

func TestTokenCancellation(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	server := httptest.NewTLSServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		close(started)
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer server.Close()
	defer close(release)
	source := newTestSource(t, server, "basic")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan *httperr.Error, 1)
	go func() { _, err := source.Token(ctx); done <- err }()
	<-started
	// A queued request can cancel without waiting for the active acquisition.
	waiting, stop := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer stop()
	_, err := source.Token(waiting)
	require.Equal(t, http.StatusGatewayTimeout, err.Code())
	require.EqualError(t, err, "OAuth2 token request timed out")
	cancel()
	require.Equal(t, http.StatusRequestTimeout, (<-done).Code())
	_, err = source.Token(ctx)
	require.Equal(t, http.StatusRequestTimeout, err.Code())
	require.EqualError(t, err, "OAuth2 token request canceled")
}

func TestClientCredentialsValidation(t *testing.T) {
	valid := AuthConfig{Type: "oauth2", TokenRequest: &TokenRequestConfig{TokenURL: "https://example.invalid/token"}}
	for _, rawURL := range []string{"", "://fake-secret", "http://example.invalid", "/relative", "https://fake-client:fake-secret@example.invalid/token", "https://example.invalid/#fragment"} {
		config := valid
		request := *valid.TokenRequest
		config.TokenRequest = &request
		request.TokenURL = rawURL
		_, err := NewClientCredentialsSource(config, fakeCredentials(), &http.Client{})
		require.NotNil(t, err)
		require.Equal(t, http.StatusBadRequest, err.Code())
		require.NotContains(t, err.Error(), "fake-secret")
	}
	for _, creds := range []Credentials{nil, {}, {"clientId": "fake-client"}, {"clientSecret": "fake-secret"}, {"clientId": " ", "clientSecret": "fake-secret"}, {"clientId": "fake-client", "clientSecret": " \t"}} {
		_, err := NewClientCredentialsSource(valid, creds, &http.Client{})
		require.NotNil(t, err)
		require.Equal(t, http.StatusBadRequest, err.Code())
		require.NotContains(t, err.Error(), "fake-secret")
	}
	for _, scope := range []string{"", "two scopes", "bad\nscope", `bad"scope`, `bad\scope`, "非ascii"} {
		config := valid
		request := *valid.TokenRequest
		config.TokenRequest = &request
		request.Scopes = []string{scope}
		_, err := NewClientCredentialsSource(config, fakeCredentials(), &http.Client{})
		require.NotNil(t, err)
		require.Equal(t, http.StatusBadRequest, err.Code())
		require.False(t, strings.Contains(err.Error(), "fake-secret"))
	}
	_, err := NewClientCredentialsSource(valid, fakeCredentials(), nil)
	require.NotNil(t, err)
	require.Equal(t, http.StatusBadRequest, err.Code())
	config := valid
	request := *valid.TokenRequest
	config.TokenRequest = &request
	request.AuthStyle = "invalid"
	_, err = NewClientCredentialsSource(config, fakeCredentials(), &http.Client{})
	require.NotNil(t, err)
	require.Equal(t, http.StatusBadRequest, err.Code())
}

func TestClientTimeoutReturnsHTTPError(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewTLSServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer server.Close()
	defer close(release)
	client := server.Client()
	client.Timeout = 30 * time.Millisecond
	source, err := NewClientCredentialsSource(AuthConfig{Type: "oauth2", TokenRequest: &TokenRequestConfig{TokenURL: server.URL}}, fakeCredentials(), client)
	require.Nil(t, err)
	token, err := source.Token(context.Background())
	require.Nil(t, token)
	require.NotNil(t, err)
	require.Equal(t, http.StatusGatewayTimeout, err.Code())
	require.EqualError(t, err, "OAuth2 token request timed out")
}
