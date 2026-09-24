/*
 * Copyright 2026 NVIDIA CORPORATION
 * SPDX-License-Identifier: Apache-2.0
 */

package kwok

import (
	"fmt"
	"hash/fnv"
	"maps"
	"sort"
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"

	"github.com/dsx-ai-factory/topograph/pkg/models"
	"github.com/dsx-ai-factory/topograph/pkg/topology"
)

const (
	NodeSelectorKey   = "kwok.x-k8s.io/node"
	NodeSelectorValue = "fake"

	defaultCPU              = "96"
	defaultMemory           = "1024Gi"
	defaultPods             = "110"
	defaultEphemeralStorage = "10Gi"
	defaultGPUResourceName  = "nvidia.com/gpu"
)

type Capacity struct {
	CPU              string
	Memory           string
	Pods             string
	EphemeralStorage string
	GPUs             int
	GPUResourceName  string
}

func DefaultCapacity() Capacity {
	return Capacity{
		CPU:              defaultCPU,
		Memory:           defaultMemory,
		Pods:             defaultPods,
		EphemeralStorage: defaultEphemeralStorage,
		GPUResourceName:  defaultGPUResourceName,
	}
}

func NodesFromModel(model *models.Model, capacity Capacity) ([]*corev1.Node, error) {
	resources, err := capacity.ResourceList()
	if err != nil {
		return nil, err
	}

	hostNames := make([]string, 0, len(model.Nodes))
	for hostName := range model.Nodes {
		hostNames = append(hostNames, hostName)
	}
	sort.Strings(hostNames)

	nodes := make([]*corev1.Node, 0, len(hostNames))
	seenNodeNames := make(map[string]string, len(hostNames))
	for _, hostName := range hostNames {
		instance := model.Nodes[hostName]
		nodeName := kubernetesNodeName(hostName)
		if seenHostName, ok := seenNodeNames[nodeName]; ok {
			return nil, fmt.Errorf("model hostnames %q and %q resolve to duplicate Kubernetes node name %q", seenHostName, hostName, nodeName)
		}
		seenNodeNames[nodeName] = hostName

		labels := make(map[string]string, len(instance.Labels)+1)
		maps.Copy(labels, instance.Labels)
		labels[NodeSelectorKey] = NodeSelectorValue

		annotations := make(map[string]string, len(instance.Annotations)+3)
		maps.Copy(annotations, instance.Annotations)
		annotations[NodeSelectorKey] = NodeSelectorValue
		annotations[topology.KeyNodeInstance] = models.InstanceID(hostName)
		annotations[topology.KeyNodeRegion] = instance.Labels[models.LabelTopologyRegion]

		nodes = append(nodes, &corev1.Node{
			ObjectMeta: metav1.ObjectMeta{
				Name:        nodeName,
				Labels:      labels,
				Annotations: annotations,
			},
			Spec: corev1.NodeSpec{
				ProviderID: "kwok://" + nodeName,
			},
			Status: corev1.NodeStatus{
				Capacity:    resources.DeepCopy(),
				Allocatable: resources.DeepCopy(),
			},
		})
	}

	return nodes, nil
}

func MarshalNodeManifest(nodes []*corev1.Node) ([]byte, error) {
	items := make([]nodeManifestItem, 0, len(nodes))
	for _, node := range nodes {
		items = append(items, nodeManifestItem{
			APIVersion: "v1",
			Kind:       "Node",
			Metadata: nodeManifestMetadata{
				Name:        node.Name,
				Labels:      node.Labels,
				Annotations: node.Annotations,
			},
			Spec: nodeManifestSpec{
				ProviderID: node.Spec.ProviderID,
			},
			Status: nodeManifestStatus{
				Capacity:    resourceStrings(node.Status.Capacity),
				Allocatable: resourceStrings(node.Status.Allocatable),
			},
		})
	}

	return yaml.Marshal(nodeManifest{
		APIVersion: "v1",
		Kind:       "List",
		Items:      items,
	})
}

func (c Capacity) ResourceList() (corev1.ResourceList, error) {
	c = c.withDefaults()
	resources := corev1.ResourceList{}

	if err := setResource(resources, corev1.ResourceCPU, c.CPU); err != nil {
		return nil, err
	}
	if err := setResource(resources, corev1.ResourceMemory, c.Memory); err != nil {
		return nil, err
	}
	if err := setResource(resources, corev1.ResourcePods, c.Pods); err != nil {
		return nil, err
	}
	if err := setResource(resources, corev1.ResourceEphemeralStorage, c.EphemeralStorage); err != nil {
		return nil, err
	}
	if c.GPUs > 0 {
		resources[corev1.ResourceName(c.GPUResourceName)] = resource.MustParse(strconv.Itoa(c.GPUs))
	}

	return resources, nil
}

func (c Capacity) withDefaults() Capacity {
	defaults := DefaultCapacity()
	if c.CPU == "" {
		c.CPU = defaults.CPU
	}
	if c.Memory == "" {
		c.Memory = defaults.Memory
	}
	if c.Pods == "" {
		c.Pods = defaults.Pods
	}
	if c.EphemeralStorage == "" {
		c.EphemeralStorage = defaults.EphemeralStorage
	}
	if c.GPUResourceName == "" {
		c.GPUResourceName = defaults.GPUResourceName
	}
	return c
}

func setResource(resources corev1.ResourceList, name corev1.ResourceName, value string) error {
	quantity, err := resource.ParseQuantity(value)
	if err != nil {
		return fmt.Errorf("invalid %s quantity %q: %v", name, value, err)
	}
	resources[name] = quantity
	return nil
}

func kubernetesNodeName(name string) string {
	name = strings.ToLower(name)
	var b strings.Builder
	b.Grow(len(name))
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '.' {
			b.WriteRune(r)
			continue
		}
		b.WriteByte('-')
	}

	normalized := strings.Trim(b.String(), "-.")
	if normalized == "" {
		normalized = "node"
	}
	if len(normalized) <= 253 {
		return normalized
	}

	hash := fnv.New64a()
	_, _ = hash.Write([]byte(normalized))
	suffix := fmt.Sprintf("-%x", hash.Sum64())
	normalized = strings.TrimRight(normalized[:253-len(suffix)], "-.") + suffix
	return normalized
}

type nodeManifest struct {
	APIVersion string             `json:"apiVersion"`
	Kind       string             `json:"kind"`
	Items      []nodeManifestItem `json:"items"`
}

type nodeManifestItem struct {
	APIVersion string               `json:"apiVersion"`
	Kind       string               `json:"kind"`
	Metadata   nodeManifestMetadata `json:"metadata"`
	Spec       nodeManifestSpec     `json:"spec"`
	Status     nodeManifestStatus   `json:"status"`
}

type nodeManifestMetadata struct {
	Name        string            `json:"name"`
	Labels      map[string]string `json:"labels,omitempty"`
	Annotations map[string]string `json:"annotations,omitempty"`
}

type nodeManifestSpec struct {
	ProviderID string `json:"providerID,omitempty"`
}

type nodeManifestStatus struct {
	Capacity    map[string]string `json:"capacity,omitempty"`
	Allocatable map[string]string `json:"allocatable,omitempty"`
}

func resourceStrings(resources corev1.ResourceList) map[string]string {
	if len(resources) == 0 {
		return nil
	}
	values := make(map[string]string, len(resources))
	for name, quantity := range resources {
		values[string(name)] = quantity.String()
	}
	return values
}
