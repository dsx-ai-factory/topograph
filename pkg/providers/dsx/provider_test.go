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
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/dsx-ai-factory/topograph/pkg/providers"
	"github.com/dsx-ai-factory/topograph/pkg/topology"
)

func TestLoader(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name        string
		params      map[string]any
		creds       map[string]any
		wantErrCode int
		wantErrMsg  string
	}{
		{
			name:        "missing base_url returns 400",
			params:      map[string]any{},
			creds:       map[string]any{},
			wantErrCode: http.StatusBadRequest,
			wantErrMsg:  "missing 'base_url'",
		},
		{
			name:        "missing vpc_id returns 400",
			params:      map[string]any{"base_url": "https://topology.example.com"},
			creds:       map[string]any{},
			wantErrCode: http.StatusBadRequest,
			wantErrMsg:  "missing 'vpc_id'",
		},
		{
			name:        "non-string token returns 400",
			params:      map[string]any{"base_url": "https://topology.example.com", "vpc_id": "vpc-1"},
			creds:       map[string]any{"token": 12345},
			wantErrCode: http.StatusBadRequest,
			wantErrMsg:  "credentials error: 'token' must be a string",
		},
		{
			name:   "valid config without token succeeds",
			params: map[string]any{"base_url": "https://topology.example.com", "vpc_id": "vpc-1"},
			creds:  map[string]any{},
		},
		{
			name:   "valid config with string token succeeds",
			params: map[string]any{"base_url": "https://topology.example.com", "vpc_id": "vpc-1"},
			creds:  map[string]any{"token": "my-bearer-token"},
		},
		{
			name:   "trimTiers param is accepted",
			params: map[string]any{"base_url": "https://topology.example.com", "vpc_id": "vpc-1", "trimTiers": 1},
			creds:  map[string]any{},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := providers.Config{Params: tc.params, Creds: tc.creds}
			p, err := Loader(ctx, cfg)
			if tc.wantErrCode != 0 {
				require.NotNil(t, err)
				require.Equal(t, tc.wantErrCode, err.Code())
				require.Contains(t, err.Error(), tc.wantErrMsg)
				require.Nil(t, p)
			} else {
				require.Nil(t, err)
				require.NotNil(t, p)
			}
		})
	}
}

func TestNamedLoader(t *testing.T) {
	name, loader := NamedLoader()
	require.Equal(t, NAME, name)
	require.NotNil(t, loader)
}

func TestGetNodeInstancesMapsNodesToThemselves(t *testing.T) {
	p := New(nil, 0)

	cis, err := p.GetNodeInstances(context.Background(), []string{"node-a", "node-b"})
	require.NoError(t, err)
	require.Equal(t, []topology.ComputeInstances{{
		Region:    "local",
		Instances: map[string]string{"node-a": "node-a", "node-b": "node-b"},
	}}, cis)

	cis, err = p.GetNodeInstances(context.Background(), nil)
	require.NoError(t, err)
	require.Empty(t, cis)
}
