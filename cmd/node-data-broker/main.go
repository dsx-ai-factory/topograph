/*
 * Copyright 2024-2026 NVIDIA CORPORATION
 * SPDX-License-Identifier: Apache-2.0
 */

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"maps"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/pflag"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"
	"sigs.k8s.io/yaml"

	"github.com/dsx-ai-factory/topograph/internal/version"
	"github.com/dsx-ai-factory/topograph/pkg/accelerator"
	"github.com/dsx-ai-factory/topograph/pkg/providers/aws"
	"github.com/dsx-ai-factory/topograph/pkg/providers/crusoe"
	"github.com/dsx-ai-factory/topograph/pkg/providers/dra"
	"github.com/dsx-ai-factory/topograph/pkg/providers/gcp"
	"github.com/dsx-ai-factory/topograph/pkg/providers/infiniband"
	"github.com/dsx-ai-factory/topograph/pkg/providers/lambdai"
	"github.com/dsx-ai-factory/topograph/pkg/providers/nebius"
	"github.com/dsx-ai-factory/topograph/pkg/providers/nscale"
	"github.com/dsx-ai-factory/topograph/pkg/providers/oci"
	"github.com/dsx-ai-factory/topograph/pkg/topology"
)

const (
	defaultConfigPath = "/etc/topograph/node-data-broker-config.yaml"
	readHeaderTimeout = 5 * time.Second
	shutdownTimeout   = 5 * time.Second
)

type nodeBroker struct {
	clientset  kubernetes.Interface
	restConfig *rest.Config
	config     nodeDataBrokerConfig
	nodeName   string
}

type nodeDataBrokerConfig struct {
	Provider    topology.Provider `yaml:"provider"`
	HealthzPort int               `yaml:"healthzPort"`
}

func main() {
	var ver bool
	var configPath string
	pflag.BoolVar(&ver, "version", false, "show the version")
	pflag.StringVarP(&configPath, "config", "c", defaultConfigPath, "config file")

	klog.InitFlags(nil)
	pflag.CommandLine.AddGoFlagSet(flag.CommandLine)
	pflag.Parse()
	defer klog.Flush()

	if ver {
		fmt.Println("Version:", version.Version)
		os.Exit(0)
	}

	config, err := newNodeDataBrokerConfig(configPath)
	if err != nil {
		klog.Error(err.Error())
		os.Exit(1)
	}
	if err := mainInternal(config); err != nil {
		klog.Error(err.Error())
		os.Exit(1)
	}
}

func newNodeDataBrokerConfig(path string) (nodeDataBrokerConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nodeDataBrokerConfig{}, fmt.Errorf("failed to read node-data-broker config %q: %w", path, err)
	}
	var config nodeDataBrokerConfig
	if err := yaml.Unmarshal(data, &config); err != nil {
		return nodeDataBrokerConfig{}, fmt.Errorf("failed to decode node-data-broker config %q: %w", path, err)
	}
	if config.Provider.Name == "" {
		return nodeDataBrokerConfig{}, fmt.Errorf("must specify provider.name")
	}
	if config.HealthzPort <= 0 {
		return nodeDataBrokerConfig{}, fmt.Errorf("must specify a positive healthzPort")
	}
	return config, nil
}

func mainInternal(brokerConfig nodeDataBrokerConfig) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	clientset, config, err := newInClusterClientset()
	if err != nil {
		return err
	}

	broker := &nodeBroker{
		clientset:  clientset,
		restConfig: config,
		config:     brokerConfig,
		nodeName:   os.Getenv("NODE_NAME"),
	}

	if err := broker.apply(ctx); err != nil {
		return err
	}

	// Keep the DaemonSet pod Running by serving a health endpoint until the pod
	// is terminated.
	return serveHealth(ctx, brokerConfig.HealthzPort)
}

func newInClusterClientset() (kubernetes.Interface, *rest.Config, error) {
	config, err := rest.InClusterConfig()
	if err != nil {
		return nil, nil, fmt.Errorf("failed to load in-cluster config: %v", err)
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create clientset: %v", err)
	}

	return clientset, config, nil
}

func (b *nodeBroker) apply(ctx context.Context) error {
	klog.InfoS("Applying node annotations", "provider", b.config.Provider.Name)

	annotations, err := b.getAnnotations(ctx)
	if err != nil {
		return err
	}
	klog.Infof("adding annotations %v in node %s for provider %s", annotations, b.nodeName, b.config.Provider.Name)

	node, err := b.clientset.CoreV1().Nodes().Get(ctx, b.nodeName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to get node %q: %v", b.nodeName, err)
	}

	mergeNodeAnnotations(node, annotations)

	_, err = b.clientset.CoreV1().Nodes().Update(ctx, node, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("failed to update node: %v", err)
	}

	return nil
}

// serveHealth runs a minimal HTTP server exposing /healthz so the DaemonSet
// pod stays Running after node annotations have been applied. It blocks until
// the context is cancelled (SIGTERM/SIGINT), then shuts down gracefully.
func serveHealth(ctx context.Context, port int) error {
	srv := &http.Server{
		Addr:              fmt.Sprintf(":%d", port),
		Handler:           healthHandler(),
		ReadHeaderTimeout: readHeaderTimeout,
	}

	errCh := make(chan error, 1)
	go func() {
		klog.Infof("Serving health endpoint on %s", srv.Addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case <-ctx.Done():
		klog.Info("Shutting down health endpoint")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	case err := <-errCh:
		return err
	}
}

func healthHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	return mux
}

func (b *nodeBroker) getAnnotations(ctx context.Context) (map[string]string, error) {
	switch b.config.Provider.Name {
	case aws.NAME:
		return aws.GetNodeAnnotations(ctx)
	case crusoe.NAME:
		return crusoe.GetNodeAnnotations(ctx, b.nodeName)
	case gcp.NAME:
		return gcp.GetNodeAnnotations(ctx)
	case oci.NAME:
		return oci.GetNodeAnnotations(ctx)
	case nebius.NAME:
		return nebius.GetNodeAnnotations(ctx)
	case nscale.NAME:
		return nscale.GetNodeAnnotations(ctx, b.config.Provider.Params)
	case dra.NAME:
		return dra.GetNodeAnnotations(ctx, b.nodeName)
	case infiniband.NAME_K8S:
		section := accelerator.SectionFromProviderParams(b.config.Provider.Params)
		return infiniband.GetNodeAnnotations(ctx, b.clientset, b.restConfig, b.nodeName, section)
	case lambdai.NAME:
		return lambdai.GetNodeAnnotations(ctx, b.clientset, b.nodeName)
	case "":
		return nil, fmt.Errorf("must set provider")
	default:
		return nil, fmt.Errorf("unsupported provider %q", b.config.Provider.Name)
	}
}

func mergeNodeAnnotations(node *corev1.Node, annotations map[string]string) {
	if node.Annotations == nil {
		node.Annotations = make(map[string]string)
	}
	maps.Copy(node.Annotations, annotations)
}
