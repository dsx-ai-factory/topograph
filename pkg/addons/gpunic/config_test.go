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

package gpunic

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"
)

const pluginYAML = `
plugins:
  gpuNicMapping:
    enabled: true
    releaseName: rel
    namespace: topograph
    refreshInterval: 5m
    switchSource:
      name: host-lldp
    gpuSource:
      gpuOperatorNamespace: gpu-operator
      daemonSet: nvidia-device-plugin-daemonset
`

func TestPluginConfig(t *testing.T) {
	var c NodeMetadataConfig
	require.NoError(t, yaml.Unmarshal([]byte(pluginYAML), &c))
	require.True(t, c.Enabled())
	p := c.Plugins.GPUNICMapping
	require.NoError(t, p.Validate())
	require.Equal(t, 5*time.Minute, p.RefreshInterval.Duration)
	require.Equal(t, DefaultLLDPSocket, p.SwitchSource.HostLLDP.Socket)

	for name, mutate := range map[string]func(*PluginConfig){
		"no release":    func(p *PluginConfig) { p.ReleaseName = "" },
		"bad source":    func(p *PluginConfig) { p.SwitchSource.Name = "other" },
		"no gpu source": func(p *PluginConfig) { p.GPUSource.DaemonSet = "" },
		"bad interval":  func(p *PluginConfig) { p.RefreshInterval.Duration = -time.Second },
	} {
		var c NodeMetadataConfig
		require.NoError(t, yaml.Unmarshal([]byte(pluginYAML), &c))
		p := c.Plugins.GPUNICMapping
		mutate(p)
		require.Error(t, p.Validate(), name)
	}

	// A disabled plugin needs only its release identity.
	off := &PluginConfig{ReleaseName: "rel", Namespace: "ns"}
	require.NoError(t, off.Validate())
	require.False(t, (&NodeMetadataConfig{Plugins: PluginsConfig{GPUNICMapping: off}}).Enabled())
	require.False(t, (&NodeMetadataConfig{}).Enabled())
}
