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
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/mitchellh/mapstructure"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestAcquireTokenFromConfig(t *testing.T) {
	calls := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		user, secret, ok := r.BasicAuth()
		assert.True(t, ok)
		assert.Equal(t, "fake-client", user)
		assert.Equal(t, "fake-secret", secret)
		assert.NoError(t, r.ParseForm())
		assert.Equal(t, "api.read", r.Form.Get("scope"))
		assert.Equal(t, "client_credentials", r.Form.Get("grant_type"))
		assert.Len(t, r.PostForm, 2, "OAuth2 must not send unrelated credential fields")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"access_token":"fake-token","expires_in":3600}`)
	}))
	defer server.Close()
	input := []byte(fmt.Sprintf(`{"type":"oauth2","tokenRequest":{"tokenUrl":%q,"scopes":["api.read"]}}`, server.URL))
	decoders := []struct {
		name   string
		decode func([]byte, any) error
	}{
		{"json", json.Unmarshal},
		{"yaml", yaml.Unmarshal},
		{"mapstructure", func(data []byte, output any) error {
			var values map[string]any
			if err := json.Unmarshal(data, &values); err != nil {
				return err
			}
			decoder, err := mapstructure.NewDecoder(&mapstructure.DecoderConfig{Result: output, ErrorUnused: true})
			if err != nil {
				return err
			}
			return decoder.Decode(values)
		}},
	}
	for _, decoder := range decoders {
		t.Run(decoder.name, func(t *testing.T) {
			var auth AuthConfig
			require.NoError(t, decoder.decode(input, &auth))
			var creds Credentials
			require.NoError(t, decoder.decode([]byte(`{"clientId":"fake-client","clientSecret":"fake-secret","username":"fake-user","password":"fake-password","apiKey":"fake-api-key","tenantCode":"fake-tenant","customLogin":"fake-login"}`), &creds))
			require.Equal(t, "fake-tenant", creds["tenantCode"])
			require.Equal(t, "fake-login", creds["customLogin"])
			encodedCreds, err := json.Marshal(creds)
			require.Nil(t, err)
			require.Contains(t, string(encodedCreds), `"tenantCode":"fake-tenant"`)
			for range 2 {
				before := calls
				token, err := AcquireToken(context.Background(), auth, creds, server.Client())
				require.Nil(t, err)
				require.Equal(t, "fake-token", token)
				require.Equal(t, before+1, calls, "one-shot acquisition must not share a cache between calls")
			}
			encoded, err := json.Marshal(auth)
			require.Nil(t, err)
			require.JSONEq(t, string(input), string(encoded))
			require.NotContains(t, string(encoded), "fake-secret")
		})
	}
}

func TestAcquireTokenInvalidAuth(t *testing.T) {
	for _, auth := range []AuthConfig{
		{}, {Type: "oauth2"}, {Type: "bearer", TokenRequest: &TokenRequestConfig{TokenURL: "https://example.invalid"}},
		{Type: "oauth2", HeaderName: "X-API-Key", TokenRequest: &TokenRequestConfig{TokenURL: "https://example.invalid"}},
		{Type: "oauth2", TokenRequest: &TokenRequestConfig{TokenURL: "https://example.invalid", URL: "https://example.invalid/login"}},
		{Type: "oauth2", TokenRequest: &TokenRequestConfig{TokenURL: "https://example.invalid", Method: "POST"}},
		{Type: "oauth2", TokenRequest: &TokenRequestConfig{TokenURL: "https://example.invalid", Encoding: "json"}},
		{Type: "oauth2", TokenRequest: &TokenRequestConfig{TokenURL: "https://example.invalid", TokenPointer: "/token"}},
	} {
		token, err := AcquireToken(context.Background(), auth, fakeCredentials(), &http.Client{})
		require.NotNil(t, err)
		require.Equal(t, http.StatusBadRequest, err.Code())
		require.Empty(t, token)
	}
}

func TestAcquireTokenFailure(t *testing.T) {
	calls := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, "fake-secret")
	}))
	defer server.Close()
	auth := AuthConfig{Type: "oauth2", TokenRequest: &TokenRequestConfig{TokenURL: server.URL}}
	token, err := AcquireToken(context.Background(), auth, fakeCredentials(), server.Client())
	require.Empty(t, token)
	require.EqualError(t, err, "OAuth2 token acquisition failed")
	require.Equal(t, http.StatusUnauthorized, err.Code())
	require.Equal(t, 1, calls)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	token, err = AcquireToken(ctx, auth, fakeCredentials(), server.Client())
	require.Empty(t, token)
	require.Equal(t, http.StatusRequestTimeout, err.Code())
	require.EqualError(t, err, "OAuth2 token request canceled")
	require.Equal(t, 1, calls)
}
