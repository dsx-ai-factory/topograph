/*
 * Copyright 2026 NVIDIA CORPORATION
 * SPDX-License-Identifier: Apache-2.0
 */

package nfd

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8sfake "k8s.io/client-go/kubernetes/fake"

	k8sengine "github.com/dsx-ai-factory/topograph/pkg/engines/k8s"
	"github.com/dsx-ai-factory/topograph/pkg/topology"
)

const testNFDNamespace = "node-feature-discovery"

func TestNamedLoader(t *testing.T) {
	name, loader := NamedLoader()
	require.Equal(t, NAME, name)
	require.NotNil(t, loader)
}

func TestGetNFDNamespace(t *testing.T) {
	testCases := []struct {
		name                string
		configuredNamespace string
		expectedNamespace   string
		expectError         bool
	}{
		{
			name:        "rejects missing environment configuration",
			expectError: true,
		},
		{
			name:                "uses trimmed environment configuration",
			configuredNamespace: "  deployed-nfd  ",
			expectedNamespace:   "deployed-nfd",
		},
		{
			name:                "rejects blank environment configuration",
			configuredNamespace: "  ",
			expectError:         true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(envNFDNamespace, tc.configuredNamespace)
			namespace, err := getNFDNamespace()
			if tc.expectError {
				require.EqualError(t, err, "NFD_NAMESPACE environment variable is required")
				require.Empty(t, namespace)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.expectedNamespace, namespace)
		})
	}
}

func TestLoaderRequiresNFDNamespace(t *testing.T) {
	t.Setenv(envNFDNamespace, "")
	eng, httpErr := Loader(context.Background(), nil)
	require.Nil(t, eng)
	require.Equal(t, http.StatusBadGateway, httpErr.Code())
	require.ErrorContains(t, httpErr, "NFD_NAMESPACE environment variable is required")
}

func TestGetParametersDefaults(t *testing.T) {
	params, err := getParameters(nil)
	require.NoError(t, err)
	require.True(t, params.Cleanup)
	require.Empty(t, params.AcceleratorDomainSourceLabel)
}

func TestGetParametersAcceleratorDomainSourceLabel(t *testing.T) {
	params, err := getParameters(map[string]any{
		"acceleratorDomainSourceLabel": "example.com/accelerator-domain",
	})
	require.NoError(t, err)
	require.Equal(t, "example.com/accelerator-domain", params.AcceleratorDomainSourceLabel)

	_, err = getParameters(map[string]any{
		"acceleratorDomainSourceLabel": "not a label",
	})
	require.ErrorContains(t, err, `acceleratorDomainSourceLabel "not a label" is not a valid Kubernetes label key`)
}

func TestResolveComputeInstancesCachesNodesWhenInstancesAreSupplied(t *testing.T) {
	client := k8sfake.NewSimpleClientset(
		&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-a"}},
	)
	eng := &NfdEngine{
		client: client,
		params: &Params{
			nodeListOpt: &metav1.ListOptions{},
		},
	}
	instances := []topology.ComputeInstances{{
		Region:    "region",
		Instances: map[string]string{"instance-a": "node-a"},
	}}

	actual, httpErr := eng.ResolveComputeInstances(context.Background(), instances, nil)

	require.Nil(t, httpErr)
	require.Equal(t, instances, actual)
	require.NotNil(t, eng.cachedNodes)
	require.Len(t, eng.cachedNodes.Items, 1)
}

func TestGenerateOutputCreatesNodeFeaturesAndGroups(t *testing.T) {
	client := k8sfake.NewSimpleClientset(
		&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-a"}},
		&corev1.Node{ObjectMeta: metav1.ObjectMeta{
			Name: "node-b",
			Labels: map[string]string{
				"example.com/accelerator-domain": "cluster.0",
			},
		}},
		&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-c"}},
	)
	dynamicClient := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
		runtime.NewScheme(),
		map[schema.GroupVersionResource]string{
			nodeFeatureGVR:      "NodeFeatureList",
			nodeFeatureGroupGVR: "NodeFeatureGroupList",
		},
	)
	eng := &NfdEngine{
		client:        client,
		dynamicClient: dynamicClient,
		params: &Params{
			Cleanup:                      true,
			AcceleratorDomainSourceLabel: "example.com/accelerator-domain",
		},
		namespace: testNFDNamespace,
	}

	out, httpErr := eng.GenerateOutput(context.Background(), testGraph(), nil)
	require.Nil(t, httpErr)
	require.Equal(t, "OK nodeFeatures=3 nodeFeatureGroups=7\n", string(out))

	features, err := dynamicClient.Resource(nodeFeatureGVR).Namespace(testNFDNamespace).List(context.Background(), metav1.ListOptions{})
	require.NoError(t, err)
	require.Len(t, features.Items, 3)
	require.Equal(t, testNFDNamespace, features.Items[0].GetNamespace())

	nodeA := findNodeFeature(t, features.Items, "node-a")
	require.Equal(t, map[string]string{nfdNodeName: "node-a"}, attributeElements(t, nodeA, nfdSystemName))
	require.Equal(t, map[string]string{
		topologyTypeAcceleratorDomain:    "nvl-a",
		topologyTypeAcceleratorSubDomain: "nvl-a.rack-1",
		topologyTypeFabric + "0":         "leaf-1",
		topologyTypeFabric + "1":         "spine-1",
	}, attributeElements(t, nodeA, nfdFeatureSet))

	nodeB := findNodeFeature(t, features.Items, "node-b")
	require.Equal(t, map[string]string{nfdNodeName: "node-b"}, attributeElements(t, nodeB, nfdSystemName))
	require.Equal(t, map[string]string{
		topologyTypeAcceleratorDomain: "cluster.0",
		topologyTypeFabric + "0":      "leaf-1",
		topologyTypeFabric + "1":      "spine-1",
	}, attributeElements(t, nodeB, nfdFeatureSet))

	groups, err := dynamicClient.Resource(nodeFeatureGroupGVR).Namespace(testNFDNamespace).List(context.Background(), metav1.ListOptions{})
	require.NoError(t, err)
	require.Len(t, groups.Items, 7)
	require.Equal(t, testNFDNamespace, groups.Items[0].GetNamespace())

	leafGroup := findGroup(t, groups.Items, topologyTypeFabric+"0", "leaf-1")
	require.Equal(t, []interface{}{"leaf-1"}, groupRuleValues(t, leafGroup, topologyTypeFabric+"0"))
	cliqueGroup := findGroup(t, groups.Items, topologyTypeAcceleratorDomain, "cluster.0")
	require.Equal(t, "example.com/accelerator-domain", cliqueGroup.GetAnnotations()[annotationTopologyLabelKey])
	require.Equal(t, []interface{}{"cluster.0"}, groupRuleValues(t, cliqueGroup, topologyTypeAcceleratorDomain))
	subDomainGroup := findGroup(t, groups.Items, topologyTypeAcceleratorSubDomain, "nvl-a.rack-1")
	require.Equal(t, topology.KeyTopologyXclrSubDomain, subDomainGroup.GetAnnotations()[annotationTopologyLabelKey])
	require.Equal(t, []interface{}{"nvl-a.rack-1"}, groupRuleValues(t, subDomainGroup, topologyTypeAcceleratorSubDomain))
}

func TestGenerateOutputCleansStaleObjects(t *testing.T) {
	staleFeature, err := makeNodeFeature("stale-node", map[string]string{topologyTypeFabric + "0": "stale-leaf"})
	require.NoError(t, err)
	staleGroup, err := makeNodeFeatureGroup(topologyTypeFabric+"0", "stale-leaf", topology.FabricTierKey(0))
	require.NoError(t, err)
	retainedFeature, err := makeNodeFeature("node-a", map[string]string{topologyTypeFabric + "0": "old-leaf"})
	require.NoError(t, err)
	retainedGroup, err := makeNodeFeatureGroup(topologyTypeFabric+"0", "leaf-1", topology.FabricTierKey(0))
	require.NoError(t, err)
	staleFeature.SetNamespace(testNFDNamespace)
	staleGroup.SetNamespace(testNFDNamespace)
	retainedFeature.SetNamespace(testNFDNamespace)
	retainedGroup.SetNamespace(testNFDNamespace)
	dynamicClient := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
		runtime.NewScheme(),
		map[schema.GroupVersionResource]string{
			nodeFeatureGVR:      "NodeFeatureList",
			nodeFeatureGroupGVR: "NodeFeatureGroupList",
		},
		staleFeature,
		staleGroup,
		retainedFeature,
		retainedGroup,
	)
	eng := &NfdEngine{
		client:        k8sfake.NewSimpleClientset(),
		dynamicClient: dynamicClient,
		params:        &Params{Cleanup: true},
		namespace:     testNFDNamespace,
	}

	out, httpErr := eng.GenerateOutput(context.Background(), testGraph(), nil)
	require.Nil(t, httpErr)
	require.Equal(t, "OK nodeFeatures=3 nodeFeatureGroups=6\n", string(out))

	features, err := dynamicClient.Resource(nodeFeatureGVR).Namespace(testNFDNamespace).List(context.Background(), metav1.ListOptions{})
	require.NoError(t, err)
	require.Len(t, features.Items, 3)
	groups, err := dynamicClient.Resource(nodeFeatureGroupGVR).Namespace(testNFDNamespace).List(context.Background(), metav1.ListOptions{})
	require.NoError(t, err)
	require.Len(t, groups.Items, 6)

	resource := dynamicClient.Resource(nodeFeatureGVR).Namespace(testNFDNamespace)
	_, err = resource.Get(context.Background(), retainedFeature.GetName(), metav1.GetOptions{})
	require.NoError(t, err)
	_, err = resource.Get(context.Background(), staleFeature.GetName(), metav1.GetOptions{})
	require.True(t, apierrors.IsNotFound(err))

	resource = dynamicClient.Resource(nodeFeatureGroupGVR).Namespace(testNFDNamespace)
	_, err = resource.Get(context.Background(), retainedGroup.GetName(), metav1.GetOptions{})
	require.NoError(t, err)
	_, err = resource.Get(context.Background(), staleGroup.GetName(), metav1.GetOptions{})
	require.True(t, apierrors.IsNotFound(err))
}

func TestGenerateOutputRejectsEmptyDesiredStateWithCleanup(t *testing.T) {
	staleFeature, err := makeNodeFeature("stale-node", map[string]string{topologyTypeFabric + "0": "stale-leaf"})
	require.NoError(t, err)
	staleGroup, err := makeNodeFeatureGroup(topologyTypeFabric+"0", "stale-leaf", topology.FabricTierKey(0))
	require.NoError(t, err)
	staleFeature.SetNamespace(testNFDNamespace)
	staleGroup.SetNamespace(testNFDNamespace)
	dynamicClient := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
		runtime.NewScheme(),
		map[schema.GroupVersionResource]string{
			nodeFeatureGVR:      "NodeFeatureList",
			nodeFeatureGroupGVR: "NodeFeatureGroupList",
		},
		staleFeature,
		staleGroup,
	)
	eng := &NfdEngine{
		client:        k8sfake.NewSimpleClientset(),
		dynamicClient: dynamicClient,
		params:        &Params{Cleanup: true},
		namespace:     testNFDNamespace,
	}

	out, httpErr := eng.GenerateOutput(context.Background(), nil, nil)
	require.Nil(t, out)
	require.Equal(t, http.StatusBadGateway, httpErr.Code())
	require.ErrorContains(t, httpErr, "generated no NFD topology objects; keeping the existing topology")

	features, err := dynamicClient.Resource(nodeFeatureGVR).Namespace(testNFDNamespace).List(context.Background(), metav1.ListOptions{})
	require.NoError(t, err)
	require.Len(t, features.Items, 1)
	groups, err := dynamicClient.Resource(nodeFeatureGroupGVR).Namespace(testNFDNamespace).List(context.Background(), metav1.ListOptions{})
	require.NoError(t, err)
	require.Len(t, groups.Items, 1)
}

func TestGenerateOutputAllowsEmptyDesiredStateWithoutCleanup(t *testing.T) {
	eng := &NfdEngine{
		client:        k8sfake.NewSimpleClientset(),
		dynamicClient: dynamicfake.NewSimpleDynamicClient(runtime.NewScheme()),
		params:        &Params{Cleanup: false},
		namespace:     testNFDNamespace,
	}

	out, httpErr := eng.GenerateOutput(context.Background(), nil, nil)
	require.Nil(t, httpErr)
	require.Equal(t, "OK nodeFeatures=0 nodeFeatureGroups=0\n", string(out))
}

func TestUpsertObjectRemovesStaleTopologyAttributes(t *testing.T) {
	existing, err := makeNodeFeature("node-a", map[string]string{
		topologyTypeFabric + "0": "leaf-1",
		topologyTypeFabric + "2": "stale-core",
	})
	require.NoError(t, err)
	existing.SetNamespace(testNFDNamespace)
	existing.SetLabels(map[string]string{
		labelManagedBy:       managedByTopograph,
		"example.com/retain": "true",
	})

	dynamicClient := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), existing)
	eng := &NfdEngine{
		dynamicClient: dynamicClient,
		params:        &Params{},
		namespace:     testNFDNamespace,
	}
	desired, err := makeNodeFeature("node-a", map[string]string{
		topologyTypeFabric + "0": "leaf-1",
	})
	require.NoError(t, err)

	require.NoError(t, eng.upsertObject(context.Background(), nodeFeatureGVR, desired))

	updated, err := dynamicClient.Resource(nodeFeatureGVR).Namespace(testNFDNamespace).
		Get(context.Background(), existing.GetName(), metav1.GetOptions{})
	require.NoError(t, err)
	require.Equal(t, map[string]string{
		topologyTypeFabric + "0": "leaf-1",
	}, attributeElements(t, *updated, nfdFeatureSet))
	require.Equal(t, "true", updated.GetLabels()["example.com/retain"])
}

func TestBuildNFDObjectsRejectsInvalidNFDNodeNameLabelValue(t *testing.T) {
	nodeLabels := k8sengine.NodeLabelMap{
		"node-name-that-is-too-long-for-a-kubernetes-label-value-because-it-has-more-than-sixty-three-characters": {
			topology.FabricTierKey(0): "leaf-1",
		},
	}

	_, _, err := buildNFDObjects(nodeLabels, nil, "")
	require.ErrorContains(t, err, "cannot be used as")
}

func TestBuildNFDObjectsUsesConfiguredAcceleratorDomainSource(t *testing.T) {
	nodeLabels := k8sengine.NodeLabelMap{
		"node-a": {
			topology.KeyTopologyXclrDomain:    "provider-domain-a",
			topology.KeyTopologyXclrSubDomain: "provider-domain-a.rack-a",
			topology.FabricTierKey(0):         "leaf-a",
		},
		"node-b": {
			topology.KeyTopologyXclrDomain: "provider-domain-b",
		},
		"node-c": {
			topology.KeyTopologyXclrDomain: "source-domain-a",
		},
	}

	features, groups, err := buildNFDObjects(
		nodeLabels,
		map[string]string{"node-a": "source-domain-a"},
		"example.com/accelerator-domain",
	)

	require.NoError(t, err)
	require.Len(t, features, 3)
	nodeAElements := attributeElements(t, *features[0], nfdFeatureSet)
	require.Equal(t, "source-domain-a", nodeAElements[topologyTypeAcceleratorDomain])
	require.Empty(t, nodeAElements[topologyTypeAcceleratorSubDomain], "sub-domain must be suppressed when a source-label value replaces the provider domain")
	require.Equal(t, "provider-domain-b", attributeElements(t, *features[1], nfdFeatureSet)[topologyTypeAcceleratorDomain])
	require.Equal(t, "source-domain-a", attributeElements(t, *features[2], nfdFeatureSet)[topologyTypeAcceleratorDomain])
	require.Len(t, groups, 3, "accelerator-domain×2 + fabric-tier-0×1; no accelerator-sub-domain group when the source label suppresses it")
	acceleratorDomainValues := make(map[string]struct{})
	acceleratorSubDomainValues := make(map[string]struct{})
	acceleratorDomainSourceKey := ""
	for _, group := range groups {
		switch group.GetLabels()[labelGroupType] {
		case topologyTypeAcceleratorDomain:
			value := group.GetAnnotations()[annotationTopologyValue]
			acceleratorDomainValues[value] = struct{}{}
			if value == "source-domain-a" {
				acceleratorDomainSourceKey = group.GetAnnotations()[annotationTopologyLabelKey]
			}
		case topologyTypeAcceleratorSubDomain:
			acceleratorSubDomainValues[group.GetAnnotations()[annotationTopologyValue]] = struct{}{}
		}
	}
	require.Equal(t, map[string]struct{}{
		"source-domain-a":   {},
		"provider-domain-b": {},
	}, acceleratorDomainValues)
	require.Equal(t, "example.com/accelerator-domain", acceleratorDomainSourceKey)
	require.Empty(t, acceleratorSubDomainValues, "no accelerator-sub-domain NodeFeatureGroup must be created for a source-label node")
}

func TestBuildNFDObjectsUsesProviderAcceleratorDomainWhenSourceIsOmitted(t *testing.T) {
	nodeLabels := k8sengine.NodeLabelMap{
		"node-a": {
			topology.KeyTopologyXclrDomain:    "provider-domain-a",
			topology.KeyTopologyXclrSubDomain: "provider-sub-domain-a",
		},
	}
	nodes := &corev1.NodeList{Items: []corev1.Node{{ObjectMeta: metav1.ObjectMeta{
		Name: "node-a",
		Labels: map[string]string{
			"nvidia.com/gpu.clique": "legacy-clique-domain",
		},
	}}}}

	features, _, err := buildNFDObjects(
		nodeLabels,
		acceleratorDomainSourceValues(nodes, ""),
		"",
	)

	require.NoError(t, err)
	require.Len(t, features, 1)
	elements := attributeElements(t, *features[0], nfdFeatureSet)
	require.Equal(t, "provider-domain-a", elements[topologyTypeAcceleratorDomain])
	require.Equal(t, "provider-sub-domain-a", elements[topologyTypeAcceleratorSubDomain])
}

func TestTopologyKindUsesFabricTierAndAcceleratorLabels(t *testing.T) {
	kind, ok := topologyKind(topology.FabricTierKey(2))
	require.True(t, ok)
	require.Equal(t, topologyTypeFabric+"2", kind)
	kind, ok = topologyKind(topology.KeyTopologyXclrDomain)
	require.True(t, ok)
	require.Equal(t, topologyTypeAcceleratorDomain, kind)
	kind, ok = topologyKind(topology.KeyTopologyXclrSubDomain)
	require.True(t, ok)
	require.Equal(t, topologyTypeAcceleratorSubDomain, kind)
}

func testGraph() *topology.Graph {
	domains := topology.NewDomainMap()
	domains.AddHostInfo(&topology.HostInfo{
		Domain:     "nvl-a",
		SubDomain:  "nvl-a.rack-1",
		InstanceID: "inst-a",
		HostName:   "node-a",
	})
	domains.AddHostInfo(&topology.HostInfo{
		Domain:     "nvl-a",
		SubDomain:  "nvl-a.rack-1",
		InstanceID: "inst-b",
		HostName:   "node-b",
	})
	domains.AddHost("nvl-b", "inst-c", "node-c")

	leaf1 := &topology.Vertex{
		ID: "leaf-1",
		Vertices: map[string]*topology.Vertex{
			"inst-a": {ID: "inst-a", Name: "node-a"},
			"inst-b": {ID: "inst-b", Name: "node-b"},
		},
	}
	leaf2 := &topology.Vertex{
		ID: "leaf-2",
		Vertices: map[string]*topology.Vertex{
			"inst-c": {ID: "inst-c", Name: "node-c"},
		},
	}
	spine := &topology.Vertex{
		ID: "spine-1",
		Vertices: map[string]*topology.Vertex{
			"leaf-1": leaf1,
			"leaf-2": leaf2,
		},
	}

	return &topology.Graph{
		Tiers: &topology.Vertex{
			Vertices: map[string]*topology.Vertex{
				"spine-1": spine,
			},
		},
		Domains: domains,
	}
}

func findNodeFeature(t *testing.T, items []unstructured.Unstructured, nodeName string) unstructured.Unstructured {
	t.Helper()
	for _, item := range items {
		if item.GetAnnotations()[annotationNodeName] == nodeName {
			return item
		}
	}
	require.FailNowf(t, "missing NodeFeature", "node %q not found", nodeName)
	return unstructured.Unstructured{}
}

func findGroup(t *testing.T, items []unstructured.Unstructured, kind, value string) unstructured.Unstructured {
	t.Helper()
	for _, item := range items {
		annotations := item.GetAnnotations()
		if item.GetLabels()[labelGroupType] == kind && annotations[annotationTopologyValue] == value {
			return item
		}
	}
	require.FailNowf(t, "missing NodeFeatureGroup", "kind %q value %q not found", kind, value)
	return unstructured.Unstructured{}
}

func attributeElements(t *testing.T, item unstructured.Unstructured, feature string) map[string]string {
	t.Helper()
	elements, found, err := unstructured.NestedStringMap(item.Object, "spec", "features", "attributes", feature, "elements")
	require.NoError(t, err)
	require.True(t, found)
	return elements
}

func groupRuleValues(t *testing.T, item unstructured.Unstructured, kind string) []interface{} {
	t.Helper()
	spec, ok := item.Object["spec"].(map[string]interface{})
	require.True(t, ok)
	rules, ok := spec["featureGroupRules"].([]interface{})
	require.True(t, ok)
	require.NotEmpty(t, rules)
	rule, ok := rules[0].(map[string]interface{})
	require.True(t, ok)
	matchFeatures, ok := rule["matchFeatures"].([]interface{})
	require.True(t, ok)
	require.NotEmpty(t, matchFeatures)
	matcher, ok := matchFeatures[0].(map[string]interface{})
	require.True(t, ok)
	matchExpressions, ok := matcher["matchExpressions"].(map[string]interface{})
	require.True(t, ok)
	expression, ok := matchExpressions[kind].(map[string]interface{})
	require.True(t, ok)
	values, ok := expression["value"].([]interface{})
	require.True(t, ok)
	return values
}
