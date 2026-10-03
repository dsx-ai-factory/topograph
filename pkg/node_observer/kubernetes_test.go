/*
 * Copyright (c) 2026, NVIDIA CORPORATION.  All rights reserved.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package node_observer

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/dsx-ai-factory/topograph/pkg/topology"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes/fake"
	clienttesting "k8s.io/client-go/testing"
)

func TestKubernetesNodeUpdates(t *testing.T) {
	old := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "a", Labels: map[string]string{"rack": "r1"}, Annotations: map[string]string{topology.KeyNodeInstance: "a"}}, Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}}}}
	tests := []struct {
		name   string
		change func(*corev1.Node)
		want   bool
	}{
		{"rack", func(n *corev1.Node) { n.Labels["rack"] = "r2" }, true},
		{"removed rack", func(n *corev1.Node) { delete(n.Labels, "rack") }, true},
		{"unrelated label", func(n *corev1.Node) { n.Labels["other"] = "value" }, false},
		{"unready", func(n *corev1.Node) { n.Status.Conditions[0].Status = corev1.ConditionFalse }, true},
		{"unknown", func(n *corev1.Node) { n.Status.Conditions[0].Status = corev1.ConditionUnknown }, true},
		{"heartbeat", func(n *corev1.Node) { n.Status.Conditions[0].LastHeartbeatTime = metav1.Now() }, false},
		{"pressure", func(n *corev1.Node) {
			n.Status.Conditions = append(n.Status.Conditions, corev1.NodeCondition{Type: corev1.NodeMemoryPressure, Status: corev1.ConditionTrue})
		}, false},
		{"deleting", func(n *corev1.Node) { now := metav1.Now(); n.DeletionTimestamp = &now }, true},
		{"broker identity", func(n *corev1.Node) { n.Annotations[topology.KeyNodeInstance] = "instance-a" }, true},
		{"unrelated annotation", func(n *corev1.Node) { n.Annotations["other"] = "value" }, false},
		{"cordoned", func(n *corev1.Node) { n.Spec.Unschedulable = true }, false},
	}
	s := &StatusInformer{nodeLabels: []string{"rack"}, nodeReadiness: true}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			node := old.DeepCopy()
			tt.change(node)
			require.Equal(t, tt.want, s.shouldRequestOnNodeUpdate(old, node))
			if tt.name == "unready" || tt.name == "unknown" {
				require.False(t, (&StatusInformer{nodeLabels: []string{"rack"}}).shouldRequestOnNodeUpdate(old, node))
			}
			require.False(t, (&StatusInformer{}).shouldRequestOnNodeUpdate(old, node))
		})
	}
}

func TestKubernetesTrigger(t *testing.T) {
	cfg := &Config{Provider: topology.Provider{Name: "kubernetes", Params: map[string]any{"topologyLabels": []string{"rack"}, "nodeSelector": map[string]string{"pool": "gpu"}}}}
	trigger, err := cfg.nodeTrigger()
	require.NoError(t, err)
	require.Equal(t, map[string]string{"pool": "gpu"}, trigger.NodeSelector)
	require.Equal(t, []string{"rack"}, trigger.NodeLabels)
	require.False(t, trigger.NodeReadiness)
	cfg.Provider.Params["requireReady"] = true
	trigger, err = cfg.nodeTrigger()
	require.NoError(t, err)
	require.True(t, trigger.NodeReadiness)
	cfg.Trigger.NodeSelector = map[string]string{"pool": "cpu"}
	_, err = cfg.nodeTrigger()
	require.ErrorContains(t, err, "must match")
}

func TestProviderIndependentNodeLabels(t *testing.T) {
	for _, provider := range []string{"crusoe", "kubernetes"} {
		cfg := &Config{
			Trigger:  Trigger{NodeLabels: []string{"custom/rack"}},
			Provider: topology.Provider{Name: provider, Params: map[string]any{"topologyLabels": []string{"rack"}}},
		}
		trigger, err := cfg.nodeTrigger()
		require.NoError(t, err)
		require.Equal(t, []string{"custom/rack"}, trigger.NodeLabels)
		ctx, cancel := context.WithCancel(context.Background())
		s, err := NewStatusInformer(ctx, fake.NewSimpleClientset(), trigger, nil, "", "", time.Second, nil)
		require.NoError(t, err)
		require.NotNil(t, s.nodeFactory, "labels alone must start a node informer")
		s.Stop(nil)
		cancel()
	}
	old := &corev1.Node{Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}}}}
	unready := old.DeepCopy()
	unready.Status.Conditions[0].Status = corev1.ConditionFalse
	require.True(t, (&StatusInformer{nodeReadiness: true}).shouldRequestOnNodeUpdate(old, unready))
	require.False(t, (&StatusInformer{}).shouldRequestOnNodeUpdate(old, unready))
}

func TestKubernetesInformerEvents(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client := fake.NewSimpleClientset()
	cfg := &Config{Provider: topology.Provider{Name: "kubernetes", Params: map[string]any{"topologyLabels": []string{"rack"}, "requireReady": true}}}
	trigger, err := cfg.nodeTrigger()
	require.NoError(t, err)
	s, err := NewStatusInformer(ctx, client, trigger, nil, "", "", time.Second, nil)
	require.NoError(t, err)
	defer s.Stop(nil)
	require.NotNil(t, s.nodeFactory)
	require.NoError(t, s.startNodeInformer())
	waitAndDrain := func() {
		require.Eventually(t, func() bool { return s.queue.Len() > 0 }, time.Second, 10*time.Millisecond)
		key, _ := s.queue.Get()
		s.queue.Done(key)
		s.queue.Forget(key)
	}
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "a", Labels: map[string]string{"rack": "r1"}}, Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}}}}
	node, err = client.CoreV1().Nodes().Create(ctx, node, metav1.CreateOptions{})
	require.NoError(t, err)
	waitAndDrain()

	node = node.DeepCopy()
	node.Labels["rack"] = "r2"
	node, err = client.CoreV1().Nodes().Update(ctx, node, metav1.UpdateOptions{})
	require.NoError(t, err)
	waitAndDrain()
	node = node.DeepCopy()
	node.Status.Conditions[0].Status = corev1.ConditionFalse
	node, err = client.CoreV1().Nodes().UpdateStatus(ctx, node, metav1.UpdateOptions{})
	require.NoError(t, err)
	waitAndDrain()
	node = node.DeepCopy()
	node.Labels["other"] = "value"
	_, err = client.CoreV1().Nodes().Update(ctx, node, metav1.UpdateOptions{})
	require.NoError(t, err)
	require.Never(t, func() bool { return s.queue.Len() > 0 }, 100*time.Millisecond, 10*time.Millisecond)
	require.NoError(t, client.CoreV1().Nodes().Delete(ctx, node.Name, metav1.DeleteOptions{}))
	waitAndDrain()
}

func TestKubernetesSelectorMembershipEvents(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client := fake.NewSimpleClientset()
	events := watch.NewRaceFreeFake()
	client.PrependWatchReactor("nodes", func(action clienttesting.Action) (bool, watch.Interface, error) {
		selector := action.(clienttesting.WatchAction).GetWatchRestrictions().Labels
		if selector.String() != "pool=gpu" {
			return true, nil, fmt.Errorf("unexpected node selector: %s", selector)
		}
		return true, events, nil
	})
	cfg := &Config{Provider: topology.Provider{Name: "kubernetes", Params: map[string]any{"topologyLabels": []string{"rack"}, "nodeSelector": map[string]string{"pool": "gpu"}}}}
	trigger, err := cfg.nodeTrigger()
	require.NoError(t, err)
	s, err := NewStatusInformer(ctx, client, trigger, nil, "", "", time.Second, nil)
	require.NoError(t, err)
	defer s.Stop(nil)
	require.NoError(t, s.startNodeInformer())
	waitAndDrain := func() {
		require.Eventually(t, func() bool { return s.queue.Len() > 0 }, time.Second, 10*time.Millisecond)
		key, _ := s.queue.Get()
		s.queue.Done(key)
		s.queue.Forget(key)
	}
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "a", Labels: map[string]string{"rack": "r1", "pool": "gpu"}}}
	// A filtered Kubernetes watch reports Added on selector entry and Deleted
	// on exit; the simple fake tracker does not implement these transitions.
	events.Add(node.DeepCopy())
	waitAndDrain()
	events.Delete(node.DeepCopy())
	waitAndDrain()
}
