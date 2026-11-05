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

package node_observer

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
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
	tests := []struct {
		name       string
		trigger    Trigger
		params     map[string]any
		wantLabels []string
		wantReady  bool
		wantErr    string
	}{
		{name: "provider defaults", wantLabels: []string{"rack"}},
		{name: "readiness default", params: map[string]any{"requireReady": true}, wantLabels: []string{"rack"}, wantReady: true},
		{name: "selector mismatch", trigger: Trigger{NodeSelector: map[string]string{"pool": "cpu"}}, wantErr: "must match"},
		{name: "invalid trigger label", trigger: Trigger{NodeLabels: []string{"bad label"}}, wantErr: "trigger.nodeLabels"},
		{name: "invalid provider params", params: map[string]any{"onMissingLabel": "ignore"}, wantErr: "onMissingLabel must be skip or fail"},
		{name: "explicit empty labels", trigger: Trigger{NodeLabels: []string{}}, wantLabels: []string{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			params := map[string]any{"topologyLabels": []string{"rack"}, "nodeSelector": map[string]string{"pool": "gpu"}}
			for key, value := range tt.params {
				params[key] = value
			}
			cfg := &Config{Provider: topology.Provider{Name: "k8s-node-labels", Params: params}, Trigger: tt.trigger}
			trigger, err := cfg.nodeTrigger()
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, map[string]string{"pool": "gpu"}, trigger.NodeSelector)
			require.Equal(t, tt.wantLabels, trigger.NodeLabels)
			require.Equal(t, tt.wantReady, trigger.NodeReadiness)
		})
	}
}

func TestKubernetesTriggerFromFile(t *testing.T) {
	tests := []struct {
		name        string
		labels      string
		extraParams string
		wantLabels  []string
		wantErr     string
	}{
		{name: "null uses defaults", labels: "null", wantLabels: []string{"rack"}},
		{name: "explicit empty list", labels: "[]", wantLabels: []string{}},
		{name: "invalid trigger label", labels: "[bad label]", wantErr: "trigger.nodeLabels"},
		{name: "invalid provider params", labels: "[custom/rack]", extraParams: "    onMissingLabel: ignore\n", wantErr: "onMissingLabel must be skip or fail"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data := fmt.Sprintf(`generateTopologyUrl: "http://topograph.invalid/v1/generate"
provider:
  name: k8s-node-labels
  params:
    topologyLabels: [rack]
%strigger:
  nodeLabels: %s
apiServer:
  podSelector:
    matchLabels:
      app: topograph
`, tt.extraParams, tt.labels)
			path := filepath.Join(t.TempDir(), "config.yaml")
			require.NoError(t, os.WriteFile(path, []byte(data), 0600))
			cfg, err := NewConfigFromFile(path)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			trigger, err := cfg.nodeTrigger()
			require.NoError(t, err)
			require.Equal(t, tt.wantLabels, trigger.NodeLabels)
		})
	}
}

func TestProviderIndependentNodeLabels(t *testing.T) {
	for _, provider := range []string{"crusoe", "k8s-node-labels"} {
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
	cfg := &Config{Provider: topology.Provider{Name: "k8s-node-labels", Params: map[string]any{"topologyLabels": []string{"rack"}, "requireReady": true}}}
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
	cfg := &Config{Provider: topology.Provider{Name: "k8s-node-labels", Params: map[string]any{"topologyLabels": []string{"rack"}, "nodeSelector": map[string]string{"pool": "gpu"}}}}
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
