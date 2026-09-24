/*
 * Copyright 2025-2026 NVIDIA CORPORATION
 * SPDX-License-Identifier: Apache-2.0
 */

package k8s

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/dsx-ai-factory/topograph/pkg/topology"
)

func TestGetNodes(t *testing.T) {
	node1 := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-1"}}
	node2 := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-2"}}
	client := fake.NewSimpleClientset(node1, node2)

	t.Run("nil options returns all nodes", func(t *testing.T) {
		nodes, err := GetNodes(context.Background(), client, nil)
		require.NoError(t, err)
		require.Len(t, nodes.Items, 2)
	})

	t.Run("explicit empty options returns all nodes", func(t *testing.T) {
		nodes, err := GetNodes(context.Background(), client, &metav1.ListOptions{})
		require.NoError(t, err)
		require.Len(t, nodes.Items, 2)
	})

	t.Run("api error is returned", func(t *testing.T) {
		errClient := fake.NewSimpleClientset()
		errClient.PrependReactor("list", "nodes", func(action k8stesting.Action) (bool, runtime.Object, error) {
			return true, nil, fmt.Errorf("api unavailable")
		})
		_, err := GetNodes(context.Background(), errClient, nil)
		require.ErrorContains(t, err, "api unavailable")
	})
}

func TestGetPodsByLabels(t *testing.T) {
	pod1 := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "pod-1",
			Namespace: "default",
			Labels:    map[string]string{"app": "broker"},
		},
	}
	pod2 := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "pod-2",
			Namespace: "default",
			Labels:    map[string]string{"app": "other"},
		},
	}
	client := fake.NewSimpleClientset(pod1, pod2)

	t.Run("returns matching pods", func(t *testing.T) {
		pods, err := GetPodsByLabels(context.Background(), client, "default", map[string]string{"app": "broker"})
		require.NoError(t, err)
		require.Len(t, pods.Items, 1)
		require.Equal(t, "pod-1", pods.Items[0].Name)
	})

	t.Run("api error is returned", func(t *testing.T) {
		errClient := fake.NewSimpleClientset()
		errClient.PrependReactor("list", "pods", func(action k8stesting.Action) (bool, runtime.Object, error) {
			return true, nil, fmt.Errorf("pods unavailable")
		})
		_, err := GetPodsByLabels(context.Background(), errClient, "default", map[string]string{"app": "broker"})
		require.ErrorContains(t, err, "pods unavailable")
	})
}

func TestGetDaemonSetPods(t *testing.T) {
	ds := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{Name: "broker", Namespace: "default"},
		Spec: appsv1.DaemonSetSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"app": "broker"},
			},
		},
	}
	pod1 := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "broker-node-1",
			Namespace: "default",
			Labels:    map[string]string{"app": "broker"},
		},
		Spec: corev1.PodSpec{NodeName: "node-1"},
	}
	pod2 := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "broker-node-2",
			Namespace: "default",
			Labels:    map[string]string{"app": "broker"},
		},
		Spec: corev1.PodSpec{NodeName: "node-2"},
	}
	// The fake client does not enforce label/field selectors server-side.
	// Selector correctness is verified via Actions() inspection below.
	client := fake.NewSimpleClientset(ds, pod1, pod2)

	t.Run("all pods without node filter", func(t *testing.T) {
		client.ClearActions()
		pods, err := GetDaemonSetPods(context.Background(), client, "broker", "default", "")
		require.NoError(t, err)
		require.Len(t, pods.Items, 2)
		// verify label selector was sent — fake client does not enforce it server-side
		actions := client.Actions()
		listAction := actions[len(actions)-1].(k8stesting.ListAction)
		require.Contains(t, listAction.GetListRestrictions().Labels.String(), "app=broker")
		require.Empty(t, listAction.GetListRestrictions().Fields.String())
	})

	t.Run("daemonset not found returns error", func(t *testing.T) {
		_, err := GetDaemonSetPods(context.Background(), client, "nonexistent", "default", "")
		require.Error(t, err)
	})

	t.Run("nodename field selector is sent", func(t *testing.T) {
		client.ClearActions()
		_, err := GetDaemonSetPods(context.Background(), client, "broker", "default", "node-1")
		require.NoError(t, err)
		actions := client.Actions()
		listAction := actions[len(actions)-1].(k8stesting.ListAction)
		require.Equal(t, "spec.nodeName=node-1", listAction.GetListRestrictions().Fields.String())
	})

	t.Run("pod list error is returned when daemonset get succeeds", func(t *testing.T) {
		errClient := fake.NewSimpleClientset(ds)
		errClient.PrependReactor("list", "pods", func(_ k8stesting.Action) (bool, runtime.Object, error) {
			return true, nil, fmt.Errorf("pods unavailable")
		})
		_, err := GetDaemonSetPods(context.Background(), errClient, "broker", "default", "")
		require.ErrorContains(t, err, "pods unavailable")
	})
}

func TestGetComputeInstances(t *testing.T) {
	t.Run("nodes with both annotations are grouped by region", func(t *testing.T) {
		nodes := &corev1.NodeList{
			Items: []corev1.Node{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "node-1",
						Annotations: map[string]string{
							topology.KeyNodeInstance: "i-001",
							topology.KeyNodeRegion:   "us-east-1",
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "node-2",
						Annotations: map[string]string{
							topology.KeyNodeInstance: "i-002",
							topology.KeyNodeRegion:   "us-east-1",
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "node-3",
						Annotations: map[string]string{
							topology.KeyNodeInstance: "i-003",
							topology.KeyNodeRegion:   "us-west-2",
						},
					},
				},
			},
		}
		cis := GetComputeInstances(nodes)
		require.Len(t, cis, 2)
		// collect instances by region for order-independent assertion
		byRegion := make(map[string]map[string]string)
		for _, ci := range cis {
			byRegion[ci.Region] = ci.Instances
		}
		require.Equal(t, map[string]string{"i-001": "node-1", "i-002": "node-2"}, byRegion["us-east-1"])
		require.Equal(t, map[string]string{"i-003": "node-3"}, byRegion["us-west-2"])
	})

	t.Run("incomplete nodes are skipped alongside valid ones", func(t *testing.T) {
		nodes := &corev1.NodeList{
			Items: []corev1.Node{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "good-node",
						Annotations: map[string]string{
							topology.KeyNodeInstance: "i-good",
							topology.KeyNodeRegion:   "us-east-1",
						},
					},
				},
				{
					// missing instance annotation
					ObjectMeta: metav1.ObjectMeta{
						Name:        "no-instance",
						Annotations: map[string]string{topology.KeyNodeRegion: "us-east-1"},
					},
				},
				{
					// missing region annotation
					ObjectMeta: metav1.ObjectMeta{
						Name:        "no-region",
						Annotations: map[string]string{topology.KeyNodeInstance: "i-bad"},
					},
				},
			},
		}
		cis := GetComputeInstances(nodes)
		require.Len(t, cis, 1)
		require.Equal(t, "us-east-1", cis[0].Region)
		require.Equal(t, map[string]string{"i-good": "good-node"}, cis[0].Instances)
	})

	t.Run("node missing instance annotation is skipped", func(t *testing.T) {
		nodes := &corev1.NodeList{
			Items: []corev1.Node{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:        "node-1",
						Annotations: map[string]string{topology.KeyNodeRegion: "us-east-1"},
					},
				},
			},
		}
		cis := GetComputeInstances(nodes)
		require.Empty(t, cis)
	})

	t.Run("node missing region annotation is skipped", func(t *testing.T) {
		nodes := &corev1.NodeList{
			Items: []corev1.Node{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:        "node-1",
						Annotations: map[string]string{topology.KeyNodeInstance: "i-001"},
					},
				},
			},
		}
		cis := GetComputeInstances(nodes)
		require.Empty(t, cis)
	})
}

func TestIsPodReady(t *testing.T) {
	testCases := []struct {
		name  string
		pod   *corev1.Pod
		ready bool
	}{
		{
			name: "Case 1: ready",
			pod: &corev1.Pod{
				Status: corev1.PodStatus{
					Conditions: []corev1.PodCondition{
						{
							Type:   corev1.DisruptionTarget,
							Status: corev1.ConditionUnknown,
						},
						{
							Type:   corev1.PodReady,
							Status: corev1.ConditionTrue,
						},
					},
				},
			},
			ready: true,
		},
		{
			name: "Case 2: implicit not ready",
			pod: &corev1.Pod{
				Status: corev1.PodStatus{
					Conditions: []corev1.PodCondition{
						{
							Type:   corev1.DisruptionTarget,
							Status: corev1.ConditionUnknown,
						},
						{
							Type:   corev1.ContainersReady,
							Status: corev1.ConditionTrue,
						},
					},
				},
			},
			ready: false,
		},
		{
			name: "Case 3: explicit not ready",
			pod: &corev1.Pod{
				Status: corev1.PodStatus{
					Conditions: []corev1.PodCondition{
						{
							Type:   corev1.DisruptionTarget,
							Status: corev1.ConditionUnknown,
						},
						{
							Type:   corev1.PodReady,
							Status: corev1.ConditionFalse,
						},
					},
				},
			},
			ready: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.ready, IsPodReady(tc.pod))
		})
	}
}

func TestNodeListOptions(t *testing.T) {
	tests := []struct {
		name   string
		params map[string]any
		want   *metav1.ListOptions
		err    string
	}{
		{name: "no parameters"},
		{
			name:   "other provider parameters are ignored",
			params: map[string]any{"accelerator": map[string]any{"source": "none"}},
		},
		{
			name:   "node selector",
			params: map[string]any{"nodeSelector": map[string]string{"key": "value"}},
			want:   &metav1.ListOptions{LabelSelector: "key=value"},
		},
		{
			name:   "invalid node selector",
			params: map[string]any{"nodeSelector": 0.1},
			err:    "could not decode configuration",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := NodeListOptions(test.params)
			if test.err != "" {
				require.ErrorContains(t, err, test.err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, test.want, got)
		})
	}
}

func TestValidateLabelKey(t *testing.T) {
	testCases := []struct {
		name string
		key  string
		err  string
	}{
		{name: "valid", key: "example.com/accelerator-domain"},
		{
			name: "empty",
			err:  `acceleratorDomainSourceLabel "" is not a valid Kubernetes label key`,
		},
		{
			name: "invalid",
			key:  "not a label",
			err:  `acceleratorDomainSourceLabel "not a label" is not a valid Kubernetes label key`,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateLabelKey("acceleratorDomainSourceLabel", tc.key)
			if tc.err == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, tc.err)
		})
	}
}
