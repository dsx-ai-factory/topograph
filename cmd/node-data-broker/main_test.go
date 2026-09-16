/*
 * Copyright 2024-2026 NVIDIA CORPORATION
 * SPDX-License-Identifier: Apache-2.0
 */

package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/dsx-ai-factory/topograph/pkg/providers/infiniband"
	"github.com/dsx-ai-factory/topograph/pkg/providers/nscale"
	"github.com/dsx-ai-factory/topograph/pkg/topology"
)

func TestGetAnnotations(t *testing.T) {
	ctx := context.TODO()
	tests := []struct {
		name     string
		provider string
		err      string
	}{
		{
			name: "Case 1: empty provider",
			err:  "must set provider",
		},
		{
			name:     "Case 2: invalid provider",
			provider: "invalid",
			err:      `unsupported provider "invalid"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			broker := &nodeBroker{
				config: nodeDataBrokerConfig{Provider: topology.Provider{Name: tt.provider}},
			}
			_, err := broker.getAnnotations(ctx)
			require.EqualError(t, err, tt.err)
		})
	}

	t.Run("invalid accelerator section", func(t *testing.T) {
		broker := &nodeBroker{
			config: nodeDataBrokerConfig{
				Provider: topology.Provider{
					Name: infiniband.NAME_K8S,
					Params: map[string]any{
						"accelerator": "invalid",
					},
				},
			},
		}
		_, err := broker.getAnnotations(ctx)
		require.EqualError(t, err, "accelerator section must be an object with a source")
	})

	t.Run("null accelerator section", func(t *testing.T) {
		broker := &nodeBroker{
			config: nodeDataBrokerConfig{
				Provider: topology.Provider{
					Name: infiniband.NAME_K8S,
					Params: map[string]any{
						"accelerator": nil,
					},
				},
			},
		}
		_, err := broker.getAnnotations(ctx)
		require.EqualError(t, err, "accelerator section must be an object with a source")
	})

	t.Run("empty accelerator section disables discovery", func(t *testing.T) {
		broker := &nodeBroker{
			nodeName: "node-1",
			config: nodeDataBrokerConfig{
				Provider: topology.Provider{
					Name: infiniband.NAME_K8S,
					Params: map[string]any{
						"accelerator": map[string]any{},
					},
				},
			},
		}
		annotations, err := broker.getAnnotations(ctx)
		require.NoError(t, err)
		require.Equal(t, map[string]string{
			topology.KeyNodeInstance: "node-1",
			topology.KeyNodeRegion:   "local",
		}, annotations)
	})
}

func TestNewNodeDataBrokerConfig(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "node-data-broker-config.yaml")
	require.NoError(t, os.WriteFile(configPath, []byte(`
provider:
  name: infiniband-k8s
  params:
    accelerator:
      source: none
healthzPort: 18080
`), 0o600))

	config, err := newNodeDataBrokerConfig(configPath)
	require.NoError(t, err)
	require.Equal(t, infiniband.NAME_K8S, config.Provider.Name)
	require.Equal(t, map[string]any{
		"accelerator": map[string]any{"source": "none"},
	}, config.Provider.Params)
	require.Equal(t, 18080, config.HealthzPort)

	_, err = newNodeDataBrokerConfig(filepath.Join(dir, "missing.yaml"))
	require.ErrorContains(t, err, "failed to read node-data-broker config")

	invalidPath := filepath.Join(dir, "invalid.yaml")
	require.NoError(t, os.WriteFile(invalidPath, []byte("provider: ["), 0o600))
	_, err = newNodeDataBrokerConfig(invalidPath)
	require.ErrorContains(t, err, "failed to decode node-data-broker config")

	missingProviderPath := filepath.Join(dir, "missing-provider.yaml")
	require.NoError(t, os.WriteFile(missingProviderPath, []byte("healthzPort: 8080"), 0o600))
	_, err = newNodeDataBrokerConfig(missingProviderPath)
	require.EqualError(t, err, "must specify provider.name")

	missingPortPath := filepath.Join(dir, "missing-port.yaml")
	require.NoError(t, os.WriteFile(missingPortPath, []byte("provider:\n  name: test"), 0o600))
	_, err = newNodeDataBrokerConfig(missingPortPath)
	require.EqualError(t, err, "must specify a positive healthzPort")

	negativePortPath := filepath.Join(dir, "negative-port.yaml")
	require.NoError(t, os.WriteFile(negativePortPath, []byte("provider:\n  name: test\nhealthzPort: -1"), 0o600))
	_, err = newNodeDataBrokerConfig(negativePortPath)
	require.EqualError(t, err, "must specify a positive healthzPort")
}

func TestMergeNodeAnnotations(t *testing.T) {
	tests := []struct {
		name string
		node *corev1.Node
		in   map[string]string
		out  map[string]string
	}{
		{
			name: "Case 1: no labels",
			node: &corev1.Node{},
			out:  map[string]string{},
		},
		{
			name: "Case 2: copy",
			node: &corev1.Node{},
			in:   map[string]string{"a": "1", "b": "2"},
			out:  map[string]string{"a": "1", "b": "2"},
		},
		{
			name: "Case 3: merge",
			node: &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Labels:      map[string]string{"a": "1", "b": "2", "c": "x"},
					Annotations: map[string]string{"a": "1", "b": "2", "c": "x"},
				},
			},
			in:  map[string]string{"c": "3", "d": "4"},
			out: map[string]string{"a": "1", "b": "2", "c": "3", "d": "4"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mergeNodeAnnotations(tt.node, tt.in)
			require.Equal(t, tt.out, tt.node.Annotations)
		})
	}
}

func TestHealthHandler(t *testing.T) {
	srv := httptest.NewServer(healthHandler())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/healthz")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusOK, resp.StatusCode)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, "ok", string(body))
}

func TestServeHealthShutsDownOnContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		// port 0 lets the OS pick a free ephemeral port.
		done <- serveHealth(ctx, 0)
	}()

	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("serveHealth did not return after context cancellation")
	}
}

// TestGetAnnotationsNscaleDispatch verifies that getAnnotations routes the
// nscale.NAME provider to nscale.GetNodeAnnotations, forwarding the
// configured imdsUrl provider param instead of touching the process-global
// http.DefaultTransport. nscale.GetNodeAnnotations' own request/parse/
// error-handling behavior (malformed IMDS response, context cancellation,
// etc.) is covered by pkg/providers/nscale's own test suite (imds_test.go);
// this test only needs to prove the switch reaches it with the configured
// endpoint.
func TestGetAnnotationsNscaleDispatch(t *testing.T) {
	body := `{"meta": {"serverID": "srv-1", "regionID": "region-a"}}`

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)

	broker := &nodeBroker{
		config: nodeDataBrokerConfig{Provider: topology.Provider{
			Name:   nscale.NAME,
			Params: map[string]any{"imdsUrl": server.URL},
		}},
	}
	annotations, err := broker.getAnnotations(context.Background())
	require.NoError(t, err)
	require.Equal(t, map[string]string{
		topology.KeyNodeInstance: "srv-1",
		topology.KeyNodeRegion:   "region-a",
	}, annotations)
}

// TestGetAnnotationsNscaleDispatchCanceledContext verifies that a canceled
// context passed into broker.getAnnotations propagates through the nscale
// dispatch to the underlying IMDS request, returning promptly with an error
// instead of hanging.
func TestGetAnnotationsNscaleDispatchCanceledContext(t *testing.T) {
	started := make(chan struct{})
	unblock := make(chan struct{})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(started)
		<-unblock
		_, _ = w.Write([]byte(`{"meta": {"serverID": "srv-1"}}`))
	}))
	t.Cleanup(func() {
		close(unblock)
		server.Close()
	})

	broker := &nodeBroker{
		config: nodeDataBrokerConfig{Provider: topology.Provider{
			Name:   nscale.NAME,
			Params: map[string]any{"imdsUrl": server.URL},
		}},
	}

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		_, err := broker.getAnnotations(ctx)
		done <- err
	}()

	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("handler did not receive request before timeout")
	}
	cancel()

	select {
	case err := <-done:
		require.Error(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("getAnnotations did not return after context cancellation")
	}
}
