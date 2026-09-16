/*
 * Copyright 2025-2026 NVIDIA CORPORATION
 * SPDX-License-Identifier: Apache-2.0
 */

package k8s

import (
	"bytes"
	"context"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
	"k8s.io/klog/v2"

	internalconfig "github.com/dsx-ai-factory/topograph/internal/config"
	"github.com/dsx-ai-factory/topograph/pkg/topology"
)

type nodeSelectorConfig struct {
	NodeSelector map[string]string `mapstructure:"nodeSelector"`
}

// NodeListOptions decodes a provider's optional nodeSelector into Kubernetes
// list options. Other provider parameters are intentionally ignored.
func NodeListOptions(params map[string]any) (*metav1.ListOptions, error) {
	config := nodeSelectorConfig{}
	if err := internalconfig.Decode(params, &config); err != nil {
		return nil, err
	}
	if len(config.NodeSelector) == 0 {
		return nil, nil
	}

	return &metav1.ListOptions{LabelSelector: labels.Set(config.NodeSelector).String()}, nil
}

func GetNodes(ctx context.Context, client kubernetes.Interface, opt *metav1.ListOptions) (*corev1.NodeList, error) {
	if opt == nil {
		opt = &metav1.ListOptions{}
	}

	nodes, err := client.CoreV1().Nodes().List(ctx, *opt)
	if err != nil {
		return nil, fmt.Errorf("failed to list node in the cluster: %v", err)
	}

	return nodes, nil
}

func GetPodsByLabels(ctx context.Context, client kubernetes.Interface, namespace string, l map[string]string) (*corev1.PodList, error) {
	opt := metav1.ListOptions{LabelSelector: labels.SelectorFromSet(l).String()}
	return client.CoreV1().Pods(namespace).List(ctx, opt)
}

func IsPodReady(pod *corev1.Pod) bool {
	for _, cond := range pod.Status.Conditions {
		if cond.Type == corev1.PodReady && cond.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}

// ValidateLabelKey validates a required Kubernetes label key and identifies
// its configuration location in any returned error.
func ValidateLabelKey(location, key string) error {
	if errs := validation.IsQualifiedName(key); len(errs) != 0 {
		return fmt.Errorf("%s %q is not a valid Kubernetes label key: %s",
			location, key, strings.Join(errs, "; "))
	}
	return nil
}

func GetDaemonSetPods(ctx context.Context, client kubernetes.Interface, name, namespace, nodename string) (*corev1.PodList, error) {
	ds, err := client.AppsV1().DaemonSets(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}

	opt := metav1.ListOptions{
		LabelSelector: labels.Set(ds.Spec.Selector.MatchLabels).String(),
	}
	if len(nodename) != 0 {
		opt.FieldSelector = "spec.nodeName=" + nodename
	}

	return client.CoreV1().Pods(namespace).List(ctx, opt)
}

func ExecInPod(ctx context.Context, client kubernetes.Interface, config *rest.Config, name, namespace string, cmd []string) (*bytes.Buffer, error) {
	execOpts := &corev1.PodExecOptions{
		Command: cmd,
		Stdin:   false,
		Stdout:  true,
		Stderr:  true,
		TTY:     false,
	}

	req := client.CoreV1().RESTClient().Post().
		Resource("pods").
		Namespace(namespace).
		Name(name).
		SubResource("exec").
		VersionedParams(execOpts, scheme.ParameterCodec)

	executor, err := remotecommand.NewSPDYExecutor(config, "POST", req.URL())
	if err != nil {
		return nil, fmt.Errorf("failed to execute command %v in pod %s/%s: %v", cmd, namespace, name, err)
	}

	var stdout, stderr bytes.Buffer
	err = executor.StreamWithContext(ctx, remotecommand.StreamOptions{
		Stdout: &stdout,
		Stderr: &stderr,
	})

	if err != nil {
		strerr := strings.ReplaceAll(stderr.String(), "\n", " ")
		return nil, fmt.Errorf("failed to execute command %v in pod %s/%s: %s: %v", cmd, namespace, name, strerr, err)
	}

	return &stdout, nil
}

// GetComputeInstances builds a ComputeInstances list from the node annotations
// written by the node-data-broker (instance ID and region). Nodes missing either
// annotation are skipped with a warning.
func GetComputeInstances(nodes *corev1.NodeList) []topology.ComputeInstances {
	regions := make(map[string]map[string]string)
	regionNames := []string{}
	for _, node := range nodes.Items {
		instance, ok := node.Annotations[topology.KeyNodeInstance]
		if !ok {
			klog.Warningf("missing %q annotation in node %s", topology.KeyNodeInstance, node.Name)
			continue
		}
		region, ok := node.Annotations[topology.KeyNodeRegion]
		if !ok {
			klog.Warningf("missing %q annotation in node %s", topology.KeyNodeRegion, node.Name)
			continue
		}
		if _, ok = regions[region]; !ok {
			regions[region] = make(map[string]string)
			regionNames = append(regionNames, region)
		}
		regions[region][instance] = node.Name
	}

	cis := make([]topology.ComputeInstances, 0, len(regions))
	for _, region := range regionNames {
		cis = append(cis, topology.ComputeInstances{Region: region, Instances: regions[region]})
	}

	return cis
}
