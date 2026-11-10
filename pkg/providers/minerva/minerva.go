/*
 * Copyright 2026 NVIDIA CORPORATION
 * SPDX-License-Identifier: Apache-2.0
 */

package minerva

import (
	"bytes"
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
	ExportTopologyURL = "v2/export-topology"

	RoleServer     = "server"
	RoleLeaf       = "leaf"
	RoleSpine      = "spine"
	RoleSuperSpine = "super_spine"

	// Topology layers of the export. The east-west layer carries the
	// Ethernet hierarchy and Layer-2 links; the nvlink layer carries the rack
	// hierarchy and NVLink neighbors.
	LayerEastWest = "east-west"
	LayerNVLink   = "nvlink"

	// Record and neighbor types of the export.
	recordTypeDevice   = "device"
	recordTypeRack     = "rack"
	recordTypeNeighbor = "neighbor"
	neighborLayer2     = "layer2"

	// maxDeviceLimit is the largest page Minerva accepts (devices per page).
	maxDeviceLimit = 500
	// maxPages bounds a traversal, so a server that never completes cannot
	// keep the request looping.
	maxPages = 10000

	headerXApiKey     = "X-Api-Key"
	headerContentType = "Content-Type"
	headerAccept      = "Accept"
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

// ExportTopologyRequest is the POST body for the export-topology API.
type ExportTopologyRequest struct {
	Layers      []string        `json:"layers"`
	DeviceLimit int             `json:"deviceLimit,omitempty"`
	Cursor      json.RawMessage `json:"cursor,omitempty"` // the previous page's nextCursor, unchanged
}

// ExportTopologyResponse is one page of the export. Exactly one of EastWest,
// NVLink or Topology is present, depending on the requested layers; this
// provider requests one layer at a time.
type ExportTopologyResponse struct {
	NextCursor json.RawMessage `json:"nextCursor"`
	Complete   bool            `json:"complete"`
	EastWest   *Records        `json:"eastWest"`
	NVLink     *Records        `json:"nvlink"`
}

type Records struct {
	Records []Record `json:"records"`
}

// Record is one hierarchy group, device, component or neighbor record. Only
// the fields this provider reads are decoded; others are ignored.
type Record struct {
	Type         string   `json:"type"`
	Name         string   `json:"name"`
	Role         string   `json:"role"`
	Parent       string   `json:"parent"`
	Rack         string   `json:"rack"`
	NeighborType string   `json:"neighborType"`
	From         Endpoint `json:"from"`
	To           Endpoint `json:"to"`
}

type Endpoint struct {
	Device string `json:"device"`
}

// Device is a device of the east-west layer and the devices it is linked to.
type Device struct {
	Name  string
	Role  string
	Peers map[string]bool // names of directly linked devices
}

// records returns the records of layer, and false when the response does not
// carry that layer's collection at all, which is not the same as an empty one.
func (r *ExportTopologyResponse) records(layer string) ([]Record, bool) {
	var rec *Records
	switch layer {
	case LayerEastWest:
		rec = r.EastWest
	case LayerNVLink:
		rec = r.NVLink
	}
	if rec == nil {
		return nil, false
	}
	return rec.Records, true
}

// deviceLimit converts the request page size to Minerva's deviceLimit: the
// largest page unless a smaller one is requested.
func deviceLimit(pageSize *int) int {
	if pageSize == nil || *pageSize <= 0 || *pageSize > maxDeviceLimit {
		return maxDeviceLimit
	}
	return *pageSize
}

// exportLayer reads every page of one topology layer and returns its records.
// Pages are requested one at a time by following nextCursor, which is passed
// back to Minerva unchanged.
func (p *Provider) exportLayer(ctx context.Context, layer string, limit int) ([]Record, *httperr.Error) {
	headers := map[string]string{
		headerContentType: contentTypeJSON,
		headerAccept:      contentTypeJSON,
		headerXApiKey:     p.creds.ApiKey,
	}

	var records []Record
	var cursor json.RawMessage
	seen := make(map[string]bool)
	for page := 0; page < maxPages; page++ {
		payload, err := json.Marshal(ExportTopologyRequest{Layers: []string{layer}, DeviceLimit: limit, Cursor: cursor})
		if err != nil {
			return nil, httperr.NewError(http.StatusInternalServerError, fmt.Sprintf("failed to marshal request: %v", err))
		}

		f := httpreq.GetRequestFunc(ctx, http.MethodPost, headers, nil, payload, p.params.ApiURL, ExportTopologyURL)
		// The server certificate is verified and redirects are refused: the API key
		// is sent in a header and the response decides the cluster topology.
		_, data, httpErr := httpreq.DoRequestWithRedirects(f, false, false)
		if httpErr != nil {
			return nil, upstreamError(layer, httpErr)
		}

		var resp ExportTopologyResponse
		if err := json.Unmarshal(data, &resp); err != nil {
			return nil, httperr.NewError(http.StatusBadGateway, fmt.Sprintf("minerva output read failed: %v", err))
		}
		pageRecords, ok := resp.records(layer)
		if !ok {
			return nil, httperr.NewError(http.StatusBadGateway,
				fmt.Sprintf("minerva export-topology (%s): response has no %s records", layer, layer))
		}
		records = append(records, pageRecords...)
		if resp.Complete {
			return records, nil
		}

		next := bytes.TrimSpace(resp.NextCursor)
		if len(next) == 0 || bytes.Equal(next, []byte("null")) {
			return nil, httperr.NewError(http.StatusBadGateway,
				fmt.Sprintf("minerva export-topology (%s): incomplete page without a nextCursor", layer))
		}
		if seen[string(next)] {
			return nil, httperr.NewError(http.StatusBadGateway,
				fmt.Sprintf("minerva export-topology (%s): nextCursor repeated, pagination is not advancing", layer))
		}
		seen[string(next)] = true
		cursor = slices.Clone(next)
	}

	return nil, httperr.NewError(http.StatusBadGateway,
		fmt.Sprintf("minerva export-topology (%s): more than %d pages", layer, maxPages))
}

// upstreamError reports a failed Minerva request as a 502: the caller's own
// request was fine, so Minerva's status (for example a 403 for a bad API key)
// must not be mistaken for the caller's. Minerva's status stays in the message.
func upstreamError(layer string, httpErr *httperr.Error) *httperr.Error {
	if httpErr.Code() == http.StatusBadGateway {
		return httperr.NewError(http.StatusBadGateway, fmt.Sprintf("minerva export-topology (%s): %s", layer, httpErr.Error()))
	}
	return httperr.NewError(http.StatusBadGateway,
		fmt.Sprintf("minerva export-topology (%s): status %d: %s", layer, httpErr.Code(), httpErr.Error()))
}

// getNetworkTree exports the east-west layer and builds the switch tree for
// the requested nodes.
func (p *Provider) getNetworkTree(ctx context.Context, pageSize *int, cis []topology.ComputeInstances) (*topology.Vertex, *httperr.Error) {
	records, httpErr := p.exportLayer(ctx, LayerEastWest, deviceLimit(pageSize))
	if httpErr != nil {
		return nil, httpErr
	}

	treeRoot := &topology.Vertex{Vertices: make(map[string]*topology.Vertex)}
	if httpErr := parseResponse(treeRoot, buildDevices(records), topology.GetNodeNameMap(cis)); httpErr != nil {
		return nil, httpErr
	}

	if len(treeRoot.Vertices) == 0 {
		return nil, httperr.NewError(http.StatusBadGateway, "no topology available from Minerva")
	}

	return treeRoot, nil
}

// getDomains exports the nvlink layer and maps each requested node to its
// NVLink domain. It returns nil when no requested node has one.
func (p *Provider) getDomains(ctx context.Context, pageSize *int, cis []topology.ComputeInstances) (topology.DomainMap, *httperr.Error) {
	records, httpErr := p.exportLayer(ctx, LayerNVLink, deviceLimit(pageSize))
	if httpErr != nil {
		return nil, httpErr
	}

	domains := buildDomains(records, nodeInstances(cis))
	if len(domains) == 0 {
		return nil, nil
	}
	return domains, nil
}

// nodeInstances maps each requested node name to its instance ID.
func nodeInstances(cis []topology.ComputeInstances) map[string]string {
	nodes := make(map[string]string)
	for _, ci := range cis {
		for instanceID, node := range ci.Instances {
			nodes[node] = instanceID
		}
	}
	return nodes
}

// buildDevices collects the devices of the east-west layer and the Layer-2
// links between them. Links are physical and unordered, so each one is
// recorded on both ends no matter which end Minerva reported it from; BGP
// sessions are not part of the physical switch tree and are ignored. A
// device that only appears as a link end has no role and takes no part in the
// tree.
func buildDevices(records []Record) map[string]*Device {
	devices := make(map[string]*Device)
	get := func(name string) *Device {
		d, ok := devices[name]
		if !ok {
			d = &Device{Name: name, Peers: make(map[string]bool)}
			devices[name] = d
		}
		return d
	}

	for _, r := range records {
		switch r.Type {
		case recordTypeDevice:
			// A device can be reported more than once; a record without a role
			// must not erase the role an earlier one gave it.
			if len(r.Name) != 0 {
				if d := get(r.Name); len(r.Role) != 0 {
					d.Role = r.Role
				}
			}
		case recordTypeNeighbor:
			a, b := r.From.Device, r.To.Device
			if r.NeighborType != neighborLayer2 || len(a) == 0 || len(b) == 0 || a == b {
				continue
			}
			get(a).Peers[b] = true
			get(b).Peers[a] = true
		}
	}

	return devices
}

// buildDomains maps each requested node to the rack that holds it in the
// nvlink layer: a rack is an NVLink domain. A device record names its rack
// directly, or through its primary parent when that parent is a rack group.
// Racks are collected first, so the result does not depend on whether a group
// record arrives before or after the devices it contains.
func buildDomains(records []Record, instances map[string]string) topology.DomainMap {
	racks := make(map[string]bool)
	for _, r := range records {
		if r.Type == recordTypeRack {
			racks[r.Name] = true
		}
	}

	domains := topology.NewDomainMap()
	for _, r := range records {
		instanceID, ok := instances[r.Name]
		if r.Type != recordTypeDevice || !ok {
			continue
		}
		rack := r.Rack
		if len(rack) == 0 && racks[r.Parent] {
			rack = r.Parent
		}
		if len(rack) != 0 {
			domains.AddHost(rack, instanceID, r.Name)
		}
	}

	return domains
}

// parseResponse builds a switch tree from the east-west devices, growing it
// outward from the requested nodes rather than materializing the whole fabric.
// Minerva reports a flat device/link list with no tier numbers, so tiers are
// derived from each device's role (see roleTier); devices with an
// unrecognized role take no part in the tree. A device's name is its vertex ID.
func parseResponse(treeRoot *topology.Vertex, devices map[string]*Device, inputNodes map[string]bool) *httperr.Error {
	tiers := computeTiers(devices)
	parents := parentLinks(devices, tiers)

	switches := make(map[string]*topology.Vertex)  // every switch, regardless of whether it's reachable from a requested node
	requested := make(map[string]*topology.Vertex) // requested servers: the starting point for growing the tree upward
	for name, dev := range devices {
		v := &topology.Vertex{ID: name, Name: name}
		switch {
		case dev.Role != RoleServer:
			v.Vertices = make(map[string]*topology.Vertex)
			switches[name] = v
		case inputNodes[name]:
			requested[name] = v
		}
	}
	frontier := maps.Clone(requested)

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

	// A requested node is grouped under the shared no-topology switch, as every
	// other provider does via topology.ClusterTopology.ToGraph, rather than
	// being dropped, when Minerva doesn't report it as a server, when it has no
	// switch parent, or when topology.Merger dropped it from the tree.
	placed := make(map[string]bool)
	var collect func(v *topology.Vertex)
	collect = func(v *topology.Vertex) {
		placed[v.ID] = true
		for _, c := range v.Vertices {
			collect(c)
		}
	}
	for _, v := range tree {
		collect(v)
	}

	noTopology := &topology.Vertex{ID: topology.NoTopology, Vertices: make(map[string]*topology.Vertex)}
	for name := range inputNodes {
		v, ok := requested[name]
		if ok && placed[name] {
			continue
		}
		if !ok {
			klog.Warningf("Requested node %q is not a server in the Minerva topology", name)
			v = &topology.Vertex{ID: name, Name: name}
		}
		noTopology.Vertices[name] = v
		metrics.SetMissingTopology(NAME, name)
	}
	if len(noTopology.Vertices) != 0 {
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

// computeTiers assigns each device a tier from roleTier, keyed by device name.
// Devices whose role isn't in roleTier (including Minerva's
// deployment-specific "Custom" roles) are left unassigned and take no part in
// the tree.
func computeTiers(devices map[string]*Device) map[string]int {
	tier := make(map[string]int, len(devices))
	for name, dev := range devices {
		t, ok := roleTier[dev.Role]
		if !ok {
			klog.V(2).InfoS("Minerva device has unrecognized role; excluded from switch tree", "role", dev.Role, "name", name)
			continue
		}
		tier[name] = t
	}

	return tier
}

// parentLinks maps each device name to the set of device names it has a direct
// link to at a higher tier. Links are unordered, so every link is classified by
// comparing tiers rather than by which end Minerva reported it from.
// Devices with no assigned tier (see computeTiers) never appear as a parent
// or a child. A server linked to several leaves keeps only one parent.
func parentLinks(devices map[string]*Device, tiers map[string]int) map[string]map[string]bool {
	parents := make(map[string]map[string]bool)
	addParent := func(child, parent string) {
		if parents[child] == nil {
			parents[child] = make(map[string]bool)
		}
		parents[child][parent] = true
	}

	for name, dev := range devices {
		tier, ok := tiers[name]
		if !ok {
			continue
		}
		for peer := range dev.Peers {
			peerTier, ok := tiers[peer]
			if !ok {
				continue
			}
			switch {
			case tier < peerTier:
				addParent(name, peer)
			case peerTier < tier:
				addParent(peer, name)
			}
		}
	}

	// The output must be a tree, so a server can have only one parent. Multi-rail
	// and multi-plane servers are cabled to a leaf in each plane; keep the
	// parent at the lowest tier, so a server never skips past its leaf, and
	// among those the one whose device name sorts first so the choice is
	// deterministic.
	for child, set := range parents {
		if tiers[child] != roleTier[RoleServer] || len(set) < 2 {
			continue
		}
		var keep string
		for parent := range set {
			if keep == "" || tiers[parent] < tiers[keep] ||
				(tiers[parent] == tiers[keep] && parent < keep) {
				keep = parent
			}
		}
		parents[child] = map[string]bool{keep: true}
	}

	return parents
}
