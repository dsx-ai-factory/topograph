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
	"os"
	"testing"

	"github.com/dsx-ai-factory/topograph/pkg/topology"
	"github.com/stretchr/testify/require"
)

func TestParseComputeNodes(t *testing.T) {
	data, err := os.ReadFile("../../../tests/output/netq/computeNodes.json")
	require.NoError(t, err)

	domains, err := parseComputeNodes(data)
	require.Nil(t, err)

	expected := topology.NewDomainMap()
	expected.AddHost("3fbfc98b-7f95-4749-ab11-bd351a2aab3e", "node1", "node1")
	expected.AddHost("3fbfc98b-7f95-4749-ab11-bd351a2aab3e", "node2", "node2")

	require.Equal(t, expected, domains)
}
