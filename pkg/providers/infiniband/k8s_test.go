// SPDX-FileCopyrightText: Copyright (c) 2025-2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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

package infiniband

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/dsx-ai-factory/topograph/pkg/accelerator"
	"github.com/dsx-ai-factory/topograph/pkg/topology"
)

func TestGetNodeAnnotationsWithoutCollection(t *testing.T) {
	ctx := context.TODO()

	sections := []string{
		"",
		`{"source":"kubernetes-label","kubernetesLabel":{"key":"example.com/domain"}}`,
		`{"source":"none"}`,
	}
	for _, encodedSection := range sections {
		section, err := accelerator.DecodeSection(encodedSection)
		require.NoError(t, err)
		annotations, err := GetNodeAnnotations(ctx, nil, nil, "node-1", section)
		require.NoError(t, err)
		require.Equal(t, map[string]string{
			topology.KeyNodeInstance: "node-1",
			topology.KeyNodeRegion:   "local",
		}, annotations)
	}

	section, err := accelerator.DecodeSection(`{"source":"invalid"}`)
	require.NoError(t, err)
	_, err = GetNodeAnnotations(ctx, nil, nil, "node-1", section)
	require.EqualError(t, err, `unsupported accelerator source "invalid"`)
}
