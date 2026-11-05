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
	"encoding/json"
	"fmt"
	"slices"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/klog/v2"
)

const (
	maxConflictRetries = 5
	// sourcesMaxAge bounds how stale the collection timestamps of an
	// unchanged mapping may get. Within it, a refresh that finds the same
	// mapping writes nothing, so an idle cluster generates no etcd churn.
	sourcesMaxAge = time.Hour
)

// Publisher writes the NodeGPUNICRailMapping of one Node.
type Publisher struct {
	Client    dynamic.Interface
	Namespace string
	Release   string
	Node      *corev1.Node
	Now       func() time.Time
}

func (p *Publisher) name() string { return ResourceName(string(p.Node.UID)) }

// Publish creates or updates the Node's record. It is idempotent: an existing
// object is adopted only when every identity and ownership field matches.
func (p *Publisher) Publish(ctx context.Context, status *Status) error {
	if err := status.Validate(); err != nil {
		return err
	}
	res := p.Client.Resource(GVR).Namespace(p.Namespace)

	obj, err := res.Get(ctx, p.name(), metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		obj, err = res.Create(ctx, p.newObject(), metav1.CreateOptions{})
		if apierrors.IsAlreadyExists(err) {
			obj, err = res.Get(ctx, p.name(), metav1.GetOptions{})
		}
	}
	if err != nil {
		return fmt.Errorf("getting %s/%s: %w", p.Namespace, p.name(), err)
	}

	for attempt := 0; ; attempt++ {
		if err := ValidateIdentity(obj, p.Release, p.Namespace, p.Node.Name, string(p.Node.UID)); err != nil {
			return fmt.Errorf("refusing to adopt %s/%s: %w", p.Namespace, p.name(), err)
		}
		if p.upToDate(obj, status) {
			klog.V(4).InfoS("GPU/NIC rail mapping is unchanged; skipping status update", "node", p.Node.Name, "resource", klog.KObj(obj))
			return nil
		}
		next := obj.DeepCopy()
		raw, err := runtime.DefaultUnstructuredConverter.ToUnstructured(status)
		if err != nil {
			return err
		}
		if err := unstructured.SetNestedMap(next.Object, raw, "status"); err != nil {
			return err
		}
		_, err = res.UpdateStatus(ctx, next, metav1.UpdateOptions{})
		if err == nil {
			klog.InfoS("Updated GPU/NIC rail mapping status", "node", p.Node.Name, "resource", klog.KObj(next), "nics", len(status.NICs))
			return nil
		}
		if !apierrors.IsConflict(err) || attempt >= maxConflictRetries {
			return fmt.Errorf("updating status of %s/%s: %w", p.Namespace, p.name(), err)
		}
		klog.V(4).InfoS("GPU/NIC rail mapping status update conflicted; retrying", "node", p.Node.Name, "resource", klog.KObj(obj), "retry", attempt+1)
		// Fetch, revalidate and retry.
		if obj, err = res.Get(ctx, p.name(), metav1.GetOptions{}); err != nil {
			return fmt.Errorf("getting %s/%s: %w", p.Namespace, p.name(), err)
		}
	}
}

// upToDate reports whether the stored status already carries the mapping and
// its collection timestamps are recent enough.
func (p *Publisher) upToDate(obj *unstructured.Unstructured, want *Status) bool {
	raw, found, _ := unstructured.NestedMap(obj.Object, "status")
	if !found {
		return false
	}
	var have Status
	if data, err := json.Marshal(raw); err != nil || json.Unmarshal(data, &have) != nil {
		return false
	}
	if !sameMapping(have, *want) {
		return false
	}
	now := time.Now
	if p.Now != nil {
		now = p.Now
	}
	at, err := time.Parse(time.RFC3339, have.Sources.LLDP)
	return err == nil && now().Sub(at) < sourcesMaxAge
}

func (p *Publisher) newObject() *unstructured.Unstructured {
	uid := string(p.Node.UID)
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": Group + "/" + Version,
		"kind":       Kind,
		"metadata": map[string]any{
			"name":      p.name(),
			"namespace": p.Namespace,
			"labels": map[string]any{
				InstanceLabel:  p.Release,
				ManagedByLabel: ManagedByValue,
				PluginLabel:    PluginName,
				NodeUIDLabel:   uid,
			},
			"annotations": map[string]any{NodeNameAnnot: p.Node.Name},
			"ownerReferences": []any{map[string]any{
				"apiVersion": "v1", "kind": "Node", "name": p.Node.Name, "uid": uid,
			}},
		},
		"spec": map[string]any{"nodeRef": map[string]any{"name": p.Node.Name, "uid": uid}},
	}}
}

// Delete removes the Node's record, if it is ours, using UID and
// resource-version preconditions.
func (p *Publisher) Delete(ctx context.Context) error {
	res := p.Client.Resource(GVR).Namespace(p.Namespace)
	obj, err := res.Get(ctx, p.name(), metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := ValidateIdentity(obj, p.Release, p.Namespace, p.Node.Name, string(p.Node.UID)); err != nil {
		return fmt.Errorf("refusing to delete %s/%s: %w", p.Namespace, p.name(), err)
	}
	if err := deleteExact(ctx, res, obj); err != nil {
		return err
	}
	klog.V(2).InfoS("Deleted GPU/NIC rail mapping", "node", p.Node.Name, "resource", klog.KObj(obj))
	return nil
}

func deleteExact(ctx context.Context, res dynamic.ResourceInterface, obj *unstructured.Unstructured) error {
	uid, rv := obj.GetUID(), obj.GetResourceVersion()
	err := res.Delete(ctx, obj.GetName(), metav1.DeleteOptions{
		Preconditions: &metav1.Preconditions{UID: &uid, ResourceVersion: &rv},
	})
	if apierrors.IsNotFound(err) {
		return nil
	}
	return err
}

// ValidateIdentity checks that obj is the mapping of the given Node, managed
// by the given release: name, labels, spec.nodeRef and the Node owner
// reference must all agree. nodeName may be empty to skip the name check.
func ValidateIdentity(obj *unstructured.Unstructured, release, namespace, nodeName, nodeUID string) error {
	if obj.GetNamespace() != namespace {
		return fmt.Errorf("namespace is %q, want %q", obj.GetNamespace(), namespace)
	}
	if obj.GetName() != ResourceName(nodeUID) {
		return fmt.Errorf("name is %q, want %q", obj.GetName(), ResourceName(nodeUID))
	}
	l := obj.GetLabels()
	if l[PluginLabel] != PluginName || l[InstanceLabel] != release || l[ManagedByLabel] != ManagedByValue || l[NodeUIDLabel] != nodeUID {
		return fmt.Errorf("management labels do not match")
	}
	ref, _, _ := unstructured.NestedStringMap(obj.Object, "spec", "nodeRef")
	if ref["uid"] != nodeUID || (nodeName != "" && ref["name"] != nodeName) {
		return fmt.Errorf("spec.nodeRef does not match the Node")
	}
	owned := slices.ContainsFunc(obj.GetOwnerReferences(), func(o metav1.OwnerReference) bool {
		return o.Kind == "Node" && o.APIVersion == "v1" && o.UID == types.UID(nodeUID) && (nodeName == "" || o.Name == nodeName)
	})
	if !owned {
		return fmt.Errorf("missing owner reference to the Node")
	}
	return nil
}
