// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"context"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/dynamic"

	"github.com/dsx-ai-factory/topograph/internal/exec"
	"github.com/dsx-ai-factory/topograph/pkg/gpunicrail"
)

// hostSysfsRoot matches the read-only host sysfs mount in the broker DaemonSet.
const hostSysfsRoot = "/host/sys"

// newGPUNICRailLoop builds the refresh loop of the gpu-nic-rail-mapping plugin.
func (b *nodeBroker) newGPUNICRailLoop() (*gpunicrail.Loop, error) {
	cfg := b.config.NodeMetadata.Plugins.GPUNICRailMapping
	if b.nodeName == "" {
		return nil, fmt.Errorf("NODE_NAME must be set for the gpu-nic-rail-mapping plugin")
	}
	matcher, err := gpunicrail.NewMatcher(cfg.RailSource.HostLLDP.RailConfig)
	if err != nil {
		return nil, err
	}
	dyn, err := dynamic.NewForConfig(b.restConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create dynamic client: %w", err)
	}

	// Resolve the live Node once; its UID names the resource.
	node, err := b.clientset.CoreV1().Nodes().Get(context.Background(), b.nodeName, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to get node %q: %w", b.nodeName, err)
	}

	lldp := gpunicrail.NewLLDPRunner(func(ctx context.Context, exe string, args []string) (string, error) {
		out, err := exec.Exec(ctx, exe, args, nil)
		if err != nil {
			return "", err
		}
		return out.String(), nil
	}, cfg.RailSource.HostLLDP.Socket)

	return &gpunicrail.Loop{
		Interval: cfg.RefreshInterval.Duration,
		Collector: &gpunicrail.Collector{
			LLDP:    lldp,
			GPU:     &gpunicrail.PodRunner{Client: b.clientset, Config: b.restConfig, Source: cfg.GPUSource, NodeName: b.nodeName},
			Matcher: matcher,
			Sysfs:   gpunicrail.Sysfs{Root: hostSysfsRoot},
		},
		Publisher: &gpunicrail.Publisher{Client: dyn, Namespace: cfg.Namespace, Release: cfg.ReleaseName, Node: node},
	}, nil
}
