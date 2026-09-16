/*
 * Copyright 2025 NVIDIA CORPORATION
 * SPDX-License-Identifier: Apache-2.0
 */

package translate

import (
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/dsx-ai-factory/topograph/internal/cluset"
	"github.com/dsx-ai-factory/topograph/internal/httperr"
	"github.com/dsx-ai-factory/topograph/pkg/topology"
)

// toTreeTopology generates SLURM cluster topology config in "topology/tree" format
func (nt *NetworkTopology) toTreeTopology(wr io.Writer, skeletonOnly bool) *httperr.Error {
	visited := make(map[string]bool)
	queue := []string{""}
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		if visited[id] {
			continue
		}
		visited[id] = true
		v, ok := nt.vertices[id]
		if !ok {
			return httperr.NewError(http.StatusBadGateway, fmt.Sprintf("missing vertex with ID %q", id))
		}
		if err := writeVertex(wr, v, skeletonOnly); err != nil {
			return httperr.NewError(http.StatusInternalServerError, err.Error())
		}
		queue = append(queue, nt.tree[id]...)
	}
	return nil
}

func writeVertex(wr io.Writer, v *topology.Vertex, skeletonOnly bool) error {
	if len(v.ID) == 0 {
		return nil
	}

	switches := make([]string, 0, len(v.Vertices))
	nodes := make([]string, 0, len(v.Vertices))
	for _, w := range v.Vertices {
		if len(w.Vertices) == 0 {
			nodes = append(nodes, w.Name)
		} else {
			if len(w.Name) != 0 {
				switches = append(switches, w.Name)
			} else {
				switches = append(switches, w.ID)
			}
		}
	}

	var comment, name string
	if len(v.Name) == 0 {
		name = v.ID
	} else {
		comment = fmt.Sprintf("# %s=%s\n", v.Name, v.ID)
		name = v.Name
	}

	if len(switches) != 0 {
		_, err := fmt.Fprintf(wr, "%sSwitchName=%s Switches=%s\n", comment, name, strings.Join(cluset.Compact(switches), ","))
		if err != nil {
			return err
		}
	}

	if !skeletonOnly && len(nodes) != 0 {
		_, err := fmt.Fprintf(wr, "%sSwitchName=%s Nodes=%s\n", comment, name, strings.Join(cluset.Compact(nodes), ","))
		if err != nil {
			return err
		}
	}
	return nil
}
