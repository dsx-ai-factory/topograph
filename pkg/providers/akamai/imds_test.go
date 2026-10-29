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

package akamai

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dsx-ai-factory/topograph/pkg/topology"
)

const imdsInstanceExample = `{
  "account_euuid": "fake-account",
  "backups": {"enabled": false},
  "host_uuid": "fake-host-uuid",
  "id": 12345,
  "image": {"id": "linode/ubuntu24.04", "label": "Ubuntu 24.04 LTS"},
  "label": "example-instance",
  "region": "us-iad",
  "specs": {"disk": 51200, "gpus": 0, "memory": 2048, "transfer": 2000, "vcpus": 1},
  "type": "g6-standard-1"
}`

type imdsRoundTripper func(*http.Request) (*http.Response, error)

func (f imdsRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// stubIMDS replaces the default transport, which providers.HttpReq uses, for the test.
func stubIMDS(t *testing.T, f func(*http.Request) (int, string)) {
	t.Helper()
	orig := http.DefaultTransport
	http.DefaultTransport = imdsRoundTripper(func(r *http.Request) (*http.Response, error) {
		status, body := f(r)
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}, Request: r}, nil
	})
	t.Cleanup(func() { http.DefaultTransport = orig })
}

func TestIMDSEndpoints(t *testing.T) {
	calls := 0
	stubIMDS(t, func(r *http.Request) (int, string) {
		calls++
		if calls == 1 {
			assert.Equal(t, "text/plain", r.Header.Get("Accept"))
			assert.Equal(t, IMDSTokenURL, r.URL.String())
			assert.Equal(t, http.MethodPut, r.Method)
			assert.Equal(t, "3600", r.Header.Get("Metadata-Token-Expiry-Seconds"))
			assert.Empty(t, r.Header.Get("Metadata-Token"))
			return http.StatusOK, "fake-imds-token\n"
		}
		assert.Equal(t, "application/json", r.Header.Get("Accept"))
		assert.Equal(t, IMDSInstanceURL, r.URL.String())
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, "fake-imds-token", r.Header.Get("Metadata-Token"))
		return http.StatusOK, imdsInstanceExample
	})
	annotations, err := GetNodeAnnotations(context.Background(), nil)
	require.NoError(t, err)
	require.Equal(t, 2, calls)
	require.Equal(t, map[string]string{topology.KeyNodeInstance: "fake-host-uuid", topology.KeyNodeRegion: "us-iad"}, annotations)
}

func TestIMDSRequiredFields(t *testing.T) {
	for _, tt := range []struct{ name, body, wantErr string }{
		{"empty", "", "failed to parse"}, {"malformed", "not JSON", "failed to parse"},
		{"null", "null", "host_uuid"}, {"array", "[]", "failed to parse"},
		{"numeric ID only", `{"id":12345,"region":"us-iad"}`, "host_uuid"},
		{"blank host", `{"host_uuid":" ","region":"us-iad"}`, "host_uuid"},
		{"missing region", `{"host_uuid":"fake-host"}`, "region"},
		{"blank region", `{"host_uuid":"fake-host","region":" "}`, "region"},
		{"non-string host", `{"host_uuid":12345,"region":"us-iad"}`, "failed to parse"},
		{"non-string region", `{"host_uuid":"fake-host","region":12345}`, "failed to parse"},
		{"trailing JSON", `{"host_uuid":"fake-host","region":"us-iad"}{}`, "failed to parse"},
		{"trim whitespace", `{"host_uuid":" fake-host ","region":" us-iad "}`, ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			stubIMDS(t, func(r *http.Request) (int, string) {
				if r.Method == http.MethodPut {
					return http.StatusOK, "fake-token"
				}
				return http.StatusOK, tt.body
			})
			annotations, err := GetNodeAnnotations(context.Background(), nil)
			if tt.wantErr != "" {
				require.Nil(t, annotations)
				require.ErrorContains(t, err, tt.wantErr)
			} else {
				require.NoError(t, err)
				require.Equal(t, map[string]string{topology.KeyNodeInstance: "fake-host", topology.KeyNodeRegion: "us-iad"}, annotations)
			}
		})
	}
}

func TestIMDSFailures(t *testing.T) {
	for _, operation := range []string{"token", "instance"} {
		for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
			t.Run(operation+http.StatusText(status), func(t *testing.T) {
				stubIMDS(t, func(r *http.Request) (int, string) {
					if operation == "instance" && r.Method == http.MethodPut {
						return http.StatusOK, "fake-token"
					}
					return status, "upstream failure"
				})
				annotations, err := GetNodeAnnotations(context.Background(), nil)
				require.Nil(t, annotations)
				require.ErrorContains(t, err, operation)
			})
		}
	}
	for _, token := range []string{"", " \n", "fake-token\nother-line"} {
		t.Run("invalid token "+token, func(t *testing.T) {
			stubIMDS(t, func(_ *http.Request) (int, string) { return http.StatusOK, token })
			annotations, err := GetNodeAnnotations(context.Background(), nil)
			require.Nil(t, annotations)
			require.EqualError(t, err, "invalid Akamai IMDS token response")
		})
	}
}

func TestPdshCmd(t *testing.T) {
	cmd := pdshCmd()
	assert.Contains(t, cmd, "-X PUT")
	assert.Contains(t, cmd, "'http://169.254.169.254/v1/token'")
	assert.Contains(t, cmd, "'http://169.254.169.254/v1/instance'")
	assert.Equal(t, `'it'\''s'`, shellQuote("it's"))
}

func TestParseIMDSOutput(t *testing.T) {
	out := bytes.NewBufferString(strings.Join([]string{
		`node1: {"host_uuid":"uuid1","region":"us-ord"}`,
		`node2: {"host_uuid":"uuid2","region":"us-sea"}`,
		`node3: {"host_uuid":"","region":"us-ord"}`,
		`node4: not-json`,
		`pdsh noise without separator`,
	}, "\n"))
	meta, err := parseIMDSOutput(out)
	require.NoError(t, err)
	require.Len(t, meta, 2)
	assert.Equal(t, "uuid1", meta["node1"].HostUUID)
	assert.Equal(t, "us-sea", meta["node2"].Region)
}

func TestGetNodeInstances(t *testing.T) {
	cis := getNodeInstances(map[string]*imdsMetadata{
		"node1": {HostUUID: "uuid1", Region: "us-ord"},
		"node2": {HostUUID: "uuid2", Region: "us-ord"},
		"node3": {HostUUID: "uuid3", Region: "us-sea"},
	})
	got := map[string]map[string]string{}
	for _, ci := range cis {
		got[ci.Region] = ci.Instances
	}
	assert.Equal(t, map[string]map[string]string{
		"us-ord": {"uuid1": "node1", "uuid2": "node2"},
		"us-sea": {"uuid3": "node3"},
	}, got)
}
