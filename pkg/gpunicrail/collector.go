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

package gpunicrail

import (
	"context"
	"fmt"
	"strings"
	"time"

	"k8s.io/klog/v2"
)

// Commands run against the GPU Operator pod.
var (
	InventoryCommand = []string{"nvidia-smi", "--query-gpu=index,uuid,pci.bus_id", "--format=csv,noheader"}
	TopologyCommand  = []string{"nvidia-smi", "topo", "-all", "-m"}
)

// Runner runs a command and returns its standard output.
type Runner interface {
	Run(ctx context.Context, cmd []string) (string, error)
}

// Collector gathers the mapping of the local Node.
type Collector struct {
	// LLDP runs lldpctl against the host lldpd socket.
	LLDP Runner
	// GPU runs nvidia-smi in the same-node GPU Operator pod.
	GPU     Runner
	Matcher *Matcher
	Sysfs   Sysfs
	Now     func() time.Time
}

// Collect returns the Node's mapping. A nil Status with a nil error means no
// rail interfaces were selected, so no record should exist.
func (c *Collector) Collect(ctx context.Context) (*Status, error) {
	now := c.Now
	if now == nil {
		now = time.Now
	}

	klog.InfoS("Collecting GPU/NIC rail mapping: querying host LLDP")
	out, err := c.LLDP.Run(ctx, []string{"lldpctl", "-f", "json"})
	if err != nil {
		return nil, fmt.Errorf("lldp: %w", err)
	}
	lldpAt := now().UTC().Format(time.RFC3339)
	names, err := ParseLLDP([]byte(out))
	if err != nil {
		return nil, err
	}
	klog.Infof("Discovered %d LLDP interfaces", len(names))
	klog.V(4).InfoS("Binding LLDP interfaces to rails and PCI devices", "interfaces", names)
	bindings, err := BindRails(names, c.Matcher, c.Sysfs.PCIAddress)
	if err != nil {
		return nil, err
	}
	klog.Infof("Resolved %d GPU/NIC rail bindings", len(bindings))
	if len(bindings) == 0 {
		return nil, nil
	}

	status := &Status{Sources: Sources{LLDP: lldpAt}}
	// Deployment scheduling selects the GPU nodes; every collected mapping
	// requires a usable GPU inventory and topology from the configured source.
	klog.InfoS("Collecting GPU inventory")
	invOut, err := c.GPU.Run(ctx, InventoryCommand)
	if err != nil {
		return nil, fmt.Errorf("GPU inventory: %w", err)
	}
	inv, err := ParseInventory(invOut)
	if err != nil {
		return nil, err
	}
	klog.InfoS("Collecting GPU topology", "gpus", len(inv))
	topoOut, err := c.GPU.Run(ctx, TopologyCommand)
	if err != nil {
		return nil, fmt.Errorf("GPU topology: %w", err)
	}
	status.Sources.NvidiaSMI = now().UTC().Format(time.RFC3339)
	matrix, err := ParseMatrix(topoOut)
	if err != nil {
		return nil, err
	}
	klog.V(2).InfoS("Collected GPU topology; selecting GPU paths", "rows", len(matrix.Cells), "columns", len(matrix.Cols))
	if status.NICs, err = Select(bindings, c.Sysfs.RDMADevices, inv, matrix); err != nil {
		return nil, err
	}
	if err := status.Validate(); err != nil {
		return nil, fmt.Errorf("invalid mapping: %w", err)
	}
	klog.InfoS("Completed GPU/NIC rail collection", "nics", len(status.NICs), "gpus", len(inv))
	return status, nil
}

// execRunner is a Runner that runs commands in the local container.
type execRunner struct {
	run    func(ctx context.Context, exe string, args []string) (string, error)
	socket string
}

// NewLLDPRunner returns a Runner that invokes lldpctl against socket. A
// non-empty socket is passed as `-u <socket>`.
func NewLLDPRunner(run func(ctx context.Context, exe string, args []string) (string, error), socket string) Runner {
	return &execRunner{run: run, socket: socket}
}

func (r *execRunner) Run(ctx context.Context, cmd []string) (string, error) {
	args := cmd[1:]
	if r.socket != "" {
		args = append([]string{"-u", r.socket}, args...)
	}
	return r.run(ctx, cmd[0], args)
}

// String implements fmt.Stringer for logs.
func (s Sources) String() string {
	return strings.TrimSpace(fmt.Sprintf("lldp=%s nvidiaSmi=%s", s.LLDP, s.NvidiaSMI))
}
