/*
 * Copyright 2026 LAMBDA
 * SPDX-License-Identifier: Apache-2.0
 */

package lambdai

import (
	"context"
	"fmt"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/dsx-ai-factory/topograph/pkg/topology"
)

const (
	// providerIDPrefix is the scheme the lambda-cloud-controller uses in
	// Node.spec.providerID, e.g. "lambda://<instance-id>".
	providerIDPrefix = "lambda://"
	// regionLabelKey is the well-known Kubernetes label the lambda-cloud-controller
	// sets to the Lambda region, e.g. "stg-sjc01-cl03".
	regionLabelKey = "topology.kubernetes.io/region"
)

// GetNodeAnnotations derives Topograph's instance and region annotations for a
// Lambda node from fields the lambda-cloud-controller sets on the Node object:
// the instance ID from .spec.providerID ("lambda://<id>") and the region from
// the "topology.kubernetes.io/region" label. The instance ID matches the
// topology API's `id` field 1:1.
//
// It runs in the node-data-broker init container, so returning an error when
// providerID or the region label is not yet populated causes the init container
// to retry until the controller has initialized the node.
func GetNodeAnnotations(ctx context.Context, client kubernetes.Interface, nodeName string) (map[string]string, error) {
	node, err := client.CoreV1().Nodes().Get(ctx, nodeName, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to get node %q: %w", nodeName, err)
	}

	instance, ok := strings.CutPrefix(node.Spec.ProviderID, providerIDPrefix)
	if !ok || len(instance) == 0 {
		return nil, fmt.Errorf("node %q providerID %q is not %q-prefixed; is lambda-cloud-controller initialized?",
			nodeName, node.Spec.ProviderID, providerIDPrefix)
	}

	region := node.Labels[regionLabelKey]
	if len(region) == 0 {
		return nil, fmt.Errorf("node %q is missing the %q label", nodeName, regionLabelKey)
	}

	return map[string]string{
		topology.KeyNodeInstance: instance,
		topology.KeyNodeRegion:   region,
	}, nil
}
