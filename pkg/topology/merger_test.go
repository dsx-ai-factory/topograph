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
	"testing"

	"github.com/stretchr/testify/require"
)

func createVertex(name string, children ...*Vertex) *Vertex {
	v := &Vertex{
		Name: name,
		ID:   name,
	}

	if len(children) != 0 {
		v.Vertices = make(map[string]*Vertex)
		for _, w := range children {
			v.Vertices[w.ID] = w
		}
	}
	return v
}

func getTestGraph() []*Vertex {
	a := createVertex("A")
	b := createVertex("B")
	c := createVertex("C")
	d := createVertex("D")

	l1 := createVertex("L1", a, b)
	l2 := createVertex("L2", a, b)
	l3 := createVertex("L3", a, b)
	l4 := createVertex("L4", a, b)
	l5 := createVertex("L5", c, d)
	l6 := createVertex("L6", c, d)
	l7 := createVertex("L7", c, d)
	l8 := createVertex("L8", c, d)

	s1 := createVertex("S1", l1, l5)
	s2 := createVertex("S2", l1, l5)
	s3 := createVertex("S3", l2, l6)
	s4 := createVertex("S4", l2, l6)
	s5 := createVertex("S5", l3, l7)
	s6 := createVertex("S6", l3, l7)
	s7 := createVertex("S7", l4, l8)
	s8 := createVertex("S8", l4, l8)

	c1 := createVertex("C1", s1, s3, s5, s7)
	c2 := createVertex("C2", s1, s3, s5, s7)
	c3 := createVertex("C3", s2, s4, s6, s8)
	c4 := createVertex("C4", s2, s4, s6, s8)

	return []*Vertex{c1, c2, c3, c4}
}

func TestMerger(t *testing.T) {
	// test valid merge
	g := getTestGraph()

	m := NewMerger(g)
	m.Merge()
	top := m.TopTier()

	a := &Vertex{ID: "A", Name: "A"}
	b := &Vertex{ID: "B", Name: "B"}
	c := &Vertex{ID: "C", Name: "C"}
	d := &Vertex{ID: "D", Name: "D"}
	l1 := &Vertex{ID: "L1", Name: "L1", Vertices: map[string]*Vertex{"A": a, "B": b}}
	l5 := &Vertex{ID: "L5", Name: "L5", Vertices: map[string]*Vertex{"C": c, "D": d}}
	s1 := &Vertex{ID: "S1", Name: "S1", Vertices: map[string]*Vertex{"L1": l1, "L5": l5}}
	c1 := &Vertex{ID: "C1", Name: "C1", Vertices: map[string]*Vertex{"S1": s1}}

	require.Equal(t, []*Vertex{c1}, top)

	// test empty merge
	m = NewMerger(nil)
	top = m.TopTier()
	require.Nil(t, top)
}
