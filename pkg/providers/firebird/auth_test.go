/*
 * Copyright 2026 NVIDIA CORPORATION
 * SPDX-License-Identifier: Apache-2.0
 */

package firebird

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dsx-ai-factory/topograph/pkg/providers"
	"github.com/dsx-ai-factory/topograph/pkg/topology"
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

func TestTokenCacheAndExpiry(t *testing.T) {
	var calls atomic.Int32
	server := newTrustedTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/api/auth/token", r.URL.Path)
		require.Equal(t, http.MethodPost, r.Method)
		require.Equal(t, "application/json", r.Header.Get("Content-Type"))
		require.Empty(t, r.Header.Get("Authorization"))
		var credentials map[string]string
		require.NoError(t, json.NewDecoder(r.Body).Decode(&credentials))
		require.Equal(t, map[string]string{"clientId": "fake-client-id", "clientSecret": "fake-client-secret"}, credentials)
		calls.Add(1)
		_, _ = w.Write([]byte(`{"accessToken":"fake-access-token","expiresIn":3600,"tokenType":"Bearer","idToken":"fake-id-token","refreshToken":"fake-refresh-token"}`))
	}))
	loaded, err := Loader(context.Background(), providers.Config{Params: map[string]any{"apiUrl": server.URL + "/api"}, Creds: fakeCredentials()})
	require.Nil(t, err)
	p := loaded.(*Provider)
	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() {
			token, err := p.accessToken(context.Background())
			require.Nil(t, err)
			require.Equal(t, "fake-access-token", token)
		})
	}
	wg.Wait()
	require.EqualValues(t, 1, calls.Load())
	p.tokenExpiresAt = time.Now().Add(-time.Second)
	_, err = p.accessToken(context.Background())
	require.Nil(t, err)
	require.EqualValues(t, 2, calls.Load())
	// A stale rejection must not invalidate a newer token.
	require.Nil(t, p.invalidateToken(context.Background(), "fake-old-token"))
	require.Equal(t, "fake-access-token", p.token)
}

func TestAuthFailures(t *testing.T) {
	for _, status := range []int{400, 401, 403} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			calls := 0
			server := newTrustedTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				require.Equal(t, "/auth/token", r.URL.Path)
				w.WriteHeader(status)
				_, _ = w.Write([]byte(`{"error":{"code":"AUTH_FAILED","message":"request rejected","details":{"tenant":"unverified"}}}`))
			}))
			p, err := Loader(context.Background(), providers.Config{Params: map[string]any{"apiUrl": server.URL}, Creds: fakeCredentials()})
			require.Nil(t, err)
			graph, err := p.GenerateTopologyConfig(context.Background(), nil, []topology.ComputeInstances{{Instances: map[string]string{"n1": "worker"}}})
			require.Nil(t, graph)
			require.Equal(t, status, err.Code())
			require.Equal(t, `Firebird authentication request failed: AUTH_FAILED: request rejected (details: {"tenant":"unverified"})`, err.Error())
			require.Equal(t, 1, calls)
		})
	}
}

func TestInvalidTokenResponses(t *testing.T) {
	for _, body := range []string{
		`invalid`, `null`, `{}`, `{"accessToken":""}`, `{"accessToken":"fake","expiresIn":-1}`,
		`{"accessToken":"fake","expiresIn":"bad"}`, `{"accessToken":"fake","expiresIn":9223372036854775807}`,
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

func TestTokenWithoutLifetimeIsNotCached(t *testing.T) {
	calls := 0
	server := newTrustedTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		_, _ = w.Write([]byte(`{"accessToken":"fake-token","expiresIn":0}`))
	}))
	p, err := Loader(context.Background(), providers.Config{Params: map[string]any{"apiUrl": server.URL}, Creds: fakeCredentials()})
	require.Nil(t, err)
	for range 2 {
		token, err := p.(*Provider).accessToken(context.Background())
		require.Nil(t, err)
		require.Equal(t, "fake-token", token)
	}
	require.Equal(t, 2, calls)
}

func TestTopologyReauthentication(t *testing.T) {
	for _, rejectAll := range []bool{false, true} {
		t.Run(fmt.Sprint(rejectAll), func(t *testing.T) {
			authCalls, topologyCalls := 0, 0
			server := newTrustedTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/auth/token" {
					authCalls++
					_, _ = fmt.Fprintf(w, `{"accessToken":"fake-token-%d","expiresIn":3600,"tokenType":"Bearer"}`, authCalls)
					return
				}
				require.Equal(t, "/topology/nodes", r.URL.Path)
				topologyCalls++
				if topologyCalls == 1 || rejectAll {
					w.WriteHeader(http.StatusUnauthorized)
					_, _ = w.Write([]byte(`{"error":{"code":"TOKEN_INVALID","message":"invalid token"}}`))
					return
				}
				require.Equal(t, "Bearer fake-token-2", r.Header.Get("Authorization"))
				_, _ = w.Write([]byte(`{"nodes":[{"nodeId":"n1","networkPath":["leaf"]}]}`))
			}))
			p, err := Loader(context.Background(), providers.Config{Params: map[string]any{"apiUrl": server.URL}, Creds: fakeCredentials()})
			require.Nil(t, err)
			graph, err := p.GenerateTopologyConfig(context.Background(), nil, []topology.ComputeInstances{{Instances: map[string]string{"n1": "worker"}}})
			if rejectAll {
				require.Nil(t, graph)
				require.Equal(t, http.StatusUnauthorized, err.Code())
			} else {
				require.Nil(t, err)
				require.Len(t, graph.Instances, 1)
			}
			require.Equal(t, 2, authCalls)
			require.Equal(t, 2, topologyCalls)
		})
	}
}

func TestWaitingForAuthCanCancel(t *testing.T) {
	p := &Provider{authGate: make(chan struct{}, 1)}
	p.authGate <- struct{}{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := p.accessToken(ctx)
	require.Equal(t, http.StatusBadGateway, err.Code())
	require.ErrorContains(t, err, "canceled")
}
