// SPDX-FileCopyrightText: Copyright (c) 2024-2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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

package infiniband

import (
	"bufio"
	"bytes"
	"context"
	"strings"

	"github.com/dsx-ai-factory/topograph/internal/exec"
	"github.com/dsx-ai-factory/topograph/pkg/accelerator"
)

type IBNetDiscoverBM struct{}

func (h *IBNetDiscoverBM) Run(ctx context.Context, node string) (*bytes.Buffer, error) {
	return exec.Pdsh(ctx, "sudo ibnetdiscover", []string{node}, "-N")
}

type pdshNvidiaSMIRunner struct{}

func (pdshNvidiaSMIRunner) Run(ctx context.Context, command string, targets []accelerator.Target) (map[string]string, error) {
	nodes := make([]string, 0, len(targets))
	for _, target := range targets {
		nodes = append(nodes, target.HostName)
	}

	stdout, err := exec.Pdsh(ctx, command, nodes)
	if err != nil {
		return nil, err
	}
	return parsePdshNvidiaSMIOutput(stdout)
}

func parsePdshNvidiaSMIOutput(stdout *bytes.Buffer) (map[string]string, error) {
	outputs := make(map[string]string)
	scanner := bufio.NewScanner(stdout)
	for scanner.Scan() {
		nodeLine := scanner.Text()
		arr := strings.SplitN(nodeLine, ":", 2)
		if len(arr) != 2 {
			continue
		}
		nodeName := strings.TrimSpace(arr[0])
		outputs[nodeName] += strings.TrimSpace(arr[1]) + "\n"
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}

	return outputs, nil
}
