/*
 * Copyright 2026 NVIDIA CORPORATION
 * SPDX-License-Identifier: Apache-2.0
 */

package accelerator

import (
	"context"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"

	internalK8s "github.com/dsx-ai-factory/topograph/internal/k8s"
	"github.com/dsx-ai-factory/topograph/pkg/topology"
)

// BaseKubernetesNodeAnnotations returns the identity annotations shared by
// Kubernetes providers whose instance ID and region are node-local.
func BaseKubernetesNodeAnnotations(hostName string) map[string]string {
	return map[string]string{
		topology.KeyNodeInstance: hostName,
		topology.KeyNodeRegion:   "local",
	}
}

// TargetsFromKubernetesNodes resolves selected Nodes through the canonical
// region/instance mapping and attaches their metadata for discovery.
func TargetsFromKubernetesNodes(nodes *corev1.NodeList, instances []topology.ComputeInstances) []Target {
	instancesByRegion := make(map[string]map[string]string, len(instances))
	for _, regionalInstances := range instances {
		instancesByRegion[regionalInstances.Region] = regionalInstances.Instances
	}

	targets := make([]Target, 0, len(nodes.Items))
	for _, node := range nodes.Items {
		instanceID := strings.TrimSpace(node.Annotations[topology.KeyNodeInstance])
		if instanceID == "" {
			klog.Warningf("missing or empty %q annotation in node %s", topology.KeyNodeInstance, node.Name)
			continue
		}
		region := strings.TrimSpace(node.Annotations[topology.KeyNodeRegion])
		if region == "" {
			klog.Warningf("missing or empty %q annotation in node %s", topology.KeyNodeRegion, node.Name)
			continue
		}
		regionalInstances, ok := instancesByRegion[region]
		if !ok {
			continue
		}
		hostName, ok := regionalInstances[instanceID]
		if !ok {
			continue
		}

		targets = append(targets, Target{
			InstanceID:  instanceID,
			HostName:    hostName,
			Labels:      node.Labels,
			Annotations: node.Annotations,
		})
	}
	return targets
}

// DiscoverKubernetesDomains runs a configured metadata discoverer against
// selected Nodes and converts its result into the canonical domain map.
func DiscoverKubernetesDomains(
	ctx context.Context,
	discoverer Discoverer,
	nodes *corev1.NodeList,
	instances []topology.ComputeInstances,
) (topology.DomainMap, error) {
	targets := TargetsFromKubernetesNodes(nodes, instances)
	assignments, err := discoverer.Discover(ctx, targets)
	if err != nil {
		return nil, err
	}
	return DomainMapFromAssignments(assignments, targets), nil
}

// NewKubernetesNodeDiscoverer returns the node-local discoverer used by the
// node-data-broker. Sources that read existing Kubernetes metadata require no
// node-local collection and therefore return an empty discoverer.
func NewKubernetesNodeDiscoverer(section Section, client kubernetes.Interface, restConfig *rest.Config) (Discoverer, error) {
	config, err := ParseConfig(section)
	if err != nil {
		return nil, err
	}

	switch config.Source {
	case SourceNvidiaSMI:
		if client == nil {
			return nil, fmt.Errorf("k8s client is required for nvidia-smi discovery")
		}
		if restConfig == nil {
			return nil, fmt.Errorf("k8s REST config is required for nvidia-smi discovery")
		}
		return NewNvidiaSMIDiscoverer(config, &kubernetesNvidiaSMIRunner{
			client:    client,
			config:    restConfig,
			namespace: config.NvidiaSMI.GPUOperatorNamespace,
			daemonSet: config.NvidiaSMI.DevicePluginDaemonSet,
		})
	case SourceKubernetesLabel, SourceNone:
		return NewNoneDiscoverer(), nil
	default:
		return nil, fmt.Errorf("unsupported accelerator source %q", config.Source)
	}
}

type kubernetesNvidiaSMIRunner struct {
	client    kubernetes.Interface
	config    *rest.Config
	namespace string
	daemonSet string
}

func (r *kubernetesNvidiaSMIRunner) Run(ctx context.Context, command string, targets []Target) (map[string]string, error) {
	outputs := make(map[string]string)
	for _, target := range targets {
		pods, err := internalK8s.GetDaemonSetPods(ctx, r.client, r.daemonSet, r.namespace, target.HostName)
		if err != nil {
			return nil, err
		}

		switch len(pods.Items) {
		case 0:
			klog.Infof("no %s on %s node", r.daemonSet, target.HostName)
		case 1:
			output, err := internalK8s.ExecInPod(
				ctx,
				r.client,
				r.config,
				pods.Items[0].Name,
				r.namespace,
				strings.Fields(command),
			)
			if err != nil {
				return nil, fmt.Errorf("failed to query NVL partition ID: %w", err)
			}
			outputs[target.HostName] = output.String()
		default:
			return nil, fmt.Errorf("expected 1 %s pod, got %d", r.daemonSet, len(pods.Items))
		}
	}

	return outputs, nil
}
