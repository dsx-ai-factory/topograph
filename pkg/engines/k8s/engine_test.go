/*
 * Copyright 2025 NVIDIA CORPORATION
 * SPDX-License-Identifier: Apache-2.0
 */

package k8s

import (
	"testing"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/dsx-ai-factory/topograph/pkg/topology"
)

func TestGetParameters(t *testing.T) {
	testCases := []struct {
		name   string
		params map[string]any
		ret    *Params
		err    string
	}{
		{
			name:   "Case 1: no params",
			params: nil,
			ret: &Params{
				labelKeys: NewTopologyLabelKeys(nil, ""),
			},
		},
		{
			name:   "Case 2: bad params",
			params: map[string]any{"nodeSelector": .1},
			err:    "could not decode configuration: 1 error(s) decoding:\n\n* 'nodeSelector' expected a map, got 'float64'",
		},
		{
			name: "Case 3: valid input",
			params: map[string]any{
				"nodeSelector":     map[string]string{"key": "val"},
				"fabricLabels":     []string{"example.com/rack", "example.com/pod"},
				"acceleratorLabel": "example.com/nvl",
			},
			ret: &Params{
				NodeSelector:     map[string]string{"key": "val"},
				FabricLabels:     []string{"example.com/rack", "example.com/pod"},
				AcceleratorLabel: "example.com/nvl",
				nodeListOpt: &metav1.ListOptions{
					LabelSelector: "key=val",
				},
				labelKeys: NewTopologyLabelKeys(
					[]string{"example.com/rack", "example.com/pod"},
					"example.com/nvl",
				),
			},
		},
		{
			name: "Case 4: reject duplicate topology label keys",
			params: map[string]any{
				"fabricLabels":     []string{"example.com/shared"},
				"acceleratorLabel": "example.com/shared",
			},
			err: `topology label key "example.com/shared" is configured for both fabricLabels[0] and acceleratorLabel`,
		},
		{
			name: "Case 5: reject invalid topology label key",
			params: map[string]any{
				"fabricLabels": []string{"not a label"},
			},
			err: `fabricLabels[0] "not a label" is not a valid Kubernetes label key`,
		},
		{
			name: "Case 6: reject duplicate label within one family",
			params: map[string]any{
				"fabricLabels": []string{"example.com/shared", "example.com/shared"},
			},
			err: `topology label key "example.com/shared" is configured for both fabricLabels[0] and fabricLabels[1]`,
		},
		{
			name: "Case 7: reject empty custom label key",
			params: map[string]any{
				"fabricLabels": []string{""},
			},
			err: `fabricLabels[0] "" is not a valid Kubernetes label key`,
		},
		{
			name: "Case 8: reject accelerator sub-domain label collision",
			params: map[string]any{
				"acceleratorLabel": topology.KeyTopologyXclrSubDomain,
			},
			err: `topology label key "accelerator.topograph.run/sub-domain" is configured for both acceleratorLabel and xclrSubDomainLabel`,
		},
		{
			name: "Case 9: accelerator domain source label",
			params: map[string]any{
				"acceleratorDomainSourceLabel": "example.com/accelerator-domain",
			},
			ret: &Params{
				AcceleratorDomainSourceLabel: "example.com/accelerator-domain",
				labelKeys:                    NewTopologyLabelKeys(nil, ""),
			},
		},
		{
			name: "Case 10: reject invalid accelerator domain source label",
			params: map[string]any{
				"acceleratorDomainSourceLabel": "not a label",
			},
			err: `acceleratorDomainSourceLabel "not a label" is not a valid Kubernetes label key`,
		},
		{
			name: "Case 11: reject customized accelerator output with source label",
			params: map[string]any{
				"acceleratorLabel":             "example.com/output-domain",
				"acceleratorDomainSourceLabel": "example.com/source-domain",
			},
			err: "engine parameters acceleratorLabel and acceleratorDomainSourceLabel cannot be set together",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			p, err := getParameters(tc.params)
			if len(tc.err) != 0 {
				require.ErrorContains(t, err, tc.err)
			} else {
				require.NoError(t, err)
				require.Equal(t, tc.ret, p)
			}
		})
	}
}
