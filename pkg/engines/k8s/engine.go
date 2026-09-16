/*
 * Copyright 2024-2026 NVIDIA CORPORATION
 * SPDX-License-Identifier: Apache-2.0
 */

package k8s

import (
	"context"
	"fmt"
	"net/http"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/dsx-ai-factory/topograph/internal/config"
	"github.com/dsx-ai-factory/topograph/internal/httperr"
	internalk8s "github.com/dsx-ai-factory/topograph/internal/k8s"
	"github.com/dsx-ai-factory/topograph/pkg/engines"
	"github.com/dsx-ai-factory/topograph/pkg/topology"
)

const NAME = "k8s"

type K8sEngine struct {
	config *rest.Config
	client kubernetes.Interface
	params *Params
	// cachedNodeMap contains resolved node names. A nil value marks a requested
	// Node that was not returned by the selector-filtered list and must be fetched.
	cachedNodeMap map[string]*corev1.Node
}

type Params struct {
	// NodeSelector (optional) specifies nodes participating in the topology
	NodeSelector map[string]string `mapstructure:"nodeSelector"`
	// FabricLabels optionally sets label keys by closest-first tier.
	FabricLabels []string `mapstructure:"fabricLabels"`
	// AcceleratorLabel optionally sets the accelerator label key.
	AcceleratorLabel string `mapstructure:"acceleratorLabel"`
	// AcceleratorDomainSourceLabel optionally selects an existing Kubernetes
	// Node label as the authoritative accelerator-domain source.
	AcceleratorDomainSourceLabel string `mapstructure:"acceleratorDomainSourceLabel"`

	// derived fields
	nodeListOpt *metav1.ListOptions
	labelKeys   *TopologyLabelKeys
}

func NamedLoader() (string, engines.Loader) {
	return NAME, Loader
}

func Loader(_ context.Context, params engines.Config) (engines.Engine, *httperr.Error) {
	p, err := getParameters(params)
	if err != nil {
		return nil, httperr.NewError(http.StatusBadRequest, err.Error())
	}

	config, err := rest.InClusterConfig()
	if err != nil {
		return nil, httperr.NewError(http.StatusBadGateway, err.Error())
	}

	if err := internalk8s.ConfigureClientRateLimits(config); err != nil {
		return nil, httperr.NewError(http.StatusBadRequest, err.Error())
	}

	client, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, httperr.NewError(http.StatusBadGateway, err.Error())
	}

	return &K8sEngine{
		config: config,
		client: client,
		params: p,
	}, nil
}

func getParameters(params engines.Config) (*Params, error) {
	p := &Params{}
	if err := config.Decode(params, p); err != nil {
		return nil, err
	}
	if p.AcceleratorLabel != "" && p.AcceleratorDomainSourceLabel != "" {
		return nil, fmt.Errorf("engine parameters acceleratorLabel and acceleratorDomainSourceLabel cannot be set together")
	}
	if p.AcceleratorDomainSourceLabel != "" {
		if err := internalk8s.ValidateLabelKey("acceleratorDomainSourceLabel", p.AcceleratorDomainSourceLabel); err != nil {
			return nil, err
		}
	}
	p.labelKeys = NewTopologyLabelKeys(p.FabricLabels, p.AcceleratorLabel)
	if err := p.labelKeys.Validate(); err != nil {
		return nil, err
	}

	if len(p.NodeSelector) != 0 {
		p.nodeListOpt = &metav1.ListOptions{
			LabelSelector: labels.Set(p.NodeSelector).String(),
		}
	}

	return p, nil
}

func (eng *K8sEngine) GenerateOutput(ctx context.Context, graph *topology.Graph, _ map[string]any) ([]byte, *httperr.Error) {
	if err := NewTopologyLabeler(eng.params.labelKeys).ApplyNodeLabels(ctx, graph, eng); err != nil {
		return nil, httperr.NewError(http.StatusBadGateway, err.Error())
	}

	return []byte("OK\n"), nil
}
