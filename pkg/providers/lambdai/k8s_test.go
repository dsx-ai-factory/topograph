/*
 * Copyright 2026 LAMBDA
 * SPDX-License-Identifier: Apache-2.0
 */

package lambdai

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/dsx-ai-factory/topograph/pkg/topology"
)

func TestGetNodeAnnotations(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name     string
		node     *corev1.Node
		nodeName string
		want     map[string]string
		err      string
	}{
		{
			name: "Case 1: success",
			node: &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name:   "node1",
					Labels: map[string]string{regionLabelKey: "stg-sjc01-cl03"},
				},
				Spec: corev1.NodeSpec{ProviderID: "lambda://e56f5f8557db44c1b162872c94deed6d"},
			},
			nodeName: "node1",
			want: map[string]string{
				topology.KeyNodeInstance: "e56f5f8557db44c1b162872c94deed6d",
				topology.KeyNodeRegion:   "stg-sjc01-cl03",
			},
		},
		{
			name:     "Case 2: node not found",
			node:     &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "other"}},
			nodeName: "node1",
			err:      `failed to get node "node1"`,
		},
		{
			name: "Case 3: providerID missing prefix",
			node: &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name:   "node1",
					Labels: map[string]string{regionLabelKey: "stg-sjc01-cl03"},
				},
				Spec: corev1.NodeSpec{ProviderID: "e56f5f8557db44c1b162872c94deed6d"},
			},
			nodeName: "node1",
			err:      `is not "lambda://"-prefixed`,
		},
		{
			name: "Case 4: providerID empty",
			node: &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name:   "node1",
					Labels: map[string]string{regionLabelKey: "stg-sjc01-cl03"},
				},
			},
			nodeName: "node1",
			err:      `is not "lambda://"-prefixed`,
		},
		{
			name: "Case 5: missing region label",
			node: &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{Name: "node1"},
				Spec:       corev1.NodeSpec{ProviderID: "lambda://e56f5f8557db44c1b162872c94deed6d"},
			},
			nodeName: "node1",
			err:      `missing the "topology.kubernetes.io/region" label`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := fake.NewSimpleClientset(tt.node)
			got, err := GetNodeAnnotations(ctx, client, tt.nodeName)
			if len(tt.err) != 0 {
				require.Error(t, err)
				require.Contains(t, err.Error(), tt.err)
				require.Nil(t, got)
			} else {
				require.NoError(t, err)
				require.Equal(t, tt.want, got)
			}
		})
	}
}
