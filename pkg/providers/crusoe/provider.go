/*
 * Copyright 2026 NVIDIA CORPORATION
 * SPDX-License-Identifier: Apache-2.0
 */

package crusoe

import (
	"context"
	"net/http"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"

	"github.com/dsx-ai-factory/topograph/internal/httperr"
	"github.com/dsx-ai-factory/topograph/internal/k8s"
	"github.com/dsx-ai-factory/topograph/pkg/accelerator"
	"github.com/dsx-ai-factory/topograph/pkg/providers"
	"github.com/dsx-ai-factory/topograph/pkg/topology"
)

const NAME = "crusoe"

// region is the only region a Crusoe Kubernetes cluster spans. Crusoe deploys
// one cluster per region, so the provider reports a constant.
const region = "local"

type Provider struct {
	client      kubernetes.Interface
	nodeListOpt *metav1.ListOptions
	trimTiers   int
}

func NamedLoader() (string, providers.Loader) {
	return NAME, Loader
}

// Loader builds the provider from its in-cluster service account. Crusoe
// publishes topology as Node labels, so the provider needs no cloud
// credentials.
func Loader(_ context.Context, config providers.Config) (providers.Provider, *httperr.Error) {
	nodeListOpt, err := k8s.NodeListOptions(config.Params)
	if err != nil {
		return nil, httperr.NewError(http.StatusBadRequest, err.Error())
	}

	trimTiers, err := providers.GetTrimTiers(config.Params)
	if err != nil {
		return nil, httperr.NewError(http.StatusBadRequest, err.Error())
	}

	cfg, err := rest.InClusterConfig()
	if err != nil {
		return nil, httperr.NewError(http.StatusBadGateway, err.Error())
	}

	client, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, httperr.NewError(http.StatusBadGateway, err.Error())
	}

	return &Provider{
		client:      client,
		nodeListOpt: nodeListOpt,
		trimTiers:   trimTiers,
	}, nil
}

func (p *Provider) GenerateTopologyConfig(ctx context.Context, _ *int, cis []topology.ComputeInstances) (*topology.Graph, *httperr.Error) {
	requested, httpErr := requestedNodeIDs(cis)
	if httpErr != nil {
		return nil, httpErr
	}

	nodeList, err := k8s.GetNodes(ctx, p.client, p.nodeListOpt)
	if err != nil {
		return nil, httperr.NewError(http.StatusBadGateway, err.Error())
	}

	topo, httpErr := buildClusterTopology(nodeMetadataFromNodeList(nodeList), requested)
	if httpErr != nil {
		return nil, httpErr
	}

	klog.Infof("Extracted topology for %d instances", topo.Len())
	return topo.ToGraph(NAME, cis, p.trimTiers, false), nil
}

// nodeMetadataFromNodeList reads the instance identity the node-data-broker
// wrote and falls back to the Kubernetes node name. Engines key their
// ComputeInstances map on that annotation, so the provider has to agree with it
// or nothing resolves in the graph. On Crusoe the two are the same string, but
// deferring to the annotation keeps the provider correct if that ever changes.
func nodeMetadataFromNodeList(nodeList *corev1.NodeList) []nodeMetadata {
	nodes := make([]nodeMetadata, 0, len(nodeList.Items))
	for _, node := range nodeList.Items {
		instanceID := strings.TrimSpace(node.Annotations[topology.KeyNodeInstance])
		if instanceID == "" {
			instanceID = node.Name
		}
		nodes = append(nodes, nodeMetadata{InstanceID: instanceID, Labels: node.Labels})
	}
	return nodes
}

// Instances2NodeMap implements slurm.instanceMapper.
func (p *Provider) Instances2NodeMap(_ context.Context, nodes []string) (map[string]string, error) {
	i2n := make(map[string]string, len(nodes))
	for _, node := range nodes {
		i2n[node] = node
	}
	return i2n, nil
}

// GetInstancesRegions implements slurm.instanceMapper.
func (p *Provider) GetInstancesRegions(_ context.Context, nodes []string) (map[string]string, error) {
	regions := make(map[string]string, len(nodes))
	for _, node := range nodes {
		regions[node] = region
	}
	return regions, nil
}

// GetNodeAnnotations returns the identity annotations the node-data-broker
// writes. Topology itself comes from the Crusoe Node labels, so there is
// nothing node-local to collect.
func GetNodeAnnotations(_ context.Context, hostname string) (map[string]string, error) {
	return accelerator.BaseKubernetesNodeAnnotations(hostname), nil
}
