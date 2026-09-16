/*
 * Copyright (c) 2024, NVIDIA CORPORATION.  All rights reserved.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package ib

import (
	"bufio"
	"bytes"
	"fmt"
	"regexp"
	"strings"

	"github.com/dsx-ai-factory/topograph/pkg/topology"
)

var (
	reEmptyLine, reHCA, reSwitch, reConn, reSwitchName, reNodeName *regexp.Regexp
)

func init() {
	reEmptyLine = regexp.MustCompile(`^\s*$`)
	reHCA = regexp.MustCompile(`^Ca\s+\d+\s+"([^"]+)"\s+# "([^"]+)"`)
	reSwitch = regexp.MustCompile(`^Switch\s+\d+\s+"([^"]+)"\s+# "([^"]+)"`)
	reConn = regexp.MustCompile(`^\[\d+\]\s+"([^"]+)"\[\d+\](\([0-9a-f]+\))?\s+# "([^"]+)"`)
	reSwitchName = regexp.MustCompile(`^[^;:]+;([^;:]+):[^;:]+$`)
	reNodeName = regexp.MustCompile(`^(\S+)\s\S+$`)
}

type Switch struct {
	ID    string
	Name  string
	Conn  map[string]string // ID:name
	Nodes map[string]string // ID:node name
}

func GenerateTopologyConfig(data []byte, instances []topology.ComputeInstances) ([]*topology.Vertex, map[string]string, error) {
	switches, hca, err := ParseIbnetdiscoverFile(data)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to parse ibnetdiscover output: %v", err)
	}
	nodes := topology.GetNodeNameMap(instances)
	roots := buildTree(switches, hca, nodes)

	top := make([]*topology.Vertex, 0, len(roots))
	for _, v := range roots {
		top = append(top, v)
	}
	merger := topology.NewMerger(top)
	merger.Merge()

	return merger.TopTier(), hca, nil
}

// process output of ibnetdiscover
func ParseIbnetdiscoverFile(data []byte) (map[string]*Switch, map[string]string, error) {
	switches := make(map[string]*Switch)
	hca := make(map[string]string)
	var entry *Switch

	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := scanner.Text()

		if strings.HasPrefix(line, "#") {
			continue
		}

		if reEmptyLine.MatchString(line) {
			if entry != nil {
				switches[entry.ID] = entry
				entry = nil
			}
			continue
		}

		if match := reHCA.FindStringSubmatch(line); len(match) != 0 {
			if nodeName := extractNodeName(match[2]); len(nodeName) != 0 {
				hca[match[1]] = nodeName
			}
			continue
		}

		if match := reSwitch.FindStringSubmatch(line); len(match) != 0 {
			entry = &Switch{
				ID:    match[1],
				Name:  extractSwitchName(match[2]),
				Conn:  make(map[string]string),
				Nodes: make(map[string]string),
			}
			continue
		}

		if match := reConn.FindStringSubmatch(line); len(match) != 0 && entry != nil {
			id := match[1]
			destName := match[3]
			entry.Conn[id] = destName
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, nil, err
	}
	return switches, hca, nil
}

func buildTree(switches map[string]*Switch, hca map[string]string, nodesInCluster map[string]bool) map[string]*topology.Vertex {
	// all visited nodes in tree
	vertices := make(map[string]*topology.Vertex)
	// current level in the tree
	level := make(map[string]*topology.Vertex)

	// first pass: find all leaves
	for swID, sw := range switches {
		for conID := range sw.Conn {
			if nodeName, ok := hca[conID]; ok {
				// If a switch has HCA, it is a leaf.
				_, isActualNode := nodesInCluster[nodeName]
				if len(nodeName) != 0 && isActualNode {
					sw.Nodes[conID] = nodeName
				}
				delete(sw.Conn, conID)
			}
		}
		if len(sw.Nodes) != 0 {
			// this is the leaf switch.
			// add the node to the first (lower) level in the tree
			v := &topology.Vertex{
				ID:       swID,
				Vertices: make(map[string]*topology.Vertex),
			}
			for _, nodeName := range sw.Nodes {
				v.Vertices[nodeName] = &topology.Vertex{ID: nodeName, Name: nodeName}
			}
			vertices[swID] = v
			level[swID] = v
			sw.Conn = nil
		}
	}

	cnt := 1
	// next pass: complete the tree level by level
	for {
		// create an upper level
		upper := make(map[string]*topology.Vertex)
		for swID, sw := range switches {
			if _, ok := vertices[swID]; ok {
				continue
			}
			children := make(map[string]*topology.Vertex)
			for conID := range sw.Conn {
				// find children if any
				if child, ok := level[conID]; ok {
					children[conID] = child
				}
			}
			if len(children) != 0 {
				v := &topology.Vertex{ID: swID, Vertices: children}
				vertices[swID] = v
				upper[swID] = v
				sw.Conn = nil
			}
		}
		if len(upper) == 0 {
			break
		}
		// complete level
		level = upper
		cnt++
	}

	return level
}

func extractSwitchName(name string) string {
	if m := reSwitchName.FindStringSubmatch(name); m != nil {
		return m[1]
	}
	if !strings.ContainsAny(name, " ;:") {
		return name
	}
	return ""
}

func extractNodeName(name string) string {
	if m := reNodeName.FindStringSubmatch(name); m != nil {
		return m[1]
	}
	return ""
}
