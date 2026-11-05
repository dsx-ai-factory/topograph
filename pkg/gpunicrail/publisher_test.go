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

package gpunicrail

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	kubefake "k8s.io/client-go/kubernetes/fake"
)

const ns, release = "topograph", "rel"

func newDynamic(objs ...runtime.Object) *dynamicfake.FakeDynamicClient {
	return dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{GVR: "NodeGPUNICRailMappingList"}, objs...)
}

func firstGPU(obj *unstructured.Unstructured) string {
	gpus, _, _ := unstructured.NestedSlice(obj.Object, "status", "nics", "0000:3b:00.0", "bestGpus")
	if len(gpus) == 0 {
		return ""
	}
	return gpus[0].(map[string]any)["uuid"].(string)
}

func node(name, uid string) *corev1.Node {
	return &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name, UID: types.UID(uid)}}
}

func status(gpu string) *Status {
	return &Status{
		Sources: Sources{LLDP: "2026-01-02T03:04:05Z"},
		NICs: map[string]NICMapping{"0000:3b:00.0": {
			Interface: "roce_vf_r0", RailID: "rail-0", TopologyTarget: "DD0", MappingMode: ModeDataDirect, BestPath: "PXB",
			BestGPUs: []GPU{{UUID: gpu, PCIAddress: "0000:17:00.0"}},
		}},
	}
}

func TestPublishCreatesAndUpdates(t *testing.T) {
	dyn := newDynamic()
	now := time.Date(2026, 1, 2, 3, 30, 0, 0, time.UTC)
	p := &Publisher{Client: dyn, Namespace: ns, Release: release, Node: node("n1", "uid-1"), Now: func() time.Time { return now }}
	ctx := context.Background()

	require.NoError(t, p.Publish(ctx, status("GPU-a")))
	obj, err := dyn.Resource(GVR).Namespace(ns).Get(ctx, "node-uid-1", metav1.GetOptions{})
	require.NoError(t, err)
	require.NoError(t, ValidateIdentity(obj, release, ns, "n1", "uid-1"))
	require.Equal(t, "GPU-a", firstGPU(obj))

	// An unchanged mapping with fresh timestamps writes nothing.
	dyn.ClearActions()
	require.NoError(t, p.Publish(ctx, status("GPU-a")))
	for _, a := range dyn.Actions() {
		require.NotEqual(t, "update", a.GetVerb())
	}

	// A changed mapping is written.
	require.NoError(t, p.Publish(ctx, status("GPU-b")))
	obj, _ = dyn.Resource(GVR).Namespace(ns).Get(ctx, "node-uid-1", metav1.GetOptions{})
	require.Equal(t, "GPU-b", firstGPU(obj))

	// Old timestamps force a refresh even when the mapping is unchanged.
	p.Now = func() time.Time { return now.Add(2 * sourcesMaxAge) }
	dyn.ClearActions()
	require.NoError(t, p.Publish(ctx, status("GPU-b")))
	var updated bool
	for _, a := range dyn.Actions() {
		updated = updated || a.GetVerb() == "update"
	}
	require.True(t, updated)
}

func TestPublishRefusesForeignObject(t *testing.T) {
	foreign := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": Group + "/" + Version, "kind": Kind,
		"metadata": map[string]any{"name": "node-uid-1", "namespace": ns},
	}}
	p := &Publisher{Client: newDynamic(foreign), Namespace: ns, Release: release, Node: node("n1", "uid-1")}
	require.ErrorContains(t, p.Publish(context.Background(), status("GPU-a")), "refusing to adopt")
	require.ErrorContains(t, p.Delete(context.Background()), "refusing to delete")
}

func TestPublishRejectsInvalidStatus(t *testing.T) {
	p := &Publisher{Client: newDynamic(), Namespace: ns, Release: release, Node: node("n1", "uid-1")}
	bad := status("GPU-a")
	n := bad.NICs["0000:3b:00.0"]
	n.BestPath = "bogus"
	bad.NICs["0000:3b:00.0"] = n
	require.Error(t, p.Publish(context.Background(), bad))
}

func TestDelete(t *testing.T) {
	dyn := newDynamic()
	p := &Publisher{Client: dyn, Namespace: ns, Release: release, Node: node("n1", "uid-1")}
	ctx := context.Background()
	require.NoError(t, p.Delete(ctx), "deleting a missing record is a no-op")
	require.NoError(t, p.Publish(ctx, status("GPU-a")))
	require.NoError(t, p.Delete(ctx))
	_, err := dyn.Resource(GVR).Namespace(ns).Get(ctx, "node-uid-1", metav1.GetOptions{})
	require.True(t, apierrors.IsNotFound(err))
}

func TestCleanerStaleAndDisabled(t *testing.T) {
	dyn := newDynamic()
	ctx := context.Background()
	for _, n := range []*corev1.Node{node("live", "uid-live"), node("gone", "uid-gone")} {
		p := &Publisher{Client: dyn, Namespace: ns, Release: release, Node: n}
		require.NoError(t, p.Publish(ctx, status("GPU-a")))
	}
	// Another release's record is never touched.
	other := &Publisher{Client: dyn, Namespace: ns, Release: "other", Node: node("live", "uid-other")}
	require.NoError(t, other.Publish(ctx, status("GPU-a")))

	c := &Cleaner{Dynamic: dyn, Kube: kubefake.NewSimpleClientset(node("live", "uid-live")), Namespace: ns, Release: release}
	names := func() []string {
		l, err := dyn.Resource(GVR).Namespace(ns).List(ctx, metav1.ListOptions{})
		require.NoError(t, err)
		var out []string
		for _, i := range l.Items {
			out = append(out, i.GetName())
		}
		return out
	}

	require.NoError(t, c.Reconcile(ctx, true))
	require.ElementsMatch(t, []string{"node-uid-live", "node-uid-other"}, names())

	require.NoError(t, c.Reconcile(ctx, false))
	require.Equal(t, []string{"node-uid-other"}, names())
}

func TestCleanerLeavesInvalidWhenDisabledDeletesWhenEnabled(t *testing.T) {
	bad := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": Group + "/" + Version, "kind": Kind,
		"metadata": map[string]any{
			"name": "node-x", "namespace": ns,
			"labels": map[string]any{PluginLabel: PluginName, InstanceLabel: release, ManagedByLabel: ManagedByValue, NodeUIDLabel: "x"},
		},
	}}
	dyn := newDynamic(bad)
	ctx := context.Background()
	c := &Cleaner{Dynamic: dyn, Kube: kubefake.NewSimpleClientset(), Namespace: ns, Release: release}

	require.NoError(t, c.Reconcile(ctx, false))
	l, _ := dyn.Resource(GVR).Namespace(ns).List(ctx, metav1.ListOptions{})
	require.Len(t, l.Items, 1)

	require.NoError(t, c.Reconcile(ctx, true))
	l, _ = dyn.Resource(GVR).Namespace(ns).List(ctx, metav1.ListOptions{})
	require.Empty(t, l.Items)
}

func TestPodRunnerSelection(t *testing.T) {
	ds := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{Name: "dp", Namespace: "gpu-operator", UID: "ds-uid"},
		Spec:       appsv1.DaemonSetSpec{Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "dp"}}},
	}
	pod := func(name, nodeName string, owner types.UID, phase corev1.PodPhase) *corev1.Pod {
		ctrl := true
		return &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "gpu-operator", Labels: map[string]string{"app": "dp"},
				OwnerReferences: []metav1.OwnerReference{{Kind: "DaemonSet", UID: owner, Controller: &ctrl}}},
			Spec:   corev1.PodSpec{NodeName: nodeName},
			Status: corev1.PodStatus{Phase: phase},
		}
	}
	run := func(pods ...*corev1.Pod) error {
		objs := []runtime.Object{ds}
		for _, p := range pods {
			objs = append(objs, p)
		}
		r := &PodRunner{Client: kubefake.NewSimpleClientset(objs...), Source: GPUSource{Namespace: "gpu-operator", DaemonSet: "dp"}, NodeName: "n1"}
		_, err := r.pod(context.Background())
		return err
	}

	require.NoError(t, run(pod("a", "n1", "ds-uid", corev1.PodRunning)))
	require.ErrorContains(t, run(), "found 0")
	require.ErrorContains(t, run(pod("a", "n1", "ds-uid", corev1.PodRunning), pod("b", "n1", "ds-uid", corev1.PodRunning)), "found 2")
	// Wrong node, wrong controller and non-running pods are not eligible.
	require.ErrorContains(t, run(pod("a", "n2", "ds-uid", corev1.PodRunning)), "found 0")
	require.ErrorContains(t, run(pod("a", "n1", "other", corev1.PodRunning)), "found 0")
	require.ErrorContains(t, run(pod("a", "n1", "ds-uid", corev1.PodPending)), "found 0")
}
