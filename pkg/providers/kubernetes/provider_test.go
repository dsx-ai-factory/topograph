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

package kubernetes

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/dsx-ai-factory/topograph/internal/httperr"
	kubernetesengine "github.com/dsx-ai-factory/topograph/pkg/engines/k8s"
	"github.com/dsx-ai-factory/topograph/pkg/topology"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
	clientgo "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
)

func readyNode(name, rack, zone string) *corev1.Node {
	return &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: map[string]string{"rack": rack, "zone": zone}}, Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}}}}
}

func TestTopologyTiers(t *testing.T) {
	for _, keys := range [][]string{{"rack"}, {"rack", "zone"}} {
		t.Run(keys[len(keys)-1], func(t *testing.T) {
			a, b, c := readyNode("a", "rack-1", "east"), readyNode("b", "rack-1", "east"), readyNode("c", "rack-1", "west")
			a.Annotations = map[string]string{topology.KeyNodeInstance: "instance-a"}
			p, err := New(fake.NewSimpleClientset(a, b, c), map[string]any{"topologyLabels": keys})
			require.NoError(t, err)
			graph, herr := p.GenerateTopologyConfig(context.Background(), nil, []topology.ComputeInstances{{Instances: map[string]string{"instance-a": "slurm-a", "b": "slurm-b", "c": "slurm-c"}}})
			require.Nil(t, herr)
			nodeLabels, err := kubernetesengine.NewTopologyLabeler(kubernetesengine.NewTopologyLabelKeys(nil, "")).BuildNodeLabels(graph)
			require.NoError(t, err)
			for _, labels := range nodeLabels {
				for _, value := range labels {
					require.Empty(t, validation.IsValidLabelValue(value), "the Kubernetes engine must emit valid label values")
				}
			}
			if len(keys) == 1 {
				require.Len(t, graph.Tiers.Vertices, 1)
				require.Len(t, graph.Tiers.Vertices["rack-1"].Vertices, 3)
				return
			}
			require.Len(t, graph.Tiers.Vertices, 2)
			east := graph.Tiers.Vertices["east"].Vertices[switchID([]string{"rack-1", "east"})]
			west := graph.Tiers.Vertices["west"].Vertices[switchID([]string{"rack-1", "west"})]
			require.Len(t, east.Vertices, 2)
			require.Len(t, west.Vertices, 1)
			require.Equal(t, "slurm-a", east.Vertices["instance-a"].Name)
			require.NotEqual(t, east.ID, west.ID)
		})
	}
}

func TestEligibleRequestedNodes(t *testing.T) {
	a, b, c, d := readyNode("a", "r1", "z1"), readyNode("b", "", ""), readyNode("c", "r1", "z1"), readyNode("d", "r2", "z1")
	a.Spec.Unschedulable = true
	b.Status.Conditions[0].Status = corev1.ConditionFalse
	now := metav1.Now()
	c.DeletionTimestamp = &now
	for _, node := range []*corev1.Node{a, b, c} {
		node.Labels["pool"] = "selected"
	}
	d.Labels["pool"] = "other"
	p, err := New(fake.NewSimpleClientset(a, b, c, d), map[string]any{"topologyLabels": []string{"rack"}, "nodeSelector": map[string]string{"pool": "selected"}, "requireReady": true})
	require.NoError(t, err)
	requested := []topology.ComputeInstances{{Instances: map[string]string{"a": "a", "b": "b", "c": "c", "d": "d", "deleted": "deleted"}}}
	graph, herr := p.GenerateTopologyConfig(context.Background(), nil, requested)
	require.Nil(t, herr)
	require.Len(t, graph.Tiers.Vertices, 1)
	require.Len(t, graph.Tiers.Vertices["r1"].Vertices, 1)
	require.Contains(t, graph.Tiers.Vertices["r1"].Vertices, "a")
	graph, herr = p.GenerateTopologyConfig(context.Background(), nil, nil)
	require.Nil(t, herr)
	require.Empty(t, graph.Tiers.Vertices)
	p, err = New(fake.NewSimpleClientset(a, b, c), map[string]any{"topologyLabels": []string{"rack"}, "requireReady": true})
	require.NoError(t, err)
	graph, herr = p.GenerateTopologyConfig(context.Background(), nil, requested)
	require.Nil(t, herr)
	require.Len(t, graph.Tiers.Vertices["r1"].Vertices, 1)
}

func TestInvalidTopologyLabel(t *testing.T) {
	for _, value := range []string{"", "not/a/value", " value "} {
		p, err := New(fake.NewSimpleClientset(readyNode("a", value, "z")), map[string]any{"topologyLabels": []string{"rack"}, "onMissingLabel": "fail"})
		require.NoError(t, err)
		graph, herr := p.GenerateTopologyConfig(context.Background(), nil, []topology.ComputeInstances{{Instances: map[string]string{"a": "a"}}})
		require.Nil(t, graph)
		require.NotNil(t, herr)
		require.Contains(t, herr.Error(), "rack")
		require.Contains(t, herr.Error(), `node "a"`)
	}
}

func TestParseParams(t *testing.T) {
	for _, keys := range []any{nil, []string{}, []string{""}, []string{"not/a/key"}, []string{"rack", "rack"}, 12, "rack", []any{1}, []any{nil}} {
		_, err := ParseParams(map[string]any{"topologyLabels": keys})
		require.Error(t, err)
	}
	_, err := ParseParams(map[string]any{"topologyLabels": []any{"rack", "zone"}})
	require.NoError(t, err)
	_, err = ParseParams(map[string]any{"topologyLabels": []string{"rack"}, "nodeSelector": map[string]string{"pool": "bad/value"}})
	require.Error(t, err)
	_, err = ParseParams(map[string]any{"topologyLabels": []string{"rack"}, "onMissingLabel": "ignore"})
	require.ErrorContains(t, err, "skip or fail")
}

func TestStableLabelCompatibleSwitchIDs(t *testing.T) {
	for _, rack := range []string{"r1", strings.Repeat("r", 29) + "-" + strings.Repeat("x", 33)} {
		id := switchID([]string{rack, "east", "region"})
		require.Empty(t, validation.IsValidLabelValue(id))
		require.Equal(t, id, switchID([]string{rack, "east", "region"}))
		require.NotEqual(t, id, switchID([]string{rack, "west", "region"}))
	}
	client := fake.NewSimpleClientset(readyNode("b", "r1", "east"))
	p, err := New(client, map[string]any{"topologyLabels": []string{"rack", "zone"}})
	require.NoError(t, err)
	requested := []topology.ComputeInstances{{Instances: map[string]string{"a": "a", "b": "b"}}}
	before, herr := p.GenerateTopologyConfig(context.Background(), nil, requested)
	require.Nil(t, herr)
	id := switchID([]string{"r1", "east"})
	_, err = client.CoreV1().Nodes().Create(context.Background(), readyNode("a", "r0", "east"), metav1.CreateOptions{})
	require.NoError(t, err)
	after, herr := p.GenerateTopologyConfig(context.Background(), nil, requested)
	require.Nil(t, herr)
	require.Equal(t, before.Tiers.Vertices["east"].Vertices[id], after.Tiers.Vertices["east"].Vertices[id])
}

func TestNodeDegradation(t *testing.T) {
	good, bad := readyNode("good", "r1", "east"), readyNode("bad", "", "east")
	good.Status.Conditions[0].Status = corev1.ConditionFalse
	client := fake.NewSimpleClientset(good, bad)
	p, err := New(client, map[string]any{"topologyLabels": []string{"rack"}})
	require.NoError(t, err)
	requested := []topology.ComputeInstances{{Instances: map[string]string{"good": "good", "bad": "bad"}}}
	graph, herr := p.GenerateTopologyConfig(context.Background(), nil, requested)
	require.Nil(t, herr)
	require.Contains(t, graph.Tiers.Vertices["r1"].Vertices, "good", "NotReady nodes stay in the topology by default")
	require.Len(t, graph.Tiers.Vertices["r1"].Vertices, 1)
	p.params.RequireReady = true
	graph, herr = p.GenerateTopologyConfig(context.Background(), nil, requested)
	require.Nil(t, graph)
	require.NotNil(t, herr)
	require.Contains(t, herr.Error(), "no requested nodes")
}

func TestNodeEligibility(t *testing.T) {
	node := readyNode("a", "r", "z")
	require.True(t, IsNodeEligible(node))
	for _, status := range []corev1.ConditionStatus{corev1.ConditionFalse, corev1.ConditionUnknown} {
		node.Status.Conditions[0].Status = status
		require.False(t, IsNodeEligible(node))
	}
	node.Status.Conditions = nil
	require.False(t, IsNodeEligible(node))
}

// nodeListTransport exercises the real client request path without a cluster.
type nodeListTransport func(*http.Request) (*http.Response, error)

func (f nodeListTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestNodeListDeadline(t *testing.T) {
	for _, earlier := range []bool{false, true} {
		t.Run(map[bool]string{false: "bounded request", true: "earlier caller deadline"}[earlier], func(t *testing.T) {
			ctx := context.Background()
			if earlier {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, time.Second)
				defer cancel()
			}
			var requestCtx context.Context
			client, err := clientgo.NewForConfigAndClient(&rest.Config{Host: "https://kubernetes.invalid"}, &http.Client{Transport: nodeListTransport(func(req *http.Request) (*http.Response, error) {
				requestCtx = req.Context()
				deadline, ok := requestCtx.Deadline()
				require.True(t, ok, "node listing must have a deadline even without a caller timeout")
				require.Positive(t, time.Until(deadline))
				require.LessOrEqual(t, time.Until(deadline), nodeListTimeout)
				if earlier {
					callerDeadline, _ := ctx.Deadline()
					require.Equal(t, callerDeadline, deadline)
				}
				return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"kind":"NodeList","apiVersion":"v1","items":[]}`))}, nil
			})})
			require.NoError(t, err)
			p, err := New(client, map[string]any{"topologyLabels": []string{"rack"}})
			require.NoError(t, err)
			_, herr := p.GenerateTopologyConfig(ctx, nil, nil)
			require.Nil(t, herr)
			require.NotNil(t, requestCtx)
			require.ErrorIs(t, requestCtx.Err(), context.Canceled)
		})
	}
}

func TestBlockedNodeListRespectsCallerCancellation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	client, err := clientgo.NewForConfigAndClient(&rest.Config{Host: "https://kubernetes.invalid"}, &http.Client{Transport: nodeListTransport(func(req *http.Request) (*http.Response, error) {
		<-req.Context().Done()
		return nil, req.Context().Err()
	})})
	require.NoError(t, err)
	p, err := New(client, map[string]any{"topologyLabels": []string{"rack"}})
	require.NoError(t, err)
	result := make(chan *httperr.Error, 1)
	go func() {
		_, herr := p.GenerateTopologyConfig(ctx, nil, nil)
		result <- herr
	}()
	select {
	case herr := <-result:
		require.NotNil(t, herr)
		require.Contains(t, herr.Error(), context.DeadlineExceeded.Error())
	case <-time.After(time.Second):
		t.Fatal("blocked node listing did not return after caller cancellation")
	}
}
