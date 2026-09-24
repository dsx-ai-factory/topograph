/*
 * Copyright (c) 2024, NVIDIA CORPORATION.  All rights reserved.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package providers

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/dsx-ai-factory/topograph/pkg/topology"
	"github.com/stretchr/testify/require"
)

func TestParsePdshOutput(t *testing.T) {
	input := `node1: instance1
node2: instance2
node3: instance3
node4: instance4
`
	expected := map[string]string{"instance1": "node1", "instance2": "node2", "instance3": "node3", "instance4": "node4"}

	output, err := ParseInstanceOutput(bytes.NewBufferString(input))
	require.NoError(t, err)
	require.Equal(t, expected, output)

	expected = map[string]string{"node1": "instance1", "node2": "instance2", "node3": "instance3", "node4": "instance4"}

	output, err = ParsePdshOutput(bytes.NewBufferString(input), true)
	require.NoError(t, err)
	require.Equal(t, expected, output)
}

func TestHttpReqRetries(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if r.Header.Get("X-Test-Header") != "test-value" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte("missing header"))
			return
		}
		if attempts < 3 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte("try again"))
			return
		}

		_, _ = w.Write([]byte("instance-123\n"))
	}))
	defer server.Close()

	body, err := HttpReq(context.Background(), http.MethodGet, server.URL, map[string]string{
		"X-Test-Header": "test-value",
	})

	require.NoError(t, err)
	require.Equal(t, "instance-123", body)
	require.Equal(t, 3, attempts)
}

func TestHttpReqReturnsHTTPError(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts++
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("not found"))
	}))
	defer server.Close()

	body, err := HttpReq(context.Background(), http.MethodGet, server.URL, nil)

	require.Empty(t, body)
	require.EqualError(t, err, "not found")
	require.Equal(t, 1, attempts)
}

func TestReadFile(t *testing.T) {
	tests := []struct {
		name   string
		exists bool
		data   string
		err    bool
	}{
		{
			name: "Case 1: file does not exist",
			err:  true,
		},
		{
			name:   "Case 2: empty file",
			exists: true,
		},
		{
			name:   "Case 3: text file",
			exists: true,
			data: `line1
line2`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var path string
			if tt.exists {
				f, err := os.CreateTemp("", "test-*")
				require.NoError(t, err)
				path = f.Name()
				defer func() { _ = os.Remove(path) }()
				defer func() { _ = f.Close() }()
				if len(tt.data) != 0 {
					n, err := f.WriteString(tt.data)
					require.NoError(t, err)
					require.Equal(t, len(tt.data), n)
					err = f.Sync()
					require.NoError(t, err)
				}
			} else {
				path = "/does/not/exist"
			}

			data, err := ReadFile(path)
			if tt.err {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				require.Equal(t, tt.data, data)
			}
		})
	}
}

func TestGetTrimTiers(t *testing.T) {
	tests := []struct {
		name     string
		params   map[string]any
		expected int
		err      string
	}{
		{
			name:   "Case 1: missing key",
			params: map[string]any{},
		},
		{
			name: "Case 2: nil value",
			params: map[string]any{
				topology.KeyTrimTiers: nil,
			},
		},
		{
			name: "Case 3: int value",
			params: map[string]any{
				topology.KeyTrimTiers: 1,
			},
			expected: 1,
		},
		{
			name: "Case 4: float64 value",
			params: map[string]any{
				topology.KeyTrimTiers: float64(2),
			},
			expected: 2,
		},
		{
			name: "Case 5: negative value",
			params: map[string]any{
				topology.KeyTrimTiers: -1,
			},
			err: "invalid 'trimTiers' value '-1': must be an integer between 0 and 2",
		},
		{
			name: "Case 6: value greater than 2",
			params: map[string]any{
				topology.KeyTrimTiers: 3,
			},
			err: "invalid 'trimTiers' value '3': must be an integer between 0 and 2",
		},
		{
			name: "Case 7: unsupported type",
			params: map[string]any{
				topology.KeyTrimTiers: "1",
			},
			expected: 0,
			err:      "invalid 'trimTiers' value '1': unsupported type string",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := GetTrimTiers(tt.params)

			if len(tt.err) != 0 {
				require.EqualError(t, err, tt.err)
			} else {
				require.NoError(t, err)
				require.Equal(t, tt.expected, result)
			}
		})
	}
}

func TestGetIMDSURL(t *testing.T) {
	tests := []struct {
		name     string
		params   map[string]any
		expected string
		err      string
	}{
		{
			name:   "Case 1: missing key",
			params: map[string]any{},
		},
		{
			name: "Case 2: nil value",
			params: map[string]any{
				topology.KeyIMDSURL: nil,
			},
		},
		{
			name: "Case 3: string value",
			params: map[string]any{
				topology.KeyIMDSURL: "http://custom-imds.example.com/meta_data.json",
			},
			expected: "http://custom-imds.example.com/meta_data.json",
		},
		{
			name: "Case 4: unsupported type",
			params: map[string]any{
				topology.KeyIMDSURL: 1,
			},
			err: "invalid 'imdsUrl' value '1': unsupported type int",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := GetIMDSURL(tt.params)

			if len(tt.err) != 0 {
				require.EqualError(t, err, tt.err)
			} else {
				require.NoError(t, err)
				require.Equal(t, tt.expected, result)
			}
		})
	}
}
