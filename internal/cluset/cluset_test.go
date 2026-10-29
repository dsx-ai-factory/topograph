// SPDX-FileCopyrightText: Copyright (c) 2024-2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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

package cluset

import (
	"fmt"
	"math"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCompactExpand(t *testing.T) {
	testCases := []struct {
		name                string
		expanded, compacted []string
	}{
		{
			name: "Case 1: empty list",
		},
		{
			name:      "Case 2: ranges",
			expanded:  []string{"abc0507", "abc0509", "abc0482", "124", "abc0483", "abc0508", "abc0484", "123"},
			compacted: []string{"[123-124]", "abc[0482-0484,0507-0509]"},
		},
		{
			name:      "Case 3: singles",
			expanded:  []string{"abc0507", "abc0509", "xyz0482"},
			compacted: []string{"abc[0507,0509]", "xyz0482"},
		},
		{
			name:      "Case 4: mix1",
			expanded:  []string{"abc0507", "abc0509", "def", "abc0482", "abc0508"},
			compacted: []string{"abc[0482,0507-0509]", "def"},
		},
		{
			name:      "Case 5: mix2",
			expanded:  []string{"abc0507", "abc0509", "abc0508", "abc0482", "xyz8", "xyz9", "xyz10"},
			compacted: []string{"abc[0482,0507-0509]", "xyz[8-10]"},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.compacted, Compact(tc.expanded))
			require.ElementsMatch(t, tc.expanded, Expand(tc.compacted))
		})
	}
}

func TestExpandList(t *testing.T) {
	testCases := []struct {
		name   string
		input  string
		output []string
	}{
		{
			name: "Case 1: empty list",
		},
		{
			name:   "Case 2: single entry",
			input:  "dgx[0001-0018]",
			output: []string{"dgx[0001-0018]"},
		},
		{
			name:   "Case 3: multiple entries",
			input:  "dgx[0001-0018,0037-0054],dgx[0055-0072],dgx[0075,0090],dgx0127",
			output: []string{"dgx[0001-0018,0037-0072,0075,0090,0127]"},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.output, Compact(ExpandList(tc.input)))

		})
	}
}

func TestExpandWithLimit(t *testing.T) {
	t.Run("valid range within limit", func(t *testing.T) {
		result, err := expandWithLimit([]string{"n[1-3]"}, 10)
		require.NoError(t, err)
		require.Equal(t, []string{"n1", "n2", "n3"}, result)
	})

	t.Run("range exactly at limit", func(t *testing.T) {
		result, err := expandWithLimit([]string{"n[1-5]"}, 5)
		require.NoError(t, err)
		require.Len(t, result, 5)
	})

	t.Run("range exceeds limit — no allocation", func(t *testing.T) {
		// A single token expands to 2 billion entries; limit is 10.
		// The check must fire before any heap allocation.
		_, err := expandWithLimit([]string{"n[0-2000000000]"}, 10)
		require.ErrorContains(t, err, "exceeds")
	})

	t.Run("multiple entries accumulate toward limit", func(t *testing.T) {
		// Two ranges of 3 each; limit of 5 should be exceeded on the second.
		_, err := expandWithLimit([]string{"n[1-3]", "m[1-3]"}, 5)
		require.ErrorContains(t, err, "exceeds")
	})

	t.Run("scalar entries count toward limit", func(t *testing.T) {
		_, err := expandWithLimit([]string{"a", "b", "c"}, 2)
		require.ErrorContains(t, err, "exceeds")
	})

	t.Run("passthrough entries (no brackets) count toward limit", func(t *testing.T) {
		result, err := expandWithLimit([]string{"node1", "node2"}, 5)
		require.NoError(t, err)
		require.Equal(t, []string{"node1", "node2"}, result)
	})

	t.Run("maximum-integer range does not bypass the guard via overflow", func(t *testing.T) {
		// hi-lo+1 wraps to math.MinInt when hi==math.MaxInt and lo==0,
		// making the old guard (hi-lo+1 > limit) evaluate to false and
		// silently skip into an unbounded expansion loop.
		// The fixed guard (hi-lo >= limit) evaluates math.MaxInt >= 10 → true.
		entry := fmt.Sprintf("n[0-%d]", math.MaxInt)
		_, err := expandWithLimit([]string{entry}, 10)
		require.ErrorContains(t, err, "exceeds")
	})

	t.Run("singleton range at MaxInt terminates without loop overflow", func(t *testing.T) {
		// lo == hi == math.MaxInt passes the size guard (hi-lo == 0 < limit)
		// but the old loop's i++ wrapped MaxInt to MinInt, keeping i <= hi
		// true and spinning forever. The fixed loop breaks before i++ fires.
		entry := fmt.Sprintf("n[%d-%d]", math.MaxInt, math.MaxInt)
		result, err := expandWithLimit([]string{entry}, maxExpandedNodes)
		require.NoError(t, err)
		require.Len(t, result, 1)
	})
}
