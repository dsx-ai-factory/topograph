/*
 * Copyright 2025 NVIDIA CORPORATION
 * SPDX-License-Identifier: Apache-2.0
 */

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
