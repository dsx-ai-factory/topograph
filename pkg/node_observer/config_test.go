/*
 * Copyright 2024-2026 NVIDIA CORPORATION
 * SPDX-License-Identifier: Apache-2.0
 */

package node_observer

import (
	"os"
	"testing"
	"time"

	"github.com/dsx-ai-factory/topograph/pkg/topology"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestNewConfigFromFile(t *testing.T) {
	testCases := []struct {
		name   string
		noFile bool
		data   string
		cfg    *Config
		err    string
	}{
		{
			name:   "Case 1: config does not exist",
			noFile: true,
			err:    "open /does/not/exist: no such file or directory",
		},
		{
			name: "Case 2: parse error",
			data: "12345",
			err:  "failed to parse",
		},
		{
			name: "Case 3: empty config",
			err:  "must specify generateTopologyUrl",
		},
		{
			name: "Case 4: missing trigger",
			data: `
generateTopologyUrl: "http://topograph.default.svc.cluster.local:49021/v1/generate"
`,
			err: "must specify nodeSelector and/or podSelector in trigger, or apiServer.podSelector",
		},
		{
			name: "Case 5: valid with default retry delay",
			data: `
generateTopologyUrl: "http://topograph.default.svc.cluster.local:49021/v1/generate"
provider:
  name: test
engine:
  name: test
  params:
    namespace: default
    plugin: topology/tree
    topologyConfigPath: topology.conf
    topologyConfigmapName: slurm-config
trigger:
  nodeSelector:
    a: b
    c: d
  podSelector:
    matchLabels:
      app.kubernetes.io/component: compute
    matchExpressions:
      - key: tier
        operator: In
        values:
          - frontend
          - backend
`,
			cfg: &Config{
				GenerateTopologyURL: "http://topograph.default.svc.cluster.local:49021/v1/generate",
				Provider:            topology.Provider{Name: "test"},
				Engine: topology.Engine{
					Name: "test",
					Params: map[string]any{
						"namespace":             "default",
						"plugin":                "topology/tree",
						"topologyConfigPath":    "topology.conf",
						"topologyConfigmapName": "slurm-config",
					},
				},
				Trigger: Trigger{
					NodeSelector: map[string]string{"a": "b", "c": "d"},
					PodSelector: &metav1.LabelSelector{
						MatchLabels: map[string]string{"app.kubernetes.io/component": "compute"},
						MatchExpressions: []metav1.LabelSelectorRequirement{
							{
								Key:      "tier",
								Operator: "In",
								Values:   []string{"frontend", "backend"},
							},
						},
					},
				},
				RetryDelay: metav1.Duration{Duration: defaultRetryDelay},
			},
		},
		{
			name: "Case 6: valid with configured retry delay",
			data: `
generateTopologyUrl: "http://topograph.default.svc.cluster.local:49021/v1/generate"
provider:
  name: test
engine:
  name: test
  params:
    namespace: default
    plugin: topology/tree
    topologyConfigPath: topology.conf
    topologyConfigmapName: slurm-config
retryDelay: 10m
trigger:
  nodeSelector:
    a: b
    c: d
  podSelector:
    matchLabels:
      app.kubernetes.io/component: compute
    matchExpressions:
      - key: tier
        operator: In
        values:
          - frontend
          - backend
`,
			cfg: &Config{
				GenerateTopologyURL: "http://topograph.default.svc.cluster.local:49021/v1/generate",
				Provider:            topology.Provider{Name: "test"},
				Engine: topology.Engine{
					Name: "test",
					Params: map[string]any{
						"namespace":             "default",
						"plugin":                "topology/tree",
						"topologyConfigPath":    "topology.conf",
						"topologyConfigmapName": "slurm-config",
					},
				},
				Trigger: Trigger{
					NodeSelector: map[string]string{"a": "b", "c": "d"},
					PodSelector: &metav1.LabelSelector{
						MatchLabels: map[string]string{"app.kubernetes.io/component": "compute"},
						MatchExpressions: []metav1.LabelSelectorRequirement{
							{
								Key:      "tier",
								Operator: "In",
								Values:   []string{"frontend", "backend"},
							},
						},
					},
				},
				RetryDelay: metav1.Duration{Duration: 10 * time.Minute},
			},
		},
		{
			name: "Case 7: valid with API server trigger and default container name",
			data: `
generateTopologyUrl: "http://topograph.default.svc.cluster.local:49021/v1/generate"
provider:
  name: test
engine:
  name: test
apiServer:
  namespace: topograph
  podSelector:
    matchLabels:
      app.kubernetes.io/component: api-server
      app.kubernetes.io/instance: topograph
`,
			cfg: &Config{
				GenerateTopologyURL: "http://topograph.default.svc.cluster.local:49021/v1/generate",
				Provider:            topology.Provider{Name: "test"},
				Engine:              topology.Engine{Name: "test"},
				APIServer: APIServer{
					Namespace:     "topograph",
					ContainerName: defaultAPIServerContainerName,
					PodSelector: &metav1.LabelSelector{
						MatchLabels: map[string]string{
							"app.kubernetes.io/component": "api-server",
							"app.kubernetes.io/instance":  "topograph",
						},
					},
				},
				RetryDelay: metav1.Duration{Duration: defaultRetryDelay},
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			var fname string
			if tc.noFile {
				fname = "/does/not/exist"
			} else {
				f, err := os.CreateTemp("", "test-*")
				require.NoError(t, err)
				fname = f.Name()
				defer func() { _ = os.Remove(fname) }()
				defer func() { _ = f.Close() }()

				n, err := f.WriteString(tc.data)
				require.NoError(t, err)
				require.Equal(t, len(tc.data), n)
				err = f.Sync()
				require.NoError(t, err)
			}
			cfg, err := NewConfigFromFile(fname)
			if len(tc.err) != 0 {
				require.ErrorContains(t, err, tc.err)
			} else {
				require.NoError(t, err)
				require.Equal(t, tc.cfg, cfg)
			}
		})
	}
}
