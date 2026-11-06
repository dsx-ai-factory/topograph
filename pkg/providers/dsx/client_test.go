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
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// capture holds the server-side view of one request.
type capture struct {
	path    string
	query   url.Values
	authHdr string
}

// newTestServer starts an httptest server that records each request into a
// *capture and responds with the given status and body.
func newTestServer(t *testing.T, status int, body []byte) (*httptest.Server, *capture) {
	t.Helper()
	cap := &capture{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cap.path = r.URL.Path
		cap.query = r.URL.Query()
		cap.authHdr = r.Header.Get("Authorization")
		w.WriteHeader(status)
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv, cap
}

// TestHTTPClientGetTopologyContextCancellation verifies that cancelling the
// caller's context causes GetTopology to return promptly with an error rather
// than waiting for requestTimeout.
func TestHTTPClientGetTopologyContextCancellation(t *testing.T) {
	ready := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(ready)
		<-r.Context().Done() // hold until the client disconnects
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	client := NewHTTPClient(srv.URL, "")

	type result struct {
		resp *TopologyResponse
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		resp, err := client.GetTopology(ctx, testVPCID, 0, "")
		ch <- result{resp, err}
	}()

	<-ready // server has the request; now cancel the caller's context
	cancel()

	select {
	case res := <-ch:
		require.Error(t, res.err)
		require.Nil(t, res.resp)
	case <-time.After(5 * time.Second):
		t.Fatal("GetTopology did not return promptly after context cancellation")
	}
}

func validResponseBody(t *testing.T) ([]byte, TopologyResponse) {
	t.Helper()
	resp := TopologyResponse{
		Switches:      []map[string]SwitchAdjacency{{"leaf": {Nodes: []NodeInfo{{NodeID: "n1"}}}}},
		NextPageToken: "tok2",
	}
	b, err := json.Marshal(resp)
	require.NoError(t, err)
	return b, resp
}

const testVPCID = "vpc-1"

func TestHTTPClientGetTopology(t *testing.T) {
	validBody, validResp := validResponseBody(t)

	tests := []struct {
		name      string
		token     string
		pageSize  int
		pageToken string
		status    int
		body      []byte
		wantPath  string
		wantQuery map[string]string // key → expected value; "" means must be absent
		wantAuth  string            // expected Authorization header value, or "" if none
		wantErr   bool
		wantResp  *TopologyResponse
	}{
		{
			name:     "nodes path",
			status:   http.StatusOK,
			body:     validBody,
			wantPath: "/v1/topology/vpcs/" + testVPCID + "/nodes",
			wantResp: &validResp,
		},
		{
			name:      "query params: page_size and page_token encoded, node_ids never sent",
			pageSize:  200,
			pageToken: "cursor-abc",
			status:    http.StatusOK,
			body:      validBody,
			wantPath:  "/v1/topology/vpcs/" + testVPCID + "/nodes",
			wantQuery: map[string]string{
				"node_ids":   "",
				"page_size":  "200",
				"page_token": "cursor-abc",
			},
			wantResp: &validResp,
		},
		{
			name:     "page_size below minimum is clamped to minPageSize",
			pageSize: 1,
			status:   http.StatusOK,
			body:     validBody,
			wantPath: "/v1/topology/vpcs/" + testVPCID + "/nodes",
			wantQuery: map[string]string{
				"page_size": strconv.Itoa(minPageSize),
			},
			wantResp: &validResp,
		},
		{
			name:     "Authorization header sent when token is set",
			token:    "secret-token",
			status:   http.StatusOK,
			body:     validBody,
			wantPath: "/v1/topology/vpcs/" + testVPCID + "/nodes",
			wantAuth: "Bearer secret-token",
			wantResp: &validResp,
		},
		{
			name:     "no Authorization header when token is empty",
			token:    "",
			status:   http.StatusOK,
			body:     validBody,
			wantPath: "/v1/topology/vpcs/" + testVPCID + "/nodes",
			wantAuth: "", // expect header absent
			wantResp: &validResp,
		},
		{
			name:      "zero pageSize omits page_size param",
			pageSize:  0,
			status:    http.StatusOK,
			body:      validBody,
			wantPath:  "/v1/topology/vpcs/" + testVPCID + "/nodes",
			wantQuery: map[string]string{"page_size": ""},
			wantResp:  &validResp,
		},
		{
			name:     "non-2xx response returns error",
			status:   http.StatusBadRequest,
			body:     []byte(`{"detail":"bad page_token"}`),
			wantPath: "/v1/topology/vpcs/" + testVPCID + "/nodes",
			wantErr:  true,
		},
		{
			name:     "malformed JSON with 200 OK returns error",
			status:   http.StatusOK,
			body:     []byte(`{not valid json`),
			wantPath: "/v1/topology/vpcs/" + testVPCID + "/nodes",
			wantErr:  true,
		},
		{
			name:     "complete valid response is parsed correctly",
			token:    "tok",
			status:   http.StatusOK,
			body:     validBody,
			wantPath: "/v1/topology/vpcs/" + testVPCID + "/nodes",
			wantAuth: "Bearer tok",
			wantResp: &validResp,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv, cap := newTestServer(t, tc.status, tc.body)

			client := NewHTTPClient(srv.URL, tc.token)
			got, err := client.GetTopology(context.Background(), testVPCID, tc.pageSize, tc.pageToken)

			// --- server-side assertions ---
			require.Equal(t, tc.wantPath, cap.path)

			for param, want := range tc.wantQuery {
				if want == "" {
					// Empty expected value means the param must be absent entirely.
					// url.Values.Get returns "" for both absent and empty-valued params,
					// so check the raw slice to distinguish the two cases.
					require.Empty(t, cap.query[param], "query param %q should be absent", param)
				} else {
					require.Equal(t, want, cap.query.Get(param),
						"query param %q: got %q, want %q", param, cap.query.Get(param), want)
				}
			}

			require.Equal(t, tc.wantAuth, cap.authHdr)

			// --- client-side assertions ---
			if tc.wantErr {
				require.Error(t, err)
				require.Nil(t, got)
			} else {
				require.NoError(t, err)
				require.Equal(t, tc.wantResp, got)
			}
		})
	}
}
