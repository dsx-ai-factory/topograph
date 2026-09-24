/*
 * Copyright 2025 NVIDIA CORPORATION
 * SPDX-License-Identifier: Apache-2.0
 */

package nebius

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/dsx-ai-factory/topograph/pkg/providers"
	"github.com/dsx-ai-factory/topograph/pkg/topology"
)

func TestGetAuthOption(t *testing.T) {
	tests := []struct {
		name           string
		creds          *credentials
		env            string
		metadataStatus int
		metadataBody   string
		metadataCalls  int
		err            string
	}{
		{
			name: "Case 1.1: no publicKeyID or privateKey in creds",
			creds: &credentials{
				ServiceAccountID: "service-account",
			},
			err: "credentials error: missing publicKeyId,privateKey",
		},
		{
			name: "Case 1.2: no privateKey in creds",
			creds: &credentials{
				ServiceAccountID: "service-account",
				PublicKeyID:      "data",
			},
			err: "credentials error: missing privateKey",
		},
		{
			name: "Case 1.3: valid service account creds",
			creds: &credentials{
				ServiceAccountID: "service-account",
				PublicKeyID:      "id",
				PrivateKey:       "key",
			},
		},
		{
			name: "Case 2.1: valid env var",
			env:  "data",
		},
		{
			name: "Case 2.2: project ID only uses env var auth",
			creds: &credentials{
				ProjectID: "project",
			},
			env: "data",
		},
		{
			name: "Case 3.1: project ID only uses IMDS auth",
			creds: &credentials{
				ProjectID: "project",
			},
			metadataCalls: 1,
		},
		{
			name:          "Case 3.2: empty creds use IMDS auth",
			creds:         &credentials{},
			metadataCalls: 1,
		},
		{
			name:          "Case 3.3: nil creds use IMDS auth",
			metadataCalls: 1,
		},
		{
			name:           "Case 3.4: IMDS auth failure",
			metadataStatus: http.StatusInternalServerError,
			metadataBody:   "metadata unavailable",
			metadataCalls:  5,
			err:            "failed to get IAM token from IMDS: metadata unavailable",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(authTokenEnvVar, tt.env)
			metadataCalls := installMetadataTokenTransport(t, tt.metadataStatus, tt.metadataBody)

			_, err := getAuthOption(context.Background(), tt.creds)
			if len(tt.err) != 0 {
				require.EqualError(t, err, tt.err)
			} else {
				require.Nil(t, err)
			}
			require.Equal(t, tt.metadataCalls, *metadataCalls)
		})
	}
}

func installMetadataTokenTransport(t *testing.T, status int, body string) *int {
	t.Helper()

	if status == 0 {
		status = http.StatusOK
	}
	if body == "" {
		body = "metadata-token"
	}

	calls := 0
	oldTransport := http.DefaultTransport
	http.DefaultTransport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		require.Equal(t, http.MethodGet, req.Method)
		require.Equal(t, IMDSTokenURL, req.URL.String())
		require.Equal(t, IMDSHeaderVal, req.Header.Get(IMDSHeaderKey))

		header := make(http.Header)
		if status < http.StatusOK || status >= http.StatusMultipleChoices {
			header.Set("Retry-After", "0")
		}

		return &http.Response{
			StatusCode: status,
			Status:     fmt.Sprintf("%d %s", status, http.StatusText(status)),
			Body:       io.NopCloser(strings.NewReader(body)),
			Header:     header,
			Request:    req,
		}, nil
	})
	t.Cleanup(func() {
		http.DefaultTransport = oldTransport
	})

	return &calls
}

func TestGetNodeAnnotations(t *testing.T) {
	calls := 0
	oldTransport := http.DefaultTransport
	http.DefaultTransport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		require.Equal(t, http.MethodGet, req.Method)
		require.Equal(t, IMDSInstanceURL, req.URL.String())
		require.Equal(t, IMDSHeaderVal, req.Header.Get(IMDSHeaderKey))

		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     fmt.Sprintf("%d %s", http.StatusOK, http.StatusText(http.StatusOK)),
			Body: io.NopCloser(strings.NewReader(`{
				"id": "computeinstance-123",
				"parent_id": "project-123",
				"region": "eu-north0"
			}`)),
			Header:  make(http.Header),
			Request: req,
		}, nil
	})
	t.Cleanup(func() {
		http.DefaultTransport = oldTransport
	})

	annotations, err := GetNodeAnnotations(context.Background())
	require.NoError(t, err)
	require.Equal(t, map[string]string{
		topology.KeyNodeInstance: "computeinstance-123",
		topology.KeyNodeRegion:   "eu-north0",
	}, annotations)
	require.Equal(t, 1, calls)
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestDecodeCredentials(t *testing.T) {
	tests := []struct {
		name     string
		input    map[string]any
		expected *credentials
		err      string
	}{
		{
			name:     "Case 1: empty creds",
			expected: &credentials{},
		},
		{
			name:     "Case 2: project ID only",
			input:    map[string]any{"projectId": "project"},
			expected: &credentials{ProjectID: "project"},
		},
		{
			name: "Case 3: service account creds",
			input: map[string]any{
				"projectId":        "project",
				"serviceAccountId": "service-account",
				"publicKeyId":      "public-key",
				"privateKey":       "private-key",
			},
			expected: &credentials{
				ProjectID:        "project",
				ServiceAccountID: "service-account",
				PublicKeyID:      "public-key",
				PrivateKey:       "private-key",
			},
		},
		{
			name:  "Case 4: invalid privateKey type",
			input: map[string]any{"privateKey": false},
			err:   "* 'privateKey' expected type 'string'",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			creds, err := decodeCredentials(tt.input)
			if len(tt.err) != 0 {
				require.ErrorContains(t, err, tt.err)
			} else {
				require.NoError(t, err)
				require.Equal(t, tt.expected, creds)
			}
		})
	}
}

func TestLoaderProjectIDOnlyUsesEnvAuth(t *testing.T) {
	t.Setenv(authTokenEnvVar, "data")

	provider, httpErr := Loader(context.Background(), providers.Config{
		Creds: map[string]any{"projectId": "project"},
	})
	require.Nil(t, httpErr)

	p, ok := provider.(*Provider)
	require.True(t, ok)

	client, err := p.clientFactory(nil)
	require.NoError(t, err)
	require.Equal(t, "project", client.ProjectID())
	require.Equal(t, int64(defaultPageSize), client.PageSize())
}

func TestImdsCmd(t *testing.T) {
	expected := fmt.Sprintf(`v=$(curl -fsS -H "Metadata: true" %s) && printf '%%s\n' "$v"`, IMDSRegionURL)
	require.Equal(t, expected, imdsCmd(IMDSRegionURL))
}

func TestGetUserAgentPrefix(t *testing.T) {
	tests := []struct {
		name     string
		version  string
		expected string
	}{
		{
			name:     "Case 1: empty version",
			version:  "",
			expected: userAgentProduct,
		},
		{
			name:     "Case 2: whitespace version",
			version:  "   ",
			expected: userAgentProduct,
		},
		{
			name:     "Case 3: non-empty version",
			version:  "main",
			expected: "nvidia-topograph/main",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.expected, getUserAgentPrefix(tt.version))
		})
	}
}
