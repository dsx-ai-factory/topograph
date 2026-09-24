/*
 * Copyright 2025-2026 NVIDIA CORPORATION
 * SPDX-License-Identifier: Apache-2.0
 */

package infiniband

import (
	"bytes"
	"context"
	"fmt"
	"os"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"

	"github.com/dsx-ai-factory/topograph/internal/k8s"
	"github.com/dsx-ai-factory/topograph/pkg/accelerator"
	"github.com/dsx-ai-factory/topograph/pkg/topology"
)

type IBNetDiscoverK8S struct {
	config *rest.Config
	client *kubernetes.Clientset
}

func NewIBNetDiscoverK8S(config *rest.Config, client *kubernetes.Clientset) *IBNetDiscoverK8S {
	return &IBNetDiscoverK8S{
		config: config,
		client: client,
	}
}

func (h *IBNetDiscoverK8S) Run(ctx context.Context, node string) (*bytes.Buffer, error) {
	dataBrokerName := os.Getenv("NODE_DATA_BROKER_NAME")
	dataBrokerNamespace := os.Getenv("NODE_DATA_BROKER_NAMESPACE")
	pods, err := k8s.GetDaemonSetPods(ctx, h.client, dataBrokerName, dataBrokerNamespace, node)
	if err != nil {
		return nil, err
	}

	if n := len(pods.Items); n != 1 {
		return nil, fmt.Errorf("expected 1 data broker pod on %q node; got %d", node, n)
	}

	return k8s.ExecInPod(ctx, h.client, h.config, pods.Items[0].Name, dataBrokerNamespace, []string{"ibnetdiscover"})
}

func GetNodeAnnotations(ctx context.Context, client kubernetes.Interface, config *rest.Config, hostname string, section accelerator.Section) (map[string]string, error) {
	annotations := accelerator.BaseKubernetesNodeAnnotations(hostname)

	discoverer, err := accelerator.NewKubernetesNodeDiscoverer(section, client, config)
	if err != nil {
		return nil, err
	}

	assignments, err := discoverer.Discover(ctx, []accelerator.Target{{InstanceID: hostname, HostName: hostname}})
	if err != nil {
		klog.Warningf("No accelerator domain for node %s: %v", hostname, err)
	} else if assignment, ok := assignments[hostname]; ok {
		annotations[topology.KeyGpuClusterID] = assignment.DomainID
	}

	return annotations, nil
}
