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

package translate

import "github.com/dsx-ai-factory/topograph/pkg/topology"

func GetTreeTestSet(testForLongLabelName bool) (*topology.Graph, map[string]string) {
	//
	//        S1
	//      /    \
	//    S2      S3
	//    |       |
	//   ---     ---
	//   I14     I21
	//   I15     I22
	//   I16     I25
	//   ---     ---
	//
	var s3name string
	if testForLongLabelName {
		s3name = "S3very-very-long-id-to-check-label-value-limits-of-63-characters"
	} else {
		s3name = "S3"
	}

	instance2node := map[string]string{
		"I21": "Node201", "I22": "Node202", "I25": "Node205",
		"I34": "Node304", "I35": "Node305", "I36": "Node306",
	}

	n21 := &topology.Vertex{ID: "I21", Name: "Node201"}
	n22 := &topology.Vertex{ID: "I22", Name: "Node202"}
	n25 := &topology.Vertex{ID: "I25", Name: "Node205"}

	n34 := &topology.Vertex{ID: "I34", Name: "Node304"}
	n35 := &topology.Vertex{ID: "I35", Name: "Node305"}
	n36 := &topology.Vertex{ID: "I36", Name: "Node306"}

	sw2 := &topology.Vertex{
		ID:       "S2",
		Vertices: map[string]*topology.Vertex{"I21": n21, "I22": n22, "I25": n25},
	}
	sw3 := &topology.Vertex{
		ID:       s3name,
		Vertices: map[string]*topology.Vertex{"I34": n34, "I35": n35, "I36": n36},
	}
	sw1 := &topology.Vertex{
		ID:       "S1",
		Vertices: map[string]*topology.Vertex{"S2": sw2, s3name: sw3},
	}
	treeRoot := &topology.Vertex{
		Vertices: map[string]*topology.Vertex{"S1": sw1},
	}

	root := &topology.Graph{Tiers: treeRoot}
	return root, instance2node
}

func GetBlockWithMultiIBTestSet() (*topology.Graph, map[string]string) {
	//
	//       IB2             IB1
	//        |               |
	//        S1              S4
	//      /    \          /    \
	//    S2      S3      S5      S6
	//    |       |       |       |
	//   ---     ---     ---     ---
	//   I14\    I21\    I31\    I41\
	//   I15-B1  I22-B2  I32-B3  I42-B4
	//   I16/    I25/    I33/    I43/
	//   ---     ---     ---     ---
	//
	instance2node := map[string]string{
		"I14": "Node104", "I15": "Node105", "I16": "Node106",
		"I21": "Node201", "I22": "Node202", "I25": "Node205",
		"I31": "Node301", "I32": "Node302", "I33": "Node303",
		"I41": "Node401", "I42": "Node402", "I43": "Node403",
	}

	n14 := &topology.Vertex{ID: "I14", Name: "Node104"}
	n15 := &topology.Vertex{ID: "I15", Name: "Node105"}
	n16 := &topology.Vertex{ID: "I16", Name: "Node106"}

	n21 := &topology.Vertex{ID: "I21", Name: "Node201"}
	n22 := &topology.Vertex{ID: "I22", Name: "Node202"}
	n25 := &topology.Vertex{ID: "I25", Name: "Node205"}

	n31 := &topology.Vertex{ID: "I31", Name: "Node301"}
	n32 := &topology.Vertex{ID: "I32", Name: "Node302"}
	n33 := &topology.Vertex{ID: "I33", Name: "Node303"}

	n41 := &topology.Vertex{ID: "I41", Name: "Node401"}
	n42 := &topology.Vertex{ID: "I42", Name: "Node402"}
	n43 := &topology.Vertex{ID: "I43", Name: "Node403"}

	sw5 := &topology.Vertex{
		ID:       "S5",
		Vertices: map[string]*topology.Vertex{"I31": n31, "I32": n32, "I33": n33},
	}
	sw6 := &topology.Vertex{
		ID:       "S6",
		Vertices: map[string]*topology.Vertex{"I41": n41, "I42": n42, "I43": n43},
	}
	sw4 := &topology.Vertex{
		ID:       "S4",
		Vertices: map[string]*topology.Vertex{"S5": sw5, "S6": sw6},
	}
	ibRoot1 := &topology.Vertex{
		ID:       "IB1",
		Vertices: map[string]*topology.Vertex{"S4": sw4},
	}

	sw2 := &topology.Vertex{
		ID:       "S2",
		Vertices: map[string]*topology.Vertex{"I14": n14, "I15": n15, "I16": n16},
	}
	sw3 := &topology.Vertex{
		ID:       "S3",
		Vertices: map[string]*topology.Vertex{"I21": n21, "I22": n22, "I25": n25},
	}
	sw1 := &topology.Vertex{
		ID:       "S1",
		Vertices: map[string]*topology.Vertex{"S2": sw2, "S3": sw3},
	}
	ibRoot2 := &topology.Vertex{
		ID:       "IB2",
		Vertices: map[string]*topology.Vertex{"S1": sw1},
	}

	treeRoot := &topology.Vertex{
		Vertices: map[string]*topology.Vertex{"IB1": ibRoot1, "IB2": ibRoot2},
	}

	domains := topology.NewDomainMap()
	for _, host := range []struct {
		domain   string
		instance string
		name     string
	}{
		{"B1", "I14", "Node104"},
		{"B1", "I15", "Node105"},
		{"B1", "I16", "Node106"},
		{"B2", "I21", "Node201"},
		{"B2", "I22", "Node202"},
		{"B2", "I25", "Node205"},
		{"B3", "I31", "Node301"},
		{"B3", "I32", "Node302"},
		{"B3", "I33", "Node303"},
		{"B4", "I41", "Node401"},
		{"B4", "I42", "Node402"},
		{"B4", "I43", "Node403"},
	} {
		domains.AddHost(host.domain, host.instance, host.name)
	}

	root := &topology.Graph{
		Tiers:   treeRoot,
		Domains: domains,
	}
	return root, instance2node
}
