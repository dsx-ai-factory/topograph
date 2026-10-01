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
}

const listActiveIBPorts = `for d in /sys/class/infiniband/*; do [ -d "$d" ] || continue; for p in "$d"/ports/*; do [ -f "$p/state" ] || continue; case "$(cat "$p/state")" in *ACTIVE*) printf '%s %s\n' "${d##*/}" "${p##*/}";; esac; done; done`

var validIBCA = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)
var validIBPort = regexp.MustCompile(`^[0-9]+$`)

// maxConcurrentIBPortDiscoveries limits simultaneous full-fabric scans.
const maxConcurrentIBPortDiscoveries = 8

func parseIBPorts(output *bytes.Buffer) ([]IBPort, error) {
	var ports []IBPort
	for line := range strings.SplitSeq(strings.TrimSpace(output.String()), "\n") {
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
		i, discovery := i, discovery
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
		var discoveries []ibPortDiscovery
		for _, node := range nodes {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			ports, err := ibnetdiscover.Ports(ctx, node)
			if err != nil {
				if ctx.Err() != nil {
					return nil, ctx.Err()
				}
				klog.Warningf("failed to list active IB ports on %q: %v", node, err)
				continue
			}
			sort.Slice(ports, func(i, j int) bool {
				if ports[i].CA == ports[j].CA {
					return ports[i].Port < ports[j].Port
				}
				return ports[i].CA < ports[j].CA
			})
			for _, port := range ports {
				discoveries = append(discoveries, ibPortDiscovery{node: node, port: port})
			}
		}
		results, err := discoverIBPorts(ctx, ibnetdiscover, discoveries)
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
