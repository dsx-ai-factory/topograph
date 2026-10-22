/*
 * Copyright 2026 NVIDIA CORPORATION
 * SPDX-License-Identifier: Apache-2.0
 */

package minerva

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"

	"k8s.io/klog/v2"

	"github.com/dsx-ai-factory/topograph/internal/httperr"
	"github.com/dsx-ai-factory/topograph/internal/httpreq"
	"github.com/dsx-ai-factory/topograph/pkg/metrics"
	"github.com/dsx-ai-factory/topograph/pkg/topology"
)

const (
	ExportTopologyURL = "v1/export-topology"

	RoleServer     = "server"
	RoleLeaf       = "leaf"
	RoleSpine      = "spine"
	RoleSuperSpine = "super_spine"

	headerXApiKey     = "X-Api-Key"
	headerContentType = "Content-Type"
	contentTypeJSON   = "application/json"
)

// roleTier gives the authoritative hierarchy tier for Minerva's standard
// device roles, per Minerva's documented standard interface-role pairs
// (Superspine-Spine, Spine-Leaf, Leaf-NIC/server).
// Minerva also supports deployment-specific "Custom" roles; devices
// with any role outside this map are excluded from the switch tree (see
// computeTiers).
var roleTier = map[string]int{
	RoleServer:     -1,
	RoleLeaf:       0,
	RoleSpine:      1,
	RoleSuperSpine: 2,
}

// ExportTopologyRequest is the POST body for /v1/export-topology.
// Sending an empty object exports the entire fabric.
type ExportTopologyRequest struct {
	Limit int `json:"limit,omitempty"`
}

type ExportTopologyResponse struct {
	Success  bool     `json:"success"`
	Topology Topology `json:"topology"`
	Message  string   `json:"message"`
	Error    string   `json:"error"`
}

type Topology struct {
	Layer2 map[string]Device `json:"layer2"`
}

type Device struct {
	DeviceName     string                   `json:"deviceName"`
	HostName       string                   `json:"host_name"`
	Role           string                   `json:"role"`
	DeviceID       string                   `json:"device_id"`
	LocalInterface map[string]InterfaceLink `json:"localInterface"`
	GPUs           []GPU                    `json:"GPUs"`
}

type InterfaceLink struct {
	RemoteInterface string `json:"remoteInterface"`
	RemoteDevice    string `json:"remoteDevice"`
}

type GPU struct {
	Name        string `json:"name"`
	ComponentID int    `json:"componentId"`
}

func (p *Provider) getNetworkTree(ctx context.Context, pageSize *int, cis []topology.ComputeInstances) (*topology.Vertex, *httperr.Error) {
	req := ExportTopologyRequest{}
	if pageSize != nil {
		req.Limit = *pageSize
	}
	payload, err := json.Marshal(req)
	if err != nil {
		return nil, httperr.NewError(http.StatusInternalServerError, fmt.Sprintf("failed to marshal request: %v", err))
	}

	headers := map[string]string{
		headerContentType: contentTypeJSON,
		headerXApiKey:     p.creds.ApiKey,
	}
	f := httpreq.GetRequestFunc(ctx, http.MethodPost, headers, nil, payload, p.params.ApiURL, ExportTopologyURL)
	// The server certificate is verified and redirects are refused: the API key
	// is sent in a header and the response decides the cluster topology.
	_, data, httpErr := httpreq.DoRequestWithRedirects(f, false, false)
	if httpErr != nil {
		return nil, httpErr
	}

	var resp ExportTopologyResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, httperr.NewError(http.StatusBadGateway, fmt.Sprintf("minerva output read failed: %v", err))
	}
	if !resp.Success {
		reason := resp.Error
		if len(reason) == 0 {
			reason = resp.Message
		}
		return nil, httperr.NewError(http.StatusBadGateway, fmt.Sprintf("minerva export-topology failed: %s", reason))
	}

	treeRoot := &topology.Vertex{Vertices: make(map[string]*topology.Vertex)}
	if httpErr := parseResponse(treeRoot, &resp, topology.GetNodeNameMap(cis)); httpErr != nil {
		return nil, httpErr
	}

	if len(treeRoot.Vertices) == 0 {
		return nil, httperr.NewError(http.StatusBadGateway, "no topology available from Minerva")
	}

	return treeRoot, nil
}

// parseResponse parses the Minerva export-topology output into a switch tree,
// growing it outward from the requested nodes rather than materializing the
// whole fabric. Minerva reports a flat Layer-2 device/link list with no tier
// numbers, so tiers are derived from each device's role (see roleTier);
// devices with an unrecognized role take no part in the tree.
func parseResponse(treeRoot *topology.Vertex, resp *ExportTopologyResponse, inputNodes map[string]bool) *httperr.Error {
	devices := resp.Topology.Layer2

	nameToID, httpErr := deviceIDIndex(devices)
	if httpErr != nil {
		return httpErr
	}
	tiers := computeTiers(devices, nameToID)
	parents := parentLinks(devices, nameToID, tiers)

	switches := make(map[string]*topology.Vertex)   // every switch, keyed by ID, regardless of whether it's reachable from a requested node
	frontier := make(map[string]*topology.Vertex)   // requested servers: the starting point for growing the tree upward
	unresolved := make(map[string]*topology.Vertex) // requested nodes not yet attached to a switch, keyed by requested name
	requested := make(map[string]*topology.Vertex)  // every requested server Minerva reports, to confirm it survives merging
	for name, dev := range devices {
		id := nameToID[name]
		v := &topology.Vertex{ID: id, Name: name}
		if dev.Role != RoleServer {
			v.Vertices = make(map[string]*topology.Vertex)
			switches[id] = v
			continue
		}
		if inputNodes[name] {
			frontier[id] = v
			unresolved[name] = v
			requested[id] = v
		}
	}

	// A requested node Minerva doesn't report as a server has no switch
	// parent either; keep it under no-topology instead of dropping it. Requested
	// names and device IDs are unrelated namespaces, so its vertex gets an ID
	// that no reported device uses.
	reportedIDs := make(map[string]bool, len(nameToID))
	for _, id := range nameToID {
		reportedIDs[id] = true
	}
	for name := range inputNodes {
		if dev, ok := devices[name]; ok && dev.Role == RoleServer {
			continue
		}
		klog.Warningf("Requested node %q is not a server in the Minerva topology", name)
		id := "missing/" + name
		for reportedIDs[id] {
			id = "_" + id
		}
		unresolved[name] = &topology.Vertex{ID: id, Name: name}
	}

	// Grow the tree outward from the requested servers, one wave of parents
	// at a time. Tiers aren't necessarily consecutive across a single link
	// (e.g. a server can connect directly to a spine, skipping the leaf
	// tier), so a switch can gain children across more than one wave; hence
	// switches is looked up, never drained, and each switch is scheduled to
	// search its own parents only once (visited), since parents is
	// precomputed and doesn't change between waves.
	visited := make(map[string]bool)
	hasParent := make(map[string]bool)
	for len(frontier) != 0 {
		next := make(map[string]*topology.Vertex)
		for id, v := range frontier {
			for parentID := range parents[id] {
				parent, ok := switches[parentID]
				if !ok {
					continue
				}
				parent.Vertices[id] = v
				if _, ok := requested[id]; ok {
					delete(unresolved, v.Name)
				}
				if _, isSwitch := switches[id]; isSwitch {
					hasParent[id] = true
				}
				if !visited[parentID] {
					visited[parentID] = true
					next[parentID] = parent
				}
			}
		}
		frontier = next
	}

	// The top tier is every switch reached from a requested server that
	// never became a child of another switch.
	var top []*topology.Vertex
	for id, v := range switches {
		if visited[id] && !hasParent[id] {
			top = append(top, v)
		}
	}

	// Clos fabrics may require merging switches into a tree format. Known
	// limitation: if a switch's children sit at different depths (e.g. a
	// spine with both a directly-attached server and a leaf branch beneath
	// it), topology.Merger.traverse's per-layer (rather than per-vertex) leaf
	// detection can drop the deeper branch's children from the merged tree.
	merger := topology.NewMerger(top)
	merger.Merge()
	tree := singleParent(merger.TopTier())
	for _, v := range tree {
		treeRoot.Vertices[v.ID] = v
	}

	// topology.Merger can drop a deeper branch under a switch whose children
	// sit at different depths. A requested server that didn't make it into the
	// tree is grouped under no-topology below instead of vanishing.
	placed := make(map[string]bool)
	var collect func(v *topology.Vertex)
	collect = func(v *topology.Vertex) {
		placed[v.ID] = true
		for _, c := range v.Vertices {
			if c != nil {
				collect(c)
			}
		}
	}
	for _, v := range tree {
		collect(v)
	}
	for id, v := range requested {
		if !placed[id] {
			klog.Warningf("Requested node %q was dropped while merging the Minerva switch tree", v.Name)
			unresolved[v.Name] = v
		}
	}

	// Requested servers with no resolvable switch parent are grouped under
	// the shared no-topology switch, matching the convention every other
	// provider uses via topology.ClusterTopology.ToGraph, rather than being
	// silently dropped or surfacing as a top-level switch themselves.
	if len(unresolved) != 0 {
		noTopology := &topology.Vertex{ID: topology.NoTopology, Vertices: make(map[string]*topology.Vertex)}
		for _, v := range unresolved {
			noTopology.Vertices[v.ID] = v
			metrics.SetMissingTopology(NAME, v.Name)
		}
		treeRoot.Vertices[topology.NoTopology] = noTopology
	}

	return nil
}

// singleParent makes the merged forest a tree. Merging collapses switches
// with identical child sets, but in a partially connected fabric (e.g. two
// spines sharing one leaf) a vertex can still sit under several parents, which
// the engines reject. Each vertex is kept under the first parent reached in ID
// order and removed from the others; switches left without children are then
// pruned. Servers are never removed, since every one stays under the parent
// that kept it.
func singleParent(roots []*topology.Vertex) []*topology.Vertex {
	seen := make(map[string]bool)

	var visit func(v *topology.Vertex)
	visit = func(v *topology.Vertex) {
		seen[v.ID] = true
		for _, id := range slices.Sorted(maps.Keys(v.Vertices)) {
			child := v.Vertices[id]
			if child == nil || seen[child.ID] {
				delete(v.Vertices, id)
				continue
			}
			visit(child)
			if child.Vertices != nil && len(child.Vertices) == 0 {
				delete(v.Vertices, id)
			}
		}
	}

	sorted := slices.Clone(roots)
	slices.SortFunc(sorted, func(a, b *topology.Vertex) int { return strings.Compare(a.ID, b.ID) })

	var tree []*topology.Vertex
	for _, root := range sorted {
		if seen[root.ID] {
			continue
		}
		visit(root)
		if len(root.Vertices) != 0 {
			tree = append(tree, root)
		}
	}

	return tree
}

// deviceIDIndex maps each device name to its ID. Vertex.Vertices (and
// topology.Merger) key children by ID, but Minerva links reference devices by
// name, so every name-keyed lookup elsewhere in this package is translated
// through this map. It returns an error if two distinct devices resolve to
// the same ID, since every downstream ID-keyed map built from it (switches,
// parents, tiers) would otherwise let one silently overwrite the other.
func deviceIDIndex(devices map[string]Device) (map[string]string, *httperr.Error) {
	nameToID := make(map[string]string, len(devices))
	idToName := make(map[string]string, len(devices))
	for name, dev := range devices {
		id := deviceID(dev)
		if other, ok := idToName[id]; ok {
			return nil, httperr.NewError(http.StatusBadGateway,
				fmt.Sprintf("minerva export-topology: duplicate device id %q for devices %q and %q", id, other, name))
		}
		idToName[id] = name
		nameToID[name] = id
	}
	return nameToID, nil
}

// computeTiers assigns each device a tier from roleTier, keyed by device ID.
// Devices whose role isn't in roleTier (including Minerva's
// deployment-specific "Custom" roles) are left unassigned and take no part in
// the tree.
func computeTiers(devices map[string]Device, nameToID map[string]string) map[string]int {
	tier := make(map[string]int, len(devices))
	for name, dev := range devices {
		t, ok := roleTier[dev.Role]
		if !ok {
			klog.V(2).InfoS("Minerva device has unrecognized role; excluded from switch tree", "role", dev.Role, "name", name)
			continue
		}
		tier[nameToID[name]] = t
	}

	return tier
}

// parentLinks maps each device ID to the set of device IDs it has a direct
// link to at a higher tier. Minerva links are declared per device and aren't
// guaranteed to be declared on both ends, so every link is classified by
// comparing tiers rather than assuming a particular device declared it.
// Devices with no assigned tier (see computeTiers) never appear as a parent
// or a child. A server linked to several leaves keeps only one parent.
func parentLinks(devices map[string]Device, nameToID map[string]string, tiers map[string]int) map[string]map[string]bool {
	parents := make(map[string]map[string]bool)
	addParent := func(child, parent string) {
		if parents[child] == nil {
			parents[child] = make(map[string]bool)
		}
		parents[child][parent] = true
	}

	for name, dev := range devices {
		id := nameToID[name]
		tier, ok := tiers[id]
		if !ok {
			continue
		}
		for _, link := range dev.LocalInterface {
			remoteID, ok := nameToID[link.RemoteDevice]
			if !ok {
				continue
			}
			remoteTier, ok := tiers[remoteID]
			if !ok {
				continue
			}
			switch {
			case tier < remoteTier:
				addParent(id, remoteID)
			case remoteTier < tier:
				addParent(remoteID, id)
			}
		}
	}

	// The output must be a tree, so a server can have only one parent. Multi-rail
	// and multi-plane servers are cabled to a leaf in each plane; keep the
	// parent at the lowest tier, so a server never skips past its leaf, and
	// among those the one whose device name sorts first so the choice is
	// deterministic.
	idToName := make(map[string]string, len(nameToID))
	for name, id := range nameToID {
		idToName[id] = name
	}
	for child, set := range parents {
		if tiers[child] != roleTier[RoleServer] || len(set) < 2 {
			continue
		}
		var keep string
		for parent := range set {
			if keep == "" || tiers[parent] < tiers[keep] ||
				(tiers[parent] == tiers[keep] && idToName[parent] < idToName[keep]) {
				keep = parent
			}
		}
		parents[child] = map[string]bool{keep: true}
	}

	return parents
}

func deviceID(dev Device) string {
	if len(dev.DeviceID) != 0 {
		return dev.DeviceID
	}
	return dev.DeviceName
}
