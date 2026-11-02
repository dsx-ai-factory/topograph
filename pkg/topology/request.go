// SPDX-FileCopyrightText: Copyright (c) 2024 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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

package topology

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"sort"
	"strings"
)

type Request struct {
	Provider Provider           `json:"provider"`
	Engine   Engine             `json:"engine"`
	Nodes    []ComputeInstances `json:"nodes"`
}

type Provider struct {
	Name   string         `json:"name"`
	Creds  map[string]any `json:"creds"` // access credentials
	Params map[string]any `json:"params"`
}

type Engine struct {
	Name   string         `json:"name"`
	Params map[string]any `json:"params"`
}

type ComputeInstances struct {
	Region    string            `json:"region"`
	Instances map[string]string `json:"instances"` // <instance ID>:<node name> map
}

func NewRequest(prv Provider, eng Engine) *Request {
	return &Request{
		Provider: prv,
		Engine:   eng,
	}
}

func (p *Request) String() string {
	var sb strings.Builder
	sb.WriteString("TopologyRequest:\n")
	fmt.Fprintf(&sb, "  Provider:%s\n", spacer(escapeLineBreaks(p.Provider.Name)))
	sb.WriteString(map2string(p.Provider.Creds, "  Credentials", true, "\n"))
	sb.WriteString(map2string(p.Provider.Params, "  Parameters", false, "\n"))
	fmt.Fprintf(&sb, "  Engine:%s\n", spacer(escapeLineBreaks(p.Engine.Name)))
	sb.WriteString(map2string(p.Engine.Params, "  Parameters", false, "\n"))
	sb.WriteString("  Nodes:")
	for _, nodes := range p.Nodes {
		sb.WriteByte(' ')
		sb.WriteString(map2string(nodes.Instances, nodes.Region, false, ""))
	}
	sb.WriteString("\n")
	return sb.String()
}

func GetTopologyRequest(body []byte) (*Request, error) {
	var payload Request

	if len(body) == 0 {
		return &payload, nil
	}

	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("failed to parse payload: %v", err)
	}

	return &payload, nil
}

func spacer(value string) string {
	if len(value) > 0 {
		return " " + value
	}

	return ""
}

// escapeLineBreaks keeps a client-supplied value on one line, so a request
// written to the log cannot start a forged log line.
func escapeLineBreaks(s string) string {
	s = strings.ReplaceAll(s, "\n", `\n`)
	return strings.ReplaceAll(s, "\r", `\r`)
}

func map2string[T string | any](m map[string]T, prefix string, hide bool, suffix string) string {
	var sb strings.Builder
	sb.WriteString(escapeLineBreaks(prefix))
	sb.WriteString(": [")
	if n := len(m); n != 0 {
		keys := make([]string, 0, n)
		for key := range m {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		terms := make([]string, 0, n)
		for _, key := range keys {
			if hide {
				terms = append(terms, fmt.Sprintf("%s:***", escapeLineBreaks(key)))
			} else {
				terms = append(terms, escapeLineBreaks(fmt.Sprintf("%s:%v", key, m[key])))
			}
		}
		sb.WriteString(strings.Join(terms, " "))
	}
	sb.WriteString("]")
	sb.WriteString(suffix)

	return sb.String()
}

// GetNodeNameList retrieves all the nodenames
func GetNodeNameList(cis []ComputeInstances) []string {
	nodes := []string{}
	for _, ci := range cis {
		for _, node := range ci.Instances {
			nodes = append(nodes, node)
		}
	}
	return nodes
}

// GetNodeNameMap retrieves all the nodenames
func GetNodeNameMap(cis []ComputeInstances) map[string]bool {
	nodes := make(map[string]bool)
	for _, ci := range cis {
		for _, node := range ci.Instances {
			nodes[node] = true
		}
	}
	return nodes
}

func (p *Request) Hash() (string, error) {
	dataToHash := Request{
		Provider: Provider{
			Name:   p.Provider.Name,
			Params: p.Provider.Params,
		},
		Engine: Engine{
			Name:   p.Engine.Name,
			Params: p.Engine.Params,
		},
	}
	return GetHash(dataToHash)
}

func GetHash(obj any) (string, error) {
	data, err := json.Marshal(obj)
	if err != nil {
		return "", fmt.Errorf("failed to marshal request for hashing: %v", err)
	}

	h := fnv.New64a()
	h.Write(data)
	return fmt.Sprintf("%x", h.Sum64()), nil
}
