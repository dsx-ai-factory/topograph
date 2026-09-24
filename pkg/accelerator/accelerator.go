/*
 * Copyright 2026 NVIDIA CORPORATION
 * SPDX-License-Identifier: Apache-2.0
 */

// Package accelerator discovers accelerator-domain assignments independently
// of the network fabric a provider discovers.
package accelerator

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	internalconfig "github.com/dsx-ai-factory/topograph/internal/config"
	"github.com/dsx-ai-factory/topograph/pkg/topology"
)

const (
	SourceNvidiaSMI       = "nvidia-smi"
	SourceKubernetesLabel = "kubernetes-label"
	SourceNone            = "none"

	DefaultGPUOperatorNamespace  = "gpu-operator"
	DefaultDevicePluginDaemonSet = "nvidia-device-plugin-daemonset"
)

type Config struct {
	Source          string                `mapstructure:"source"`
	KubernetesLabel KubernetesLabelConfig `mapstructure:"kubernetesLabel"`
	NvidiaSMI       NvidiaSMIConfig       `mapstructure:"nvidiaSmi"`
}

type KubernetesLabelConfig struct {
	Key string `mapstructure:"key"`
}

type NvidiaSMIConfig struct {
	GPUOperatorNamespace  string `mapstructure:"gpuOperatorNamespace"`
	DevicePluginDaemonSet string `mapstructure:"devicePluginDaemonSet"`
}

// Section is the accelerator section extracted from provider parameters. It
// retains whether the section was omitted so omission can be distinguished
// from an explicitly configured null value.
type Section struct {
	value   any
	present bool
}

// KubernetesLabelSection returns an accelerator section configured to read
// domain IDs from the supplied Kubernetes Node label.
func KubernetesLabelSection(key string) Section {
	return Section{
		value: map[string]any{
			"source": SourceKubernetesLabel,
			"kubernetesLabel": map[string]any{
				"key": key,
			},
		},
		present: true,
	}
}

// Present reports whether the accelerator section was explicitly supplied.
func (s Section) Present() bool {
	return s.present
}

// SectionFromProviderParams extracts the accelerator section without parsing
// source-specific fields in the provider.
func SectionFromProviderParams(providerParams map[string]any) Section {
	value, present := providerParams["accelerator"]
	return Section{value: value, present: present}
}

// DecodeSection decodes an accelerator section transported as JSON. An empty
// value represents an omitted section.
func DecodeSection(encoded string) (Section, error) {
	if strings.TrimSpace(encoded) == "" {
		return Section{}, nil
	}

	var value any
	if err := json.Unmarshal([]byte(encoded), &value); err != nil {
		return Section{}, fmt.Errorf("could not decode accelerator section: %w", err)
	}
	return Section{value: value, present: true}, nil
}

// ParseConfig parses and validates the accelerator section of provider
// parameters. An omitted or empty section disables accelerator discovery.
func ParseConfig(section Section) (Config, error) {
	config := Config{Source: SourceNone}
	if !section.present {
		config.SetDefaults()
		return config, nil
	}
	if section.value == nil {
		return Config{}, fmt.Errorf("accelerator section must be an object with a source")
	}

	value := reflect.ValueOf(section.value)
	if value.Kind() != reflect.Map {
		return Config{}, fmt.Errorf("accelerator section must be an object with a source")
	}
	if value.Len() != 0 {
		config = Config{}
		if err := internalconfig.Decode(section.value, &config); err != nil {
			return Config{}, err
		}
	}

	config.SetDefaults()
	if err := config.Validate(); err != nil {
		return Config{}, err
	}
	return config, nil
}

func (c *Config) SetDefaults() {
	c.Source = strings.ToLower(strings.TrimSpace(c.Source))
	c.KubernetesLabel.Key = strings.TrimSpace(c.KubernetesLabel.Key)
	c.NvidiaSMI.GPUOperatorNamespace = strings.TrimSpace(c.NvidiaSMI.GPUOperatorNamespace)
	if c.NvidiaSMI.GPUOperatorNamespace == "" {
		c.NvidiaSMI.GPUOperatorNamespace = DefaultGPUOperatorNamespace
	}
	c.NvidiaSMI.DevicePluginDaemonSet = strings.TrimSpace(c.NvidiaSMI.DevicePluginDaemonSet)
	if c.NvidiaSMI.DevicePluginDaemonSet == "" {
		c.NvidiaSMI.DevicePluginDaemonSet = DefaultDevicePluginDaemonSet
	}
}

func (c Config) Validate() error {
	if err := ValidateSource(c.Source); err != nil {
		return err
	}
	if c.Source == SourceKubernetesLabel {
		if strings.TrimSpace(c.KubernetesLabel.Key) == "" {
			return fmt.Errorf("accelerator kubernetesLabel.key must be set for source %q", SourceKubernetesLabel)
		}
	}
	return nil
}

func ValidateSource(source string) error {
	if source == "" {
		return fmt.Errorf("accelerator source must be set")
	}
	switch source {
	case SourceNvidiaSMI, SourceNone:
		return nil
	case SourceKubernetesLabel:
		return nil
	default:
		return fmt.Errorf("unsupported accelerator source %q", source)
	}
}

func NewNoneDiscoverer() Discoverer {
	return noneDiscoverer{}
}

type Target struct {
	InstanceID  string
	HostName    string
	Labels      map[string]string
	Annotations map[string]string
}

type Assignment struct {
	DomainID    string
	SubDomainID string
}

type Assignments map[string]Assignment

type Discoverer interface {
	Discover(context.Context, []Target) (Assignments, error)
}

// TargetsFromComputeInstances converts the canonical request identity mapping
// into accelerator discovery targets.
func TargetsFromComputeInstances(instances []topology.ComputeInstances) []Target {
	targets := make([]Target, 0)
	for _, regionalInstances := range instances {
		for instanceID, hostName := range regionalInstances.Instances {
			targets = append(targets, Target{InstanceID: instanceID, HostName: hostName})
		}
	}
	return targets
}

// DomainMapFromAssignments converts discovered accelerator assignments into
// the canonical topology domain map using the target identity mapping.
func DomainMapFromAssignments(assignments Assignments, targets []Target) topology.DomainMap {
	domainMap := topology.NewDomainMap()
	for _, target := range targets {
		assignment, ok := assignments[target.InstanceID]
		if !ok {
			continue
		}
		domainMap.AddHostInfo(&topology.HostInfo{
			Domain:     assignment.DomainID,
			SubDomain:  assignment.SubDomainID,
			InstanceID: target.InstanceID,
			HostName:   target.HostName,
		})
	}
	return domainMap
}

type metadataDiscoverer struct {
	key   string
	value func(Target, string) string
}

// NewKubernetesDiscoverer parses provider parameters and returns a discoverer
// that resolves accelerator domains from Kubernetes Node metadata.
func NewKubernetesDiscoverer(section Section) (Discoverer, error) {
	config, err := ParseConfig(section)
	if err != nil {
		return nil, err
	}
	return NewKubernetesDiscovererFromConfig(config)
}

// NewKubernetesDiscovererFromConfig returns a discoverer that resolves
// accelerator domains from Kubernetes Node metadata using validated config.
func NewKubernetesDiscovererFromConfig(config Config) (Discoverer, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	switch config.Source {
	case SourceNvidiaSMI:
		return &metadataDiscoverer{
			key: topology.KeyGpuClusterID,
			value: func(target Target, key string) string {
				return target.Annotations[key]
			},
		}, nil
	case SourceKubernetesLabel:
		return &metadataDiscoverer{
			key: config.KubernetesLabel.Key,
			value: func(target Target, key string) string {
				return target.Labels[key]
			},
		}, nil
	case SourceNone:
		return noneDiscoverer{}, nil
	default:
		return nil, fmt.Errorf("unsupported accelerator source %q", config.Source)
	}
}

func (d *metadataDiscoverer) Discover(_ context.Context, targets []Target) (Assignments, error) {
	assignments := make(Assignments)
	for _, target := range targets {
		domainID := strings.TrimSpace(d.value(target, d.key))
		if domainID == "" {
			continue
		}
		assignments[target.InstanceID] = Assignment{DomainID: domainID}
	}

	return assignments, nil
}

type noneDiscoverer struct{}

func (noneDiscoverer) Discover(context.Context, []Target) (Assignments, error) {
	return make(Assignments), nil
}
