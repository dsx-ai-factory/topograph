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

package accelerator

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

const NvidiaSMICommand = "nvidia-smi --query-gpu=fabric.clusterUuid,fabric.cliqueId --format=csv,noheader"

type CommandRunner interface {
	Run(context.Context, string, []Target) (map[string]string, error)
}

type nvidiaSMIDiscoverer struct {
	runner CommandRunner
}

// NewCommandDiscoverer parses provider parameters and returns a discoverer
// suitable for environments that can execute commands on accelerator nodes.
func NewCommandDiscoverer(section Section, runner CommandRunner) (Discoverer, error) {
	config, err := ParseConfig(section)
	if err != nil {
		return nil, err
	}

	switch config.Source {
	case SourceNvidiaSMI:
		return NewNvidiaSMIDiscoverer(config, runner)
	case SourceNone:
		return NewNoneDiscoverer(), nil
	default:
		return nil, fmt.Errorf("accelerator source %q is not supported by command discovery", config.Source)
	}
}

func NewNvidiaSMIDiscoverer(config Config, runner CommandRunner) (Discoverer, error) {
	config.SetDefaults()
	if err := config.Validate(); err != nil {
		return nil, err
	}
	if config.Source != SourceNvidiaSMI {
		return nil, fmt.Errorf("accelerator source %q cannot use an nvidia-smi command runner", config.Source)
	}
	if runner == nil {
		return nil, fmt.Errorf("nvidia-smi command runner is required")
	}

	return &nvidiaSMIDiscoverer{runner: runner}, nil
}

func (d *nvidiaSMIDiscoverer) Discover(ctx context.Context, targets []Target) (Assignments, error) {
	outputs, err := d.runner.Run(ctx, NvidiaSMICommand, targets)
	if err != nil {
		return nil, fmt.Errorf("failed to query NVL partition IDs: %w", err)
	}

	assignments := make(Assignments)
	for _, target := range targets {
		output := strings.TrimSpace(outputs[target.HostName])
		if output == "" {
			continue
		}

		partitionID, err := ParseNvidiaSMIOutput(output)
		if err != nil {
			return nil, fmt.Errorf("invalid nvidia-smi output for node %q: %w", target.HostName, err)
		}
		assignments[target.InstanceID] = Assignment{DomainID: partitionID}
	}

	return assignments, nil
}

func ParseNvidiaSMIOutput(output string) (string, error) {
	partitions := make(map[string]struct{})
	for line := range strings.Lines(output) {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		partitionID, err := parseNVLPartitionID(line)
		if err != nil {
			return "", err
		}
		partitions[partitionID] = struct{}{}
	}

	if len(partitions) == 0 {
		return "", fmt.Errorf("missing NVL partition ID")
	}

	partitionIDs := make([]string, 0, len(partitions))
	for partitionID := range partitions {
		partitionIDs = append(partitionIDs, partitionID)
	}
	sort.Strings(partitionIDs)

	if len(partitionIDs) != 1 {
		return "", fmt.Errorf("ambiguous NVL partition IDs: %s", strings.Join(partitionIDs, ", "))
	}

	return partitionIDs[0], nil
}

func parseNVLPartitionID(line string) (string, error) {
	fields := strings.Split(line, ",")
	if len(fields) != 2 {
		return "", fmt.Errorf("expected ClusterUUID and CliqueId CSV fields, got %q", line)
	}

	clusterUUID := strings.TrimSpace(fields[0])
	if clusterUUID == "" {
		return "", fmt.Errorf("missing ClusterUUID")
	}
	if clusterUUID == "N/A" {
		return "", fmt.Errorf("ClusterUUID is N/A")
	}

	cliqueID := strings.TrimSpace(fields[1])
	if cliqueID == "" {
		return "", fmt.Errorf("missing CliqueId")
	}
	if cliqueID == "N/A" {
		return "", fmt.Errorf("CliqueId is N/A")
	}

	return clusterUUID + "." + cliqueID, nil
}
