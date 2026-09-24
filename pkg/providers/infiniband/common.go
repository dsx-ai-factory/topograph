/*
 * Copyright 2025 NVIDIA CORPORATION
 * SPDX-License-Identifier: Apache-2.0
 */

package infiniband

import (
	"bytes"
	"context"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"

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
}

const listActiveIBPorts = `for d in /sys/class/infiniband/*; do [ -d "$d" ] || continue; for p in "$d"/ports/*; do [ -f "$p/state" ] || continue; case "$(cat "$p/state")" in *ACTIVE*) printf '%s %s\n' "${d##*/}" "${p##*/}";; esac; done; done`

var validIBCA = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)
var validIBPort = regexp.MustCompile(`^[0-9]+$`)

func parseIBPorts(output *bytes.Buffer) ([]IBPort, error) {
	var ports []IBPort
	for _, line := range strings.Split(strings.TrimSpace(output.String()), "\n") {
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 {
			return nil, fmt.Errorf("invalid IB port listing %q", line)
		}
		if !validIBCA.MatchString(fields[0]) || !validIBPort.MatchString(fields[1]) {
			return nil, fmt.Errorf("invalid IB port listing %q", line)
		}
		ports = append(ports, IBPort{CA: fields[0], Port: fields[1]})
	}
	sort.Slice(ports, func(i, j int) bool {
		if ports[i].CA == ports[j].CA {
			return ports[i].Port < ports[j].Port
		}
		return ports[i].CA < ports[j].CA
	})
	return ports, nil
}

func getIbTree(ctx context.Context, cis []topology.ComputeInstances, ibnetdiscover IBNetDiscover, selector *switchSelector) (*topology.Vertex, error) {
	nodeVisited := make(map[string]bool)
	rootMap := make(map[string]*topology.Vertex)
	acceptedGroups := make(map[string][]string)

	for _, node := range topology.GetNodeNameList(cis) {
		if _, exists := nodeVisited[node]; !exists {
			var outputs []*bytes.Buffer
			if selector == nil {
				stdout, err := ibnetdiscover.Run(ctx, node)
				if err != nil {
					klog.Warningf("failed to run ibnetdiscover: %v", err)
					continue
				}
				outputs = append(outputs, stdout)
			} else {
				ports, err := ibnetdiscover.Ports(ctx, node)
				if err != nil {
					klog.Warningf("failed to list active IB ports on %q: %v", node, err)
					continue
				}
				for _, port := range ports {
					stdout, err := ibnetdiscover.RunPort(ctx, node, port)
					if err != nil {
						klog.Warningf("failed to run ibnetdiscover on %q port %s/%s: %v", node, port.CA, port.Port, err)
						continue
					}
					outputs = append(outputs, stdout)
				}
			}
			for _, stdout := range outputs {
				if !strings.Contains(stdout.String(), "Topology file:") {
					klog.Warningf("Missing ibnetdiscover output for node %q", node)
					continue
				}
				var ibRoots []*topology.Vertex
				var hca map[string]string
				var err error
				if selector == nil {
					ibRoots, hca, err = ib.GenerateTopologyConfig(stdout.Bytes(), cis)
				} else {
					ibRoots, hca, err = ib.GenerateTopologyConfigFiltered(stdout.Bytes(), cis, selector.acceptsLeaf, selector.excludes)
				}
				if err != nil {
					return nil, fmt.Errorf("IB GenerateTopologyConfig failed: %v", err)
				}
				if selector == nil {
					for _, nodeName := range hca {
						nodeVisited[nodeName] = true
					}
				}
				if selector != nil {
					groups := make(map[string][]string)
					for _, root := range ibRoots {
						if err := collectLeafGroups(root, groups); err != nil {
							return nil, err
						}
					}
					duplicate := false
					newNodes := false
					for nodeName, peers := range groups {
						if previous, ok := acceptedGroups[nodeName]; ok {
							duplicate = true
							if !slices.Equal(previous, peers) {
								return nil, fmt.Errorf("selected IB ports give conflicting leaf groups for node %q", nodeName)
							}
						} else {
							newNodes = true
						}
					}
					if duplicate && newNodes {
						return nil, fmt.Errorf("selected IB ports have overlapping but incomplete node sets")
					}
					if duplicate {
						continue
					}
					for nodeName, peers := range groups {
						acceptedGroups[nodeName] = peers
					}
				}
				for _, v := range ibRoots {
					if selector != nil {
						markVisitedNodes(v, nodeVisited)
					}
					rootMap[v.ID] = v
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

func markVisitedNodes(v *topology.Vertex, visited map[string]bool) {
	if len(v.Vertices) == 0 && v.Name != "" {
		visited[v.Name] = true
	}
	for _, child := range v.Vertices {
		markVisitedNodes(child, visited)
	}
}
