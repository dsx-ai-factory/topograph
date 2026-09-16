/*
 * Copyright 2026 NVIDIA CORPORATION
 * SPDX-License-Identifier: Apache-2.0
 */

package nfd

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	"golang.org/x/sync/errgroup"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/dynamic"
	nfdv1alpha1 "sigs.k8s.io/node-feature-discovery/api/nfd/v1alpha1"

	k8sengine "github.com/dsx-ai-factory/topograph/pkg/engines/k8s"
	"github.com/dsx-ai-factory/topograph/pkg/topology"
)

const (
	nfdAPIGroup   = "nfd.k8s-sigs.io"
	nfdAPIVersion = "v1alpha1"
	nfdFeatureSet = "topograph.network"
	nfdSystemName = "system.name"
	nfdNodeName   = "nodename"

	nfdNodeFeatureKind               = "NodeFeature"
	nfdNodeFeatureGroupKind          = "NodeFeatureGroup"
	topologyTypeFabric               = "fabric-tier-"
	topologyTypeAcceleratorDomain    = "accelerator-domain"
	topologyTypeAcceleratorSubDomain = "accelerator-sub-domain"

	labelNFDNodeName = "nfd.node.kubernetes.io/node-name"
	labelManagedBy   = "app.kubernetes.io/managed-by"
	labelEngine      = "topograph.run/engine"
	labelResource    = "topograph.run/resource"
	labelGroupType   = "topograph.run/group-type"

	managedByTopograph         = "topograph"
	resourceNodeFeature        = "nodefeature"
	resourceNodeFeatureGroup   = "nodefeaturegroup"
	annotationNodeName         = "topograph.run/node-name"
	annotationTopologyLabelKey = "topograph.run/label-key"
	annotationTopologyValue    = "topograph.run/label-value"
)

var (
	nodeFeatureGVR = schema.GroupVersionResource{
		Group:    nfdAPIGroup,
		Version:  nfdAPIVersion,
		Resource: "nodefeatures",
	}
	nodeFeatureGroupGVR = schema.GroupVersionResource{
		Group:    nfdAPIGroup,
		Version:  nfdAPIVersion,
		Resource: "nodefeaturegroups",
	}
)

// buildNFDObjects converts node topology labels into per-node features and
// groups for each distinct topology value.
func buildNFDObjects(
	nodeLabels k8sengine.NodeLabelMap,
	acceleratorDomainSourceValues map[string]string,
	acceleratorDomainSourceLabel string,
) ([]*unstructured.Unstructured, []*unstructured.Unstructured, error) {
	groupValues := make(map[string]map[string]string)
	nodeFeatures := make([]*unstructured.Unstructured, 0, len(nodeLabels))

	for _, nodeName := range slices.Sorted(maps.Keys(nodeLabels)) {
		if errs := validation.IsValidLabelValue(nodeName); len(errs) != 0 {
			return nil, nil, fmt.Errorf("node name %q cannot be used as %q label value: %s",
				nodeName, labelNFDNodeName, strings.Join(errs, "; "))
		}

		labels := nodeLabels[nodeName]
		acceleratorDomainSourceValue := strings.TrimSpace(acceleratorDomainSourceValues[nodeName])
		elements := make(map[string]string, len(labels))
		for _, labelKey := range slices.Sorted(maps.Keys(labels)) {
			kind, ok := topologyKind(labelKey)
			if !ok {
				continue
			}
			if (kind == topologyTypeAcceleratorDomain || kind == topologyTypeAcceleratorSubDomain) && acceleratorDomainSourceValue != "" {
				continue
			}
			value := strings.TrimSpace(labels[labelKey])
			if value == "" {
				continue
			}

			elements[kind] = value
			if _, ok := groupValues[kind]; !ok {
				groupValues[kind] = make(map[string]string)
			}
			if kind == topologyTypeAcceleratorDomain && acceleratorDomainSourceLabel != "" &&
				groupValues[kind][value] == acceleratorDomainSourceLabel {
				continue
			}
			groupValues[kind][value] = labelKey
		}
		if acceleratorDomainSourceValue != "" {
			kind := topologyTypeAcceleratorDomain
			elements[kind] = acceleratorDomainSourceValue
			if _, ok := groupValues[kind]; !ok {
				groupValues[kind] = make(map[string]string)
			}
			groupValues[kind][acceleratorDomainSourceValue] = acceleratorDomainSourceLabel
		}
		if len(elements) == 0 {
			continue
		}

		nodeFeature, err := makeNodeFeature(nodeName, elements)
		if err != nil {
			return nil, nil, err
		}
		nodeFeatures = append(nodeFeatures, nodeFeature)
	}

	nodeFeatureGroups := make([]*unstructured.Unstructured, 0)
	for _, kind := range slices.Sorted(maps.Keys(groupValues)) {
		valuesByLabelKey := groupValues[kind]
		for _, value := range slices.Sorted(maps.Keys(valuesByLabelKey)) {
			nodeFeatureGroup, err := makeNodeFeatureGroup(kind, value, valuesByLabelKey[value])
			if err != nil {
				return nil, nil, err
			}
			nodeFeatureGroups = append(nodeFeatureGroups, nodeFeatureGroup)
		}
	}

	return nodeFeatures, nodeFeatureGroups, nil
}

func topologyKind(labelKey string) (string, bool) {
	if labelKey == topology.KeyTopologyXclrDomain {
		return topologyTypeAcceleratorDomain, true
	}
	if labelKey == topology.KeyTopologyXclrSubDomain {
		return topologyTypeAcceleratorSubDomain, true
	}
	if level, ok := strings.CutPrefix(labelKey, topology.KeyFabricTierPrefix); ok {
		if _, err := strconv.Atoi(level); err == nil {
			return topologyTypeFabric + level, true
		}
	}
	return "", false
}

// makeNodeFeature builds the NFD feature data published for one node.
func makeNodeFeature(nodeName string, elements map[string]string) (*unstructured.Unstructured, error) {
	obj := &nfdv1alpha1.NodeFeature{
		TypeMeta: metav1.TypeMeta{
			APIVersion: nfdAPIGroup + "/" + nfdAPIVersion,
			Kind:       nfdNodeFeatureKind,
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: stableObjectName("topograph-node", nodeName),
			Labels: map[string]string{
				labelNFDNodeName: nodeName,
				labelManagedBy:   managedByTopograph,
				labelEngine:      NAME,
				labelResource:    resourceNodeFeature,
			},
			Annotations: map[string]string{
				annotationNodeName: nodeName,
			},
		},
		Spec: nfdv1alpha1.NodeFeatureSpec{
			Features: nfdv1alpha1.Features{
				Attributes: map[string]nfdv1alpha1.AttributeFeatureSet{
					nfdSystemName: {
						Elements: map[string]string{nfdNodeName: nodeName},
					},
					nfdFeatureSet: {
						Elements: elements,
					},
				},
			},
		},
	}

	return toUnstructured(obj)
}

// makeNodeFeatureGroup builds an NFD group that matches one topology value.
func makeNodeFeatureGroup(kind, value, labelKey string) (*unstructured.Unstructured, error) {
	matchExpressions := nfdv1alpha1.MatchExpressionSet{
		kind: {
			Op:    nfdv1alpha1.MatchIn,
			Value: nfdv1alpha1.MatchValue{value},
		},
	}
	obj := &nfdv1alpha1.NodeFeatureGroup{
		TypeMeta: metav1.TypeMeta{
			APIVersion: nfdAPIGroup + "/" + nfdAPIVersion,
			Kind:       nfdNodeFeatureGroupKind,
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: stableObjectName("topograph-"+kind, value),
			Labels: map[string]string{
				labelManagedBy: managedByTopograph,
				labelEngine:    NAME,
				labelResource:  resourceNodeFeatureGroup,
				labelGroupType: kind,
			},
			Annotations: map[string]string{
				annotationTopologyLabelKey: labelKey,
				annotationTopologyValue:    value,
			},
		},
		Spec: nfdv1alpha1.NodeFeatureGroupSpec{
			Rules: []nfdv1alpha1.GroupRule{
				{
					Name: fmt.Sprintf("%s equals %s", kind, value),
					MatchFeatures: nfdv1alpha1.FeatureMatcher{
						{
							Feature:          nfdFeatureSet,
							MatchExpressions: &matchExpressions,
						},
					},
				},
			},
		},
	}

	return toUnstructured(obj)
}

// toUnstructured converts a typed NFD API object for use with the dynamic client.
func toUnstructured(obj any) (*unstructured.Unstructured, error) {
	object, err := runtime.DefaultUnstructuredConverter.ToUnstructured(obj)
	if err != nil {
		return nil, fmt.Errorf("convert NFD object to unstructured: %w", err)
	}
	return &unstructured.Unstructured{Object: object}, nil
}

// applyObjects creates or updates node features and feature groups concurrently.
func (eng *NfdEngine) applyObjects(
	ctx context.Context,
	nodeFeatures []*unstructured.Unstructured,
	nodeFeatureGroups []*unstructured.Unstructured,
) error {
	g, ctx := errgroup.WithContext(ctx)
	g.Go(func() error {
		for _, obj := range nodeFeatures {
			if err := eng.upsertObject(ctx, nodeFeatureGVR, obj); err != nil {
				return err
			}
		}
		return nil
	})
	g.Go(func() error {
		for _, obj := range nodeFeatureGroups {
			if err := eng.upsertObject(ctx, nodeFeatureGroupGVR, obj); err != nil {
				return err
			}
		}
		return nil
	})
	return g.Wait()
}

// cleanupObjects removes managed node features and feature groups that are no
// longer part of the generated topology.
func (eng *NfdEngine) cleanupObjects(
	ctx context.Context,
	nodeFeatures []*unstructured.Unstructured,
	nodeFeatureGroups []*unstructured.Unstructured,
) error {
	g, ctx := errgroup.WithContext(ctx)
	g.Go(func() error {
		return eng.cleanupResource(ctx, nodeFeatureGVR, resourceNodeFeature, objectNames(nodeFeatures))
	})
	g.Go(func() error {
		return eng.cleanupResource(ctx, nodeFeatureGroupGVR, resourceNodeFeatureGroup, objectNames(nodeFeatureGroups))
	})
	return g.Wait()
}

// upsertObject replaces the desired spec on an existing object or creates it
// when it does not exist. Replacing spec ensures attributes omitted from the
// latest topology are removed instead of surviving a JSON merge patch.
func (eng *NfdEngine) upsertObject(ctx context.Context, gvr schema.GroupVersionResource, obj *unstructured.Unstructured) error {
	obj = obj.DeepCopy()
	obj.SetNamespace(eng.namespace)
	resource := eng.resource(gvr)
	patch := []map[string]any{
		{"op": "add", "path": "/spec", "value": obj.Object["spec"]},
	}
	for key, value := range obj.GetLabels() {
		patch = append(patch, map[string]any{
			"op": "add", "path": "/metadata/labels/" + jsonPointerToken(key), "value": value,
		})
	}
	for key, value := range obj.GetAnnotations() {
		patch = append(patch, map[string]any{
			"op": "add", "path": "/metadata/annotations/" + jsonPointerToken(key), "value": value,
		})
	}

	data, err := json.Marshal(patch)
	if err != nil {
		return err
	}

	_, err = resource.Patch(ctx, obj.GetName(), types.JSONPatchType, data, metav1.PatchOptions{})
	if apierrors.IsNotFound(err) {
		_, err = resource.Create(ctx, obj.DeepCopy(), metav1.CreateOptions{})
		if apierrors.IsAlreadyExists(err) {
			_, err = resource.Patch(ctx, obj.GetName(), types.JSONPatchType, data, metav1.PatchOptions{})
		}
	}

	return err
}

// jsonPointerToken escapes a JSON object key for use as a JSON Patch path.
func jsonPointerToken(value string) string {
	return strings.NewReplacer("~", "~0", "/", "~1").Replace(value)
}

// cleanupResource deletes managed objects whose names are absent from desired.
func (eng *NfdEngine) cleanupResource(
	ctx context.Context,
	gvr schema.GroupVersionResource,
	resourceKind string,
	desired map[string]struct{},
) error {
	selector := labels.Set{
		labelManagedBy: managedByTopograph,
		labelEngine:    NAME,
		labelResource:  resourceKind,
	}.String()
	res := eng.resource(gvr)
	list, err := res.List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		return err
	}

	for i := range list.Items {
		name := list.Items[i].GetName()
		if _, ok := desired[name]; ok {
			continue
		}
		if err := res.Delete(ctx, name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
			return err
		}
	}

	return nil
}

// resource returns the dynamic resource client for the NFD namespace.
func (eng *NfdEngine) resource(gvr schema.GroupVersionResource) dynamic.ResourceInterface {
	return eng.dynamicClient.Resource(gvr).Namespace(eng.namespace)
}

// objectNames returns the names of objects as a set.
func objectNames(objects []*unstructured.Unstructured) map[string]struct{} {
	names := make(map[string]struct{}, len(objects))
	for _, obj := range objects {
		names[obj.GetName()] = struct{}{}
	}
	return names
}

// stableObjectName produces a deterministic DNS label from a prefix and value.
func stableObjectName(prefix, value string) string {
	prefix = dnsLabelPart(prefix)
	if prefix == "" {
		prefix = "topograph"
	}

	hash := shortHash(value)
	part := dnsLabelPart(value)
	if part == "" {
		part = "value"
	}

	maxPartLen := 63 - len(prefix) - len(hash) - 2
	if maxPartLen < 1 {
		maxPartLen = 1
	}
	if len(part) > maxPartLen {
		part = strings.Trim(part[:maxPartLen], "-")
		if part == "" {
			part = "value"
		}
	}

	return fmt.Sprintf("%s-%s-%s", prefix, part, hash)
}

// shortHash returns the first eight hexadecimal characters of a SHA-256 hash.
func shortHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])[:8]
}

// dnsLabelPart normalizes a value into characters allowed in a DNS label.
func dnsLabelPart(value string) string {
	value = strings.ToLower(value)
	var b strings.Builder
	lastDash := false

	for _, r := range value {
		valid := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
		if valid {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash {
			b.WriteByte('-')
			lastDash = true
		}
	}

	return strings.Trim(b.String(), "-")
}
