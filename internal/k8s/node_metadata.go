/*
 * Copyright 2026 NVIDIA CORPORATION
 * SPDX-License-Identifier: Apache-2.0
 */

package k8s

import (
	"context"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/klog/v2"

	"github.com/dsx-ai-factory/topograph/pkg/topology"
)

// MetadataSource selects an existing Node label or annotation.
type MetadataSource struct {
	Type string `yaml:"type"`
	Key  string `yaml:"key"`
}

// NodeMetadata configures identity sources for all Kubernetes consumers in a
// topology request. A nil configuration retains the broker's canonical
// annotations; a non-nil configuration requires both sources.
type NodeMetadata struct {
	InstanceID *MetadataSource `yaml:"instanceID,omitempty"`
	Region     *MetadataSource `yaml:"region,omitempty"`
}

func (m *NodeMetadata) Validate() error {
	if m == nil {
		return nil
	}
	for _, field := range []struct {
		name   string
		source *MetadataSource
	}{{"instanceID", m.InstanceID}, {"region", m.Region}} {
		if field.source == nil {
			return fmt.Errorf("nodeMetadata.%s is required when nodeMetadata is set", field.name)
		}
		if field.source.Type != "label" && field.source.Type != "annotation" {
			return fmt.Errorf("nodeMetadata.%s.type must be label or annotation", field.name)
		}
		if errs := validation.IsQualifiedName(field.source.Key); len(errs) != 0 {
			return fmt.Errorf("nodeMetadata.%s.key %q is not a valid Kubernetes metadata key: %s",
				field.name, field.source.Key, strings.Join(errs, "; "))
		}
	}
	return nil
}

type nodeMetadataContextKey struct{}

// WithNodeMetadata attaches request-wide source selection, avoiding process-wide
// mutable settings and keeping discovery and translation on the same mapping.
func WithNodeMetadata(ctx context.Context, metadata *NodeMetadata) context.Context {
	if metadata == nil {
		return ctx
	}
	return context.WithValue(ctx, nodeMetadataContextKey{}, metadata)
}

// resolveNodeMetadata projects configured sources onto canonical annotations in
// a private copy of the API response. No Node metadata is written to Kubernetes.
// Existing consumers therefore share the same identity mapping, including those
// passing annotations to accelerator discovery. A nil configuration leaves the
// original annotations untouched.
func resolveNodeMetadata(ctx context.Context, nodes *corev1.NodeList) *corev1.NodeList {
	metadata, _ := ctx.Value(nodeMetadataContextKey{}).(*NodeMetadata)
	if metadata == nil {
		return nodes
	}
	resolved := nodes.DeepCopy()
	for i := range resolved.Items {
		node := &resolved.Items[i]
		// Read both values before updating either destination: source keys can
		// themselves refer to canonical annotations.
		instance := metadataValue(node, metadata.InstanceID)
		region := metadataValue(node, metadata.Region)
		if node.Annotations == nil {
			node.Annotations = make(map[string]string)
		}
		for _, field := range []struct {
			source *MetadataSource
			key    string
			value  string
		}{{metadata.InstanceID, topology.KeyNodeInstance, instance}, {metadata.Region, topology.KeyNodeRegion, region}} {
			if field.source == nil {
				continue
			}
			if field.value == "" {
				delete(node.Annotations, field.key)
				klog.Warningf("missing or empty %q %s in node %s", field.source.Key, field.source.Type, node.Name)
				continue
			}
			node.Annotations[field.key] = field.value
		}
	}
	return resolved
}

func metadataValue(node *corev1.Node, source *MetadataSource) string {
	if source == nil {
		return ""
	}
	if source.Type == "label" {
		return strings.TrimSpace(node.Labels[source.Key])
	}
	return strings.TrimSpace(node.Annotations[source.Key])
}
