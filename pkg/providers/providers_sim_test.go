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

package providers

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGetSimParams(t *testing.T) {
	testCases := []struct {
		name   string
		params map[string]any
		sim    *SimulationParams
		err    string
	}{
		{
			name:   "Case 1: no input",
			params: nil,
			err:    "no model file name for simulation",
		},
		{
			name:   "Case 2: empty input",
			params: make(map[string]any),
			err:    "no model file name for simulation",
		},
		{
			name:   "Case 3: missing model",
			params: map[string]any{"key": "value"},
			err:    "no model file name for simulation",
		},
		{
			name:   "Case 4: valid input",
			params: map[string]any{"modelFileName": "/path/to/model"},
			sim: &SimulationParams{
				ModelFileName: "/path/to/model",
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			params, err := GetSimulationParams(tc.params)
			if len(tc.err) != 0 {
				require.EqualError(t, err, tc.err)
			} else {
				require.NoError(t, err)
				require.NotNil(t, params)
				require.Equal(t, tc.sim, params)
			}
		})
	}
}
