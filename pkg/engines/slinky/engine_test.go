/*
 * Copyright (c) 2024, NVIDIA CORPORATION.  All rights reserved.
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

package slinky

import (
	"bytes"
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
	k8stesting "k8s.io/client-go/testing"

	"github.com/dsx-ai-factory/topograph/pkg/engines/slurm"
	"github.com/dsx-ai-factory/topograph/pkg/models"
	"github.com/dsx-ai-factory/topograph/pkg/topology"
	"github.com/dsx-ai-factory/topograph/pkg/translate"
)

const testAcceleratorDomainSourceLabel = "example.com/accelerator-domain"

func TestGetParameters(t *testing.T) {
	podSelector := map[string]any{
		"matchLabels": map[string]string{"key": "value"},
	}
	nodeSelector := map[string]string{"key": "value"}
	invalidSelector := map[string]any{
		"matchExpressions": []metav1.LabelSelectorRequirement{
			{Operator: "BAD"},
		},
	}
	labelSelector := metav1.LabelSelector{
		MatchLabels: map[string]string{"key": "value"},
	}

	testCases := []struct {
		name   string
		params map[string]any
		ret    *Params
		err    string
	}{
		{
			name: "Case 1: no params",
			err:  `must specify engine parameter "`,
		},
		{
			name: "Case 2: missing key",
			params: map[string]any{
				topology.KeyTopoConfigmapName: "name",
				topology.KeyNamespace:         "namespace",
			},
			err: `must specify engine parameter "`,
		},
		{
			name: "Case 3: bad label selector",
			params: map[string]any{
				topology.KeyNamespace:         "namespace",
				topology.KeyPodSelector:       "BAD",
				topology.KeyTopoConfigPath:    "path",
				topology.KeyTopoConfigmapName: "name",
			},
			err: `could not decode configuration:`,
		},
		{
			name: "Case 4: invalid pod label selector",
			params: map[string]any{
				topology.KeyNamespace:         "namespace",
				topology.KeyPodSelector:       invalidSelector,
				topology.KeyTopoConfigPath:    "path",
				topology.KeyTopoConfigmapName: "name",
			},
			err: `"BAD" is not a valid label selector operator`,
		},
		{
			name: "Case 5: nil topology",
			params: map[string]any{
				topology.KeyNamespace:         "namespace",
				topology.KeyPodSelector:       podSelector,
				topology.KeyTopoConfigPath:    "path",
				topology.KeyTopoConfigmapName: "name",
				topology.KeyTopologies:        map[string]any{"topo": nil},
			},
			err: `topology "topo": nil entry`,
		},
		{
			name: "Case 6: invalid topology",
			params: map[string]any{
				topology.KeyNamespace:         "namespace",
				topology.KeyPodSelector:       podSelector,
				topology.KeyTopoConfigPath:    "path",
				topology.KeyTopoConfigmapName: "name",
				topology.KeyTopologies: map[string]any{
					"topo": map[string]any{
						"plugin":      topology.TopologyBlock,
						"blockSizes":  []int{16, 32},
						"nodes":       []string{"node1", "node2"},
						"podSelector": podSelector,
					},
				},
			},
			err: `topology "topo": cannot set both nodes and podSelector`,
		},
		{
			name: "Case 7: minimal valid input",
			params: map[string]any{
				topology.KeyNamespace:         "namespace",
				topology.KeyPodSelector:       podSelector,
				topology.KeyTopoConfigPath:    "path",
				topology.KeyTopoConfigmapName: "name",
			},
			ret: &Params{
				Namespace:     "namespace",
				PodSelector:   labelSelector,
				ConfigPath:    "path",
				ConfigMapName: "name",
				podListOpt:    &metav1.ListOptions{LabelSelector: "key=value"},
			},
		},
		{
			name: "Case 8: cluster-wide valid parameters",
			params: map[string]any{
				topology.KeyNamespace:    "namespace",
				topology.KeyPodSelector:  podSelector,
				topology.KeyNodeSelector: nodeSelector,
				topology.KeyPlugin:       topology.TopologyBlock,
				topology.KeyBlockSizes:   []int{16},
				topology.KeyBlockName: map[string]any{
					topology.KeyNodeNameRegexp: `^([^-]+)-`,
					topology.KeyFormat:         `${1}`,
				},
				topology.KeyTopoConfigPath:    "path",
				topology.KeyTopoConfigmapName: "name",
			},
			ret: &Params{
				BaseParams: slurm.BaseParams{
					Plugin:     topology.TopologyBlock,
					BlockSizes: []int{16},
					BlockName: &translate.BlockNameConfig{
						NodeNameRegexp: `^([^-]+)-`,
						Format:         `${1}`,
					},
				},
				Namespace:     "namespace",
				PodSelector:   labelSelector,
				NodeSelector:  nodeSelector,
				ConfigPath:    "path",
				ConfigMapName: "name",
				podListOpt:    &metav1.ListOptions{LabelSelector: "key=value"},
				nodeListOpt:   &metav1.ListOptions{LabelSelector: "key=value"},
			},
		},
		{
			name: "Case 9: per-partition valid parameters",
			params: map[string]any{
				topology.KeyNamespace:         "namespace",
				topology.KeyPodSelector:       podSelector,
				topology.KeyNodeSelector:      nodeSelector,
				topology.KeyTopoConfigPath:    "path",
				topology.KeyTopoConfigmapName: "name",
				topology.KeyTopologies: map[string]any{
					"topo1": map[string]any{
						"plugin":     topology.TopologyBlock,
						"blockSizes": []int{16, 32},
						"blockName": map[string]any{
							"nodeNameRegexp": `^(node)[0-9]+`,
							"format":         `${1}`,
						},
						"nodes": []string{"node1", "node2"},
					},
					"topo2": map[string]any{
						topology.KeyPlugin: topology.TopologyTree,
						"podSelector":      podSelector,
					},
				},
			},
			ret: &Params{
				Namespace:     "namespace",
				PodSelector:   labelSelector,
				NodeSelector:  nodeSelector,
				ConfigPath:    "path",
				ConfigMapName: "name",
				Topologies: map[string]*Topology{
					"topo1": {
						Topology: slurm.Topology{
							Plugin:     topology.TopologyBlock,
							BlockSizes: []int{16, 32},
							BlockName: &translate.BlockNameConfig{
								NodeNameRegexp: `^(node)[0-9]+`,
								Format:         `${1}`,
							},
							Nodes: []string{"node1", "node2"},
						},
					},
					"topo2": {
						Topology: slurm.Topology{
							Plugin: topology.TopologyTree,
						},
						PodSelector: labelSelector,
					},
				},
				podListOpt:  &metav1.ListOptions{LabelSelector: "key=value"},
				nodeListOpt: &metav1.ListOptions{LabelSelector: "key=value"},
			},
		},
		{
			name: "Case 10: accelerator domain source label",
			params: map[string]any{
				topology.KeyNamespace:          "namespace",
				topology.KeyPodSelector:        podSelector,
				topology.KeyTopoConfigPath:     "path",
				topology.KeyTopoConfigmapName:  "name",
				"acceleratorDomainSourceLabel": testAcceleratorDomainSourceLabel,
			},
			ret: &Params{
				Namespace:                    "namespace",
				PodSelector:                  labelSelector,
				ConfigPath:                   "path",
				ConfigMapName:                "name",
				AcceleratorDomainSourceLabel: testAcceleratorDomainSourceLabel,
				podListOpt:                   &metav1.ListOptions{LabelSelector: "key=value"},
			},
		},
		{
			name: "Case 11: reject invalid accelerator domain source label",
			params: map[string]any{
				topology.KeyNamespace:          "namespace",
				topology.KeyPodSelector:        podSelector,
				topology.KeyTopoConfigPath:     "path",
				topology.KeyTopoConfigmapName:  "name",
				"acceleratorDomainSourceLabel": "not a label",
			},
			err: `acceleratorDomainSourceLabel "not a label" is not a valid Kubernetes label key`,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			p, err := getParameters(tc.params)
			if len(tc.err) != 0 {
				require.ErrorContains(t, err, tc.err)
			} else {
				require.NoError(t, err)
				require.Equal(t, tc.ret, p)
			}
		})
	}
}

func TestGetComputeInstances(t *testing.T) {
	nodeErr1 := corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "err1"}}
	nodeErr2 := corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "err2", Annotations: map[string]string{topology.KeyNodeInstance: "instance"}}}
	node1 := corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "host1", Annotations: map[string]string{topology.KeyNodeInstance: "i1", topology.KeyNodeRegion: "r1"}}}
	node2 := corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "host2", Annotations: map[string]string{topology.KeyNodeInstance: "i2", topology.KeyNodeRegion: "r1"}}}
	node3 := corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "host3", Annotations: map[string]string{topology.KeyNodeInstance: "i3", topology.KeyNodeRegion: "r2"}}}
	nodeNone := corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "none"}}
	nodeMap := map[string]string{"host1": "node1", "host2": "node2", "host3": "node3", "err1": "node1", "err2": "node2"}

	testCases := []struct {
		name  string
		nodes *corev1.NodeList
		cis   []topology.ComputeInstances
		err   string
	}{
		{
			name:  "Case 1: instance error",
			nodes: &corev1.NodeList{Items: []corev1.Node{node1, nodeErr1}},
			cis: []topology.ComputeInstances{
				{
					Region:    "r1",
					Instances: map[string]string{"i1": "node1"},
				},
			},
		},
		{
			name:  "Case 2: region error",
			nodes: &corev1.NodeList{Items: []corev1.Node{nodeErr2, node2}},
			cis: []topology.ComputeInstances{
				{
					Region:    "r1",
					Instances: map[string]string{"i2": "node2"},
				},
			},
		},
		{
			name:  "Case 3: valid input",
			nodes: &corev1.NodeList{Items: []corev1.Node{node1, node2, node3, nodeNone}},
			cis: []topology.ComputeInstances{
				{
					Region:    "r1",
					Instances: map[string]string{"i1": "node1", "i2": "node2"},
				},
				{
					Region:    "r2",
					Instances: map[string]string{"i3": "node3"},
				},
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			cis, err := getComputeInstances(tc.nodes, nodeMap)
			if len(tc.err) != 0 {
				require.EqualError(t, err, tc.err)
			} else {
				require.Nil(t, err)
				require.Equal(t, tc.cis, cis)
			}
		})
	}
}

func TestResolveComputeInstancesCachesClusterNodesWhenOutputNeedsThem(t *testing.T) {
	const namespace = "slurm"
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{
		Name: "k8s-node",
	}}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "slurmd",
			Namespace: namespace,
			Labels:    map[string]string{topology.KeySlurmNodeName: "slurm-node"},
		},
		Spec: corev1.PodSpec{NodeName: node.Name},
		Status: corev1.PodStatus{Conditions: []corev1.PodCondition{{
			Type:   corev1.PodReady,
			Status: corev1.ConditionTrue,
		}}},
	}
	client := fake.NewSimpleClientset(node, pod)
	eng := &SlinkyEngine{
		client: client,
		params: &Params{
			Namespace:       namespace,
			UseDynamicNodes: true,
			nodeListOpt:     &metav1.ListOptions{},
			podListOpt:      &metav1.ListOptions{},
		},
	}
	instances := []topology.ComputeInstances{{
		Region:    "region",
		Instances: map[string]string{"instance": "slurm-node"},
	}}

	actual, httpErr := eng.ResolveComputeInstances(context.Background(), instances, nil)

	require.Nil(t, httpErr)
	require.Equal(t, instances, actual)
	require.NotNil(t, eng.cachedClusterNodes)
	client.ClearActions()

	cached, httpErr := eng.getClusterNodes(context.Background())
	require.Nil(t, httpErr)
	require.Same(t, eng.cachedClusterNodes, cached)
	require.Empty(t, client.Actions())
}

func TestResolveComputeInstancesDoesNotLoadNodesForSourceLabelAlone(t *testing.T) {
	client := fake.NewSimpleClientset()
	client.PrependReactor("list", "nodes", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, fmt.Errorf("unexpected Node list")
	})
	eng := &SlinkyEngine{
		client: client,
		params: &Params{
			AcceleratorDomainSourceLabel: testAcceleratorDomainSourceLabel,
			nodeListOpt:                  &metav1.ListOptions{},
			podListOpt:                   &metav1.ListOptions{},
		},
	}
	instances := []topology.ComputeInstances{{
		Region:    "region",
		Instances: map[string]string{"instance": "slurm-node"},
	}}

	actual, httpErr := eng.ResolveComputeInstances(context.Background(), instances, nil)

	require.Nil(t, httpErr)
	require.Equal(t, instances, actual)
	require.Nil(t, eng.cachedClusterNodes)
	require.Empty(t, client.Actions())
}

func TestWithLabelBackedDomains(t *testing.T) {
	ctx := context.Background()
	client := fake.NewSimpleClientset()

	nodes := []*corev1.Node{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:        "k8s-node-0",
				Labels:      map[string]string{testAcceleratorDomainSourceLabel: "clique-a"},
				Annotations: map[string]string{topology.KeyNodeInstance: "instance-0"},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:        "k8s-node-1",
				Labels:      map[string]string{testAcceleratorDomainSourceLabel: " clique-b "},
				Annotations: map[string]string{topology.KeyNodeInstance: "instance-1"},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:        "k8s-node-no-instance",
				Labels:      map[string]string{testAcceleratorDomainSourceLabel: "clique-c"},
				Annotations: map[string]string{},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:        "k8s-node-no-pod",
				Labels:      map[string]string{testAcceleratorDomainSourceLabel: "clique-d"},
				Annotations: map[string]string{topology.KeyNodeInstance: "instance-3"},
			},
		},
	}
	for _, node := range nodes {
		_, err := client.CoreV1().Nodes().Create(ctx, node, metav1.CreateOptions{})
		require.NoError(t, err)
	}

	for _, pod := range []*corev1.Pod{
		makeReadySlurmdPod("pod-0", "k8s-node-0", "slurm-0"),
		makeReadySlurmdPod("pod-1", "k8s-node-1", "slurm-1"),
		makeReadySlurmdPod("pod-no-instance", "k8s-node-no-instance", "slurm-no-instance"),
	} {
		_, err := client.CoreV1().Pods("test-ns").Create(ctx, pod, metav1.CreateOptions{})
		require.NoError(t, err)
	}

	existingDomains := topology.NewDomainMap()
	existingDomains.AddHost("provider-domain", "provider-instance", "provider-node")
	graph := &topology.Graph{
		Tiers:   &topology.Vertex{ID: "root"},
		Domains: existingDomains,
	}
	eng := &SlinkyEngine{
		client: client,
		params: &Params{
			Namespace:  "test-ns",
			podListOpt: &metav1.ListOptions{LabelSelector: "app=slinky"},
		},
	}

	clusterNodes, httpErr := eng.getClusterNodes(ctx)
	require.Nil(t, httpErr)
	got, httpErr := withLabelBackedDomains(graph, clusterNodes, testAcceleratorDomainSourceLabel)
	require.Nil(t, httpErr)
	require.NotSame(t, graph, got)
	require.Same(t, graph.Tiers, got.Tiers)

	expectedDomains := topology.NewDomainMap()
	expectedDomains.AddHost("clique-a", "instance-0", "slurm-0")
	expectedDomains.AddHost("clique-b", "instance-1", "slurm-1")
	require.Equal(t, expectedDomains, got.Domains)
	require.Equal(t, existingDomains, graph.Domains)
}

func TestWithLabelBackedDomainsNoMatchingNodes(t *testing.T) {
	ctx := context.Background()
	client := fake.NewSimpleClientset()

	_, err := client.CoreV1().Nodes().Create(ctx, &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "k8s-node-0",
			Annotations: map[string]string{topology.KeyNodeInstance: "instance-0"},
		},
	}, metav1.CreateOptions{})
	require.NoError(t, err)

	_, err = client.CoreV1().Pods("test-ns").Create(ctx, makeReadySlurmdPod("pod-0", "k8s-node-0", "slurm-0"), metav1.CreateOptions{})
	require.NoError(t, err)

	eng := &SlinkyEngine{
		client: client,
		params: &Params{
			Namespace:  "test-ns",
			podListOpt: &metav1.ListOptions{LabelSelector: "app=slinky"},
		},
	}

	clusterNodes, httpErr := eng.getClusterNodes(ctx)
	require.Nil(t, httpErr)
	got, httpErr := withLabelBackedDomains(&topology.Graph{}, clusterNodes, testAcceleratorDomainSourceLabel)
	require.Nil(t, got)
	require.ErrorContains(t, httpErr, `acceleratorDomainSourceLabel="example.com/accelerator-domain" produced no usable label-backed domains`)
	// The node maps to a SLURM node but has no configured source label.
	require.ErrorContains(t, httpErr, "Scanned 1 node(s)")
	require.ErrorContains(t, httpErr, fmt.Sprintf("1 missing the %q label", testAcceleratorDomainSourceLabel))
}

func TestWithLabelBackedDomainsNoSelectedNodes(t *testing.T) {
	// No Kubernetes nodes selected at all (e.g. a too-narrow nodeSelector) must
	// produce a distinct, actionable error rather than the generic no-match one.
	clusterNodes := &clusterNodes{
		nodes:   &corev1.NodeList{},
		nodeMap: map[string]string{},
	}

	got, httpErr := withLabelBackedDomains(&topology.Graph{}, clusterNodes, testAcceleratorDomainSourceLabel)
	require.Nil(t, got)
	require.NotNil(t, httpErr)
	require.ErrorContains(t, httpErr, "no selected Kubernetes nodes found; check engine nodeSelector")
}

func TestWithLabelBackedDomainsMissingBrokerAnnotation(t *testing.T) {
	ctx := context.Background()
	client := fake.NewSimpleClientset()

	// Each Node has the configured source label but an unusable
	// node-data-broker-written instance annotation.
	for index, annotations := range []map[string]string{
		nil,
		{topology.KeyNodeInstance: ""},
		{topology.KeyNodeInstance: " \t "},
	} {
		nodeName := fmt.Sprintf("k8s-node-%d", index)
		_, err := client.CoreV1().Nodes().Create(ctx, &corev1.Node{
			ObjectMeta: metav1.ObjectMeta{
				Name:        nodeName,
				Labels:      map[string]string{testAcceleratorDomainSourceLabel: "clique-a"},
				Annotations: annotations,
			},
		}, metav1.CreateOptions{})
		require.NoError(t, err)

		_, err = client.CoreV1().Pods("test-ns").Create(ctx,
			makeReadySlurmdPod(fmt.Sprintf("pod-%d", index), nodeName, fmt.Sprintf("slurm-%d", index)),
			metav1.CreateOptions{})
		require.NoError(t, err)
	}

	eng := &SlinkyEngine{
		client: client,
		params: &Params{
			Namespace:  "test-ns",
			podListOpt: &metav1.ListOptions{LabelSelector: "app=slinky"},
		},
	}

	clusterNodes, httpErr := eng.getClusterNodes(ctx)
	require.Nil(t, httpErr)
	got, httpErr := withLabelBackedDomains(&topology.Graph{}, clusterNodes, testAcceleratorDomainSourceLabel)
	require.Nil(t, got)
	require.ErrorContains(t, httpErr, fmt.Sprintf("3 with the label but a missing/empty %q annotation", topology.KeyNodeInstance))
	require.ErrorContains(t, httpErr, "nodes missing annotation: k8s-node-0, k8s-node-1, k8s-node-2")
}

func TestGenerateOutputUsesConfiguredAcceleratorDomainSource(t *testing.T) {
	ctx := context.Background()
	client := fake.NewSimpleClientset()

	for _, node := range []*corev1.Node{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:        "k8s-node-0",
				Labels:      map[string]string{testAcceleratorDomainSourceLabel: "clique-a"},
				Annotations: map[string]string{topology.KeyNodeInstance: "instance-0"},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:        "k8s-node-1",
				Labels:      map[string]string{testAcceleratorDomainSourceLabel: "clique-b"},
				Annotations: map[string]string{topology.KeyNodeInstance: "instance-1"},
			},
		},
	} {
		_, err := client.CoreV1().Nodes().Create(ctx, node, metav1.CreateOptions{})
		require.NoError(t, err)
	}

	for _, pod := range []*corev1.Pod{
		makeReadySlurmdPod("pod-0", "k8s-node-0", "alpha"),
		makeReadySlurmdPod("pod-1", "k8s-node-1", "beta"),
	} {
		_, err := client.CoreV1().Pods("test-ns").Create(ctx, pod, metav1.CreateOptions{})
		require.NoError(t, err)
	}

	providerDomains := topology.NewDomainMap()
	providerDomains.AddHostInfo(&topology.HostInfo{
		Domain:     "provider-domain",
		SubDomain:  "provider-sub-domain-a",
		InstanceID: "instance-0",
		HostName:   "alpha",
	})
	providerDomains.AddHostInfo(&topology.HostInfo{
		Domain:     "provider-domain",
		SubDomain:  "provider-sub-domain-b",
		InstanceID: "instance-1",
		HostName:   "beta",
	})

	eng := &SlinkyEngine{
		client: client,
		params: &Params{
			BaseParams: slurm.BaseParams{
				Plugin:     topology.TopologyBlock,
				BlockSizes: []int{1},
			},
			Namespace:                    "test-ns",
			ConfigMapName:                "slurm-config",
			ConfigPath:                   "topology.conf",
			AcceleratorDomainSourceLabel: testAcceleratorDomainSourceLabel,
			podListOpt:                   &metav1.ListOptions{LabelSelector: "app=slinky"},
		},
	}

	result, httpErr := eng.GenerateOutput(ctx, &topology.Graph{Domains: providerDomains}, nil)
	require.Nil(t, httpErr)
	require.Equal(t, []byte("OK\n"), result)

	cm, err := client.CoreV1().ConfigMaps("test-ns").Get(ctx, "slurm-config", metav1.GetOptions{})
	require.NoError(t, err)
	require.Equal(t, `# block001=clique-a
BlockName=block001 Nodes=alpha
# block002=clique-b
BlockName=block002 Nodes=beta
BlockSizes=1
`, cm.Data["topology.conf"])
}

func TestGenerateOutputUsesProviderDomainsWhenSourceIsOmitted(t *testing.T) {
	ctx := context.Background()
	client := fake.NewSimpleClientset()
	for _, resource := range []string{"nodes", "pods"} {
		client.PrependReactor("list", resource, func(k8stesting.Action) (bool, runtime.Object, error) {
			return true, nil, fmt.Errorf("unexpected Kubernetes list")
		})
	}
	providerDomains := topology.NewDomainMap()
	providerDomains.AddHost("provider-domain-a", "instance-0", "alpha")
	providerDomains.AddHost("provider-domain-b", "instance-1", "beta")

	eng := &SlinkyEngine{
		client: client,
		params: &Params{
			BaseParams: slurm.BaseParams{
				Plugin:     topology.TopologyBlock,
				BlockSizes: []int{1},
			},
			Namespace:     "test-ns",
			ConfigMapName: "slurm-config",
			ConfigPath:    "topology.conf",
		},
	}

	result, httpErr := eng.GenerateOutput(ctx, &topology.Graph{Domains: providerDomains}, nil)
	require.Nil(t, httpErr)
	require.Equal(t, []byte("OK\n"), result)

	cm, err := client.CoreV1().ConfigMaps("test-ns").Get(ctx, "slurm-config", metav1.GetOptions{})
	require.NoError(t, err)
	require.Equal(t, `# block001=provider-domain-a
BlockName=block001 Nodes=alpha
# block002=provider-domain-b
BlockName=block002 Nodes=beta
BlockSizes=1
`, cm.Data["topology.conf"])
	require.Zero(t, countClientActions(client.Actions(), "list", "nodes"))
	require.Zero(t, countClientActions(client.Actions(), "list", "pods"))
}

func TestGenerateOutputDoesNotLoadNodesForNonBlockTopology(t *testing.T) {
	testCases := []struct {
		name       string
		baseParams slurm.BaseParams
		topologies map[string]*Topology
	}{
		{
			name:       "tree",
			baseParams: slurm.BaseParams{Plugin: topology.TopologyTree},
		},
		{
			name: "flat",
			topologies: map[string]*Topology{
				"default": {
					Topology: slurm.Topology{
						Plugin:  topology.TopologyFlat,
						Default: true,
					},
				},
			},
		},
	}

	model, err := models.NewModelFromFile("small-tree.yaml")
	require.NoError(t, err)
	instanceToNode := make(map[string]string, len(model.Nodes))
	for hostName := range model.Nodes {
		instanceToNode[fmt.Sprintf("i-%s", hostName)] = hostName
	}
	graph, _ := model.ToGraph([]topology.ComputeInstances{{Instances: instanceToNode}})

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			client := fake.NewSimpleClientset()
			for _, resource := range []string{"nodes", "pods"} {
				client.PrependReactor("list", resource, func(k8stesting.Action) (bool, runtime.Object, error) {
					return true, nil, fmt.Errorf("unexpected Kubernetes list")
				})
			}
			eng := &SlinkyEngine{
				client: client,
				params: &Params{
					BaseParams:                   tc.baseParams,
					Topologies:                   tc.topologies,
					AcceleratorDomainSourceLabel: testAcceleratorDomainSourceLabel,
					ConfigUpdateMode:             ConfigUpdateModeNone,
				},
			}

			result, httpErr := eng.GenerateOutput(context.Background(), graph, nil)

			require.Nil(t, httpErr)
			require.Equal(t, []byte("OK\n"), result)
			require.Empty(t, client.Actions())
		})
	}
}

func TestUsesBlockTopology(t *testing.T) {
	require.False(t, usesBlockTopology(nil))
	require.False(t, usesBlockTopology(&translate.Config{Plugin: topology.TopologyTree}))
	require.True(t, usesBlockTopology(&translate.Config{Plugin: topology.TopologyBlock}))
	require.True(t, usesBlockTopology(&translate.Config{
		Topologies: map[string]*translate.TopologySpec{
			"block": {Plugin: topology.TopologyBlock},
		},
	}))
	require.False(t, usesBlockTopology(&translate.Config{
		Topologies: map[string]*translate.TopologySpec{
			"flat": {Plugin: topology.TopologyFlat},
			"nil":  nil,
		},
	}))
}

func makeReadySlurmdPod(name, nodeName, slurmName string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "test-ns",
			Labels: map[string]string{
				"app":                     "slinky",
				topology.KeySlurmNodeName: slurmName,
			},
		},
		Spec: corev1.PodSpec{
			NodeName: nodeName,
			Containers: []corev1.Container{
				{Name: "test", Image: "test"},
			},
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			Conditions: []corev1.PodCondition{
				{
					Type:   corev1.PodReady,
					Status: corev1.ConditionTrue,
				},
			},
		},
	}
}

func TestResolveSlurmNodeName(t *testing.T) {
	testCases := []struct {
		name string
		pod  *corev1.Pod
		want string
	}{
		{
			name: "label takes precedence over hostname",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{topology.KeySlurmNodeName: "slurm-node-1"}},
				Spec:       corev1.PodSpec{Hostname: "host-1"},
			},
			want: "slurm-node-1",
		},
		{
			name: "present but empty label yields empty, no hostname fallback",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{topology.KeySlurmNodeName: ""}},
				Spec:       corev1.PodSpec{Hostname: "host-1"},
			},
			want: "",
		},
		{
			name: "falls back to hostname when label absent",
			pod:  &corev1.Pod{Spec: corev1.PodSpec{Hostname: "host-1"}},
			want: "host-1",
		},
		{
			name: "empty when neither label nor hostname set",
			pod:  &corev1.Pod{},
			want: "",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, resolveSlurmNodeName(tc.pod))
		})
	}
}

func TestGetClusterNodes(t *testing.T) {
	namespace := "test-ns"
	podSel := metav1.LabelSelector{MatchLabels: map[string]string{"app": "slinky"}}
	sel, err := metav1.LabelSelectorAsSelector(&podSel)
	require.NoError(t, err)

	readyCond := []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}
	pod := func(name, nodeName, slurmLabel, hostname string, ready bool) *corev1.Pod {
		labels := map[string]string{"app": "slinky"}
		if slurmLabel != "" {
			labels[topology.KeySlurmNodeName] = slurmLabel
		}
		status := corev1.PodStatus{Phase: corev1.PodRunning}
		if ready {
			status.Conditions = readyCond
		}
		return &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, Labels: labels},
			Spec:       corev1.PodSpec{NodeName: nodeName, Hostname: hostname},
			Status:     status,
		}
	}

	// pod-empty-label carries the slurm.node.name label with an empty value (distinct
	// from the label being absent): resolveSlurmNodeName must not fall back to the
	// hostname, so the guard still skips it.
	emptyLabelPod := pod("pod-empty-label", "k8s-5", "", "host-5", true)
	emptyLabelPod.Labels[topology.KeySlurmNodeName] = ""

	client := fake.NewSimpleClientset(
		pod("pod-label", "k8s-1", "slurm-1", "", true),     // mapped via label
		pod("pod-hostname", "k8s-2", "", "slurm-2", true),  // mapped via hostname fallback
		pod("pod-empty", "k8s-3", "", "", true),            // skipped: ready but no SLURM name (the guard)
		pod("pod-notready", "k8s-4", "slurm-4", "", false), // skipped: not Ready
		emptyLabelPod, // skipped: label present but empty (no hostname fallback)
	)
	eng := &SlinkyEngine{
		client: client,
		params: &Params{
			Namespace:   namespace,
			podListOpt:  &metav1.ListOptions{LabelSelector: sel.String()},
			nodeListOpt: &metav1.ListOptions{},
		},
	}

	clusterNodes, httpErr := eng.getClusterNodes(context.Background())
	require.Nil(t, httpErr)
	require.Equal(t, map[string]string{"k8s-1": "slurm-1", "k8s-2": "slurm-2"}, clusterNodes.nodeMap)
}

// Helper for annotation checks
func requireAnnotation(t *testing.T, annotations map[string]string, key, expected string) {
	val, ok := annotations[key]
	require.True(t, ok, "annotation %s should exist", key)
	require.Equal(t, expected, val, "annotation %s should have correct value", key)
}

func TestConfigMapAnnotationsAndMetadata(t *testing.T) {
	labelSelector := metav1.LabelSelector{
		MatchLabels: map[string]string{"app.kubernetes.io/component": "compute"},
	}
	testCases := []struct {
		name       string
		params     *Params
		wantPlugin bool
		wantBlock  bool
	}{
		{
			name: "minimal params, no plugin/block",
			params: &Params{
				Namespace:     "test-namespace",
				PodSelector:   labelSelector,
				ConfigPath:    "topology.conf",
				ConfigMapName: "slurm-topology",
			},
			wantPlugin: false, wantBlock: false,
		},
		{
			name: "with plugin only",
			params: &Params{Namespace: "test-namespace",
				BaseParams: slurm.BaseParams{
					Plugin: topology.TopologyBlock,
				},
				PodSelector:   labelSelector,
				ConfigPath:    "topology.conf",
				ConfigMapName: "slurm-topology",
			},
			wantPlugin: true, wantBlock: false,
		},
		{
			name: "with block sizes only",
			params: &Params{
				BaseParams: slurm.BaseParams{
					BlockSizes: []int{8, 16, 32},
				},
				Namespace:     "test-namespace",
				PodSelector:   labelSelector,
				ConfigPath:    "topology.conf",
				ConfigMapName: "slurm-topology",
			},
			wantPlugin: false, wantBlock: true,
		},
		{
			name: "with plugin and block sizes",
			params: &Params{
				BaseParams: slurm.BaseParams{
					Plugin:     topology.TopologyBlock,
					BlockSizes: []int{8, 16, 32},
				},
				Namespace:     "test-namespace",
				PodSelector:   labelSelector,
				ConfigPath:    "topology.conf",
				ConfigMapName: "slurm-topology",
			},
			wantPlugin: true, wantBlock: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			engine := &SlinkyEngine{params: tc.params}
			annotations := engine.generateConfigMapAnnotations()

			// Required annotation checks
			requireAnnotation(t, annotations, topology.KeyConfigMapEngine, NAME)
			requireAnnotation(t, annotations, topology.KeyConfigMapTopologyManagedBy, "topograph")
			requireAnnotation(t, annotations, topology.KeyConfigMapNamespace, tc.params.Namespace)
			timestamp, ok := annotations[topology.KeyConfigMapLastUpdated]
			require.True(t, ok)
			_, err := time.Parse(time.RFC3339, timestamp)
			require.NoError(t, err)

			if tc.wantPlugin {
				requireAnnotation(t, annotations, topology.KeyConfigMapPlugin, tc.params.Plugin)
			} else {
				require.NotContains(t, annotations, topology.KeyConfigMapPlugin)
			}
			if tc.wantBlock {
				requireAnnotation(t, annotations, topology.KeyConfigMapBlockSizes, intToStr(tc.params.BlockSizes))
			} else {
				require.NotContains(t, annotations, topology.KeyConfigMapBlockSizes)
			}
		})
	}
}

const (
	//medium.yaml - tree topology skeleton
	mediumTreeTopologyYamlSkeleton = `- topology: topo-0
  cluster_default: false
  tree:
    switches:
        - switch: sw3
          children: sw[21-22]
        - switch: sw21
          children: sw11
        - switch: sw22
          children: sw14
        - switch: sw11
        - switch: sw14
`
	//medium.yaml - full tree topology
	mediumTreeTopologyYamlFull = `- topology: topo-0
  cluster_default: false
  tree:
    switches:
        - switch: sw3
          children: sw[21-22]
        - switch: sw21
          children: sw11
        - switch: sw22
          children: sw14
        - switch: sw11
          nodes: "1101"
        - switch: sw14
          nodes: "1402"
`
	//medium.yaml - block topology skeleton
	mediumBlockTopologyYamlSkeleton = `- topology: topo-0
  cluster_default: false
  block:
    block_sizes:
        - 1
        - 2
    blocks:
        - block: block1
        - block: block2
`
	//medium.yaml - full block topology
	mediumBlockTopologyYamlFull = `- topology: topo-0
  cluster_default: false
  block:
    block_sizes:
        - 1
        - 2
    blocks:
        - block: block1
          nodes: "1101"
        - block: block2
          nodes: "1301"
`
	//medium.yaml - combined topology skeleton
	mediumCombinedTopologyYamlSkeleton = `- topology: topo-0
  cluster_default: false
  tree:
    switches:
        - switch: sw3
          children: sw[21-22]
        - switch: sw21
          children: sw11
        - switch: sw22
          children: sw13
        - switch: sw11
        - switch: sw13
- topology: topo-1
  cluster_default: false
  block:
    block_sizes:
        - 1
        - 2
    blocks:
        - block: block1
        - block: block2
`
	//medium.yaml - combined topology full
	mediumCombinedTopologyYamlFull = `- topology: topo-0
  cluster_default: false
  tree:
    switches:
        - switch: sw3
          children: sw[21-22]
        - switch: sw21
          children: sw11
        - switch: sw22
          children: sw13
        - switch: sw11
          nodes: "1101"
        - switch: sw13
          nodes: "1302"
- topology: topo-1
  cluster_default: false
  block:
    block_sizes:
        - 1
        - 2
    blocks:
        - block: block1
          nodes: "1101"
        - block: block2
          nodes: "1302"
`
	noUpdateConfigMap = `existing: topology`
)

// slurmTopologiesForDynamicTest builds per-partition slurm.Topology entries for BaseParams.Topologies.
// Each entry includes podSelector under Other (seeRemain) for getPartitionNodes, matching engine decoding in getPartitionNodes.
func slurmTopologiesForDynamicTest(plugins []string) map[string]*Topology {
	out := make(map[string]*Topology, len(plugins))
	for i, plugin := range plugins {
		key := fmt.Sprintf("topo-%d", i)
		out[key] = &Topology{
			Topology: slurm.Topology{
				Plugin:    plugin,
				Partition: key,
			},
			PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{"app": "slinky"}},
		}
	}
	return out
}

func TestGetPartitionNodes(t *testing.T) {
	const namespace = "slurm"

	// A controller pod that is not running, so getPartitionNodes never reaches
	// ExecInPod and exercises the no-running-pods path deterministically.
	pendingControllerClient := func() *fake.Clientset {
		return fake.NewSimpleClientset(&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "slurm-controller-0",
				Namespace: namespace,
				Labels:    map[string]string{slurmComponentLabelKey: slurmComponentController},
			},
			Status: corev1.PodStatus{Phase: corev1.PodPending},
		})
	}

	testCases := []struct {
		name            string
		useDynamicNodes bool
		client          *fake.Clientset
		params          []any
		want            string
		errMsg          string
	}{
		{
			name:            "dynamic nodes short-circuits without listing pods",
			useDynamicNodes: true,
			client:          fake.NewSimpleClientset(),
			params:          []any{namespace},
			want:            dynamicShowPartitionNodes,
		},
		{
			name:   "wrong parameter count",
			client: fake.NewSimpleClientset(),
			params: []any{namespace, "extra"},
			errMsg: "expects a namespace as a parameter",
		},
		{
			name:   "non-string parameter",
			client: fake.NewSimpleClientset(),
			params: []any{42},
			errMsg: "expects a string parameter",
		},
		{
			name:   "no running controller or login pods",
			client: pendingControllerClient(),
			params: []any{namespace},
			errMsg: "no running controller or login pods found for partition discovery",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			eng := &SlinkyEngine{
				client: tc.client,
				params: &Params{Namespace: namespace, UseDynamicNodes: tc.useDynamicNodes},
			}

			got, err := eng.getPartitionNodes(context.Background(), "gpu", tc.params)
			if tc.errMsg != "" {
				require.Error(t, err)
				require.Contains(t, err.Error(), tc.errMsg)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

// runningComponentPod builds a Running pod labeled for the given Slurm component.
func runningComponentPod(name, namespace, component string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels:    map[string]string{slurmComponentLabelKey: component},
		},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}
}

// stubExecInPod overrides the execInPod seam for the duration of a test.
func stubExecInPod(t *testing.T, fn func(podName string) (string, error)) {
	t.Helper()
	orig := execInPod
	execInPod = func(_ context.Context, _ kubernetes.Interface, _ *rest.Config, name, _ string, _ []string) (*bytes.Buffer, error) {
		out, err := fn(name)
		if err != nil {
			return nil, err
		}
		return bytes.NewBufferString(out), nil
	}
	t.Cleanup(func() { execInPod = orig })
}

func TestGetPartitionNodesControllerListErrorFallsBackToLogin(t *testing.T) {
	const namespace = "slurm"

	// Login pod exists and is running; the controller listing fails (e.g. RBAC
	// scoped to login only). Discovery must fall back to the login pod.
	client := fake.NewSimpleClientset(runningComponentPod("slurm-login-0", namespace, slurmComponentLogin))
	client.PrependReactor("list", "pods", func(action k8stesting.Action) (bool, runtime.Object, error) {
		if la, ok := action.(k8stesting.ListAction); ok {
			if sel := la.GetListRestrictions().Labels; sel != nil && sel.String() == slurmComponentLabelKey+"="+slurmComponentController {
				return true, nil, fmt.Errorf("forbidden: cannot list controller pods")
			}
		}
		return false, nil, nil
	})

	stubExecInPod(t, func(podName string) (string, error) {
		require.Equal(t, "slurm-login-0", podName)
		return "PartitionName=gpu Nodes=node[0-3]", nil
	})

	eng := &SlinkyEngine{client: client, params: &Params{Namespace: namespace}}
	got, err := eng.getPartitionNodes(context.Background(), "gpu", []any{namespace})
	require.NoError(t, err)
	require.Equal(t, "PartitionName=gpu Nodes=node[0-3]", got)
}

func TestGetPartitionNodesControllerExecErrorFallsBackToLogin(t *testing.T) {
	const namespace = "slurm"

	// Both a controller and a login pod are running, but exec fails on the
	// controller (e.g. pods/exec allowed only on login). Discovery must not
	// abort - it should fall back to the login pod.
	client := fake.NewSimpleClientset(
		runningComponentPod("slurm-controller-0", namespace, slurmComponentController),
		runningComponentPod("slurm-login-0", namespace, slurmComponentLogin),
	)

	stubExecInPod(t, func(podName string) (string, error) {
		if podName == "slurm-controller-0" {
			return "", fmt.Errorf("forbidden: cannot exec into controller pod")
		}
		return "PartitionName=gpu Nodes=node[0-3]", nil
	})

	eng := &SlinkyEngine{client: client, params: &Params{Namespace: namespace}}
	got, err := eng.getPartitionNodes(context.Background(), "gpu", []any{namespace})
	require.NoError(t, err)
	require.Equal(t, "PartitionName=gpu Nodes=node[0-3]", got)
}

func TestGetPartitionNodesAllExecFail(t *testing.T) {
	const namespace = "slurm"

	client := fake.NewSimpleClientset(
		runningComponentPod("slurm-controller-0", namespace, slurmComponentController),
		runningComponentPod("slurm-login-0", namespace, slurmComponentLogin),
	)

	stubExecInPod(t, func(_ string) (string, error) {
		return "", fmt.Errorf("boom")
	})

	eng := &SlinkyEngine{client: client, params: &Params{Namespace: namespace}}
	_, err := eng.getPartitionNodes(context.Background(), "gpu", []any{namespace})
	require.Error(t, err)
	require.Contains(t, err.Error(), "partition discovery failed on all")
	require.Contains(t, err.Error(), "slurm-controller-0")
	require.Contains(t, err.Error(), "slurm-login-0")
}

func TestGenerateDynamicNodesOutput(t *testing.T) {
	slinkyPodSel := metav1.LabelSelector{MatchLabels: map[string]string{"app": "slinky"}}

	fakeSuccessClient := func(slurmNames []string, createConfigMap bool) *fake.Clientset {
		client := fake.NewSimpleClientset()
		for i, slurmName := range slurmNames {
			// Add nodes
			node1 := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: fmt.Sprintf("k8s-node-%d", i),
				},
				Spec:   corev1.NodeSpec{},
				Status: corev1.NodeStatus{},
			}
			_, err := client.CoreV1().Nodes().Create(context.Background(), node1, metav1.CreateOptions{})
			require.NoError(t, err)

			// Add pods
			pod1 := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      fmt.Sprintf("k8s-pod-%d", i),
					Namespace: "test-ns",
					Labels: map[string]string{
						"app":             "slinky",
						"slurm.node.name": slurmName,
					},
				},
				Spec: corev1.PodSpec{
					NodeName: fmt.Sprintf("k8s-node-%d", i),
					Containers: []corev1.Container{
						{Name: "test", Image: "test"},
					},
				},
				Status: corev1.PodStatus{
					Phase: corev1.PodRunning,

					Conditions: []corev1.PodCondition{
						{
							Type:   corev1.PodReady,
							Status: corev1.ConditionTrue,
						},
					},
				},
			}
			_, err = client.CoreV1().Pods("test-ns").Create(context.Background(), pod1, metav1.CreateOptions{})
			require.NoError(t, err)
		}
		// Add config map
		if createConfigMap {
			cm := &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "slurm-config",
					Namespace: "test-ns",
				},
				Data: map[string]string{
					"topology.yaml": "existing: topology",
				},
			}
			_, err := client.CoreV1().ConfigMaps("test-ns").Create(context.Background(), cm, metav1.CreateOptions{})
			require.NoError(t, err)
		}

		return client
	}

	testCases := []struct {
		name                  string
		k8sClient             func([]string, bool) *fake.Clientset
		createConfigMap       bool
		topologyFile          string
		topologyConfig        []string
		slurmName             []string
		slurmConfigUpdateMode string
		expectTopologyYaml    string
		expectTopologySpec    []string
		expectError           bool
		errorMsg              string
	}{
		{
			name:                  "successful dynamic nodes for tree topology with skeleton only update",
			k8sClient:             fakeSuccessClient,
			createConfigMap:       true,
			topologyFile:          "medium.yaml",
			topologyConfig:        []string{topology.TopologyTree},
			slurmName:             []string{"1101", "1402"},
			slurmConfigUpdateMode: "skeleton-only",
			expectTopologyYaml:    mediumTreeTopologyYamlSkeleton,
			expectTopologySpec:    []string{"topo-0:sw3:sw21:sw11", "topo-0:sw3:sw22:sw14"},
			expectError:           false,
		},
		{
			name:               "successful dynamic nodes for tree topology with full update",
			k8sClient:          fakeSuccessClient,
			createConfigMap:    true,
			topologyFile:       "medium.yaml",
			topologyConfig:     []string{topology.TopologyTree},
			slurmName:          []string{"1101", "1402"},
			expectTopologyYaml: mediumTreeTopologyYamlFull,
			expectTopologySpec: []string{"topo-0:sw3:sw21:sw11", "topo-0:sw3:sw22:sw14"},
			expectError:        false,
		},
		{
			name:                  "successful dynamic nodes for tree topology with no update",
			k8sClient:             fakeSuccessClient,
			createConfigMap:       true,
			topologyFile:          "medium.yaml",
			topologyConfig:        []string{topology.TopologyTree},
			slurmName:             []string{"1101", "1402"},
			slurmConfigUpdateMode: "none",
			expectTopologyYaml:    noUpdateConfigMap,
			expectTopologySpec:    []string{"topo-0:sw3:sw21:sw11", "topo-0:sw3:sw22:sw14"},
			expectError:           false,
		},
		{
			name:                  "successful dynamic nodes for block topology with skeleton only update",
			k8sClient:             fakeSuccessClient,
			createConfigMap:       true,
			topologyFile:          "medium.yaml",
			topologyConfig:        []string{topology.TopologyBlock},
			slurmName:             []string{"1101", "1301"},
			slurmConfigUpdateMode: "skeleton-only",
			expectTopologyYaml:    mediumBlockTopologyYamlSkeleton,
			expectTopologySpec:    []string{"topo-0:block1", "topo-0:block2"},
			expectError:           false,
		},
		{
			name:               "successful dynamic nodes for block topology with full update",
			k8sClient:          fakeSuccessClient,
			createConfigMap:    true,
			topologyFile:       "medium.yaml",
			topologyConfig:     []string{topology.TopologyBlock},
			slurmName:          []string{"1101", "1301"},
			expectTopologyYaml: mediumBlockTopologyYamlFull,
			expectTopologySpec: []string{"topo-0:block1", "topo-0:block2"},
			expectError:        false,
		},
		{
			name:                  "successful dynamic nodes for block topology with no update",
			k8sClient:             fakeSuccessClient,
			createConfigMap:       true,
			topologyFile:          "medium.yaml",
			topologyConfig:        []string{topology.TopologyBlock},
			slurmName:             []string{"1101", "1301"},
			slurmConfigUpdateMode: "none",
			expectTopologyYaml:    noUpdateConfigMap,
			expectTopologySpec:    []string{"topo-0:block1", "topo-0:block2"},
			expectError:           false,
		},
		{
			name:                  "successful dynamic nodes for combined topology with skeleton only update",
			k8sClient:             fakeSuccessClient,
			createConfigMap:       false,
			topologyFile:          "medium.yaml",
			topologyConfig:        []string{topology.TopologyTree, topology.TopologyBlock},
			slurmName:             []string{"1101", "1302"},
			slurmConfigUpdateMode: "skeleton-only",
			expectTopologyYaml:    mediumCombinedTopologyYamlSkeleton,
			expectTopologySpec:    []string{"topo-0:sw3:sw21:sw11,topo-1:block1", "topo-0:sw3:sw22:sw13,topo-1:block2"},
			expectError:           false,
		},
		{
			name:               "successful dynamic nodes for combined topology with full update",
			k8sClient:          fakeSuccessClient,
			createConfigMap:    false,
			topologyFile:       "medium.yaml",
			topologyConfig:     []string{topology.TopologyTree, topology.TopologyBlock},
			slurmName:          []string{"1101", "1302"},
			expectTopologyYaml: mediumCombinedTopologyYamlFull,
			expectTopologySpec: []string{"topo-0:sw3:sw21:sw11,topo-1:block1", "topo-0:sw3:sw22:sw13,topo-1:block2"},
			expectError:        false,
		},
		{
			name: "error getting pods",
			k8sClient: func([]string, bool) *fake.Clientset {
				client := fake.NewSimpleClientset()
				client.PrependReactor("list", "pods", func(action k8stesting.Action) (bool, runtime.Object, error) {
					return true, nil, errors.NewInternalError(fmt.Errorf("failed to list pods"))
				})
				return client
			},
			topologyFile:   "medium.yaml",
			topologyConfig: []string{topology.TopologyTree},
			expectError:    true,
			errorMsg:       `topology "topo-0": failed to list pods with selector "app=slinky": Internal error occurred: failed to list pods`,
		},
		{
			name: "error getting config map",
			k8sClient: func(_ []string, _ bool) *fake.Clientset {
				client := fakeSuccessClient([]string{"1101", "1402"}, true)
				client.PrependReactor("get", "configmaps", func(action k8stesting.Action) (bool, runtime.Object, error) {
					return true, nil, errors.NewInternalError(fmt.Errorf("failed to get config map"))
				})
				return client
			},
			topologyFile:   "medium.yaml",
			topologyConfig: []string{topology.TopologyTree},
			expectError:    true,
			errorMsg:       "failed to get config map",
		},
		{
			name: "error patching node",
			k8sClient: func(slurmNames []string, createConfigMap bool) *fake.Clientset {
				client := fakeSuccessClient(slurmNames, createConfigMap)
				client.PrependReactor("patch", "nodes", func(action k8stesting.Action) (bool, runtime.Object, error) {
					return true, nil, errors.NewInternalError(fmt.Errorf("failed to patch node"))
				})
				return client
			},
			createConfigMap: true,
			topologyFile:    "medium.yaml",
			topologyConfig:  []string{topology.TopologyTree},
			slurmName:       []string{"1101", "1402"},
			expectError:     true,
			errorMsg:        "failed to patch node annotation",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			client := tc.k8sClient(tc.slurmName, tc.createConfigMap)

			model, err := models.NewModelFromFile(tc.topologyFile)
			require.NoError(t, err)
			instance2node := make(map[string]string, len(model.Nodes))
			for hostName := range model.Nodes {
				instance2node[fmt.Sprintf("i-%s", hostName)] = hostName
			}
			topo, _ := model.ToGraph([]topology.ComputeInstances{{Instances: instance2node}})

			podListSel, err := metav1.LabelSelectorAsSelector(&slinkyPodSel)
			require.NoError(t, err)
			podListOpt := &metav1.ListOptions{LabelSelector: podListSel.String()}

			params := &Params{
				Namespace:        "test-ns",
				ConfigMapName:    "slurm-config",
				ConfigPath:       "topology.yaml",
				PodSelector:      slinkyPodSel,
				UseDynamicNodes:  true,
				podListOpt:       podListOpt,
				nodeListOpt:      &metav1.ListOptions{},
				ConfigUpdateMode: tc.slurmConfigUpdateMode,
				Topologies:       slurmTopologiesForDynamicTest(tc.topologyConfig),
			}
			engine := &SlinkyEngine{
				client: client,
				params: params,
			}

			result, httpErr := engine.GenerateOutput(context.Background(), topo, nil)

			if tc.expectError {
				require.Error(t, httpErr)
				if tc.errorMsg != "" {
					require.Contains(t, httpErr.Error(), tc.errorMsg)
				}
				return
			}
			require.Nil(t, httpErr)

			require.Equal(t, 0, countClientActions(client.Actions(), "get", "nodes"))
			require.Equal(t, len(tc.expectTopologySpec), countClientActions(client.Actions(), "patch", "nodes"))

			cm, err := client.CoreV1().ConfigMaps(params.Namespace).Get(context.Background(), params.ConfigMapName, metav1.GetOptions{})
			require.NoError(t, err)
			require.Equal(t, tc.expectTopologyYaml, cm.Data[params.ConfigPath])

			for i, topoSpec := range tc.expectTopologySpec {
				updatedNode, err := client.CoreV1().Nodes().Get(context.Background(), fmt.Sprintf("k8s-node-%d", i), metav1.GetOptions{})
				require.NoError(t, err)
				requireAnnotation(t, updatedNode.Annotations, topology.KeySlinkyTopologySpec, topoSpec)
				require.Equal(t, []byte("OK\n"), result)
			}

			// A second reconciliation over unchanged nodes must use the existing
			// List result and issue no per-node Get or Patch requests.
			client.ClearActions()
			result, httpErr = engine.GenerateOutput(context.Background(), topo, nil)
			require.Nil(t, httpErr)
			require.Equal(t, []byte("OK\n"), result)
			require.Equal(t, 0, countClientActions(client.Actions(), "get", "nodes"))
			require.Equal(t, 0, countClientActions(client.Actions(), "patch", "nodes"))
		})
	}
}

func countClientActions(actions []k8stesting.Action, verb, resource string) int {
	count := 0
	for _, action := range actions {
		if action.GetVerb() == verb && action.GetResource().Resource == resource {
			count++
		}
	}
	return count
}

func TestResolveTopologies(t *testing.T) {
	makePod := func(name, slurmName, partition string, ready bool) *corev1.Pod {
		status := corev1.ConditionTrue
		if !ready {
			status = corev1.ConditionFalse
		}
		return &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: "test-ns",
				Labels: map[string]string{
					"partition":               partition,
					topology.KeySlurmNodeName: slurmName,
				},
			},
			Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "c", Image: "i"}}},
			Status: corev1.PodStatus{
				Phase: corev1.PodRunning,
				Conditions: []corev1.PodCondition{
					{Type: corev1.PodReady, Status: status},
				},
			},
		}
	}

	ctx := context.Background()
	client := fake.NewSimpleClientset()
	for _, p := range []*corev1.Pod{
		makePod("p1", "node1", "a", true),
		makePod("p2", "node2", "a", true),
		makePod("p3", "node3", "a", false), // not ready, must be skipped
		makePod("p4", "node4", "b", true),
	} {
		_, err := client.CoreV1().Pods("test-ns").Create(ctx, p, metav1.CreateOptions{})
		require.NoError(t, err)
	}

	selA := metav1.LabelSelector{MatchLabels: map[string]string{"partition": "a"}}
	selB := metav1.LabelSelector{MatchLabels: map[string]string{"partition": "b"}}

	eng := &SlinkyEngine{
		client: client,
		params: &Params{
			Namespace: "test-ns",
			Topologies: map[string]*Topology{
				"byNodes":     {Topology: slurm.Topology{Plugin: topology.TopologyTree, Nodes: []string{"n1", "n2"}}},
				"bySelectorA": {Topology: slurm.Topology{Plugin: topology.TopologyBlock}, PodSelector: selA},
				"bySelectorB": {Topology: slurm.Topology{Plugin: topology.TopologyTree}, PodSelector: selB},
				"fallback":    {Topology: slurm.Topology{Plugin: topology.TopologyFlat, Partition: "scontrol-partition"}},
			},
		},
	}

	got, err := eng.resolveTopologies(ctx)
	require.NoError(t, err)
	require.Len(t, got, 4)

	require.Equal(t, []string{"n1", "n2"}, got["byNodes"].Nodes)
	require.ElementsMatch(t, []string{"node1", "node2"}, got["bySelectorA"].Nodes)
	require.Equal(t, []string{"node4"}, got["bySelectorB"].Nodes)
	// fallback entry: Nodes empty so slurm.GetTranslateConfig falls back to the finder
	require.Empty(t, got["fallback"].Nodes)
	require.Equal(t, "scontrol-partition", got["fallback"].Partition)
}

func TestGetParametersTopologyValidation(t *testing.T) {
	testCases := []struct {
		name  string
		nodes any
	}{
		{
			name:  "non-empty nodes and pod selector",
			nodes: []string{"n1"},
		},
		{
			name:  "empty nodes and pod selector",
			nodes: []string{},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			params := map[string]any{
				topology.KeyNamespace:         "test-ns",
				topology.KeyPodSelector:       map[string]any{"matchLabels": map[string]string{"app": "slurm"}},
				topology.KeyTopoConfigPath:    "topology.conf",
				topology.KeyTopoConfigmapName: "slurm-config",
				"topologies": map[string]any{
					"bad": map[string]any{
						"plugin": topology.TopologyTree,
						"nodes":  tc.nodes,
						"podSelector": map[string]any{
							"matchLabels": map[string]string{"partition": "a"},
						},
					},
				},
			}

			_, err := getParameters(params)
			require.ErrorContains(t, err, `cannot set both nodes and podSelector`)
		})
	}
}
