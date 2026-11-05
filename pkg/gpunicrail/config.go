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

package gpunicrail

import (
	"fmt"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Defaults.
const (
	DefaultRefreshInterval = 10 * time.Minute
	DefaultLLDPSocket      = "/var/run/lldpd.socket"
	RailSourceHostLLDP     = "host-lldp"
)

// NodeMetadataConfig is the `nodeMetadata` section of the broker and observer
// configuration files.
type NodeMetadataConfig struct {
	Plugins PluginsConfig `json:"plugins"`
}

// PluginsConfig lists the node-metadata plugins.
type PluginsConfig struct {
	GPUNICRailMapping *PluginConfig `json:"gpuNicRailMapping,omitempty"`
}

// PluginConfig configures the gpu-nic-rail-mapping plugin.
type PluginConfig struct {
	Enabled bool `json:"enabled"`
	// ReleaseName and Namespace identify the Topograph release and the
	// namespace that holds the mapping resources. They are set by the chart.
	ReleaseName     string          `json:"releaseName,omitempty"`
	Namespace       string          `json:"namespace,omitempty"`
	RefreshInterval metav1.Duration `json:"refreshInterval,omitempty"`
	RailSource      RailSource      `json:"railSource,omitempty"`
	GPUSource       GPUSource       `json:"gpuSource,omitempty"`
}

// RailSource selects how rails are discovered. Exactly one source is allowed.
type RailSource struct {
	Name     string         `json:"name"`
	HostLLDP *HostLLDPSouce `json:"hostLLDP,omitempty"`
}

// HostLLDPSouce configures rail discovery from the host lldpd.
type HostLLDPSouce struct {
	RailConfig `json:",inline"`
	// Socket is the lldpd control socket. Default /var/run/lldpd.socket.
	Socket string `json:"socket,omitempty"`
}

// Enabled reports whether c is present and enabled.
func (c *NodeMetadataConfig) Enabled() bool {
	p := c.Plugins.GPUNICRailMapping
	return p != nil && p.Enabled
}

// ValidateIdentity checks only the release identity. The Node Observer needs
// nothing else: it never runs discovery, only cleanup of this release's
// resources, and the chart gives it just the enabled flag and the identity.
func (c *PluginConfig) ValidateIdentity() error {
	if c.ReleaseName == "" || c.Namespace == "" {
		return fmt.Errorf("gpuNicRailMapping: releaseName and namespace are required")
	}
	return nil
}

// Validate checks an enabled plugin and fills defaults. A disabled plugin only
// needs the release identity so the observer can clean up after it.
func (c *PluginConfig) Validate() error {
	if err := c.ValidateIdentity(); err != nil {
		return err
	}
	if !c.Enabled {
		return nil
	}
	if c.RefreshInterval.Duration == 0 {
		c.RefreshInterval.Duration = DefaultRefreshInterval
	}
	if c.RefreshInterval.Duration < 0 {
		return fmt.Errorf("gpuNicRailMapping: refreshInterval must be positive")
	}
	if c.RailSource.Name != RailSourceHostLLDP || c.RailSource.HostLLDP == nil {
		return fmt.Errorf("gpuNicRailMapping: railSource must select %q with a hostLLDP section", RailSourceHostLLDP)
	}
	if c.RailSource.HostLLDP.Socket == "" {
		c.RailSource.HostLLDP.Socket = DefaultLLDPSocket
	}
	if _, err := NewMatcher(c.RailSource.HostLLDP.RailConfig); err != nil {
		return fmt.Errorf("gpuNicRailMapping: railSource.hostLLDP: %w", err)
	}
	if c.GPUSource.Namespace == "" || c.GPUSource.DaemonSet == "" {
		return fmt.Errorf("gpuNicRailMapping: gpuSource.gpuOperatorNamespace and gpuSource.daemonSet are required")
	}
	return nil
}
