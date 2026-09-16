/*
 * Copyright 2026 NVIDIA CORPORATION
 * SPDX-License-Identifier: Apache-2.0
 */

package accelerator

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kubernetesfake "k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"

	"github.com/dsx-ai-factory/topograph/pkg/topology"
)

const testKubernetesLabel = "example.com/accelerator-domain"

func configuredSection(value any) Section {
	return SectionFromProviderParams(map[string]any{"accelerator": value})
}

func TestConfigDefaultsAndValidation(t *testing.T) {
	config := Config{}
	config.SetDefaults()

	require.Empty(t, config.Source)
	require.Empty(t, config.KubernetesLabel.Key)
	require.Equal(t, DefaultGPUOperatorNamespace, config.NvidiaSMI.GPUOperatorNamespace)
	require.Equal(t, DefaultDevicePluginDaemonSet, config.NvidiaSMI.DevicePluginDaemonSet)
	require.EqualError(t, config.Validate(), "accelerator source must be set")

	config.Source = "invalid"
	require.EqualError(t, config.Validate(), `unsupported accelerator source "invalid"`)

	config.Source = SourceKubernetesLabel
	require.EqualError(t, config.Validate(), `accelerator kubernetesLabel.key must be set for source "kubernetes-label"`)
}

func TestParseConfig(t *testing.T) {
	tests := []struct {
		name         string
		params       map[string]any
		source       string
		labelKey     string
		gpuNamespace string
		devicePlugin string
		err          string
	}{
		{name: "omitted section", source: SourceNone},
		{name: "empty section", params: map[string]any{"accelerator": map[string]any{}}, source: SourceNone},
		{
			name:   "null section",
			params: map[string]any{"accelerator": nil},
			err:    "accelerator section must be an object with a source",
		},
		{
			name:   "non-object section",
			params: map[string]any{"accelerator": "nvidia-smi"},
			err:    "accelerator section must be an object with a source",
		},
		{
			name: "non-empty section requires source",
			params: map[string]any{"accelerator": map[string]any{
				"nvidiaSmi": map[string]any{"gpuOperatorNamespace": "gpu-operator"},
			}},
			err: "accelerator source must be set",
		},
		{
			name:   "label source requires key",
			params: map[string]any{"accelerator": map[string]any{"source": SourceKubernetesLabel}},
			err:    `accelerator kubernetesLabel.key must be set for source "kubernetes-label"`,
		},
		{
			name: "configured label source",
			params: map[string]any{"accelerator": map[string]any{
				"source": SourceKubernetesLabel,
				"kubernetesLabel": map[string]any{
					"key": " example.com/domain ",
				},
			}},
			source:   SourceKubernetesLabel,
			labelKey: "example.com/domain",
		},
		{
			name: "configured nvidia-smi source",
			params: map[string]any{"accelerator": map[string]any{
				"source": " NVIDIA-SMI ",
				"nvidiaSmi": map[string]any{
					"gpuOperatorNamespace":  "custom-operator",
					"devicePluginDaemonSet": "custom-plugin",
				},
			}},
			source:       SourceNvidiaSMI,
			gpuNamespace: "custom-operator",
			devicePlugin: "custom-plugin",
		},
		{
			name:   "invalid source",
			params: map[string]any{"accelerator": map[string]any{"source": "invalid"}},
			err:    `unsupported accelerator source "invalid"`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config, err := ParseConfig(SectionFromProviderParams(test.params))
			if test.err != "" {
				require.EqualError(t, err, test.err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, test.source, config.Source)
			require.Equal(t, test.labelKey, config.KubernetesLabel.Key)
			expectedNamespace := test.gpuNamespace
			if expectedNamespace == "" {
				expectedNamespace = DefaultGPUOperatorNamespace
			}
			expectedDevicePlugin := test.devicePlugin
			if expectedDevicePlugin == "" {
				expectedDevicePlugin = DefaultDevicePluginDaemonSet
			}
			require.Equal(t, expectedNamespace, config.NvidiaSMI.GPUOperatorNamespace)
			require.Equal(t, expectedDevicePlugin, config.NvidiaSMI.DevicePluginDaemonSet)
		})
	}
}

func TestDecodeSection(t *testing.T) {
	section, err := DecodeSection(`{"source":"nvidia-smi","nvidiaSmi":{"gpuOperatorNamespace":"custom"}}`)
	require.NoError(t, err)
	config, err := ParseConfig(section)
	require.NoError(t, err)
	require.Equal(t, SourceNvidiaSMI, config.Source)
	require.Equal(t, "custom", config.NvidiaSMI.GPUOperatorNamespace)
	require.Equal(t, DefaultDevicePluginDaemonSet, config.NvidiaSMI.DevicePluginDaemonSet)

	section, err = DecodeSection("")
	require.NoError(t, err)
	config, err = ParseConfig(section)
	require.NoError(t, err)
	require.Equal(t, SourceNone, config.Source)

	_, err = DecodeSection("{")
	require.ErrorContains(t, err, "could not decode accelerator section")

	section, err = DecodeSection("null")
	require.NoError(t, err)
	_, err = ParseConfig(section)
	require.EqualError(t, err, "accelerator section must be an object with a source")
}

func TestKubernetesDiscoverer(t *testing.T) {
	targets := []Target{
		{
			InstanceID:  "instance-1",
			HostName:    "node-1",
			Labels:      map[string]string{testKubernetesLabel: "label-domain"},
			Annotations: map[string]string{topology.KeyGpuClusterID: "annotation-domain"},
		},
		{InstanceID: "instance-2", HostName: "node-2"},
	}

	tests := []struct {
		name        string
		params      map[string]any
		assignments Assignments
	}{
		{
			name: "nvidia-smi annotation",
			params: map[string]any{"accelerator": map[string]any{
				"source": SourceNvidiaSMI,
			}},
			assignments: Assignments{
				"instance-1": {DomainID: "annotation-domain"},
			},
		},
		{
			name: "Kubernetes label",
			params: map[string]any{"accelerator": map[string]any{
				"source": SourceKubernetesLabel,
				"kubernetesLabel": map[string]any{
					"key": testKubernetesLabel,
				},
			}},
			assignments: Assignments{
				"instance-1": {DomainID: "label-domain"},
			},
		},
		{
			name:        "none",
			assignments: Assignments{},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			discoverer, err := NewKubernetesDiscoverer(SectionFromProviderParams(test.params))
			require.NoError(t, err)

			assignments, err := discoverer.Discover(context.Background(), targets)
			require.NoError(t, err)
			require.Equal(t, test.assignments, assignments)
		})
	}
}

func TestDiscoverKubernetesDomainsUsesCanonicalIdentity(t *testing.T) {
	nodes := &corev1.NodeList{Items: []corev1.Node{
		{ObjectMeta: metav1.ObjectMeta{
			Name:   "kubernetes-node-1",
			Labels: map[string]string{testKubernetesLabel: "domain-1"},
			Annotations: map[string]string{
				topology.KeyNodeInstance: "instance-1",
				topology.KeyNodeRegion:   "region-1",
			},
		}},
		{ObjectMeta: metav1.ObjectMeta{Name: "missing-annotations"}},
		{ObjectMeta: metav1.ObjectMeta{
			Name:   "unrequested-node",
			Labels: map[string]string{testKubernetesLabel: "domain-2"},
			Annotations: map[string]string{
				topology.KeyNodeInstance: "instance-2",
				topology.KeyNodeRegion:   "region-2",
			},
		}},
	}}
	instances := []topology.ComputeInstances{{
		Region:    "region-1",
		Instances: map[string]string{"instance-1": "scheduler-node-1"},
	}}
	discoverer, err := NewKubernetesDiscoverer(KubernetesLabelSection(testKubernetesLabel))
	require.NoError(t, err)

	domains, err := DiscoverKubernetesDomains(context.Background(), discoverer, nodes, instances)
	require.NoError(t, err)
	expected := topology.NewDomainMap()
	expected.AddHost("domain-1", "instance-1", "scheduler-node-1")
	require.Equal(t, expected, domains)
}

func TestTargetsAndDomainMapFromComputeInstances(t *testing.T) {
	targets := TargetsFromComputeInstances([]topology.ComputeInstances{{
		Region: "region-1",
		Instances: map[string]string{
			"instance-1": "node-1",
			"instance-2": "node-2",
		},
	}})
	require.ElementsMatch(t, []Target{
		{InstanceID: "instance-1", HostName: "node-1"},
		{InstanceID: "instance-2", HostName: "node-2"},
	}, targets)

	domains := DomainMapFromAssignments(Assignments{
		"instance-1": {DomainID: "domain-1", SubDomainID: "sub-domain-1"},
	}, targets)
	require.Equal(t, topology.DomainMap{
		"domain-1": {
			"node-1": {
				Domain:     "domain-1",
				SubDomain:  "sub-domain-1",
				InstanceID: "instance-1",
				HostName:   "node-1",
			},
		},
	}, domains)
}

type fakeCommandRunner struct {
	outputs map[string]string
	err     error
}

func (r fakeCommandRunner) Run(context.Context, string, []Target) (map[string]string, error) {
	return r.outputs, r.err
}

type commandRunnerFunc func(context.Context, string, []Target) (map[string]string, error)

func (f commandRunnerFunc) Run(ctx context.Context, command string, targets []Target) (map[string]string, error) {
	return f(ctx, command, targets)
}

func TestCommandDiscoverer(t *testing.T) {
	targets := []Target{{InstanceID: "instance-1", HostName: "node-1"}}

	discoverer, err := NewCommandDiscoverer(SectionFromProviderParams(nil), nil)
	require.NoError(t, err)
	assignments, err := discoverer.Discover(context.Background(), targets)
	require.NoError(t, err)
	require.Empty(t, assignments)

	discoverer, err = NewCommandDiscoverer(configuredSection(map[string]any{
		"source": SourceNvidiaSMI,
	}), fakeCommandRunner{outputs: map[string]string{"node-1": "uuid, 7"}})
	require.NoError(t, err)
	assignments, err = discoverer.Discover(context.Background(), targets)
	require.NoError(t, err)
	require.Equal(t, Assignments{"instance-1": {DomainID: "uuid.7"}}, assignments)

	_, err = NewCommandDiscoverer(configuredSection(map[string]any{
		"source": SourceKubernetesLabel,
		"kubernetesLabel": map[string]any{
			"key": testKubernetesLabel,
		},
	}), nil)
	require.EqualError(t, err, `accelerator source "kubernetes-label" is not supported by command discovery`)
}

func TestKubernetesNodeDiscovererWithoutCollection(t *testing.T) {
	sections := []Section{
		SectionFromProviderParams(nil),
		configuredSection(map[string]any{
			"source": SourceKubernetesLabel,
			"kubernetesLabel": map[string]any{
				"key": testKubernetesLabel,
			},
		}),
		configuredSection(map[string]any{"source": SourceNone}),
	}
	for _, section := range sections {
		discoverer, err := NewKubernetesNodeDiscoverer(section, nil, nil)
		require.NoError(t, err)

		assignments, err := discoverer.Discover(context.Background(), []Target{{InstanceID: "node-1", HostName: "node-1"}})
		require.NoError(t, err)
		require.Empty(t, assignments)
	}

	_, err := NewKubernetesNodeDiscoverer(configuredSection(map[string]any{"source": "invalid"}), nil, nil)
	require.EqualError(t, err, `unsupported accelerator source "invalid"`)

	_, err = NewKubernetesNodeDiscoverer(configuredSection(map[string]any{"source": SourceNvidiaSMI}), nil, nil)
	require.EqualError(t, err, "k8s client is required for nvidia-smi discovery")
}

func TestKubernetesNodeDiscovererUsesSectionConfig(t *testing.T) {
	section, err := DecodeSection(`{
		"source":"nvidia-smi",
		"nvidiaSmi":{
			"gpuOperatorNamespace":"custom-operator",
			"devicePluginDaemonSet":"custom-plugin"
		}
	}`)
	require.NoError(t, err)

	discoverer, err := NewKubernetesNodeDiscoverer(section, kubernetesfake.NewClientset(), &rest.Config{})
	require.NoError(t, err)
	nvidiaDiscoverer, ok := discoverer.(*nvidiaSMIDiscoverer)
	require.True(t, ok)
	runner, ok := nvidiaDiscoverer.runner.(*kubernetesNvidiaSMIRunner)
	require.True(t, ok)
	require.Equal(t, "custom-operator", runner.namespace)
	require.Equal(t, "custom-plugin", runner.daemonSet)
}

func TestNvidiaSMIDiscoverer(t *testing.T) {
	targets := []Target{{InstanceID: "instance-1", HostName: "node-1"}}
	discoverer, err := NewNvidiaSMIDiscoverer(Config{Source: SourceNvidiaSMI}, fakeCommandRunner{outputs: map[string]string{
		"node-1": "uuid, 7\nuuid, 7\n",
	}})
	require.NoError(t, err)

	assignments, err := discoverer.Discover(context.Background(), targets)
	require.NoError(t, err)
	require.Equal(t, Assignments{"instance-1": {DomainID: "uuid.7"}}, assignments)

	discoverer, err = NewNvidiaSMIDiscoverer(Config{Source: SourceNvidiaSMI}, fakeCommandRunner{err: errors.New("command failed")})
	require.NoError(t, err)
	_, err = discoverer.Discover(context.Background(), targets)
	require.EqualError(t, err, "failed to query NVL partition IDs: command failed")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var observedContextErr error
	discoverer, err = NewNvidiaSMIDiscoverer(Config{Source: SourceNvidiaSMI}, commandRunnerFunc(
		func(ctx context.Context, _ string, _ []Target) (map[string]string, error) {
			observedContextErr = ctx.Err()
			return nil, ctx.Err()
		},
	))
	require.NoError(t, err)
	_, err = discoverer.Discover(ctx, targets)
	require.ErrorIs(t, observedContextErr, context.Canceled)
	require.ErrorIs(t, err, context.Canceled)
}

func TestParseNvidiaSMIOutput(t *testing.T) {
	tests := []struct {
		name      string
		output    string
		partition string
		err       string
	}{
		{name: "duplicates", output: "uuid, 7\nuuid , 7\n", partition: "uuid.7"},
		{name: "missing", err: "missing NVL partition ID"},
		{name: "missing UUID", output: ", 7", err: "missing ClusterUUID"},
		{name: "missing clique", output: "uuid, ", err: "missing CliqueId"},
		{name: "malformed CSV", output: "uuid", err: `expected ClusterUUID and CliqueId CSV fields, got "uuid"`},
		{name: "N/A UUID", output: "N/A, 7", err: "ClusterUUID is N/A"},
		{name: "N/A clique", output: "uuid, N/A", err: "CliqueId is N/A"},
		{name: "ambiguous", output: "uuid, 7\nuuid, 8", err: "ambiguous NVL partition IDs: uuid.7, uuid.8"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			partition, err := ParseNvidiaSMIOutput(test.output)
			if test.err != "" {
				require.EqualError(t, err, test.err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, test.partition, partition)
		})
	}
}
