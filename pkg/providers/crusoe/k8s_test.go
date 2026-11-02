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

package crusoe

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestReadFabricLabels(t *testing.T) {
	testCases := []struct {
		name   string
		labels map[string]string
		want   fabricLabels
		ok     bool
	}{
		{
			name: "both labels present",
			labels: map[string]string{
				labelPartitionID: "partition-uuid",
				labelPodID:       "pod-uuid",
			},
			want: fabricLabels{PartitionID: "partition-uuid", PodID: "pod-uuid"},
			ok:   true,
		},
		{
			name: "surrounding whitespace is trimmed",
			labels: map[string]string{
				labelPartitionID: " partition-uuid ",
				labelPodID:       " pod-uuid ",
			},
			want: fabricLabels{PartitionID: "partition-uuid", PodID: "pod-uuid"},
			ok:   true,
		},
		{
			name:   "no labels at all",
			labels: map[string]string{},
		},
		{
			name:   "partition without a pod",
			labels: map[string]string{labelPartitionID: "partition-uuid"},
		},
		{
			name:   "pod without a partition",
			labels: map[string]string{labelPodID: "pod-uuid"},
		},
		{
			name: "empty label values",
			labels: map[string]string{
				labelPartitionID: "",
				labelPodID:       "",
			},
		},
		{
			name: "whitespace-only label values",
			labels: map[string]string{
				labelPartitionID: "   ",
				labelPodID:       "pod-uuid",
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := readFabricLabels(tc.labels)
			require.Equal(t, tc.ok, ok)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestReadNVLinkDomain(t *testing.T) {
	testCases := []struct {
		name   string
		labels map[string]string
		want   string
	}{
		{
			name:   "clique becomes a prefixed domain",
			labels: map[string]string{labelGPUClique: "cluster-uuid.32766"},
			want:   "cluster-uuid.32766",
		},
		{
			name:   "surrounding whitespace is trimmed",
			labels: map[string]string{labelGPUClique: " cluster-uuid.32766 "},
			want:   "cluster-uuid.32766",
		},
		{
			name:   "no clique label",
			labels: map[string]string{labelPartitionID: "partition-uuid"},
		},
		{
			name:   "empty clique value",
			labels: map[string]string{labelGPUClique: ""},
		},
		{
			name:   "whitespace-only clique value",
			labels: map[string]string{labelGPUClique: "   "},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, readNVLinkDomain(tc.labels))
		})
	}
}
