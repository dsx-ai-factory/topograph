/*
 * Copyright 2026, NVIDIA CORPORATION
 * SPDX-License-Identifier: Apache-2.0
 */
package lambdai

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/dsx-ai-factory/topograph/pkg/engines/slurm"
	"github.com/dsx-ai-factory/topograph/pkg/providers"
	"github.com/dsx-ai-factory/topograph/pkg/topology"
)

// The exact operator-facing messages, spelled out rather than built from the
// production format strings: the wording is the contract being tested.
const (
	errMissingWorkspace = "credentials error: missing 'workspaceId': supply the Lambda workspace ID in the " +
		"request credentials or the credentialsPath file"
	errNoAuth = "credentials error: missing 'token' credential: supply the Lambda API token in the " +
		"request credentials or the credentialsPath file; to authenticate with Kubernetes workload identity " +
		"instead, the pod needs LAMBDA_IDENTITY_LRN, which lambda-pod-identity-webhook injects when the " +
		"API-server ServiceAccount carries the 'lambda.ai/identity-lrn' annotation"
	errBlankToken   = "credentials error: empty 'token' credential"
	errBlankTokenWI = "credentials error: empty 'token' credential; omit it entirely to use the pod's workload identity"
)

// TestLoader covers credential and parameter handling: which authentication mode
// Loader selects, the workspace it resolves and hands the client, and the error
// each misconfiguration reports.
func TestLoader(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name        string
		identityLRN string // LAMBDA_IDENTITY_LRN; non-empty selects workload-identity mode
		config      providers.Config
		err         string
		wantWI      bool   // expect the workload-identity credential, not a static token
		wantToken   string // when static, the exact token the client must be given

		// the exact workspace the client must query, once resolved and trimmed
		wantWorkspaceID string
	}{
		{
			name: "Case 1: success",
			config: providers.Config{
				Creds: map[string]any{
					"workspaceId": "workspace-123",
					"token":       "token-abc",
				},
				Params: map[string]any{
					"url": "https://api.example.com",
				},
			},
		},
		{
			name: "Case 2: missing workspaceID",
			config: providers.Config{
				Creds: map[string]any{
					"token": "token-abc",
				},
				Params: map[string]any{
					"url": "https://api.example.com",
				},
			},
			err: errMissingWorkspace,
		},
		{
			name: "Case 3: missing token",
			config: providers.Config{
				Creds: map[string]any{
					"workspaceId": "workspace-123",
				},
				Params: map[string]any{
					"url": "https://api.example.com",
				},
			},
			err: errNoAuth,
		},
		{
			name: "Case 4: missing baseURL",
			config: providers.Config{
				Creds: map[string]any{
					"workspaceId": "workspace-123",
					"token":       "token-abc",
				},
			},
			err: "parameters error: missing 'url'",
		},
		{
			name: "Case 5: invalid trimTiers",
			config: providers.Config{
				Creds: map[string]any{
					"workspaceId": "workspace-123",
					"token":       "token-abc",
				},
				Params: map[string]any{
					"url":                 "https://api.example.com",
					topology.KeyTrimTiers: false,
				},
			},
			err: "parameters error: invalid 'trimTiers' value 'false': unsupported type bool",
		},
		{
			name: "Case 6: empty baseURL",
			config: providers.Config{
				Creds: map[string]any{
					"workspaceId": "workspace-123",
					"token":       "token-abc",
				},
				Params: map[string]any{
					"url": "",
				},
			},
			err: "parameters error: missing 'url'",
		},
		{
			name:        "Case 8: workload identity missing workspaceId",
			identityLRN: "lrn:iam:identity:abc",
			config: providers.Config{
				Params: map[string]any{
					"url": "https://api.example.com",
				},
			},
			err: errMissingWorkspace,
		},
		{
			name:        "Case 9: supplied token wins over the ambient workload identity",
			identityLRN: "lrn:iam:identity:abc",
			config: providers.Config{
				Creds: map[string]any{
					"workspaceId": "workspace-123",
					"token":       "token-abc",
				},
				Params: map[string]any{
					"url": "https://api.example.com",
				},
			},
		},
		{
			// A workspaceId credential alone is not a token, so the ambient
			// identity still applies rather than failing on the missing token.
			name:        "Case 10: workspaceId cred with workload identity stays in WI mode",
			identityLRN: "lrn:iam:identity:abc",
			config: providers.Config{
				Creds: map[string]any{
					"workspaceId": "workspace-123",
				},
				Params: map[string]any{
					"url": "https://api.example.com",
				},
			},
			wantWI: true,
		},
		{
			// Falling back to the static path with no token must still report the
			// original credential error rather than silently using the pod identity.
			name: "Case 11: no token and no workload identity still errors",
			config: providers.Config{
				Creds: map[string]any{
					"workspaceId": "workspace-123",
				},
				Params: map[string]any{
					"url": "https://api.example.com",
				},
			},
			err: errNoAuth,
		},
		{
			// Security: an explicitly supplied but malformed token must be
			// rejected, never silently downgraded to the pod identity.
			name:        "Case 12: empty token with workload identity is rejected",
			identityLRN: "lrn:iam:identity:abc",
			config: providers.Config{
				Creds: map[string]any{
					"workspaceId": "workspace-123",
					"token":       "",
				},
				Params: map[string]any{
					"url": "https://api.example.com",
				},
			},
			err: errBlankTokenWI,
		},
		{
			name:        "Case 13: nil token with workload identity is rejected",
			identityLRN: "lrn:iam:identity:abc",
			config: providers.Config{
				Creds: map[string]any{
					"workspaceId": "workspace-123",
					"token":       nil,
				},
				Params: map[string]any{
					"url": "https://api.example.com",
				},
			},
			err: errBlankTokenWI,
		},
		{
			name:        "Case 14: whitespace-only token with workload identity is rejected",
			identityLRN: "lrn:iam:identity:abc",
			config: providers.Config{
				Creds: map[string]any{
					"workspaceId": "workspace-123",
					"token":       "   ",
				},
				Params: map[string]any{
					"url": "https://api.example.com",
				},
			},
			err: errBlankTokenWI,
		},
		{
			// Without workload identity an empty token previously passed
			// validation and produced a credential-less "Bearer " header.
			name: "Case 15: empty token without workload identity is rejected",
			config: providers.Config{
				Creds: map[string]any{
					"workspaceId": "workspace-123",
					"token":       "",
				},
				Params: map[string]any{
					"url": "https://api.example.com",
				},
			},
			err: errBlankToken,
		},
		{
			name:        "Case 16: empty workspaceId credential is rejected",
			identityLRN: "",
			config: providers.Config{
				Creds: map[string]any{
					"workspaceId": "",
					"token":       "token-abc",
				},
				Params: map[string]any{
					"url": "https://api.example.com",
				},
			},
			err: errMissingWorkspace,
		},
		{
			// A whitespace-only workspace is unusable: sending it as the
			// workspace_id query value queries no workspace at all.
			name: "Case 16b: whitespace-only workspaceId credential is rejected",
			config: providers.Config{
				Creds: map[string]any{
					"workspaceId": "   ",
					"token":       "token-abc",
				},
				Params: map[string]any{
					"url": "https://api.example.com",
				},
			},
			err: errMissingWorkspace,
		},
		{
			name: "Case 16e: surrounding whitespace is trimmed off the workspaceId",
			config: providers.Config{
				Creds: map[string]any{
					"workspaceId": "  workspace-123\n",
					"token":       "token-abc",
				},
				Params: map[string]any{
					"url": "https://api.example.com",
				},
			},
			wantToken:       "token-abc",
			wantWorkspaceID: "workspace-123",
		},
		{
			// Security: mapstructure matches credential keys case-insensitively, so
			// mode selection must too. A case variant that populates the token must
			// not be treated as absent and downgraded to the pod identity.
			name:        "Case 17: case-variant token with workload identity is still used",
			identityLRN: "lrn:iam:identity:abc",
			config: providers.Config{
				Creds: map[string]any{
					"WorkspaceId": "workspace-123",
					"Token":       "token-abc",
				},
				Params: map[string]any{
					"url": "https://api.example.com",
				},
			},
			wantToken: "token-abc",
		},
		{
			name:        "Case 18: blank case-variant token with workload identity is rejected",
			identityLRN: "lrn:iam:identity:abc",
			config: providers.Config{
				Creds: map[string]any{
					"workspaceId": "workspace-123",
					"TOKEN":       "  ",
				},
				Params: map[string]any{
					"url": "https://api.example.com",
				},
			},
			err: errBlankTokenWI,
		},
		{
			// Security: duplicate case-insensitive spellings both feed the same
			// decoded field, and randomized map iteration picks the winner, so the
			// ambiguity is reported rather than resolved arbitrarily.
			name:        "Case 19: duplicate token spellings are rejected",
			identityLRN: "lrn:iam:identity:abc",
			config: providers.Config{
				Creds: map[string]any{
					"workspaceId": "workspace-123",
					"token":       "token-abc",
					"Token":       "token-xyz",
				},
				Params: map[string]any{
					"url": "https://api.example.com",
				},
			},
			err: "credentials error: ambiguous 'token' credential: Token, token",
		},
		{
			name: "Case 20: duplicate workspaceId spellings are rejected",
			config: providers.Config{
				Creds: map[string]any{
					"workspaceId": "workspace-123",
					"WorkspaceID": "workspace-999",
					"token":       "token-abc",
				},
				Params: map[string]any{
					"url": "https://api.example.com",
				},
			},
			err: "credentials error: ambiguous 'workspaceId' credential: WorkspaceID, workspaceId",
		},
		{
			// An ambiguous url picks the API host the same way.
			name: "Case 22: duplicate url parameter spellings are rejected",
			config: providers.Config{
				Creds: map[string]any{"workspaceId": "workspace-123", "token": "token-abc"},
				Params: map[string]any{
					"Url": "https://a.example.com",
					"URL": "https://b.example.com",
				},
			},
			err: "parameters error: ambiguous 'url' parameter: URL, Url",
		},
		{
			// A single inexact spelling is unambiguous: mapstructure feeds it to
			// the same field, so it must keep working rather than be rejected
			// alongside the duplicates.
			name: "Case 23: a single case-variant spelling is accepted in either map",
			config: providers.Config{
				Creds: map[string]any{
					"WorkspaceId": "workspace-123",
					"token":       "token-abc",
				},
				Params: map[string]any{"URL": "https://api.example.com"},
			},
			wantToken:       "token-abc",
			wantWorkspaceID: "workspace-123",
		},
		{
			// The workspaceId parameter is gone. It must not quietly keep working,
			// or a config that relied on it would look correct while omitting the
			// credential the provider now requires.
			name: "Case 24: a workspaceId parameter no longer supplies the workspace",
			config: providers.Config{
				Creds: map[string]any{"token": "token-abc"},
				Params: map[string]any{
					"url":         "https://api.example.com",
					"workspaceId": "workspace-123",
				},
			},
			err: errMissingWorkspace,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Mode is selected by env, not params: static cases pin both names
			// empty so ambient env can't flip them into workload-identity mode.
			t.Setenv(envIdentityLRN, tt.identityLRN)
			t.Setenv(envRoleLRN, "")
			resetCredentialCache()
			provider, err := Loader(ctx, tt.config)

			if len(tt.err) != 0 {
				require.Nil(t, provider)
				require.NotNil(t, err)
				require.Equal(t, http.StatusBadRequest, err.Code())
				require.Equal(t, tt.err, err.Error())
				return
			}

			require.Nil(t, err)
			require.NotNil(t, provider)

			if tt.wantWorkspaceID != "" {
				client, cerr := provider.(*Provider).clientFactory(nil)
				require.NoError(t, cerr)
				require.Equal(t, tt.wantWorkspaceID, client.WorkspaceID(),
					"the resolved workspace must reach the client verbatim")
			}

			// Assert which credential Loader actually wired in, not merely that
			// it built something: the static and workload-identity paths differ
			// only in the token source.
			tp := tokenProviderOf(t, provider)
			if tt.wantWI {
				require.IsType(t, &workloadIdentityCredential{}, tp, "expected the workload-identity credential")
			} else {
				require.IsType(t, staticToken(""), tp, "expected a static token")
				if tt.wantToken != "" {
					require.Equal(t, staticToken(tt.wantToken), tp,
						"the caller's token must be the one handed to the client")
				}
			}
		})
	}
}

// tokenProviderOf digs out the token source Loader wired into the client it
// builds, so tests can assert the selected authentication mode directly.
func tokenProviderOf(t *testing.T, p providers.Provider) tokenProvider {
	t.Helper()
	prv, ok := p.(*Provider)
	require.True(t, ok, "expected *Provider, got %T", p)
	client, err := prv.clientFactory(nil)
	require.NoError(t, err)
	lc, ok := client.(*lambdaiClient)
	require.True(t, ok, "expected *lambdaiClient, got %T", client)
	return lc.token
}

// TestGenerateTopologyConfig drives the real client against a mock of the Lambda
// topology API, asserting the verified request contract (path, region +
// workspace_id query, Bearer auth), the {data, page_token} response envelope,
// page_token pagination, and the networkPath -> leaf/spine/core mapping.
func TestGenerateTopologyConfig(t *testing.T) {
	ctx := context.Background()
	// static-token mode: no workload identity under either env var
	t.Setenv(envIdentityLRN, "")
	t.Setenv(envRoleLRN, "")

	const page1 = `{"data":[
		{"id":"i-1","networkPath":[{"id":"leaf1"},{"id":"spine1"},{"id":"core1"}],"nvlink":null},
		{"id":"i-2","networkPath":[{"id":"leaf1"},{"id":"spine1"},{"id":"core1"}],"nvlink":null}
	],"page_token":"page2"}`
	const page2 = `{"data":[
		{"id":"i-3","networkPath":[{"id":"leaf2"},{"id":"spine1"},{"id":"core1"}],"nvlink":null}
	],"page_token":null}`

	var paths, regions, workspaces, auths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodGet, r.Method)
		paths = append(paths, r.URL.Path)
		regions = append(regions, r.URL.Query().Get("region"))
		workspaces = append(workspaces, r.URL.Query().Get("workspace_id"))
		auths = append(auths, r.Header.Get("Authorization"))

		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("page_token") == "" {
			_, _ = w.Write([]byte(page1))
		} else {
			_, _ = w.Write([]byte(page2))
		}
	}))
	defer server.Close()

	provider, httpErr := Loader(ctx, providers.Config{
		Creds: map[string]any{
			authWorkspaceID: "ws-1",
			authToken:       "tok-1",
		},
		Params: map[string]any{
			apiBaseURL: server.URL,
		},
	})
	require.Nil(t, httpErr)

	graph, httpErr := provider.GenerateTopologyConfig(ctx, nil, []topology.ComputeInstances{
		{
			Region:    "stg-sjc01-cl03",
			Instances: map[string]string{"i-1": "node1", "i-2": "node2", "i-3": "node3"},
		},
	})
	require.Nil(t, httpErr)
	require.NotNil(t, graph)

	// Two calls: the second is driven by the page_token returned on page 1.
	require.Equal(t, []string{apiPath, apiPath}, paths)
	require.Equal(t, []string{"stg-sjc01-cl03", "stg-sjc01-cl03"}, regions)
	require.Equal(t, []string{"ws-1", "ws-1"}, workspaces)
	require.Equal(t, []string{"Bearer tok-1", "Bearer tok-1"}, auths)

	// Both pages merged; networkPath objects mapped into the switch hierarchy.
	out, httpErr := slurm.GenerateOutput(ctx, graph, nil)
	require.Nil(t, httpErr)
	require.Equal(t, `SwitchName=core1 Switches=spine1
SwitchName=spine1 Switches=leaf[1-2]
SwitchName=leaf1 Nodes=node[1-2]
SwitchName=leaf2 Nodes=node3
`, string(out))
}

// TestGenerateTopologyConfigWorkloadIdentity drives the workload-identity path
// end to end against a single mock server that answers both the OIDC
// token-exchange and the topology endpoint. The webhook model is simulated by
// setting the LAMBDA_IDENTITY_LRN and LAMBDA_WORKLOAD_IDENTITY_TOKEN_FILE env vars;
// the test asserts that the provider reads the projected ServiceAccount token,
// exchanges it once, and uses the minted key as the Bearer credential on the
// topology request. No token or workspaceId credential is supplied.
func TestGenerateTopologyConfigWorkloadIdentity(t *testing.T) {
	ctx := context.Background()
	resetCredentialCache()

	saTokenPath := writeToken(t, "sa-jwt-value\n")
	t.Setenv(envIdentityLRN, "lrn:iam:identity:abc")
	t.Setenv(envRoleLRN, "")
	t.Setenv(envTokenFile, saTokenPath)

	const topoPage = `{"data":[
		{"id":"i-1","networkPath":[{"id":"leaf1"},{"id":"spine1"}],"nvlink":null}
	],"page_token":null}`

	var exchangeHits, topoHits int
	var topoAuths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case oidcTokenPath:
			exchangeHits++
			require.Equal(t, http.MethodPost, r.Method)
			var req exchangeRequest
			require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
			require.Equal(t, "sa-jwt-value", req.Token)
			require.Equal(t, "lrn:iam:identity:abc", req.IdentityLRN)
			_, _ = w.Write([]byte(`{"data":{"access_token":"minted-key","expires_in":3600}}`))
		case apiPath:
			topoHits++
			topoAuths = append(topoAuths, r.Header.Get("Authorization"))
			_, _ = w.Write([]byte(topoPage))
		default:
			t.Errorf("unexpected request path %q", r.URL.Path)
		}
	}))
	defer server.Close()

	provider, httpErr := Loader(ctx, providers.Config{
		// workspaceId is a credential; naming no token keeps this in
		// workload-identity mode.
		Creds:  map[string]any{authWorkspaceID: "ws-1"},
		Params: map[string]any{apiBaseURL: server.URL},
	})
	require.Nil(t, httpErr)

	graph, httpErr := provider.GenerateTopologyConfig(ctx, nil, []topology.ComputeInstances{
		{
			Region:    "stg-sjc01-cl03",
			Instances: map[string]string{"i-1": "node1"},
		},
	})
	require.Nil(t, httpErr)
	require.NotNil(t, graph)

	require.Equal(t, 1, exchangeHits)
	require.Equal(t, 1, topoHits)
	require.Equal(t, []string{"Bearer minted-key"}, topoAuths)
}

// TestInstanceListRetriesOnUnauthorized covers the workload-identity 401 retry:
// a minted key rejected mid-life is invalidated and re-exchanged, and the
// topology request is replayed once with the fresh key. This is the production
// path that reaches workloadIdentityCredential.InvalidateIfCurrent.
func TestInstanceListRetriesOnUnauthorized(t *testing.T) {
	ctx := context.Background()
	resetCredentialCache()

	saTokenPath := writeToken(t, "sa-jwt-value")
	t.Setenv(envIdentityLRN, "lrn:iam:identity:abc")
	t.Setenv(envRoleLRN, "")
	t.Setenv(envTokenFile, saTokenPath)

	const topoPage = `{"data":[
		{"id":"i-1","networkPath":[{"id":"leaf1"},{"id":"spine1"}],"nvlink":null}
	],"page_token":null}`

	var exchangeHits, topoHits int
	var topoAuths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case oidcTokenPath:
			exchangeHits++
			_, _ = fmt.Fprintf(w, `{"data":{"access_token":"minted-%d","expires_in":3600}}`, exchangeHits)
		case apiPath:
			topoHits++
			topoAuths = append(topoAuths, r.Header.Get("Authorization"))
			// The first minted key is rejected; the retry with a fresh key wins.
			if topoHits == 1 {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
				return
			}
			_, _ = w.Write([]byte(topoPage))
		default:
			t.Errorf("unexpected request path %q", r.URL.Path)
		}
	}))
	defer server.Close()

	provider, httpErr := Loader(ctx, providers.Config{
		// workspaceId is a credential; naming no token keeps this in
		// workload-identity mode.
		Creds:  map[string]any{authWorkspaceID: "ws-1"},
		Params: map[string]any{apiBaseURL: server.URL},
	})
	require.Nil(t, httpErr)

	graph, httpErr := provider.GenerateTopologyConfig(ctx, nil, []topology.ComputeInstances{
		{
			Region:    "stg-sjc01-cl03",
			Instances: map[string]string{"i-1": "node1"},
		},
	})
	require.Nil(t, httpErr)
	require.NotNil(t, graph)

	require.Equal(t, 2, exchangeHits, "the rejected key should be invalidated and re-exchanged")
	require.Equal(t, 2, topoHits, "the topology request should be replayed once")
	require.Equal(t, []string{"Bearer minted-1", "Bearer minted-2"}, topoAuths)
}

// TestSuppliedTokenWinsOverWorkloadIdentity asserts that a token supplied with
// the request is the one that reaches the wire even when the pod carries an
// ambient workload identity, and that no token exchange is attempted. Otherwise
// the request would silently authenticate as the pod principal.
func TestSuppliedTokenWinsOverWorkloadIdentity(t *testing.T) {
	ctx := context.Background()
	resetCredentialCache()

	// A full workload-identity environment is present and must be ignored.
	t.Setenv(envIdentityLRN, "lrn:iam:identity:abc")
	t.Setenv(envRoleLRN, "")
	t.Setenv(envTokenFile, writeToken(t, "sa-jwt-value"))

	const topoPage = `{"data":[
		{"id":"i-1","networkPath":[{"id":"leaf1"},{"id":"spine1"}],"nvlink":null}
	],"page_token":null}`

	var exchangeHits int
	var topoAuths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case oidcTokenPath:
			exchangeHits++
			_, _ = w.Write([]byte(`{"data":{"access_token":"minted-key","expires_in":3600}}`))
		case apiPath:
			topoAuths = append(topoAuths, r.Header.Get("Authorization"))
			_, _ = w.Write([]byte(topoPage))
		default:
			t.Errorf("unexpected request path %q", r.URL.Path)
		}
	}))
	defer server.Close()

	provider, httpErr := Loader(ctx, providers.Config{
		Creds: map[string]any{
			authWorkspaceID: "ws-1",
			authToken:       "static-tok",
		},
		Params: map[string]any{apiBaseURL: server.URL},
	})
	require.Nil(t, httpErr)

	graph, httpErr := provider.GenerateTopologyConfig(ctx, nil, []topology.ComputeInstances{
		{Region: "stg-sjc01-cl03", Instances: map[string]string{"i-1": "node1"}},
	})
	require.Nil(t, httpErr)
	require.NotNil(t, graph)

	require.Equal(t, []string{"Bearer static-tok"}, topoAuths, "the supplied token must be used")
	require.Equal(t, 0, exchangeHits, "no token exchange should be attempted")
}

// TestInstanceListStaleUnauthorizedKeepsRefreshedKey covers the race that
// compare-and-invalidate exists for: a request is rejected while holding key 1,
// but by the time its 401 comes back another request has already minted key 2.
// Invalidating on behalf of the stale key must leave key 2 intact, otherwise the
// valid replacement is wiped and every caller pays for a needless re-exchange.
//
// Ordering is deterministic without timers: the topology handler performs the
// competing refresh inline before returning the 401, so the replacement is
// guaranteed to be installed by the time InstanceList reacts to the rejection.
func TestInstanceListStaleUnauthorizedKeepsRefreshedKey(t *testing.T) {
	ctx := context.Background()
	tokenPath := writeToken(t, "sa-jwt")

	var (
		mu         sync.Mutex
		exchanges  int
		topoAuths  []string
		refreshTok string
		refreshErr error
	)
	var cred *workloadIdentityCredential

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case oidcTokenPath:
			mu.Lock()
			exchanges++
			n := exchanges
			mu.Unlock()
			_, _ = fmt.Fprintf(w, `{"data":{"access_token":"minted-%d","expires_in":3600}}`, n)
		case apiPath:
			auth := r.Header.Get("Authorization")
			mu.Lock()
			topoAuths = append(topoAuths, auth)
			mu.Unlock()

			if auth != "Bearer minted-1" {
				_, _ = w.Write([]byte(`{"data":[{"id":"i-1","networkPath":[{"id":"leaf1"}],"nvlink":null}],"page_token":null}`))
				return
			}
			// Stand in for a concurrent request that saw the 401 first and has
			// already replaced the rejected key with a freshly minted one.
			cred.InvalidateIfCurrent("minted-1")
			tok, herr := cred.Token(ctx)
			mu.Lock()
			refreshTok = tok
			if herr != nil {
				refreshErr = herr
			}
			mu.Unlock()

			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
		default:
			t.Errorf("unexpected request path %q", r.URL.Path)
		}
	}))
	defer server.Close()

	cred = testCredential(server.URL, tokenPath, func() time.Time { return time.Unix(1_000_000, 0) })
	client := &lambdaiClient{
		baseURL:     server.URL,
		token:       cred,
		workspaceID: "ws-1",
		pageSize:    defaultPageSize,
	}

	resp, err := client.InstanceList(ctx, &InstanceListRequest{Region: "r-1"})
	require.NoError(t, err, "the retry with the replacement key should succeed")
	require.Len(t, resp.Items, 1)

	mu.Lock()
	defer mu.Unlock()
	require.NoError(t, refreshErr)
	require.Equal(t, "minted-2", refreshTok, "the competing refresh should have installed key 2")
	require.Equal(t, []string{"Bearer minted-1", "Bearer minted-2"}, topoAuths,
		"the retry must use the replacement key")
	require.Equal(t, 2, exchanges,
		"only two exchanges: a third means the stale 401 wiped the replacement key")

	cred.mu.Lock()
	defer cred.mu.Unlock()
	require.NotNil(t, cred.cur, "the refreshed key must not have been cleared")
	require.Equal(t, "minted-2", cred.cur.accessToken)
}

// TestStaticTokenDoesNotRetryOnUnauthorized asserts a rejected static token is
// not re-minted or replayed -- only a workload-identity key is refreshable.
func TestStaticTokenDoesNotRetryOnUnauthorized(t *testing.T) {
	ctx := context.Background()
	t.Setenv(envIdentityLRN, "")
	t.Setenv(envRoleLRN, "")

	var topoHits int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, apiPath, r.URL.Path)
		topoHits++
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
	}))
	defer server.Close()

	provider, httpErr := Loader(ctx, providers.Config{
		Creds: map[string]any{
			authWorkspaceID: "ws-1",
			authToken:       "static-tok",
		},
		Params: map[string]any{apiBaseURL: server.URL},
	})
	require.Nil(t, httpErr)

	_, httpErr = provider.GenerateTopologyConfig(ctx, nil, []topology.ComputeInstances{
		{Region: "stg-sjc01-cl03", Instances: map[string]string{"i-1": "node1"}},
	})
	require.NotNil(t, httpErr)
	require.Equal(t, 1, topoHits, "a static token must not be replayed on 401")
}

// TestInjectedIdentityLRN covers the webhook rename: it injects
// LAMBDA_IDENTITY_LRN and, for the transition, the deprecated LAMBDA_ROLE_LRN
// alongside it. Reading the current name first keeps working once the webhook
// stops setting the old one, while the fallback keeps pods admitted by an older
// webhook authenticating.
func TestInjectedIdentityLRN(t *testing.T) {
	const (
		current    = "lrn:iam:identity:c0000000000000000000000000000000"
		deprecated = "lrn:iam:identity:d0000000000000000000000000000000"
	)

	tests := []struct {
		name                 string
		identityLRN, roleLRN string
		want                 string
	}{
		{name: "current name only", identityLRN: current, want: current},
		{name: "deprecated name only", roleLRN: deprecated, want: deprecated},
		{name: "both: current wins", identityLRN: current, roleLRN: deprecated, want: current},
		{name: "neither: no identity injected", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(envIdentityLRN, tt.identityLRN)
			t.Setenv(envRoleLRN, tt.roleLRN)

			require.Equal(t, tt.want, injectedIdentityLRN())
		})
	}
}

// TestLoaderDeprecatedRoleLRNEnv pins the fallback end to end: a pod carrying
// only the deprecated env var still selects workload-identity mode rather than
// failing on the missing token.
func TestLoaderDeprecatedRoleLRNEnv(t *testing.T) {
	t.Setenv(envIdentityLRN, "")
	t.Setenv(envRoleLRN, "lrn:iam:identity:abc")
	resetCredentialCache()

	provider, err := Loader(context.Background(), providers.Config{
		Creds:  map[string]any{"workspaceId": "workspace-123"},
		Params: map[string]any{"url": "https://api.example.com"},
	})
	require.Nil(t, err)
	require.IsType(t, &workloadIdentityCredential{}, tokenProviderOf(t, provider))
}
