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

package gcp

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPdshCmd(t *testing.T) {
	expected := fmt.Sprintf(`echo $(curl -s -H "Metadata-Flavor: Google" %s)`, IMDSInstanceURL)
	require.Equal(t, expected, pdshCmd(IMDSInstanceURL))
}

func TestConvertRegion(t *testing.T) {
	testCases := []struct {
		name string
		in   string
		out  string
	}{
		{
			name: "Case 1: empty string",
		},
		{
			name: "Case 2: single path",
			in:   "region",
			out:  "region",
		},
		{
			name: "Case 3: nested path",
			in:   "projects/project/zones/region",
			out:  "region",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.out, convertRegion(tc.in))
		})
	}
}
