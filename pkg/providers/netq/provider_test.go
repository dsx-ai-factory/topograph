// SPDX-FileCopyrightText: Copyright (c) 2025 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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

package netq

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/dsx-ai-factory/topograph/pkg/providers"
)

func TestLoader(t *testing.T) {
	ctx := context.TODO()

	testCases := []struct {
		name   string
		config providers.Config
		err    string
	}{
		{
			name:   "Case 1: missing apiUrl",
			config: providers.Config{},
			err:    "apiUrl not provided",
		},
		{
			name: "Case 2: missing creds",
			config: providers.Config{
				Params: map[string]any{
					"apiUrl": "url",
				},
			},
			err: "missing 'username'",
		},
		{
			name: "Case 3: missing password",
			config: providers.Config{
				Params: map[string]any{
					"apiUrl": "url",
				},
				Creds: map[string]any{
					"username": "user",
				},
			},
			err: "missing 'password'",
		},
		{
			name: "Case 4: valid input",
			config: providers.Config{
				Params: map[string]any{
					"apiUrl": "url",
				},
				Creds: map[string]any{
					"username": "user",
					"password": "pwd",
				},
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Loader(ctx, tc.config)
			if len(tc.err) != 0 {
				require.EqualError(t, err, tc.err)
			} else {
				require.Nil(t, err)
			}
		})
	}
}
