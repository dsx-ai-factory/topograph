/*
 * Copyright 2025 NVIDIA CORPORATION
 * SPDX-License-Identifier: Apache-2.0
 */

package infiniband

import (
	"bytes"
	"context"
	"errors"
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/dsx-ai-factory/topograph/pkg/topology"
)

type testIBNetDiscover struct {
	err bool
}

func (h *testIBNetDiscover) Run(ctx context.Context, node string) (*bytes.Buffer, error) {
	if h.err {
		return nil, errors.New("error")
	}
	data, err := os.ReadFile("../../../tests/output/ibnetdiscover/example.out")
	if err != nil {
		return nil, err
	}
	return bytes.NewBuffer(data), nil
}

func TestGetIbTree(t *testing.T) {
	testCases := []struct {
		name string
		err  bool
		root *topology.Vertex
	}{
		{
			name: "Case 1: error in ibnetdiscover",
			err:  true,
			root: &topology.Vertex{Vertices: map[string]*topology.Vertex{}},
		},
		{
			name: "Case 2: valid input",
			root: &topology.Vertex{
				Vertices: map[string]*topology.Vertex{
					"S-2c5eab0300b879c0": {
						ID: "S-2c5eab0300b879c0",
						Vertices: map[string]*topology.Vertex{
							"S-2c5eab0300c25f00": {
								ID: "S-2c5eab0300c25f00",
								Vertices: map[string]*topology.Vertex{
									"S-2c5eab0300c26140": {
										ID: "S-2c5eab0300c26140",
										Vertices: map[string]*topology.Vertex{
											"b07-p1-dgx-07-c01": {
												Name: "b07-p1-dgx-07-c01",
												ID:   "b07-p1-dgx-07-c01",
											},
											"b07-p1-dgx-07-c02": {
												Name: "b07-p1-dgx-07-c02",
												ID:   "b07-p1-dgx-07-c02",
											},
											"b07-p1-dgx-07-c03": {
												Name: "b07-p1-dgx-07-c03",
												ID:   "b07-p1-dgx-07-c03",
											},
											"b07-p1-dgx-07-c04": {
												Name: "b07-p1-dgx-07-c04",
												ID:   "b07-p1-dgx-07-c04",
											},
										},
									},
								},
							},
						},
					},
				},
			},
		},
	}

	ctx := context.TODO()
	cis := []topology.ComputeInstances{
		{
			Region: "on-prem",
			Instances: map[string]string{
				"b07-p1-dgx-07-c01": "b07-p1-dgx-07-c01",
				"b07-p1-dgx-07-c02": "b07-p1-dgx-07-c02",
				"b07-p1-dgx-07-c03": "b07-p1-dgx-07-c03",
				"b07-p1-dgx-07-c04": "b07-p1-dgx-07-c04",
			},
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			root, err := getIbTree(ctx, cis, &testIBNetDiscover{err: tc.err})
			require.NoError(t, err)
			require.NotNil(t, root)
			require.Equal(t, tc.root, root)
		})
	}
}
