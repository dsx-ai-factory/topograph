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

package k8s

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/client-go/rest"
)

func TestConfigureClientRateLimits(t *testing.T) {
	testCases := []struct {
		name      string
		qps       *string
		burst     *string
		wantQPS   float32
		wantBurst int
	}{
		{
			name: "no overrides",
		},
		{
			name:      "both overrides",
			qps:       ptr("50"),
			burst:     ptr("100"),
			wantQPS:   50,
			wantBurst: 100,
		},
		{
			name:      "whitespace-padded overrides",
			qps:       ptr(" 50 "),
			burst:     ptr("\t100\n"),
			wantQPS:   50,
			wantBurst: 100,
		},
		{
			name:      "QPS only",
			qps:       ptr("50"),
			wantQPS:   50,
			wantBurst: rest.DefaultBurst,
		},
		{
			name:      "QPS with blank burst",
			qps:       ptr("50"),
			burst:     ptr(" "),
			wantQPS:   50,
			wantBurst: rest.DefaultBurst,
		},
		{
			name:      "burst only",
			burst:     ptr("100"),
			wantQPS:   rest.DefaultQPS,
			wantBurst: 100,
		},
		{
			name:      "burst with blank QPS",
			qps:       ptr(" "),
			burst:     ptr("100"),
			wantQPS:   rest.DefaultQPS,
			wantBurst: 100,
		},
		{
			name:      "zero uses defaults",
			qps:       ptr("0"),
			burst:     ptr("0"),
			wantQPS:   rest.DefaultQPS,
			wantBurst: rest.DefaultBurst,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			unsetEnv(t, envKubeQPS)
			unsetEnv(t, envKubeBurst)
			if tc.qps != nil {
				t.Setenv(envKubeQPS, *tc.qps)
			}
			if tc.burst != nil {
				t.Setenv(envKubeBurst, *tc.burst)
			}
			config := &rest.Config{}
			require.NoError(t, ConfigureClientRateLimits(config))
			require.Equal(t, tc.wantQPS, config.QPS)
			require.Equal(t, tc.wantBurst, config.Burst)
			if tc.qps == nil && tc.burst == nil {
				require.Nil(t, config.RateLimiter)
			} else {
				require.Equal(t, tc.wantQPS, config.RateLimiter.QPS())
			}
		})
	}
}

func TestConfigureClientRateLimitsTreatsBlankEnvironmentAsUnset(t *testing.T) {
	testCases := []struct {
		name  string
		env   string
		value string
	}{
		{
			name: "empty QPS",
			env:  envKubeQPS,
		},
		{
			name:  "whitespace-only QPS",
			env:   envKubeQPS,
			value: " \t\n",
		},
		{
			name: "empty burst",
			env:  envKubeBurst,
		},
		{
			name:  "whitespace-only burst",
			env:   envKubeBurst,
			value: " \t\n",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			unsetEnv(t, envKubeQPS)
			unsetEnv(t, envKubeBurst)
			t.Setenv(tc.env, tc.value)

			config := &rest.Config{}
			require.NoError(t, ConfigureClientRateLimits(config))
			require.Zero(t, config.QPS)
			require.Zero(t, config.Burst)
			require.Nil(t, config.RateLimiter)
		})
	}
}

func TestConfigureClientRateLimitsRejectsInvalidEnvironment(t *testing.T) {
	testCases := []struct {
		name  string
		env   string
		value string
		err   string
	}{
		{
			name:  "invalid QPS",
			env:   envKubeQPS,
			value: "fast",
			err:   "KUBE_QPS must be a non-negative number",
		},
		{
			name:  "negative QPS",
			env:   envKubeQPS,
			value: "-1",
			err:   "KUBE_QPS must be a non-negative number",
		},
		{
			name:  "non-finite QPS",
			env:   envKubeQPS,
			value: "Inf",
			err:   "KUBE_QPS must be a non-negative number",
		},
		{
			name:  "invalid burst",
			env:   envKubeBurst,
			value: "large",
			err:   "KUBE_BURST must be a non-negative integer",
		},
		{
			name:  "negative burst",
			env:   envKubeBurst,
			value: "-1",
			err:   "KUBE_BURST must be a non-negative integer",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			unsetEnv(t, envKubeQPS)
			unsetEnv(t, envKubeBurst)
			t.Setenv(tc.env, tc.value)
			err := ConfigureClientRateLimits(&rest.Config{})
			require.EqualError(t, err, tc.err)
		})
	}
}

func ptr(value string) *string {
	return &value
}

func unsetEnv(t *testing.T, name string) {
	t.Helper()
	value, ok := os.LookupEnv(name)
	require.NoError(t, os.Unsetenv(name))
	t.Cleanup(func() {
		if ok {
			require.NoError(t, os.Setenv(name, value))
		} else {
			require.NoError(t, os.Unsetenv(name))
		}
	})
}
