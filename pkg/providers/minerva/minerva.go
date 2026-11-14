// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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

package minerva

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"sort"
	"strings"
	"time"

	"k8s.io/klog/v2"

	"github.com/dsx-ai-factory/topograph/internal/httperr"
	"github.com/dsx-ai-factory/topograph/internal/httpreq"
	"github.com/dsx-ai-factory/topograph/pkg/metrics"
	"github.com/dsx-ai-factory/topograph/pkg/topology"
)

const (
	ExportTopologyURL = "v2/export-topology"

	fabricNorthSouth = "north-south"

	// roleUnknown is the role of a device whose role is not modeled.
	roleUnknown = "unknown"

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

	headerXApiKey     = "X-Api-Key"
	headerContentType = "Content-Type"
	headerAccept      = "Accept"
	contentTypeJSON   = "application/json"
)

// The server calls the provider with a context that has no deadline, and the
// HTTP client sets none, so an endpoint that accepts a connection but never
// answers would block generation forever. requestTimeout bounds one page;
// layerTimeout bounds a whole traversal, so a slow, endless one cannot run
// for maxPages times requestTimeout. maxPages bounds a traversal by count, so a
// server that never completes cannot keep the request looping. They are
// variables so tests can shorten them.
var (
	requestTimeout = 2 * time.Minute
	layerTimeout   = 10 * time.Minute
	maxPages       = 10000
)

// roleTier gives the hierarchy tier of the standard device roles a Minerva
// fabric reports: a server, a leaf, a spine and a super spine. The export API
// does not enumerate device roles, so this is the set of standard roles that
// Minerva deployments report, not a documented contract. Minerva also supports
// deployment-specific "Custom" roles, and a role is "unknown" when it is not
// modeled; devices with any role outside this map are excluded from the switch
// tree (see computeTiers).
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

// ExportTopologyResponse is one page of the export. The records are in EastWest
// or NVLink, according to the layer requested; this provider requests one layer
// at a time.
type ExportTopologyResponse struct {
	NextCursor json.RawMessage `json:"nextCursor"`
	Complete   bool            `json:"complete"`
	EastWest   *Records        `json:"eastWest"`
	NVLink     *Records        `json:"nvlink"`
}

// Records is the ordered record collection of one layer.
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
	FabricType   string   `json:"fabricType"`
	Children     []Child  `json:"children"`
	NeighborType string   `json:"neighborType"`
	From         Endpoint `json:"from"`
	To           Endpoint `json:"to"`
}

// Child is a direct child listed in a hierarchy group record.
type Child struct {
	Type string `json:"type"`
	Name string `json:"name"`
}

// Endpoint is one end of a neighbor record: the device that owns it, when known.
type Endpoint struct {
	Device string `json:"device"`
}

// Device is a device of the east-west layer and the devices it is linked to.
type Device struct {
	Name  string
	Role  string
	Peers map[string]bool // names of directly linked devices

	// Excluded marks a north-south device. It is not part of the compute fabric,
	// whatever role Minerva reports for it and whether or not it is requested.
	Excluded bool
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
	ctx, cancel := context.WithTimeout(ctx, layerTimeout)
	defer cancel()

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

		// The server certificate is verified and redirects are refused: the API key
		// is sent in a header and the response decides the cluster topology.
		data, httpErr := p.post(ctx, headers, payload)
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

// post sends one export request, giving up after requestTimeout. The server
// certificate is verified unless the insecureSkipVerify credential is set;
// redirects are never followed.
func (p *Provider) post(ctx context.Context, headers map[string]string, payload []byte) ([]byte, *httperr.Error) {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()

	f := httpreq.GetRequestFunc(ctx, http.MethodPost, headers, nil, payload, p.params.ApiURL, ExportTopologyURL)
	_, data, httpErr := httpreq.DoRequestWithRedirects(f, p.creds.InsecureSkipVerify, false)
	return data, httpErr
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
	parseResponse(treeRoot, buildDevices(records), nodeInstances(cis))

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
// device that only appears as a link end has no role. Minerva exports a device
// record only for the devices it selected as hierarchy anchors, so a compute
// node can be reported solely as the far end of its leaf's links; parseResponse
// gives such a requested node the server role.
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
			// must not erase the role an earlier one gave it. The east-west
			// layer also carries north-south devices, which are not part of the
			// compute fabric: such a device is marked excluded and gets no role,
			// so it takes no part in the switch tree, and stays excluded
			// whatever another record says.
			if len(r.Name) != 0 {
				d := get(r.Name)
				switch {
				case r.FabricType == fabricNorthSouth:
					d.Excluded = true
					d.Role = ""
				case len(r.Role) != 0 && !d.Excluded:
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
	// Group names are not unique: Minerva scopes a group by its ancestry, and a
	// record names only its primary parent. Two racks with one name under
	// different parents are different racks, so when a name is shared the domain
	// ID is qualified with the parent's name.
	//
	// That is as far as the export allows. A group record carries only names (its
	// own, its parent's and its children's), with no field for the full ancestry,
	// so two racks with the same name under same-named parents in different pods
	// cannot be told apart and are one domain here. The only path in the API is
	// the pagination cursor, which is API-owned and must not be interpreted.
	type rackKey struct{ parent, name string }
	byName := make(map[string][]rackKey)
	members := make(map[rackKey]map[string]bool) // devices a rack record lists as children
	for _, r := range records {
		if r.Type != recordTypeRack {
			continue
		}
		key := rackKey{r.Parent, r.Name}
		if members[key] == nil {
			members[key] = make(map[string]bool)
			byName[r.Name] = append(byName[r.Name], key)
		}
		for _, c := range r.Children {
			if c.Type == recordTypeDevice {
				members[key][c.Name] = true
			}
		}
	}

	// Domain IDs must be unique. A rack with a unique name keeps its name. Racks
	// that share a name are qualified with the parent's name, and an ID that is
	// already taken, for example by a rack literally named "SU-1.Rack-1", gets a
	// numeric suffix. Keys are visited in sorted order, so the IDs do not depend
	// on the order of the records.
	keys := make([]rackKey, 0, len(members))
	for key := range members {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].name != keys[j].name {
			return keys[i].name < keys[j].name
		}
		return keys[i].parent < keys[j].parent
	})
	ids := make(map[rackKey]string, len(keys))
	used := make(map[string]bool, len(keys))
	// A device's rack field can name a rack that has no group record. Such a
	// rack is a domain under its own name (see resolve), so the name is reserved
	// before any qualified ID is allocated, or a qualified ID could equal it and
	// merge two racks.
	for _, r := range records {
		if r.Type != recordTypeDevice || len(r.Rack) == 0 {
			continue
		}
		if _, grouped := byName[r.Rack]; !grouped {
			used[r.Rack] = true
		}
	}
	for _, key := range keys {
		if len(byName[key.name]) == 1 {
			ids[key] = key.name
			used[key.name] = true
		}
	}
	for _, key := range keys {
		if _, ok := ids[key]; ok {
			continue
		}
		id := key.name
		if len(key.parent) != 0 {
			id = key.parent + "." + key.name
		}
		for base, n := id, 2; used[id]; n++ {
			id = fmt.Sprintf("%s-%d", base, n)
		}
		ids[key] = id
		used[id] = true
	}
	domainID := func(key rackKey) string { return ids[key] }

	// resolve names the rack of a device record: the rack it names, or its primary
	// parent when that is a rack. When several racks share the name, the rack that
	// lists the device as a child is the one.
	resolve := func(r Record) (string, bool) {
		name := r.Rack
		if len(name) == 0 {
			if _, ok := byName[r.Parent]; !ok {
				return "", false
			}
			name = r.Parent
		}
		candidates := byName[name]
		switch len(candidates) {
		case 0:
			return name, true // a rack field that names no rack group
		case 1:
			return domainID(candidates[0]), true
		}
		var found []rackKey
		for _, key := range candidates {
			if members[key][r.Name] {
				found = append(found, key)
			}
		}
		if len(found) != 1 {
			klog.Warningf("Minerva device %q is in rack %q, which is the name of %d racks, and no single one lists it; it gets no NVLink domain", r.Name, name, len(candidates))
			return "", false
		}
		return domainID(found[0]), true
	}

	domains := topology.NewDomainMap()
	for _, r := range records {
		instanceID, ok := instances[r.Name]
		if r.Type != recordTypeDevice || !ok {
			continue
		}
		if rack, ok := resolve(r); ok {
			domains.AddHost(rack, instanceID, r.Name)
		}
	}

	return domains
}

// parseResponse builds a switch tree from the east-west devices, growing it
// outward from the requested nodes rather than materializing the whole fabric.
// Minerva reports a flat device/link list with no tier numbers, so tiers are
// derived from each device's role (see roleTier); devices with an
// unrecognized role take no part in the tree. A switch's vertex ID is its name,
// unless that name is a requested instance ID or the ID of the fallback group,
// in which case it is prefixed with "switch-" until the ID is unique. The prefix uses only characters
// valid in a Kubernetes label value, since engines publish switch IDs as labels.
// A compute node's vertex ID is the instance ID it was requested with, and its
// name is the node name. inputNodes maps each requested node name to its
// instance ID.
func parseResponse(treeRoot *topology.Vertex, devices map[string]*Device, inputNodes map[string]string) {
	// Minerva exports a device record only for the devices it selected as
	// hierarchy anchors: a neighbor reference does not add the far-end device,
	// so a compute node can be reported solely as the far end of its leaf's
	// links, with no role. A role is also "unknown" when it is not modeled. The
	// caller names the compute nodes, so a requested node whose role Minerva
	// does not report is a server. A role that Minerva reports is kept, and a
	// north-south device is never a compute node.
	for name := range inputNodes {
		if d, ok := devices[name]; ok && !d.Excluded && (len(d.Role) == 0 || d.Role == roleUnknown) {
			d.Role = RoleServer
		}
	}

	tiers := computeTiers(devices)
	parents := parentLinks(devices, tiers)

	// Vertices are merged, placed and keyed by ID, so a switch must not share an
	// ID with a requested compute node: it would hide the node from the merge
	// and from the check that every requested node made it into the tree. Nor
	// may it share the ID of the fallback group, which would replace it.
	instanceIDs := make(map[string]bool, len(inputNodes))
	for _, instanceID := range inputNodes {
		instanceIDs[instanceID] = true
	}

	// The fallback group is called no-topology, which engines recognize. Only if
	// a requested instance ID is itself no-topology does the group need another
	// ID, since a group cannot contain a node with its own ID: the Slurm engine
	// rejects it with a self-edge error. It is chosen before the switch IDs so
	// that they can avoid it.
	//
	// This is a deliberate trade-off, not an oversight. The graph engine skips
	// only the exact ID no-topology as the "no network layer" marker, so in that
	// one collision it reports the renamed group as a layer of those nodes. No
	// group ID works for both engines: keeping no-topology fails Slurm outright,
	// and a layer that is only cosmetic is the lesser failure. Removing it needs
	// an engine change, not a different ID here. The documented behavior is in
	// docs/providers/minerva.md.
	groupID := topology.NoTopology
	for instanceIDs[groupID] {
		groupID += "-group"
	}

	// Every switch needs an ID of its own, since a vertex replaces another with
	// the same ID. A switch keeps its name unless that is a requested instance ID
	// or the fallback group's ID. The others are renamed, in name order so that
	// the result does not depend on map order, to an ID that no other vertex has:
	// not a switch that kept its name, not an ID given to an earlier renamed
	// switch, and not the name of any device.
	taken := make(map[string]bool)
	var renamed []string
	for name, dev := range devices {
		if dev.Role == RoleServer {
			continue
		}
		if instanceIDs[name] || name == groupID {
			renamed = append(renamed, name)
		} else {
			taken[name] = true
		}
	}
	slices.Sort(renamed)
	switchIDs := make(map[string]string, len(renamed))
	for _, name := range renamed {
		id := name
		for instanceIDs[id] || id == groupID || taken[id] || (id != name && devices[id] != nil) {
			id = "switch-" + id
		}
		taken[id] = true
		switchIDs[name] = id
	}

	switches := make(map[string]*topology.Vertex)  // every switch, regardless of whether it's reachable from a requested node
	requested := make(map[string]*topology.Vertex) // requested servers: the starting point for growing the tree upward
	for name, dev := range devices {
		if dev.Role != RoleServer {
			id, ok := switchIDs[name]
			if !ok {
				id = name
			}
			switches[name] = &topology.Vertex{ID: id, Name: name, Vertices: make(map[string]*topology.Vertex)}
		} else if instanceID, ok := inputNodes[name]; ok {
			requested[name] = &topology.Vertex{ID: instanceID, Name: name}
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
		for key, v := range frontier {
			for parentID := range parents[key] {
				parent, ok := switches[parentID]
				if !ok {
					continue
				}
				parent.Vertices[v.ID] = v
				if _, isSwitch := switches[key]; isSwitch {
					hasParent[key] = true
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

	noTopology := &topology.Vertex{ID: groupID, Vertices: make(map[string]*topology.Vertex)}
	for name, instanceID := range inputNodes {
		v, ok := requested[name]
		if ok && placed[v.ID] {
			continue
		}
		if !ok {
			klog.Warningf("Requested node %q is not a server in the Minerva topology", name)
			v = &topology.Vertex{ID: instanceID, Name: name}
		}
		noTopology.Vertices[v.ID] = v
		metrics.SetMissingTopology(NAME, name)
	}
	if len(noTopology.Vertices) != 0 {
		treeRoot.Vertices[groupID] = noTopology
	}
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
		if dev.Excluded || !ok {
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
