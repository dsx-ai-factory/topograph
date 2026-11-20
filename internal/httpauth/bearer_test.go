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
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dsx-ai-factory/topograph/internal/httperr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func bearerAuth(endpoint string) AuthConfig {
	return AuthConfig{Type: "bearer", Bearer: &BearerLoginConfig{URL: endpoint, TokenPointer: "/data/access_token"}}
}

func TestAcquireBearerToken(t *testing.T) {
	for _, encoding := range []string{"", "json", "form"} {
		for _, method := range []string{"", http.MethodPost, http.MethodPut} {
			t.Run(encoding+"/"+method, func(t *testing.T) {
				calls := 0
				creds := Credentials{"user-name": "fake-user +/@", "secret": "fake-secret &=+", "tenant": "fake-tenant"}
				server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls++
					expectedMethod := method
					if expectedMethod == "" {
						expectedMethod = http.MethodPost
					}
					assert.Equal(t, expectedMethod, r.Method)
					assert.Empty(t, r.Header.Get("Authorization"))
					assert.Equal(t, "application/json", r.Header.Get("Accept"))
					assert.Empty(t, r.URL.RawQuery)
					if encoding == "form" {
						assert.Equal(t, "application/x-www-form-urlencoded", r.Header.Get("Content-Type"))
						assert.NoError(t, r.ParseForm())
						expected := make(url.Values)
						for key, value := range creds {
							expected.Set(key, value)
						}
						assert.Equal(t, expected, r.PostForm)
					} else {
						assert.Equal(t, "application/json", r.Header.Get("Content-Type"))
						var actual Credentials
						assert.NoError(t, json.NewDecoder(r.Body).Decode(&actual))
						assert.Equal(t, creds, actual, "the login body must contain exactly the credentials")
					}
					_, _ = io.WriteString(w, `{"data":{"access_token":"fake.jwt.token"}}`)
				}))
				defer server.Close()
				auth := bearerAuth(server.URL)
				auth.Bearer.Encoding, auth.Bearer.Method = encoding, method
				token, err := AcquireToken(context.Background(), auth, creds, server.Client())
				require.Nil(t, err)
				require.Equal(t, "fake.jwt.token", token)
				require.Equal(t, 1, calls)
			})
		}
	}
}

func TestBearerTokenPointers(t *testing.T) {
	for _, tc := range []struct {
		name, pointer, body string
		valid               bool
	}{
		{"array", "/data/0/token", `{"data":[{"token":"fake-token"}]}`, true},
		{"escaped keys", "/a~1b/~0token", `{"a/b":{"~token":"fake-token"}}`, true},
		{"escape order", "/~01", `{"~1":"fake-token"}`, true},
		{"empty key", "/", `{"":"fake-token"}`, true},
		{"root array", "/0", `["fake-token"]`, true},
		{"numeric key", "/01", `{"01":"fake-token"}`, true},
		{"leading zero index", "/01", `["fake-token","fake-token"]`, false},
		{"negative index", "/-1", `["fake-token"]`, false},
		{"append index", "/-", `["fake-token"]`, false},
		{"outside array", "/3", `["fake-token"]`, false},
		{"scalar traversal", "/data/token", `{"data":"fake-token"}`, false},
		{"missing", "/token", `{}`, false},
		{"null", "/token", `{"token":null}`, false},
		{"number", "/token", `{"token":123}`, false},
		{"object", "/token", `{"token":{}}`, false},
		{"blank", "/token", `{"token":"  "}`, false},
		{"header injection", "/token", `{"token":"fake-token\r\n"}`, false},
		{"malformed", "/token", `fake-secret`, false},
		{"multiple documents", "/token", `{"token":"fake-token"} {}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, tc.body) }))
			defer server.Close()
			auth := bearerAuth(server.URL)
			auth.Bearer.TokenPointer = tc.pointer
			token, err := AcquireToken(context.Background(), auth, Credentials{"secret": "fake-secret"}, server.Client())
			if tc.valid {
				require.Nil(t, err)
				require.Equal(t, "fake-token", token)
			} else {
				require.Empty(t, token)
				require.NotNil(t, err)
				require.Equal(t, http.StatusBadGateway, err.Code())
				require.EqualError(t, err, "authentication failed: invalid bearer token response")
			}
		})
	}
}

func TestBearerConfigValidatedBeforeRequest(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	defer server.Close()
	for _, change := range []func(*AuthConfig){
		func(a *AuthConfig) { a.Type = "unknown" },
		func(a *AuthConfig) { a.Bearer = nil },
		func(a *AuthConfig) { a.Bearer.Method = http.MethodGet },
		func(a *AuthConfig) { a.Bearer.Encoding = "xml" },
		func(a *AuthConfig) { a.Bearer.URL = "http://example.invalid" },
		func(a *AuthConfig) { a.Bearer.URL = "https://fake-user:fake-secret@example.invalid" },
		func(a *AuthConfig) { a.Bearer.URL = "://fake-secret" },
		func(a *AuthConfig) { a.Bearer.TokenPointer = "" },
		func(a *AuthConfig) { a.Bearer.TokenPointer = "token" },
		func(a *AuthConfig) { a.Bearer.TokenPointer = "/bad~" },
		func(a *AuthConfig) { a.Bearer.TokenPointer = "/bad~2" },
	} {
		auth := bearerAuth(server.URL)
		change(&auth)
		token, err := AcquireToken(context.Background(), auth, Credentials{"secret": "fake-secret"}, server.Client())
		require.Empty(t, token)
		require.NotNil(t, err)
		require.Equal(t, http.StatusBadRequest, err.Code())
		require.NotContains(t, err.Error(), "fake-secret")
	}
	for _, creds := range []Credentials{nil, {}} {
		_, err := AcquireToken(context.Background(), bearerAuth(server.URL), creds, server.Client())
		require.NotNil(t, err)
		require.Equal(t, http.StatusBadRequest, err.Code())
	}
	_, err := AcquireToken(context.Background(), bearerAuth(server.URL), Credentials{"secret": "fake-secret"}, nil)
	require.NotNil(t, err)
	require.Equal(t, http.StatusBadRequest, err.Code())
	require.Zero(t, calls.Load())
}

func TestBearerHTTPFailures(t *testing.T) {
	for _, status := range []int{301, 400, 401, 403, 429, 500, 503} {
		calls := 0
		server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			calls++
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(status)
			_, _ = io.WriteString(w, "fake-secret fake-token")
		}))
		_, err := AcquireToken(context.Background(), bearerAuth(server.URL), Credentials{"secret": "fake-secret"}, server.Client())
		server.Close()
		require.NotNil(t, err)
		expectedCode := status
		if status < 400 {
			expectedCode = http.StatusBadGateway // redirects are not followed
		}
		require.Equal(t, expectedCode, err.Code())
		require.EqualError(t, err, "authentication failed: HTTP request failed")
		expectedCalls := 1
		if status == 429 || status == 500 || status == 503 {
			expectedCalls = 5
		}
		require.Equal(t, expectedCalls, calls, "bearer login follows the shared HTTP retry policy")
	}
}

func TestBearerResponseLimit(t *testing.T) {
	for _, tc := range []struct {
		name string
		size int
	}{
		{"at limit", maxTokenResponseBytes},
		{"over limit", maxTokenResponseBytes + 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := `{"data":{"access_token":"fake-token"}}`
			// Trailing JSON whitespace pads the response without changing the token.
			body += strings.Repeat(" ", tc.size-len(body))
			var calls atomic.Int32
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				_, _ = io.WriteString(w, body)
			}))
			defer server.Close()
			token, err := AcquireToken(context.Background(), bearerAuth(server.URL), Credentials{"secret": "fake-secret"}, server.Client())
			require.Equal(t, int32(1), calls.Load(), "oversized responses must not be retried")
			if tc.size > maxTokenResponseBytes {
				require.Empty(t, token)
				require.NotNil(t, err)
				require.Equal(t, http.StatusBadGateway, err.Code())
				require.EqualError(t, err, "bearer token response exceeds size limit")
			} else {
				require.Nil(t, err)
				require.Equal(t, "fake-token", token)
			}
		})
	}
}

func TestBearerRedirectAndTransportErrors(t *testing.T) {
	var targetCalls atomic.Int32
	target := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { targetCalls.Add(1) }))
	defer target.Close()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	client := server.Client()
	auth := bearerAuth(server.URL)
	_, err := AcquireToken(context.Background(), auth, Credentials{"secret": "fake-secret"}, client)
	require.NotNil(t, err)
	require.Equal(t, http.StatusBadGateway, err.Code())
	require.Zero(t, targetCalls.Load())
	require.Nil(t, client.CheckRedirect)
	server.Close()
	_, err = AcquireToken(context.Background(), auth, Credentials{"secret": "fake-secret"}, client)
	require.NotNil(t, err)
	require.Equal(t, http.StatusBadGateway, err.Code())
	require.EqualError(t, err, "authentication failed: HTTP request failed")
}

func TestBearerCancellationAndTimeout(t *testing.T) {
	for _, phase := range []string{"headers", "body"} {
		t.Run(phase, func(t *testing.T) {
			release := make(chan struct{})
			started := make(chan struct{}, 2)
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				if phase == "body" {
					w.WriteHeader(http.StatusOK)
					w.(http.Flusher).Flush()
				}
				select {
				case started <- struct{}{}:
				default:
				}
				select {
				case <-r.Context().Done():
				case <-release:
				}
			}))
			defer server.Close()
			defer close(release)
			auth := bearerAuth(server.URL)
			creds := Credentials{"secret": "fake-secret"}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan *httperr.Error, 1)
			go func() { _, err := AcquireToken(ctx, auth, creds, server.Client()); done <- err }()
			<-started
			cancel()
			err := <-done
			require.NotNil(t, err)
			require.Equal(t, http.StatusRequestTimeout, err.Code())
			require.EqualError(t, err, "authentication failed: HTTP request canceled")
			_, err = AcquireToken(ctx, auth, creds, server.Client())
			require.NotNil(t, err)
			require.Equal(t, http.StatusRequestTimeout, err.Code())
			client := server.Client()
			client.Timeout = 30 * time.Millisecond
			_, err = AcquireToken(context.Background(), auth, creds, client)
			require.NotNil(t, err)
			require.Equal(t, http.StatusGatewayTimeout, err.Code())
			require.EqualError(t, err, "authentication failed: HTTP request timed out")
		})
	}
}

func TestAcquireBearerTokenValidatesBeforeFactory(t *testing.T) {
	_, _, err := AcquireBearerToken(nil, "/token", http.DefaultClient)
	require.NotNil(t, err)
	require.Equal(t, http.StatusBadRequest, err.Code())
	calls := 0
	factory := func() (*http.Request, *httperr.Error) { calls++; return nil, nil }
	for _, pointer := range []string{"", "not-a-pointer", "/invalid~2"} {
		_, _, err := AcquireBearerToken(factory, pointer, http.DefaultClient)
		require.NotNil(t, err)
		require.Equal(t, http.StatusBadRequest, err.Code())
	}
	_, _, err = AcquireBearerToken(factory, "/token", nil)
	require.NotNil(t, err)
	require.Equal(t, http.StatusBadRequest, err.Code())
	require.Zero(t, calls)
}
