/*
 * Copyright 2025 NVIDIA CORPORATION
 * SPDX-License-Identifier: Apache-2.0
 */

package k8s

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	internalk8s "github.com/dsx-ai-factory/topograph/internal/k8s"
	"github.com/dsx-ai-factory/topograph/pkg/topology"
)

func TestGetComputeInstances(t *testing.T) {
	nodeErr1 := corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "err1"}}
	nodeErr2 := corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "err2", Annotations: map[string]string{topology.KeyNodeInstance: "instance"}}}
	node1 := corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node1", Annotations: map[string]string{topology.KeyNodeInstance: "i1", topology.KeyNodeRegion: "r1"}}}
	node2 := corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node2", Annotations: map[string]string{topology.KeyNodeInstance: "i2", topology.KeyNodeRegion: "r1"}}}
	node3 := corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node3", Annotations: map[string]string{topology.KeyNodeInstance: "i3", topology.KeyNodeRegion: "r2"}}}

	testCases := []struct {
		name  string
		nodes *corev1.NodeList
		cis   []topology.ComputeInstances
	}{
		{
			name:  "Case 1: missing instance",
			nodes: &corev1.NodeList{Items: []corev1.Node{node1, nodeErr1}},
			cis: []topology.ComputeInstances{
				{
					Region:    "r1",
					Instances: map[string]string{"i1": "node1"},
				},
			},
		},
		{
			name:  "Case 2: missing region",
			nodes: &corev1.NodeList{Items: []corev1.Node{nodeErr2, node2}},
			cis: []topology.ComputeInstances{
				{
					Region:    "r1",
					Instances: map[string]string{"i2": "node2"},
				},
			},
		},
		{
			name:  "Case 3: valid input",
			nodes: &corev1.NodeList{Items: []corev1.Node{node1, node2, node3}},
			cis: []topology.ComputeInstances{
				{
					Region:    "r1",
					Instances: map[string]string{"i1": "node1", "i2": "node2"},
				},
				{
					Region:    "r2",
					Instances: map[string]string{"i3": "node3"},
				},
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			cis := internalk8s.GetComputeInstances(tc.nodes)
			require.Equal(t, tc.cis, cis)
		})
	}
}

func TestAddNodeLabelsReusesListedNodesAndPatchesOnlyChanges(t *testing.T) {
	changedNode := &corev1.Node{ObjectMeta: metav1.ObjectMeta{
		Name: "changed",
		Annotations: map[string]string{
			topology.KeyNodeInstance: "i-changed",
			topology.KeyNodeRegion:   "region",
		},
		Labels: map[string]string{
			topology.FabricTierKey(0): "old-leaf",
			topology.FabricTierKey(3): "stale-tier",
			"workload.example/label":  "keep",
		},
	}}
	unchangedNode := &corev1.Node{ObjectMeta: metav1.ObjectMeta{
		Name: "unchanged",
		Annotations: map[string]string{
			topology.KeyNodeInstance: "i-unchanged",
			topology.KeyNodeRegion:   "region",
		},
		Labels: map[string]string{
			topology.FabricTierKey(0): "leaf",
		},
	}}
	client := fake.NewSimpleClientset(changedNode, unchangedNode)
	eng := &K8sEngine{
		client: client,
		params: &Params{
			labelKeys: NewTopologyLabelKeys(nil, ""),
		},
	}

	_, httpErr := eng.ResolveComputeInstances(context.Background(), nil, nil)
	require.Nil(t, httpErr)
	require.NotNil(t, eng.cachedNodeMap)

	client.ClearActions()
	require.NoError(t, eng.AddNodeLabels(context.Background(), "changed", map[string]string{
		topology.FabricTierKey(0): "new-leaf",
		topology.FabricTierKey(1): "new-spine",
	}))
	require.NoError(t, eng.AddNodeLabels(context.Background(), "unchanged", map[string]string{
		topology.FabricTierKey(0): "leaf",
	}))

	require.Equal(t, 0, countKubernetesActions(client.Actions(), "list", "nodes"))
	require.Equal(t, 0, countKubernetesActions(client.Actions(), "get", "nodes"))
	require.Equal(t, 0, countKubernetesActions(client.Actions(), "update", "nodes"))
	require.Equal(t, 1, countKubernetesActions(client.Actions(), "patch", "nodes"))

	actual, err := client.CoreV1().Nodes().Get(context.Background(), "changed", metav1.GetOptions{})
	require.NoError(t, err)
	require.Equal(t, map[string]string{
		topology.FabricTierKey(0): "new-leaf",
		topology.FabricTierKey(1): "new-spine",
		"workload.example/label":  "keep",
	}, actual.Labels)

	client.ClearActions()
	require.NoError(t, eng.AddNodeLabels(context.Background(), "changed", map[string]string{
		topology.FabricTierKey(0): "new-leaf",
		topology.FabricTierKey(1): "new-spine",
	}))
	require.Equal(t, 0, countKubernetesActions(client.Actions(), "patch", "nodes"))
}

func TestAddNodeLabelsListsOnceWhenNodesWereNotPreviouslyLoaded(t *testing.T) {
	client := fake.NewSimpleClientset(&corev1.Node{ObjectMeta: metav1.ObjectMeta{
		Name: "node",
		Labels: map[string]string{
			topology.FabricTierKey(0): "old-leaf",
		},
	}})
	eng := &K8sEngine{
		client: client,
		params: &Params{
			labelKeys: NewTopologyLabelKeys(nil, ""),
		},
	}

	require.NoError(t, eng.AddNodeLabels(context.Background(), "node", map[string]string{
		topology.FabricTierKey(0): "new-leaf",
	}))
	require.NoError(t, eng.AddNodeLabels(context.Background(), "node", map[string]string{
		topology.FabricTierKey(0): "new-leaf",
	}))

	require.Equal(t, 1, countKubernetesActions(client.Actions(), "list", "nodes"))
	require.Equal(t, 0, countKubernetesActions(client.Actions(), "get", "nodes"))
	require.Equal(t, 1, countKubernetesActions(client.Actions(), "patch", "nodes"))
}

func TestAddNodeLabelsSkipsNodesOutsideSelector(t *testing.T) {
	selectedNode := &corev1.Node{ObjectMeta: metav1.ObjectMeta{
		Name:   "selected",
		Labels: map[string]string{"topology.example/enabled": "true"},
	}}
	excludedNode := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "excluded"}}
	client := fake.NewSimpleClientset(selectedNode, excludedNode)
	eng := &K8sEngine{
		client: client,
		params: &Params{
			NodeSelector: map[string]string{"topology.example/enabled": "true"},
			labelKeys:    NewTopologyLabelKeys(nil, ""),
		},
	}
	eng.cacheNodes(&corev1.NodeList{Items: []corev1.Node{*selectedNode, *excludedNode}})

	require.NoError(t, eng.AddNodeLabels(context.Background(), "excluded", map[string]string{
		topology.FabricTierKey(0): "leaf",
	}))
	require.NoError(t, eng.AddNodeLabels(context.Background(), "selected", map[string]string{
		topology.FabricTierKey(0): "leaf",
	}))

	require.Equal(t, 0, countKubernetesActions(client.Actions(), "get", "nodes"))
	require.Equal(t, 1, countKubernetesActions(client.Actions(), "patch", "nodes"))
}

func TestAddNodeLabelsSkipsNodeOutsideSelectorFilteredInstances(t *testing.T) {
	client := fake.NewSimpleClientset()
	eng := &K8sEngine{
		client: client,
		params: &Params{
			NodeSelector: map[string]string{"topology.example/enabled": "true"},
			labelKeys:    NewTopologyLabelKeys(nil, ""),
		},
	}
	eng.cacheNodes(&corev1.NodeList{})

	err := eng.AddNodeLabels(context.Background(), "unknown", map[string]string{
		topology.FabricTierKey(0): "leaf",
	})

	require.NoError(t, err)
	require.Equal(t, 0, countKubernetesActions(client.Actions(), "get", "nodes"))
	require.Equal(t, 0, countKubernetesActions(client.Actions(), "patch", "nodes"))
}

func TestAddNodeLabelsSkipsProviderOnlyNodeWithSuppliedInstancesAndNoSelector(t *testing.T) {
	requestedNode := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "requested"}}
	providerOnlyNode := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "provider-only"}}
	client := fake.NewSimpleClientset(requestedNode, providerOnlyNode)
	eng := &K8sEngine{
		client: client,
		params: &Params{
			labelKeys: NewTopologyLabelKeys(nil, ""),
		},
	}
	instances := []topology.ComputeInstances{{
		Region:    "region",
		Instances: map[string]string{"instance": requestedNode.Name},
	}}
	actual, httpErr := eng.ResolveComputeInstances(context.Background(), instances, nil)
	require.Nil(t, httpErr)
	require.Equal(t, instances, actual)
	require.NotContains(t, eng.cachedNodeMap, providerOnlyNode.Name)

	client.ClearActions()
	err := eng.AddNodeLabels(context.Background(), providerOnlyNode.Name, map[string]string{
		topology.FabricTierKey(0): "leaf",
	})

	require.NoError(t, err)
	require.Equal(t, 0, countKubernetesActions(client.Actions(), "get", "nodes"))
	require.Equal(t, 0, countKubernetesActions(client.Actions(), "patch", "nodes"))
}

func TestResolveComputeInstancesCachesSelectedNodesAndDefersExcludedNodes(t *testing.T) {
	selectedNode := &corev1.Node{ObjectMeta: metav1.ObjectMeta{
		Name:   "selected",
		Labels: map[string]string{"topology.example/enabled": "true"},
	}}
	excludedNode := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "excluded"}}
	client := fake.NewSimpleClientset(selectedNode, excludedNode)
	eng := &K8sEngine{
		client: client,
		params: &Params{
			NodeSelector: map[string]string{"topology.example/enabled": "true"},
			nodeListOpt: &metav1.ListOptions{
				LabelSelector: "topology.example/enabled=true",
			},
			labelKeys: NewTopologyLabelKeys(nil, ""),
		},
	}
	instances := []topology.ComputeInstances{{
		Region: "region",
		Instances: map[string]string{
			"i-selected": "selected",
			"i-excluded": "excluded",
		},
	}}

	actual, httpErr := eng.ResolveComputeInstances(context.Background(), instances, nil)

	require.Nil(t, httpErr)
	require.Equal(t, instances, actual)
	require.Contains(t, eng.cachedNodeMap, "selected")
	require.NotNil(t, eng.cachedNodeMap["selected"])
	require.Contains(t, eng.cachedNodeMap, "excluded")
	require.Nil(t, eng.cachedNodeMap["excluded"])
	require.Equal(t, 1, countKubernetesActions(client.Actions(), "list", "nodes"))
	listAction, ok := client.Actions()[0].(k8stesting.ListAction)
	require.True(t, ok)
	require.Equal(t, "topology.example/enabled=true", listAction.GetListRestrictions().Labels.String())

	client.ClearActions()
	require.NoError(t, eng.AddNodeLabels(context.Background(), "selected", map[string]string{
		topology.FabricTierKey(0): "leaf",
	}))
	require.NoError(t, eng.AddNodeLabels(context.Background(), "excluded", map[string]string{
		topology.FabricTierKey(0): "leaf",
	}))
	require.NoError(t, eng.AddNodeLabels(context.Background(), "excluded", map[string]string{
		topology.FabricTierKey(0): "leaf",
	}))

	require.Equal(t, 1, countKubernetesActions(client.Actions(), "get", "nodes"))
	require.Equal(t, 1, countKubernetesActions(client.Actions(), "patch", "nodes"))
	require.NotNil(t, eng.cachedNodeMap["excluded"])
}

func TestAddNodeLabelsGetsUnresolvedNodeAndReportsNotFound(t *testing.T) {
	client := fake.NewSimpleClientset()
	eng := &K8sEngine{
		client:        client,
		params:        &Params{labelKeys: NewTopologyLabelKeys(nil, "")},
		cachedNodeMap: map[string]*corev1.Node{"missing": nil},
	}

	err := eng.AddNodeLabels(context.Background(), "missing", map[string]string{
		topology.FabricTierKey(0): "leaf",
	})

	require.EqualError(t, err, `node "missing" was not found in Kubernetes`)
	require.Equal(t, 1, countKubernetesActions(client.Actions(), "get", "nodes"))
	require.Equal(t, 0, countKubernetesActions(client.Actions(), "patch", "nodes"))
}

func TestAddNodeLabelsPropagatesUnresolvedNodeGetError(t *testing.T) {
	client := fake.NewSimpleClientset()
	client.PrependReactor("get", "nodes", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("API unavailable")
	})
	eng := &K8sEngine{
		client:        client,
		params:        &Params{labelKeys: NewTopologyLabelKeys(nil, "")},
		cachedNodeMap: map[string]*corev1.Node{"node": nil},
	}

	err := eng.AddNodeLabels(context.Background(), "node", map[string]string{
		topology.FabricTierKey(0): "leaf",
	})

	require.EqualError(t, err, `failed to get node "node": API unavailable`)
	require.Equal(t, 1, countKubernetesActions(client.Actions(), "get", "nodes"))
	require.Equal(t, 0, countKubernetesActions(client.Actions(), "patch", "nodes"))
}

func countKubernetesActions(actions []k8stesting.Action, verb, resource string) int {
	count := 0
	for _, action := range actions {
		if action.GetVerb() == verb && action.GetResource().Resource == resource {
			count++
		}
	}
	return count
}

func TestMergeNodeLabels(t *testing.T) {
	testCases := []struct {
		name                         string
		acceleratorLabel             string
		acceleratorDomainSourceLabel string
		node                         *corev1.Node
		in                           map[string]string
		out                          map[string]string
	}{
		{
			name: "Case 1: no labels",
			node: &corev1.Node{},
			out:  map[string]string{},
		},
		{
			name: "Case 2: copy",
			node: &corev1.Node{},
			in:   map[string]string{"a": "1", "b": "2"},
			out:  map[string]string{"a": "1", "b": "2"},
		},
		{
			name: "Case 3: merge",
			node: &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Labels:      map[string]string{"a": "1", "b": "2", "c": "x"},
					Annotations: map[string]string{"a": "1", "b": "2", "c": "x"},
				},
			},
			in:  map[string]string{"c": "3", "d": "4"},
			out: map[string]string{"a": "1", "b": "2", "c": "3", "d": "4"},
		},
		{
			name:                         "Case 4: source label overrides provider accelerator domain",
			acceleratorDomainSourceLabel: "example.com/accelerator-domain",
			node: &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"example.com/accelerator-domain":  "source-domain",
						topology.KeyTopologyXclrDomain:    "old-domain",
						topology.KeyTopologyXclrSubDomain: "old-sub-domain",
						topology.FabricTierKey(0):         "old-leaf",
						topology.FabricTierKey(3):         "stale-fabric",
						"fabric.topograph.run/core":       "legacy-core",
						"workload.example/label":          "keep",
					},
				},
			},
			in: map[string]string{
				topology.KeyTopologyXclrDomain:    "api-domain",
				topology.KeyTopologyXclrSubDomain: "new-sub-domain",
				topology.FabricTierKey(0):         "new-leaf",
				topology.FabricTierKey(1):         "new-spine",
			},
			out: map[string]string{
				"example.com/accelerator-domain": "source-domain",
				topology.FabricTierKey(0):        "new-leaf",
				topology.FabricTierKey(1):        "new-spine",
				"fabric.topograph.run/core":      "legacy-core",
				"workload.example/label":         "keep",
			},
		},
		{
			name: "Case 5: omitted source label uses provider accelerator domain",
			node: &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"example.com/accelerator-domain": "unconfigured-source-domain",
					},
				},
			},
			in: map[string]string{
				topology.KeyTopologyXclrDomain:    "provider-domain",
				topology.KeyTopologyXclrSubDomain: "provider-sub-domain",
			},
			out: map[string]string{
				"example.com/accelerator-domain":  "unconfigured-source-domain",
				topology.KeyTopologyXclrDomain:    "provider-domain",
				topology.KeyTopologyXclrSubDomain: "provider-sub-domain",
			},
		},
		{
			name:             "Case 6: customized accelerator output without source label",
			acceleratorLabel: "custom.example/accelerator",
			node:             &corev1.Node{},
			in: map[string]string{
				"custom.example/accelerator": "api-domain",
				topology.FabricTierKey(0):    "new-leaf",
				topology.FabricTierKey(1):    "new-spine",
			},
			out: map[string]string{
				"custom.example/accelerator": "api-domain",
				topology.FabricTierKey(0):    "new-leaf",
				topology.FabricTierKey(1):    "new-spine",
			},
		},
		{
			name:                         "Case 7: configured source missing uses provider accelerator domain",
			acceleratorDomainSourceLabel: "example.com/accelerator-domain",
			node:                         &corev1.Node{},
			in: map[string]string{
				topology.KeyTopologyXclrDomain:    "api-domain",
				topology.KeyTopologyXclrSubDomain: "api-sub-domain",
			},
			out: map[string]string{
				topology.KeyTopologyXclrDomain:    "api-domain",
				topology.KeyTopologyXclrSubDomain: "api-sub-domain",
			},
		},
		{
			name: "Case 8: remove stale accelerator sub-domain label",
			node: &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						topology.KeyTopologyXclrSubDomain: "stale-sub-domain",
					},
				},
			},
			out: map[string]string{},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			keys := NewTopologyLabelKeys(nil, "")
			if tc.acceleratorLabel != "" {
				keys = NewTopologyLabelKeys(nil, tc.acceleratorLabel)
			}
			tc.node.Labels = mergeNodeLabels(
				tc.node.Labels,
				tc.in,
				keys,
				tc.acceleratorDomainSourceLabel,
			)
			require.Equal(t, tc.out, tc.node.Labels)
		})
	}
}
