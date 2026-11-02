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

package node_observer

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestHealthCheckURL(t *testing.T) {
	testCases := []struct {
		name        string
		generateURL string
		expected    string
		err         string
	}{
		{
			name:        "in-cluster service URL",
			generateURL: "http://topograph.topograph.svc.cluster.local:49021/v1/generate",
			expected:    "http://topograph.topograph.svc.cluster.local:49021/healthz",
		},
		{
			name:        "strips query and fragment",
			generateURL: "https://host:8443/v1/generate?foo=bar#frag",
			expected:    "https://host:8443/healthz",
		},
		{
			name:        "no path",
			generateURL: "http://host:49021",
			expected:    "http://host:49021/healthz",
		},
		{
			name:        "invalid URL",
			generateURL: "http://[::1]:namedport/v1/generate",
			err:         "failed to parse generateTopologyUrl",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := healthCheckURL(tc.generateURL)
			if len(tc.err) != 0 {
				require.Error(t, err)
				require.Contains(t, err.Error(), tc.err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.expected, got)
		})
	}
}

func TestWaitForTopographReady(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Fail the first probe, then succeed, to exercise the retry loop.
		if atomic.AddInt32(&hits, 1) < 2 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	err := waitForTopograph(context.Background(), srv.URL+"/healthz", time.Millisecond, time.Minute)
	require.NoError(t, err)
	require.GreaterOrEqual(t, atomic.LoadInt32(&hits), int32(2))
}

func TestWaitForTopographTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	err := waitForTopograph(context.Background(), srv.URL+"/healthz", time.Millisecond, 50*time.Millisecond)
	require.ErrorIs(t, err, context.DeadlineExceeded)
}

func TestWaitForTopographContextCancelled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := waitForTopograph(ctx, srv.URL+"/healthz", time.Hour, time.Hour)
	require.ErrorIs(t, err, context.Canceled)
}
