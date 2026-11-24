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

package httpauth

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestHeaders(t *testing.T) {
	req, err := http.NewRequest(http.MethodGet, "https://example.invalid", nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", BasicHeader("fake-user", "fake:password"))
	username, password, ok := req.BasicAuth()
	require.True(t, ok)
	require.Equal(t, "fake-user", username)
	require.Equal(t, "fake:password", password)
	require.Equal(t, "Bearer fake.jwt.token", BearerHeader("fake.jwt.token"))
}

func TestValidateBearerToken(t *testing.T) {
	for _, tokenType := range []string{"", "Bearer", "bEaReR"} {
		require.NoError(t, ValidateBearerToken("fake.jwt.token", tokenType))
	}
	for _, tc := range []struct{ token, tokenType string }{
		{"", ""}, {" \t", "Bearer"}, {"fake\rtoken", "Bearer"},
		{"fake\ntoken", "Bearer"}, {"fake-token", "Basic"},
		{"fake\x00token", "Bearer"}, {"fake\x7ftoken", "Bearer"}, {"fake\ttoken", "Bearer"},
	} {
		err := ValidateBearerToken(tc.token, tc.tokenType)
		require.EqualError(t, err, "invalid bearer token")
	}
}
