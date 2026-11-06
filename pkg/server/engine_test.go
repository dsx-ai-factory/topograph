// SPDX-FileCopyrightText: Copyright (c) 2024 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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

package server

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/dsx-ai-factory/topograph/internal/httperr"
	"github.com/dsx-ai-factory/topograph/pkg/config"
	"github.com/dsx-ai-factory/topograph/pkg/engines"
	"github.com/dsx-ai-factory/topograph/pkg/providers"
	"github.com/dsx-ai-factory/topograph/pkg/providers/nodelabels"
	"github.com/dsx-ai-factory/topograph/pkg/registry"
	"github.com/dsx-ai-factory/topograph/pkg/topology"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

type resolvingEngine struct {
	received []topology.ComputeInstances
	calls    int
	output   func() ([]byte, *httperr.Error)
}

func (e *resolvingEngine) ResolveComputeInstances(_ context.Context, instances []topology.ComputeInstances, _ any) ([]topology.ComputeInstances, *httperr.Error) {
	e.calls++
	e.received = instances
	return instances, nil
}

func (e *resolvingEngine) GenerateOutput(_ context.Context, _ *topology.Graph, _ map[string]any) ([]byte, *httperr.Error) {
	if e.output != nil {
		return e.output()
	}
	return nil, nil
}

func TestKubernetesProviderFailurePreservesOutput(t *testing.T) {
	ctx := context.Background()
	client := fake.NewSimpleClientset(
		&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "worker-a", Labels: map[string]string{"rack": "r1"}}},
		&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "topology", Namespace: "slurm"}},
	)
	prv, err := nodelabels.New(client, map[string]any{"topologyLabels": []string{"rack"}})
	require.NoError(t, err)
	outputCalls := 0
	eng := &resolvingEngine{output: func() ([]byte, *httperr.Error) {
		outputCalls++
		cm, err := client.CoreV1().ConfigMaps("slurm").Get(ctx, "topology", metav1.GetOptions{})
		require.NoError(t, err)
		cm.Data = map[string]string{"topology.conf": "last successful topology"}
		_, err = client.CoreV1().ConfigMaps("slurm").Update(ctx, cm, metav1.UpdateOptions{})
		require.NoError(t, err)
		return []byte(cm.Data["topology.conf"]), nil
	}}
	oldSrv := srv
	oldProvider, oldEngine := registry.Providers["k8s-node-labels"], registry.Engines["test-output"]
	t.Cleanup(func() {
		srv = oldSrv
		registry.Providers["k8s-node-labels"] = oldProvider
		if oldEngine == nil {
			delete(registry.Engines, "test-output")
		} else {
			registry.Engines["test-output"] = oldEngine
		}
	})
	srv = &HttpServer{cfg: &config.Config{}}
	registry.Providers["k8s-node-labels"] = func(context.Context, providers.Config) (providers.Provider, *httperr.Error) { return prv, nil }
	registry.Engines["test-output"] = func(context.Context, engines.Config) (engines.Engine, *httperr.Error) { return eng, nil }
	request := &topology.Request{
		Provider: topology.Provider{Name: "k8s-node-labels"},
		Engine:   topology.Engine{Name: "test-output"},
		Nodes:    []topology.ComputeInstances{{Instances: map[string]string{"worker-a": "worker-a"}}},
	}
	_, herr := processTopologyRequest(request)
	require.Nil(t, herr)
	require.Equal(t, 1, outputCalls)
	before, err := client.CoreV1().ConfigMaps("slurm").Get(ctx, "topology", metav1.GetOptions{})
	require.NoError(t, err)
	client.ClearActions()
	require.NoError(t, client.CoreV1().Nodes().Delete(ctx, "worker-a", metav1.DeleteOptions{}))
	data, herr := processTopologyRequest(request)
	require.Nil(t, data)
	require.NotNil(t, herr)
	require.Equal(t, http.StatusServiceUnavailable, herr.Code())
	require.Equal(t, 1, outputCalls, "failed generation must not call the output engine")
	after, err := client.CoreV1().ConfigMaps("slurm").Get(ctx, "topology", metav1.GetOptions{})
	require.NoError(t, err)
	require.Equal(t, before.Data, after.Data)
	for _, action := range client.Actions() {
		if action.GetResource().Resource == "configmaps" {
			require.Equal(t, "get", action.GetVerb(), "failed generation must not write the ConfigMap")
		}
	}
}

type resolvingProvider struct {
	instances []topology.ComputeInstances
	calls     int
}

func (p *resolvingProvider) GetComputeInstances(_ context.Context) ([]topology.ComputeInstances, *httperr.Error) {
	p.calls++
	return p.instances, nil
}

func (*resolvingProvider) GenerateTopologyConfig(_ context.Context, _ *int, _ []topology.ComputeInstances) (*topology.Graph, *httperr.Error) {
	return nil, nil
}

func TestCheckCredentials(t *testing.T) {
	credPayload := map[string]any{"key1": "val1"}
	credConfig := map[string]any{"key2": "val2"}

	testCases := []struct {
		name     string
		payload  map[string]any
		config   map[string]any
		expected map[string]any
	}{
		{
			name:     "Case 1: payload only",
			payload:  credPayload,
			expected: credPayload,
		},
		{
			name:     "Case 2: config only",
			config:   credConfig,
			expected: credConfig,
		},
		{
			name:     "Case 3: both",
			payload:  credPayload,
			config:   credConfig,
			expected: credPayload,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.expected, checkCredentials(tc.payload, tc.config))
		})
	}

}

func TestResolveComputeInstancesPrecedence(t *testing.T) {
	requested := []topology.ComputeInstances{{
		Region:    "request",
		Instances: map[string]string{"request-instance": "request-node"},
	}}
	provided := []topology.ComputeInstances{{
		Region:    "provider",
		Instances: map[string]string{"provider-instance": "provider-node"},
	}}

	t.Run("request instances win and still pass through engine", func(t *testing.T) {
		eng := &resolvingEngine{}
		prv := &resolvingProvider{instances: provided}

		actual, httpErr := resolveComputeInstances(context.Background(), eng, prv, requested)

		require.Nil(t, httpErr)
		require.Equal(t, requested, actual)
		require.Equal(t, requested, eng.received)
		require.Equal(t, 1, eng.calls)
		require.Zero(t, prv.calls)
	})

	t.Run("provider instances pass through engine", func(t *testing.T) {
		eng := &resolvingEngine{}
		prv := &resolvingProvider{instances: provided}

		actual, httpErr := resolveComputeInstances(context.Background(), eng, prv, nil)

		require.Nil(t, httpErr)
		require.Equal(t, provided, actual)
		require.Equal(t, provided, eng.received)
		require.Equal(t, 1, eng.calls)
		require.Equal(t, 1, prv.calls)
	})

	t.Run("an empty provider result remains authoritative", func(t *testing.T) {
		eng := &resolvingEngine{}
		prv := &resolvingProvider{instances: []topology.ComputeInstances{}}

		actual, httpErr := resolveComputeInstances(context.Background(), eng, prv, nil)

		require.Nil(t, httpErr)
		require.Empty(t, actual)
		require.Zero(t, eng.calls)
		require.Equal(t, 1, prv.calls)
	})
}

type retrier struct {
	codes    []int
	attempts int
}

func (r *retrier) callback(_ *topology.Request) ([]byte, *httperr.Error) {
	r.attempts++
	var code int
	if len(r.codes) == 0 {
		code = http.StatusInternalServerError
	} else {
		code = r.codes[0]
		r.codes = r.codes[1:]
	}

	if code == http.StatusOK {
		return []byte{1, 2, 3, 4, 5}, nil
	}

	return nil, httperr.NewError(code, "error")
}

func TestProcessRequestWithRetries(t *testing.T) {
	backOff = time.Millisecond
	defer func() { backOff = defaultBackOff }()

	tr := &topology.Request{
		Provider: topology.Provider{
			Name: "test",
		},
		Engine: topology.Engine{
			Name: "test",
		},
	}

	testCases := []struct {
		name     string
		retrier  *retrier
		err      string
		code     int
		attempts int
	}{
		{
			name:     "Case 1: retry and failure",
			retrier:  &retrier{},
			err:      "error",
			code:     500,
			attempts: maxRetries,
		},
		{
			name:     "Case 2: retry and success",
			retrier:  &retrier{codes: []int{http.StatusInternalServerError, http.StatusOK}},
			attempts: 2,
		},
		{
			name:     "Case 3: user error",
			retrier:  &retrier{codes: []int{http.StatusBadRequest}},
			err:      "error",
			code:     400,
			attempts: 1,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ret, err := processRequestWithRetries(tr, tc.retrier.callback)
			if len(tc.err) != 0 {
				require.EqualError(t, err, tc.err)
				require.Equal(t, tc.code, err.Code())
			} else {
				require.Nil(t, err)
				require.Equal(t, []byte{1, 2, 3, 4, 5}, ret)
			}
			require.Equal(t, tc.attempts, tc.retrier.attempts)
		})
	}
}

func TestProcessTopologyRequest(t *testing.T) {
	srv = &HttpServer{
		cfg: &config.Config{},
	}
	testCases := []struct {
		name string
		tr   *topology.Request
		cfg  string
		err  string
		code int
	}{
		{
			name: "Case 1: invalid engine name",
			tr: &topology.Request{
				Engine: topology.Engine{Name: "bad"},
			},
			err:  `unsupported engine "bad"`,
			code: http.StatusBadRequest,
		},
		{
			name: "Case 2: invalid provider name",
			tr: &topology.Request{
				Engine:   topology.Engine{Name: "slurm"},
				Provider: topology.Provider{Name: "bad"},
			},
			err:  `unsupported provider "bad"`,
			code: http.StatusBadRequest,
		},
		{
			name: "Case 3: invalid engine parameters",
			tr: &topology.Request{
				Engine: topology.Engine{
					Name: "slinky",
					Params: map[string]any{
						"namespace":             "data",
						"topologyConfigPath":    "data",
						"topologyConfigmapName": "data",
					},
				},
				Provider: topology.Provider{Name: "test"},
			},
			err:  `must specify engine parameter "podSelector"`,
			code: http.StatusBadRequest,
		},
		{
			name: "Case 4: invalid provider parameters",
			tr: &topology.Request{
				Engine: topology.Engine{Name: "slurm"},
				Provider: topology.Provider{
					Name:   "test",
					Params: map[string]any{"modelFileName": "not_exist.yaml"},
				},
			},
			err:  `failed to read model file not_exist.yaml: failed to read not_exist.yaml: open models/not_exist.yaml: file does not exist`,
			code: http.StatusBadRequest,
		},
		{
			name: "Case 5: valid input",
			tr: &topology.Request{
				Engine: topology.Engine{Name: "slurm"},
				Provider: topology.Provider{
					Name:   "test",
					Params: map[string]any{"modelFileName": "small-tree.yaml"},
				},
			},
			cfg: `SwitchName=S1 Switches=S[2-3]
SwitchName=S2 Nodes=I[21-22,25]
SwitchName=S3 Nodes=I[34-36]
`,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			data, err := processTopologyRequest(tc.tr)
			if len(tc.err) != 0 {
				require.NotNil(t, err)
				require.EqualError(t, err, tc.err)
				require.Equal(t, tc.code, err.Code())
			} else {
				require.Nil(t, err)
				require.Equal(t, tc.cfg, string(data))
			}
		})
	}
}

// TestProcessTopologyRequestPassesCredentialSource checks what the provider
// loader is told about where its credentials came from.
func TestProcessTopologyRequestPassesCredentialSource(t *testing.T) {
	var got providers.Config
	oldSrv, oldProvider, oldEngine := srv, registry.Providers["cred-source"], registry.Engines["cred-source"]
	t.Cleanup(func() {
		srv = oldSrv
		if oldProvider == nil {
			delete(registry.Providers, "cred-source")
		}
		if oldEngine == nil {
			delete(registry.Engines, "cred-source")
		}
	})
	registry.Providers["cred-source"] = func(_ context.Context, cfg providers.Config) (providers.Provider, *httperr.Error) {
		got = cfg
		return nil, httperr.NewError(http.StatusTeapot, "stop")
	}
	registry.Engines["cred-source"] = func(context.Context, engines.Config) (engines.Engine, *httperr.Error) {
		return &resolvingEngine{}, nil
	}

	serverParams := map[string]any{"base_url": "https://server.example"}
	for _, tc := range []struct {
		name         string
		serverCreds  map[string]any
		payloadCreds map[string]any
		want         bool
	}{
		{"server creds fill in for a request without any", map[string]any{"token": "s"}, nil, true},
		{"request creds replace the server's", map[string]any{"token": "s"}, map[string]any{"token": "p"}, false},
		{"no credentials anywhere", nil, nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv = &HttpServer{cfg: &config.Config{Credentials: tc.serverCreds, ProviderParams: serverParams}}
			_, herr := processTopologyRequest(&topology.Request{
				Provider: topology.Provider{Name: "cred-source", Creds: tc.payloadCreds, Params: map[string]any{"base_url": "https://other.example"}},
				Engine:   topology.Engine{Name: "cred-source"},
			})
			require.NotNil(t, herr)
			require.Equal(t, tc.want, got.ServerCreds)
			require.Equal(t, serverParams, got.ServerParams)
			require.Equal(t, "https://other.example", got.Params["base_url"])
		})
	}
}
