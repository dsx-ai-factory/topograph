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
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"
)

// Cleaner removes mapping resources that belong to one Topograph release.
// It never discovers hardware or computes mappings.
type Cleaner struct {
	Dynamic   dynamic.Interface
	Kube      kubernetes.Interface
	Namespace string
	Release   string
}

func (c *Cleaner) selector() string {
	return labels.SelectorFromSet(labels.Set{
		PluginLabel: PluginName, InstanceLabel: c.Release, ManagedByLabel: ManagedByValue,
	}).String()
}

// Reconcile deletes this release's resources. With enabled false (plugin
// disabled) it deletes every resource that passes identity validation. With
// enabled true it deletes only stale ones: resources whose Node is gone or
// whose identity fields disagree.
func (c *Cleaner) Reconcile(ctx context.Context, enabled bool) error {
	res := c.Dynamic.Resource(GVR).Namespace(c.Namespace)
	list, err := res.List(ctx, metav1.ListOptions{LabelSelector: c.selector()})
	if apierrors.IsNotFound(err) {
		return nil // the CRD is not installed: nothing to clean
	}
	if err != nil {
		return fmt.Errorf("listing %s: %w", Resource, err)
	}

	var nodes map[string]string // UID -> name
	if enabled {
		if nodes, err = c.nodes(ctx); err != nil {
			return err
		}
	}

	var firstErr error
	for i := range list.Items {
		obj := &list.Items[i]
		valid := c.validate(obj) == nil
		var del bool
		switch {
		case !enabled:
			del = valid
			if !valid {
				klog.Warningf("Leaving %s/%s: identity validation failed", obj.GetNamespace(), obj.GetName())
			}
		case !valid:
			del = true
		default:
			uid := obj.GetLabels()[NodeUIDLabel]
			name, exists := nodes[uid]
			ref, _, _ := unstructured.NestedStringMap(obj.Object, "spec", "nodeRef")
			del = !exists || name != ref["name"]
		}
		if !del {
			continue
		}
		klog.Infof("Deleting %s/%s", obj.GetNamespace(), obj.GetName())
		if err := deleteExact(ctx, res, obj); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("deleting %s/%s: %w", obj.GetNamespace(), obj.GetName(), err)
		}
	}
	return firstErr
}

func (c *Cleaner) validate(obj *unstructured.Unstructured) error {
	uid := obj.GetLabels()[NodeUIDLabel]
	return ValidateIdentity(obj, c.Release, c.Namespace, "", uid)
}

func (c *Cleaner) nodes(ctx context.Context) (map[string]string, error) {
	out := map[string]string{}
	opts := metav1.ListOptions{Limit: 500}
	for {
		l, err := c.Kube.CoreV1().Nodes().List(ctx, opts)
		if err != nil {
			return nil, fmt.Errorf("listing nodes: %w", err)
		}
		for _, n := range l.Items {
			out[string(n.UID)] = n.Name
		}
		if l.Continue == "" {
			return out, nil
		}
		opts.Continue = l.Continue
	}
}

// CleanupInterval is how often the observer reconciles.
const CleanupInterval = 10 * time.Minute

// RunCleanup reconciles immediately and then every interval until ctx ends.
// Errors are logged and retried on the next tick.
func RunCleanup(ctx context.Context, c *Cleaner, enabled bool, interval time.Duration) {
	for {
		if err := c.Reconcile(ctx, enabled); err != nil && ctx.Err() == nil {
			klog.ErrorS(err, "GPU/NIC rail mapping cleanup failed")
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}
	}
}
