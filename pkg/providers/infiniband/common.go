// SPDX-FileCopyrightText: Copyright (c) 2025 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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
	"bytes"
	"context"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"

	"golang.org/x/sync/errgroup"
	"k8s.io/klog/v2"

	"github.com/dsx-ai-factory/topograph/pkg/ib"
	"github.com/dsx-ai-factory/topograph/pkg/topology"
)

type IBNetDiscover interface {
	Run(context.Context, string) (*bytes.Buffer, error)
	Ports(context.Context, string) ([]IBPort, error)
	RunPort(context.Context, string, IBPort) (*bytes.Buffer, error)
}

type IBPort struct {
	CA   string
	Port string
	// GUID is the node GUID of the channel adapter (16 lowercase hex digits).
	// It is empty when the node did not report one.
	GUID string
}

// key identifies the fabric attachment point that an ibnetdiscover scan covers.
// It matches the "<guid>/<port>" keys returned by ib.HCAPorts.
func (p IBPort) key() string {
	return p.GUID + "/" + p.Port
}

func compareIBPorts(a, b IBPort) int {
	if c := strings.Compare(a.CA, b.CA); c != 0 {
		return c
	}
	return strings.Compare(a.Port, b.Port)
}

const listActiveIBPorts = `for d in /sys/class/infiniband/*; do [ -d "$d" ] || continue; for p in "$d"/ports/*; do [ -f "$p/state" ] || continue; case "$(cat "$p/state")" in *ACTIVE*) printf '%s %s %s\n' "${d##*/}" "${p##*/}" "$(tr -d : < "$d/node_guid" 2>/dev/null)";; esac; done; done`

var validIBCA = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)
var validIBPort = regexp.MustCompile(`^[0-9]+$`)
var validIBGUID = regexp.MustCompile(`^[0-9A-Fa-f]{16}$`)

// maxConcurrentIBPortDiscoveries limits simultaneous full-fabric scans.
const maxConcurrentIBPortDiscoveries = 8

func parseIBPorts(output *bytes.Buffer) ([]IBPort, error) {
	var ports []IBPort
	for line := range strings.SplitSeq(strings.TrimSpace(output.String()), "\n") {
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 && len(fields) != 3 {
			return nil, fmt.Errorf("invalid IB port listing %q", line)
		}
		if !validIBCA.MatchString(fields[0]) || !validIBPort.MatchString(fields[1]) {
			return nil, fmt.Errorf("invalid IB port listing %q", line)
		}
		port := IBPort{CA: fields[0], Port: fields[1]}
		if len(fields) == 3 {
			if !validIBGUID.MatchString(fields[2]) {
				return nil, fmt.Errorf("invalid IB port listing %q", line)
			}
			port.GUID = strings.ToLower(fields[2])
		}
		ports = append(ports, port)
	}
	slices.SortFunc(ports, compareIBPorts)
	return ports, nil
}

type ibPortDiscovery struct {
	node   string
	port   IBPort
	output *bytes.Buffer
	err    error
}

func discoverIBPorts(ctx context.Context, ibnetdiscover IBNetDiscover, discoveries []ibPortDiscovery) ([]ibPortDiscovery, error) {
	results := make([]ibPortDiscovery, len(discoveries))
	group := new(errgroup.Group)
	group.SetLimit(maxConcurrentIBPortDiscoveries)

	for i, discovery := range discoveries {
		if err := ctx.Err(); err != nil {
			break
		}
		results[i].node = discovery.node
		results[i].port = discovery.port
		group.Go(func() error {
			if err := ctx.Err(); err != nil {
				return err
			}
			results[i].output, results[i].err = ibnetdiscover.RunPort(ctx, discovery.node, discovery.port)
			return nil
		})
	}
	if err := group.Wait(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return results, nil
}

// listIBPortDiscoveries lists the active IB ports of every node, at most
// maxConcurrentIBPortDiscoveries nodes at a time, in node and port order.
func listIBPortDiscoveries(ctx context.Context, ibnetdiscover IBNetDiscover, nodes []string) ([]ibPortDiscovery, error) {
	perNode := make([][]IBPort, len(nodes))
	group := new(errgroup.Group)
	group.SetLimit(maxConcurrentIBPortDiscoveries)
	for i, node := range nodes {
		if ctx.Err() != nil {
			break
		}
		group.Go(func() error {
			if err := ctx.Err(); err != nil {
				return err
			}
			ports, err := ibnetdiscover.Ports(ctx, node)
			if err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				klog.Warningf("failed to list active IB ports on %q: %v", node, err)
				return nil
			}
			slices.SortFunc(ports, compareIBPorts)
			perNode[i] = ports
			return nil
		})
	}
	if err := group.Wait(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	var discoveries []ibPortDiscovery
	for i, node := range nodes {
		for _, port := range perNode[i] {
			discoveries = append(discoveries, ibPortDiscovery{node: node, port: port})
		}
	}
	return discoveries, nil
}

// nextIBPortBatch picks the next scans to run and returns what is left.
// Ports already covered by a finished scan are dropped. Among ports with a GUID
// only the first one per CA/port label joins a batch: nodes in the same rail
// normally use the same label, so one scan per rail runs in parallel and the
// rest are re-checked once its output is known. Ports without a GUID cannot be
// deduplicated and are always scanned.
func nextIBPortBatch(pending []ibPortDiscovery, covered map[string]bool) (batch, rest []ibPortDiscovery) {
	labels := make(map[string]bool)
	for _, d := range pending {
		if d.port.GUID != "" {
			if covered[d.port.key()] {
				continue
			}
			label := d.port.CA + "/" + d.port.Port
			if len(batch) >= maxConcurrentIBPortDiscoveries || labels[label] {
				rest = append(rest, d)
				continue
			}
			labels[label] = true
		} else if len(batch) >= maxConcurrentIBPortDiscoveries {
			rest = append(rest, d)
			continue
		}
		batch = append(batch, d)
	}
	return batch, rest
}

func getIbTree(ctx context.Context, cis []topology.ComputeInstances, ibnetdiscover IBNetDiscover, selector *switchSelector) (*topology.Vertex, error) {
	nodeVisited := make(map[string]bool)
	rootMap := make(map[string]*topology.Vertex)
	acceptedGroups := make(map[string][]string)
	nodes := topology.GetNodeNameList(cis)
	slices.Sort(nodes)
	nodes = slices.Compact(nodes)

	processOutput := func(node string, stdout *bytes.Buffer) error {
		if stdout == nil || !strings.Contains(stdout.String(), "Topology file:") {
			klog.Warningf("Missing ibnetdiscover output for node %q", node)
			return nil
		}
		ibRoots, hca, err := ib.GenerateTopologyConfig(stdout.Bytes(), cis, selector.ibSelector())
		if err != nil {
			return fmt.Errorf("IB GenerateTopologyConfig failed: %v", err)
		}
		if selector == nil {
			for _, nodeName := range hca {
				nodeVisited[nodeName] = true
			}
		} else {
			groups := make(map[string][]string)
			for _, root := range ibRoots {
				if err := collectLeafGroups(root, groups); err != nil {
					return err
				}
			}
			duplicate := false
			newNodes := false
			groupNodes := make([]string, 0, len(groups))
			for nodeName := range groups {
				groupNodes = append(groupNodes, nodeName)
			}
			sort.Strings(groupNodes)
			for _, nodeName := range groupNodes {
				peers := groups[nodeName]
				if previous, ok := acceptedGroups[nodeName]; ok {
					duplicate = true
					if !slices.Equal(previous, peers) {
						return fmt.Errorf("selected IB ports give conflicting leaf groups for node %q", nodeName)
					}
				} else {
					newNodes = true
				}
			}
			if duplicate && newNodes {
				return fmt.Errorf("selected IB ports have overlapping but incomplete node sets")
			}
			if duplicate {
				return nil
			}
			for _, nodeName := range groupNodes {
				peers := groups[nodeName]
				acceptedGroups[nodeName] = peers
			}
		}
		for _, v := range ibRoots {
			rootMap[v.ID] = v
		}
		return nil
	}

	if selector == nil {
		for _, node := range nodes {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if nodeVisited[node] {
				continue
			}
			stdout, err := ibnetdiscover.Run(ctx, node)
			if err != nil {
				if ctx.Err() != nil {
					return nil, ctx.Err()
				}
				klog.Warningf("failed to run ibnetdiscover: %v", err)
				continue
			}
			if err := processOutput(node, stdout); err != nil {
				return nil, err
			}
		}
	} else {
		pending, err := listIBPortDiscoveries(ctx, ibnetdiscover, nodes)
		if err != nil {
			return nil, err
		}
		// Ports whose fabric attachment (node GUID and port number) already
		// appeared in a completed scan sit on a subnet that is already known,
		// so scanning them again would only repeat the same output.
		covered := make(map[string]bool)
		for len(pending) > 0 {
			var batch []ibPortDiscovery
			batch, pending = nextIBPortBatch(pending, covered)
			if len(batch) == 0 {
				break
			}
			results, err := discoverIBPorts(ctx, ibnetdiscover, batch)
			if err != nil {
				return nil, err
			}
			for _, result := range results {
				if result.err != nil {
					klog.Warningf("failed to run ibnetdiscover on %q port %s/%s: %v", result.node, result.port.CA, result.port.Port, result.err)
					continue
				}
				if err := processOutput(result.node, result.output); err != nil {
					return nil, err
				}
				if result.output != nil {
					for key := range ib.HCAPorts(result.output.Bytes()) {
						covered[key] = true
					}
				}
			}
		}
	}
	if selector != nil && len(rootMap) == 0 {
		return nil, fmt.Errorf("switchSelector matched no usable InfiniBand topology")
	}

	roots := make([]*topology.Vertex, 0, len(rootMap))
	for _, v := range rootMap {
		roots = append(roots, v)
	}

	merger := topology.NewMerger(roots)
	treeRoot := &topology.Vertex{
		Vertices: make(map[string]*topology.Vertex),
	}
	for _, v := range merger.TopTier() {
		treeRoot.Vertices[v.ID] = v
	}

	return treeRoot, nil
}

func collectLeafGroups(v *topology.Vertex, groups map[string][]string) error {
	if len(v.Vertices) == 0 {
		return nil
	}
	var peers []string
	for _, child := range v.Vertices {
		if len(child.Vertices) == 0 && child.Name != "" {
			peers = append(peers, child.Name)
		}
	}
	if len(peers) != 0 {
		sort.Strings(peers)
		for _, nodeName := range peers {
			if previous, ok := groups[nodeName]; ok && !slices.Equal(previous, peers) {
				return fmt.Errorf("node %q appears under conflicting IB leaf switches", nodeName)
			}
			groups[nodeName] = peers
		}
	}
	for _, child := range v.Vertices {
		if err := collectLeafGroups(child, groups); err != nil {
			return err
		}
	}
	return nil
}
