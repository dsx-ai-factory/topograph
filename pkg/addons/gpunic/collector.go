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

package gpunic

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

// Runner runs a command and returns its standard output, including partial
// stdout on failure when available.
type Runner interface {
	Run(ctx context.Context, cmd []string) (string, error)
}

// Collector gathers the mapping of the local Node.
type Collector struct {
	// LLDP runs lldpcli against the host lldpd socket.
	LLDP Runner
	// GPU runs nvidia-smi in the same-node GPU Operator pod.
	GPU   Runner
	Sysfs Sysfs
	Now   func() time.Time
}

// Collect returns the Node's mapping. A nil Status with a nil error means no
// PCI-backed NIC interfaces have LLDP neighbors, so no record should exist.
func (c *Collector) Collect(ctx context.Context) (*Status, error) {
	now := c.Now
	if now == nil {
		now = time.Now
	}

	klog.InfoS("Collecting GPU/NIC mapping: querying host LLDP")
	out, err := c.LLDP.Run(ctx, []string{"lldpcli", "-f", "json0", "show", "neighbors", "details"})
	if err != nil {
		return nil, fmt.Errorf("lldp: %w", err)
	}
	lldpAt := now().UTC().Format(time.RFC3339)
	switches, err := ParseLLDP([]byte(out))
	if err != nil {
		return nil, err
	}
	nics, err := c.Sysfs.NICs()
	if err != nil {
		return nil, err
	}
	bindings, err := BindSwitches(nics, switches)
	if err != nil {
		return nil, err
	}
	klog.InfoS("Resolved GPU/NIC bindings", "nics", len(bindings), "switchNames", len(switches))
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
	topoOut, err := c.topology(ctx)
	if err != nil {
		return nil, fmt.Errorf("GPU topology: %w", err)
	}
	status.Sources.NvidiaSMI = now().UTC().Format(time.RFC3339)
	matrix, err := ParseMatrix(topoOut)
	if err != nil {
		return nil, err
	}
	klog.V(2).InfoS("Collected GPU topology; selecting GPU paths", "rows", len(matrix.Cells), "columns", len(matrix.Cols), "nicEntries", len(matrix.NICs), "dataDirectEntries", len(matrix.DDs))
	if status.NICs, err = Select(bindings, c.Sysfs, inv, matrix); err != nil {
		return nil, err
	}
	if err := status.Validate(); err != nil {
		return nil, fmt.Errorf("invalid mapping: %w", err)
	}
	klog.InfoS("Completed GPU/NIC collection", "nics", len(status.NICs), "gpus", len(inv))
	return status, nil
}

// topology prefers the complete matrix, which includes Data Direct on newer
// NVIDIA versions. Retry the older command only for the explicit unsupported
// -all diagnostic; unrelated failures must not silently downgrade discovery.
func (c *Collector) topology(ctx context.Context) (string, error) {
	out, err := c.GPU.Run(ctx, TopologyCommand)
	if err == nil {
		return out, nil
	}
	diagnostic := strings.ToLower(out + "\n" + err.Error())
	if !strings.Contains(diagnostic, `option "-all" is not recognized`) {
		return "", err
	}
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	klog.InfoS("nvidia-smi does not support -all; retrying topology with -m")
	return c.GPU.Run(ctx, []string{"nvidia-smi", "topo", "-m"})
}

// execRunner is a Runner that runs commands in the local container.
type execRunner struct {
	run    func(ctx context.Context, exe string, args []string) (string, error)
	socket string
}

// NewLLDPRunner returns a Runner that invokes lldpcli against socket. A
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
