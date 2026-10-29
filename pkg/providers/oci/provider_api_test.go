// SPDX-FileCopyrightText: Copyright (c) 2024 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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

package oci

import (
	"testing"

	"github.com/oracle/oci-go-sdk/v65/common"
	"github.com/stretchr/testify/require"
)

func TestOciClient(t *testing.T) {
	tenant := "tenant"
	page := 10

	client := &ociClient{
		tenantID: tenant,
		limit:    &page,
	}

	require.Equal(t, &tenant, client.TenantID())
	require.Equal(t, &page, client.Limit())
}

func TestGetConfigurationProvider(t *testing.T) {
	tenant, user, region, fingerprint, key, dummy := "tenant", "user", "region", "12345", "key", ""
	testCases := []struct {
		name     string
		creds    map[string]any
		provider common.ConfigurationProvider
		err      string
	}{
		{
			name:  "Case 1: invalid tenancyId",
			creds: map[string]any{"tenancyId": false},
			err:   "* 'tenancyId' expected type 'string'",
		},
		{
			name:  "Case 2: missing userId",
			creds: map[string]any{"tenancyId": tenant},
			err:   "credentials error: missing 'userId'",
		},
		{
			name: "Case 3: missing region",
			creds: map[string]any{
				"tenancyId": tenant,
				"userId":    user,
			},
			err: "credentials error: missing 'region'",
		},
		{
			name: "Case 4: missing fingerprint",
			creds: map[string]any{
				"tenancyId": tenant,
				"userId":    user,
				"region":    region,
			},
			err: "credentials error: missing 'fingerprint'",
		},
		{
			name: "Case 5: missing privateKey",
			creds: map[string]any{
				"tenancyId":   tenant,
				"userId":      user,
				"region":      region,
				"fingerprint": fingerprint,
			},
			err: "credentials error: missing 'privateKey'",
		},
		{
			name: "Case 6: valid",
			creds: map[string]any{
				"tenancyId":   tenant,
				"userId":      user,
				"region":      region,
				"fingerprint": fingerprint,
				"privateKey":  key,
			},
			provider: common.NewRawConfigurationProvider(tenant, user, region, fingerprint, key, &dummy),
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			provider, err := getConfigurationProvider(tc.creds)
			if len(tc.err) != 0 {
				require.ErrorContains(t, err, tc.err)
			} else {
				require.Nil(t, err)
				require.Equal(t, tc.provider, provider)
			}
		})
	}
}
