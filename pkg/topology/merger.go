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

package topology

import (
	"slices"
	"sort"
)

// Merger finds and merges similar vertices in a graph representing N-tier hierarchy
type Merger struct {
	// array of tiers, starting from the leaf level
	tiers [][]*Vertex
	// resulting nodes after graph node merging
	nodes map[string]*Vertex
	// mapping of original node IDs to the resulting nodes after graph node merging
	refs map[string]*Vertex
}

func NewMerger(top []*Vertex) *Merger {
	if len(top) == 0 {
		return &Merger{}
	}

	m := &Merger{
		tiers: [][]*Vertex{},
		nodes: make(map[string]*Vertex),
		refs:  make(map[string]*Vertex),
	}
	// sort vertices for consistency
	sort.Slice(top, func(i, j int) bool {
		return top[i].ID < top[j].ID
	})

	m.traverse(top)
	return m
}

func (m *Merger) traverse(layer []*Vertex) {
	visited := make(map[string]bool)

	var isLeaf bool
	for _, v := range layer {
		visited[v.ID] = true
		if len(v.Vertices) == 0 {
			isLeaf = true
			m.nodes[v.ID] = v
		}
	}

	if !isLeaf {
		next := []*Vertex{}
		for _, v := range layer {
			keys := make([]string, 0, len(v.Vertices))
			for id := range v.Vertices {
				keys = append(keys, id)
			}
			sort.Strings(keys)
			for _, id := range keys {
				if !visited[id] {
					visited[id] = true
					next = append(next, v.Vertices[id])
				}
			}
		}
		m.traverse(next)
	}
	m.tiers = append(m.tiers, layer)
}

func (m *Merger) TopTier() []*Vertex {
	n := len(m.tiers)
	if n == 0 {
		return nil
	}
	return m.tiers[n-1]
}

func (m *Merger) Merge() {
	for i := 1; i < len(m.tiers); i++ {
		m.tiers[i] = m.mergeLayer(m.tiers[i])
	}
}

func (m *Merger) mergeLayer(layer []*Vertex) []*Vertex {
	type filter struct {
		signature []string
		id        string
	}
	filters := []filter{}

	result := []*Vertex{}
	for _, v := range layer {
		vertices := make(map[string]bool)
		for id := range v.Vertices {
			if ref, ok := m.refs[id]; ok {
				vertices[ref.ID] = true
			} else {
				vertices[id] = true
			}
		}

		signature := make([]string, 0, len(vertices))
		for id := range vertices {
			signature = append(signature, id)
		}
		sort.Strings(signature)

		var seen bool
		for _, f := range filters {
			if slices.Equal(f.signature, signature) {
				seen = true
				m.refs[v.ID] = m.nodes[f.id]
				break
			}
		}
		if !seen {
			filters = append(filters, filter{signature: signature, id: v.ID})
			merged := &Vertex{
				Name:     v.Name,
				ID:       v.ID,
				Vertices: make(map[string]*Vertex),
			}
			for _, id := range signature {
				merged.Vertices[id] = m.nodes[id]
			}
			m.nodes[v.ID] = merged
			result = append(result, merged)
		}
	}

	return result
}
