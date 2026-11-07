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
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"

	internalk8s "github.com/dsx-ai-factory/topograph/internal/k8s"
)

// GPUSource locates the GPU Operator workload that provides nvidia-smi.
type GPUSource struct {
	Namespace string `json:"gpuOperatorNamespace" yaml:"gpuOperatorNamespace"`
	DaemonSet string `json:"daemonSet" yaml:"daemonSet"`
	// Container is the container to exec into; empty selects the pod's default.
	Container string `json:"container,omitempty" yaml:"container,omitempty"`
}

// PodRunner runs commands in exactly one pod of the configured DaemonSet that
// is scheduled on this Node.
type PodRunner struct {
	Client   kubernetes.Interface
	Config   *rest.Config
	Source   GPUSource
	NodeName string
}

func (r *PodRunner) Run(ctx context.Context, cmd []string) (string, error) {
	pod, err := r.pod(ctx)
	if err != nil {
		return "", err
	}
	klog.V(4).InfoS("Running GPU discovery command", "node", r.NodeName, "pod", klog.KObj(pod), "container", r.Source.Container, "command", cmd)
	out, err := internalk8s.ExecInPodContainer(ctx, r.Client, r.Config, pod.Name, r.Source.Namespace, r.Source.Container, cmd)
	// NVIDIA CLI option errors can be printed to stdout. Preserve that output
	// on failure so the collector can distinguish unsupported flags from GPU,
	// transport, RBAC, or other errors without guessing from an exit code.
	if out == nil {
		return "", err
	}
	return out.String(), err
}

// pod selects the single eligible pod: selected by the DaemonSet's full label
// selector, controlled by that DaemonSet, running, and on this Node. RBAC
// cannot express the last two conditions, so they are enforced here.
func (r *PodRunner) pod(ctx context.Context) (*corev1.Pod, error) {
	ds, err := r.Client.AppsV1().DaemonSets(r.Source.Namespace).Get(ctx, r.Source.DaemonSet, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("getting DaemonSet %s/%s: %w", r.Source.Namespace, r.Source.DaemonSet, err)
	}
	selector, err := metav1.LabelSelectorAsSelector(ds.Spec.Selector)
	if err != nil {
		return nil, fmt.Errorf("DaemonSet %s/%s selector: %w", r.Source.Namespace, r.Source.DaemonSet, err)
	}
	pods, err := r.Client.CoreV1().Pods(r.Source.Namespace).List(ctx, metav1.ListOptions{
		LabelSelector: selector.String(),
		FieldSelector: "spec.nodeName=" + r.NodeName,
	})
	if err != nil {
		return nil, fmt.Errorf("listing pods of DaemonSet %s/%s: %w", r.Source.Namespace, r.Source.DaemonSet, err)
	}
	var eligible []*corev1.Pod
	for i := range pods.Items {
		if p := &pods.Items[i]; eligiblePod(p, ds, r.NodeName) {
			eligible = append(eligible, p)
		}
	}
	if len(eligible) != 1 {
		return nil, fmt.Errorf("expected exactly 1 eligible %s pod on node %s, found %d", r.Source.DaemonSet, r.NodeName, len(eligible))
	}
	return eligible[0], nil
}

func eligiblePod(p *corev1.Pod, ds *appsv1.DaemonSet, node string) bool {
	if p.Spec.NodeName != node || p.DeletionTimestamp != nil || p.Status.Phase != corev1.PodRunning {
		return false
	}
	ref := metav1.GetControllerOf(p)
	return ref != nil && ref.Kind == "DaemonSet" && ref.UID == ds.UID
}
