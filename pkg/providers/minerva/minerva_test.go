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
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/util/validation"

	"github.com/dsx-ai-factory/topograph/internal/httperr"
	"github.com/dsx-ai-factory/topograph/pkg/topology"
)

const (
	fixtureEastWest = "../../../tests/output/minerva/export-topology-east-west.json"
	fixtureNVLink   = "../../../tests/output/minerva/export-topology-nvlink.json"
)

func deviceRec(name, role string) Record {
	return Record{Type: recordTypeDevice, Name: name, Role: role}
}

func linkRec(a, b string) Record {
	return Record{Type: recordTypeNeighbor, NeighborType: neighborLayer2, From: Endpoint{Device: a}, To: Endpoint{Device: b}}
}

// fabric builds the device map the tree builder works on from records.
func fabric(records ...Record) map[string]*Device { return buildDevices(records) }

// names returns the requested nodes, each with its own name as instance ID.
func names(nodes ...string) map[string]string {
	m := make(map[string]string, len(nodes))
	for _, n := range nodes {
		m[n] = n
	}
	return m
}

func newTreeRoot() *topology.Vertex {
	return &topology.Vertex{Vertices: make(map[string]*topology.Vertex)}
}

func readFixture(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	return data
}

func fixtureRecords(t *testing.T, path, layer string) []Record {
	t.Helper()
	var resp ExportTopologyResponse
	require.NoError(t, json.Unmarshal(readFixture(t, path), &resp))
	require.True(t, resp.Complete)
	records, ok := resp.records(layer)
	require.True(t, ok)
	return records
}

// newProvider returns a provider pointed at srv, with a fixed API key.
func newProvider(srv *httptest.Server) *Provider {
	return &Provider{params: &ProviderParams{ApiURL: srv.URL}, creds: &Credentials{ApiKey: "test-key"}}
}

// layerServer answers each export request with the fixture of the layer it
// asks for. It reports what it received with assert, since handlers run on the
// server's goroutine.
func layerServer(t *testing.T, fixtures map[string][]byte) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/"+ExportTopologyURL, r.URL.Path)
		assert.Equal(t, "test-key", r.Header.Get("X-Api-Key"))
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))
		assert.Equal(t, "application/json", r.Header.Get("Accept"))

		var body ExportTopologyRequest
		if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&body)) || !assert.Len(t, body.Layers, 1) {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		data, ok := fixtures[body.Layers[0]]
		if !assert.True(t, ok, "unexpected layer %v", body.Layers) {
			http.Error(w, "bad layer", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(data)
	}))
}

func TestBuildDevices(t *testing.T) {
	records := fixtureRecords(t, fixtureEastWest, LayerEastWest)
	devices := buildDevices(records)

	// 12 east-west devices: the nvlink-switch belongs to the nvlink layer
	require.Len(t, devices, 12)
	// Minerva exports no device record for a server: it is only a link end, and
	// parseResponse gives a requested one the server role
	require.Empty(t, devices["srv-01"].Role)
	require.Equal(t, "unknown", devices["p1-leaf-09"].Role)

	// links are unordered: both ends know each other whichever end was reported
	require.Equal(t, map[string]bool{"srv-01": true, "srv-02": true, "p1-spine-01": true}, devices["p1-leaf-01"].Peers)
	require.Equal(t, map[string]bool{"p1-leaf-01": true, "p2-leaf-01": true}, devices["srv-01"].Peers)

	// BGP sessions and self-links are not physical links
	devices = buildDevices([]Record{
		deviceRec("a", RoleLeaf),
		{Type: recordTypeNeighbor, NeighborType: "bgp", From: Endpoint{Device: "a"}, To: Endpoint{Device: "b"}},
		linkRec("a", "a"),
		linkRec("a", ""),
	})
	require.Len(t, devices, 1)
	require.Empty(t, devices["a"].Peers)

	// a device that only appears as a link end has no role, and a device record
	// that arrives after its links still sets the role
	devices = buildDevices([]Record{linkRec("a", "b"), deviceRec("a", RoleLeaf)})
	require.Equal(t, RoleLeaf, devices["a"].Role)
	require.Empty(t, devices["b"].Role)

	// a repeated device record without a role does not erase the role
	devices = buildDevices([]Record{deviceRec("a", RoleLeaf), deviceRec("a", "")})
	require.Equal(t, RoleLeaf, devices["a"].Role)
}

func TestBuildDomains(t *testing.T) {
	records := fixtureRecords(t, fixtureNVLink, LayerNVLink)
	instances := map[string]string{"srv-01": "i-01", "srv-02": "i-02", "srv-03": "i-03", "srv-other": "i-04"}

	domains := buildDomains(records, instances)
	require.Equal(t, topology.DomainMap{
		// srv-01 and srv-02 name their rack; srv-03 gets it from its primary parent
		"NVL-Rack-1": {
			"srv-01": {Domain: "NVL-Rack-1", InstanceID: "i-01", HostName: "srv-01"},
			"srv-02": {Domain: "NVL-Rack-1", InstanceID: "i-02", HostName: "srv-02"},
		},
		"NVL-Rack-2": {
			"srv-03": {Domain: "NVL-Rack-2", InstanceID: "i-03", HostName: "srv-03"},
		},
	}, domains)

	// only requested nodes are placed
	domains = buildDomains(records, map[string]string{"srv-03": "i-03"})
	require.Len(t, domains, 1)
	require.Contains(t, domains["NVL-Rack-2"], "srv-03")

	// a rack record that follows the devices it contains still counts
	var devicesFirst, groups []Record
	for _, r := range records {
		if r.Type == recordTypeDevice {
			devicesFirst = append(devicesFirst, r)
		} else {
			groups = append(groups, r)
		}
	}
	require.Equal(t, buildDomains(records, instances), buildDomains(append(devicesFirst, groups...), instances))

	// a device whose parent is not a rack and that names no rack has no domain
	domains = buildDomains([]Record{
		{Type: "pod", Name: "Pod-1"},
		{Type: recordTypeDevice, Name: "srv-01", Role: RoleServer, Parent: "Pod-1"},
	}, map[string]string{"srv-01": "i-01"})
	require.Empty(t, domains)
}

// TestParseResponse parses a fixture shaped like a real export: an unknown-role
// switch, BGP records, and servers cabled to a leaf in each of two planes.
func TestParseResponse(t *testing.T) {
	devices := buildDevices(fixtureRecords(t, fixtureEastWest, LayerEastWest))

	treeRoot := newTreeRoot()
	parseResponse(treeRoot, devices, names("srv-01", "srv-02", "srv-03"))

	srv01 := &topology.Vertex{ID: "srv-01", Name: "srv-01"}
	srv02 := &topology.Vertex{ID: "srv-02", Name: "srv-02"}
	srv03 := &topology.Vertex{ID: "srv-03", Name: "srv-03"}

	// each server is cabled to a leaf in both planes; only one parent is kept
	// so the result is a tree, and the plane-2 switches never appear.
	leaf01 := &topology.Vertex{ID: "p1-leaf-01", Name: "p1-leaf-01", Vertices: map[string]*topology.Vertex{
		"srv-01": srv01, "srv-02": srv02,
	}}
	leaf02 := &topology.Vertex{ID: "p1-leaf-02", Name: "p1-leaf-02", Vertices: map[string]*topology.Vertex{
		"srv-03": srv03,
	}}
	spine01 := &topology.Vertex{ID: "p1-spine-01", Name: "p1-spine-01", Vertices: map[string]*topology.Vertex{
		"p1-leaf-01": leaf01, "p1-leaf-02": leaf02,
	}}
	super01 := &topology.Vertex{ID: "super-01", Name: "super-01", Vertices: map[string]*topology.Vertex{
		"p1-spine-01": spine01,
	}}
	require.Equal(t, &topology.Vertex{Vertices: map[string]*topology.Vertex{"super-01": super01}}, treeRoot)

	// only a subset of nodes requested: p1-leaf-01 (whose servers are not
	// requested) must not appear, and the super-spine must remain the root.
	treeRoot = newTreeRoot()
	parseResponse(treeRoot, devices, names("srv-03"))
	require.Equal(t, &topology.Vertex{Vertices: map[string]*topology.Vertex{
		"super-01": {ID: "super-01", Name: "super-01", Vertices: map[string]*topology.Vertex{
			"p1-spine-01": {ID: "p1-spine-01", Name: "p1-spine-01", Vertices: map[string]*topology.Vertex{
				"p1-leaf-02": {ID: "p1-leaf-02", Name: "p1-leaf-02", Vertices: map[string]*topology.Vertex{
					"srv-03": {ID: "srv-03", Name: "srv-03"},
				}},
			}},
		}},
	}}, treeRoot)

	// an empty set of requested nodes yields an empty tree, not an error
	treeRoot = newTreeRoot()
	parseResponse(treeRoot, devices, names())
	require.Empty(t, treeRoot.Vertices)

	// a link to a device without a record is not a switch: the requested
	// server has no parent and is grouped under no-topology
	treeRoot = newTreeRoot()
	parseResponse(treeRoot, fabric(deviceRec("node-a", RoleServer), linkRec("node-a", "sw-undeclared")),
		names("node-a"))
	require.Equal(t, map[string]*topology.Vertex{
		topology.NoTopology: {ID: topology.NoTopology, Vertices: map[string]*topology.Vertex{
			"node-a": {ID: "node-a", Name: "node-a"},
		}},
	}, treeRoot.Vertices)

	// a mixed request keeps the resolved tree intact and groups the other server separately
	treeRoot = newTreeRoot()
	parseResponse(treeRoot, fabric(
		deviceRec("leaf-01", RoleLeaf), deviceRec("node-ok", RoleServer), deviceRec("node-orphan", RoleServer),
		linkRec("leaf-01", "node-ok"),
	), names("node-ok", "node-orphan"))
	require.Equal(t, map[string]*topology.Vertex{
		"leaf-01": {ID: "leaf-01", Name: "leaf-01", Vertices: map[string]*topology.Vertex{
			"node-ok": {ID: "node-ok", Name: "node-ok"},
		}},
		topology.NoTopology: {ID: topology.NoTopology, Vertices: map[string]*topology.Vertex{
			"node-orphan": {ID: "node-orphan", Name: "node-orphan"},
		}},
	}, treeRoot.Vertices)
}

// TestParseResponseUsesInstanceIDs verifies that a compute node's vertex ID is
// the instance ID it was requested with and its name the node name, including a
// node placed under no-topology, so results can be matched to the request.
func TestParseResponseUsesInstanceIDs(t *testing.T) {
	devices := buildDevices(fixtureRecords(t, fixtureEastWest, LayerEastWest))

	treeRoot := newTreeRoot()
	parseResponse(treeRoot, devices, map[string]string{"srv-03": "i-03", "srv-missing": "i-99"})

	leaf02 := treeRoot.Vertices["super-01"].Vertices["p1-spine-01"].Vertices["p1-leaf-02"]
	require.Equal(t, map[string]*topology.Vertex{"i-03": {ID: "i-03", Name: "srv-03"}}, leaf02.Vertices)
	require.Equal(t, map[string]*topology.Vertex{"i-99": {ID: "i-99", Name: "srv-missing"}},
		treeRoot.Vertices[topology.NoTopology].Vertices)

	// a server with no switch parent keeps its instance ID too
	devices = fabric(deviceRec("leaf-01", RoleLeaf), deviceRec("srv-orphan", RoleServer))
	treeRoot = newTreeRoot()
	parseResponse(treeRoot, devices, map[string]string{"srv-orphan": "i-orphan"})
	require.Equal(t, map[string]*topology.Vertex{"i-orphan": {ID: "i-orphan", Name: "srv-orphan"}},
		treeRoot.Vertices[topology.NoTopology].Vertices)
}

// TestParseResponseInstanceIDsDoNotCollideWithSwitches verifies that a
// requested instance ID equal to a switch name does not hide the requested
// node: the switch gets another vertex ID, and both nodes stay in the tree.
func TestParseResponseInstanceIDsDoNotCollideWithSwitches(t *testing.T) {
	devices := fabric(
		deviceRec("leaf-a", RoleLeaf), deviceRec("leaf-b", RoleLeaf),
		deviceRec("srv-a", RoleServer), deviceRec("srv-b", RoleServer),
		linkRec("leaf-a", "srv-a"), linkRec("leaf-b", "srv-b"),
	)

	treeRoot := newTreeRoot()
	parseResponse(treeRoot, devices, map[string]string{"srv-a": "leaf-b", "srv-b": "i-b"})
	require.Equal(t, map[string]*topology.Vertex{
		"leaf-a": {ID: "leaf-a", Name: "leaf-a", Vertices: map[string]*topology.Vertex{
			"leaf-b": {ID: "leaf-b", Name: "srv-a"},
		}},
		"switch-leaf-b": {ID: "switch-leaf-b", Name: "leaf-b", Vertices: map[string]*topology.Vertex{
			"i-b": {ID: "i-b", Name: "srv-b"},
		}},
	}, treeRoot.Vertices)
}

// TestParseResponseReservesNoTopology verifies that a switch named like the
// fallback group is not replaced by it, taking the servers beneath it along.
func TestParseResponseReservesNoTopology(t *testing.T) {
	devices := fabric(
		deviceRec(topology.NoTopology, RoleLeaf), deviceRec("srv-a", RoleServer), deviceRec("srv-orphan", RoleServer),
		linkRec(topology.NoTopology, "srv-a"),
	)

	treeRoot := newTreeRoot()
	parseResponse(treeRoot, devices, names("srv-a", "srv-orphan"))
	require.Equal(t, map[string]*topology.Vertex{
		"switch-" + topology.NoTopology: {ID: "switch-" + topology.NoTopology, Name: topology.NoTopology,
			Vertices: map[string]*topology.Vertex{"srv-a": {ID: "srv-a", Name: "srv-a"}}},
		topology.NoTopology: {ID: topology.NoTopology, Vertices: map[string]*topology.Vertex{
			"srv-orphan": {ID: "srv-orphan", Name: "srv-orphan"},
		}},
	}, treeRoot.Vertices)
}

// TestParseResponseFallbackIDDoesNotReplaceSwitch verifies that the renamed
// fallback group does not take the ID of a real switch, which it would replace.
func TestParseResponseFallbackIDDoesNotReplaceSwitch(t *testing.T) {
	devices := fabric(
		deviceRec("no-topology-group", RoleLeaf), deviceRec("srv-a", RoleServer),
		linkRec("no-topology-group", "srv-a"),
	)

	treeRoot := newTreeRoot()
	parseResponse(treeRoot, devices, map[string]string{"srv-a": "i-a", "srv-missing": topology.NoTopology})
	require.Equal(t, map[string]*topology.Vertex{
		"switch-no-topology-group": {ID: "switch-no-topology-group", Name: "no-topology-group",
			Vertices: map[string]*topology.Vertex{"i-a": {ID: "i-a", Name: "srv-a"}}},
		"no-topology-group": {ID: "no-topology-group", Vertices: map[string]*topology.Vertex{
			topology.NoTopology: {ID: topology.NoTopology, Name: "srv-missing"},
		}},
	}, treeRoot.Vertices)
}

// TestParseResponseRenamedIDsAreLabelValues verifies that the IDs given to
// renamed switches can be used as Kubernetes label values, which the Kubernetes
// engine publishes them as.
func TestParseResponseRenamedIDsAreLabelValues(t *testing.T) {
	devices := fabric(
		deviceRec("leaf-b", RoleLeaf), deviceRec(topology.NoTopology, RoleLeaf),
		deviceRec("srv-a", RoleServer), deviceRec("srv-b", RoleServer),
		linkRec("leaf-b", "srv-a"), linkRec(topology.NoTopology, "srv-b"),
	)

	treeRoot := newTreeRoot()
	parseResponse(treeRoot, devices, map[string]string{"srv-a": "leaf-b", "srv-b": "i-b"})
	require.Len(t, treeRoot.Vertices, 2)
	for id := range vertexCounts(treeRoot) {
		require.Empty(t, validation.IsValidLabelValue(id), "vertex ID %q", id)
	}
}

// TestParseResponseInstanceIDNoTopology verifies that a requested instance ID
// equal to the fallback group's name keeps its ID: the group is given another
// one, so the group does not contain a node with its own ID.
func TestParseResponseInstanceIDNoTopology(t *testing.T) {
	devices := fabric(deviceRec("srv-orphan", RoleServer))

	treeRoot := newTreeRoot()
	parseResponse(treeRoot, devices, map[string]string{"srv-orphan": topology.NoTopology})
	require.Equal(t, map[string]*topology.Vertex{
		"no-topology-group": {ID: "no-topology-group", Vertices: map[string]*topology.Vertex{
			topology.NoTopology: {ID: topology.NoTopology, Name: "srv-orphan"},
		}},
	}, treeRoot.Vertices)

	// without the collision the group keeps the name engines recognize
	treeRoot = newTreeRoot()
	parseResponse(treeRoot, devices, map[string]string{"srv-orphan": "i-orphan"})
	require.Contains(t, treeRoot.Vertices, topology.NoTopology)
}

// TestParseResponseRoleHierarchy covers non-uniform fabrics where a switch's
// distance to the nearest server doesn't match its actual role-based level.
//
// A spine with both a directly-attached server and a leaf branch below it
// isn't covered here — that shape hits a separate, pre-existing limitation in
// topology.Merger (see TestParseResponseKeepsRequestedServers).
func TestParseResponseRoleHierarchy(t *testing.T) {
	t.Run("leaf with no server of its own is not inverted above the spine", func(t *testing.T) {
		devices := fabric(
			deviceRec("spine-01", RoleSpine), deviceRec("leaf-01", RoleLeaf), deviceRec("server-a", RoleServer),
			linkRec("spine-01", "leaf-01"), linkRec("spine-01", "server-a"),
		)
		treeRoot := newTreeRoot()
		parseResponse(treeRoot, devices, names("server-a"))
		require.Equal(t, map[string]*topology.Vertex{
			"spine-01": {ID: "spine-01", Name: "spine-01", Vertices: map[string]*topology.Vertex{
				"server-a": {ID: "server-a", Name: "server-a"},
			}},
		}, treeRoot.Vertices)
	})

	t.Run("super_spine sits above spine", func(t *testing.T) {
		devices := fabric(
			deviceRec("super-01", RoleSuperSpine), deviceRec("spine-01", RoleSpine),
			deviceRec("leaf-01", RoleLeaf), deviceRec("node-x", RoleServer),
			linkRec("super-01", "spine-01"), linkRec("spine-01", "leaf-01"), linkRec("leaf-01", "node-x"),
		)
		treeRoot := newTreeRoot()
		parseResponse(treeRoot, devices, names("node-x"))
		require.Equal(t, map[string]*topology.Vertex{
			"super-01": {ID: "super-01", Name: "super-01", Vertices: map[string]*topology.Vertex{
				"spine-01": {ID: "spine-01", Name: "spine-01", Vertices: map[string]*topology.Vertex{
					"leaf-01": {ID: "leaf-01", Name: "leaf-01", Vertices: map[string]*topology.Vertex{
						"node-x": {ID: "node-x", Name: "node-x"},
					}},
				}},
			}},
		}, treeRoot.Vertices)
	})

	t.Run("unrecognized role is excluded and its server falls under no-topology", func(t *testing.T) {
		devices := fabric(deviceRec("custom-tor", "custom-tor"), deviceRec("node-z", RoleServer), linkRec("custom-tor", "node-z"))
		treeRoot := newTreeRoot()
		parseResponse(treeRoot, devices, names("node-z"))
		require.Equal(t, map[string]*topology.Vertex{
			topology.NoTopology: {ID: topology.NoTopology, Vertices: map[string]*topology.Vertex{
				"node-z": {ID: "node-z", Name: "node-z"},
			}},
		}, treeRoot.Vertices)
	})
}

// TestExportLayerPagination verifies that every page is requested, with the
// previous page's nextCursor passed back unchanged and the layer and device
// limit kept, and that the records of all pages are combined.
func TestExportLayerPagination(t *testing.T) {
	var requests []ExportTopologyRequest
	var raw []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, err := io.ReadAll(r.Body)
		assert.NoError(t, err)
		raw = append(raw, string(data))
		var body ExportTopologyRequest
		assert.NoError(t, json.Unmarshal(data, &body))
		requests = append(requests, body)

		w.Header().Set("Content-Type", "application/json")
		switch len(requests) {
		case 1:
			_, _ = w.Write([]byte(`{"complete":false,"nextCursor":{"lastGroupPath":"Pod-1/Rack-1","lastDevice":"srv-02"},
				"eastWest":{"records":[{"type":"device","name":"srv-01","role":"server"}]}}`))
		default:
			_, _ = w.Write([]byte(`{"complete":true,"nextCursor":null,
				"eastWest":{"records":[{"type":"device","name":"srv-02","role":"server"}]}}`))
		}
	}))
	defer srv.Close()

	records, httpErr := newProvider(srv).exportLayer(context.Background(), LayerEastWest, 2)
	require.Nil(t, httpErr)
	require.Len(t, records, 2)
	require.Equal(t, "srv-01", records[0].Name)
	require.Equal(t, "srv-02", records[1].Name)

	require.Len(t, raw, 2)
	require.JSONEq(t, `{"layers":["east-west"],"deviceLimit":2}`, raw[0])
	require.JSONEq(t, `{"layers":["east-west"],"deviceLimit":2,"cursor":{"lastGroupPath":"Pod-1/Rack-1","lastDevice":"srv-02"}}`, raw[1])
}

// setTimeouts shortens the export timeouts for the duration of a test.
func setTimeouts(t *testing.T, request, layer time.Duration) {
	t.Helper()
	oldRequest, oldLayer := requestTimeout, layerTimeout
	requestTimeout, layerTimeout = request, layer
	t.Cleanup(func() { requestTimeout, layerTimeout = oldRequest, oldLayer })
}

// TestExportLayerTimeouts verifies that an endpoint that never answers, or
// answers forever, ends the export with a 502 rather than blocking it.
func TestExportLayerTimeouts(t *testing.T) {
	t.Run("a page that never answers", func(t *testing.T) {
		setTimeouts(t, 100*time.Millisecond, time.Minute)
		release := make(chan struct{})
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			select {
			case <-release:
			case <-r.Context().Done():
			}
		}))
		defer srv.Close()
		defer close(release)

		start := time.Now()
		_, httpErr := newProvider(srv).exportLayer(context.Background(), LayerEastWest, 10)
		require.NotNil(t, httpErr)
		require.Equal(t, http.StatusBadGateway, httpErr.Code())
		require.Contains(t, httpErr.Error(), "deadline exceeded")
		require.Less(t, time.Since(start), 5*time.Second)
	})

	t.Run("a traversal that never completes", func(t *testing.T) {
		// every page is answered within requestTimeout, so only layerTimeout ends it
		setTimeouts(t, time.Minute, 200*time.Millisecond)
		var n int
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			n++
			time.Sleep(20 * time.Millisecond)
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"complete":false,"nextCursor":{"lastGroupPath":"p","lastDevice":"d%d"},"eastWest":{"records":[]}}`, n)
		}))
		defer srv.Close()

		start := time.Now()
		_, httpErr := newProvider(srv).exportLayer(context.Background(), LayerEastWest, 10)
		require.NotNil(t, httpErr)
		require.Equal(t, http.StatusBadGateway, httpErr.Code())
		require.Contains(t, httpErr.Error(), "deadline exceeded")
		require.Less(t, time.Since(start), 5*time.Second)
	})
}

// TestExportLayerMissingLayer verifies that a response without the requested
// layer's collection is an error, not an empty topology, while an empty
// collection is fine.
func TestExportLayerMissingLayer(t *testing.T) {
	serve := func(t *testing.T, page string) *Provider {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(page))
		}))
		t.Cleanup(srv.Close)
		return newProvider(srv)
	}

	for _, layer := range []string{LayerEastWest, LayerNVLink} {
		for _, page := range []string{
			`{"complete":true,"nextCursor":null}`,
			`{"complete":true,"nextCursor":null,"eastWest":null,"nvlink":null}`,
			// the other layer's collection does not satisfy this one
			`{"complete":true,"nextCursor":null,"topology":{"records":[]}}`,
		} {
			_, httpErr := serve(t, page).exportLayer(context.Background(), layer, 10)
			require.NotNil(t, httpErr, "%s: %s", layer, page)
			require.Equal(t, http.StatusBadGateway, httpErr.Code())
			require.Contains(t, httpErr.Error(), "response has no "+layer+" records")
		}
	}

	records, httpErr := serve(t, `{"complete":true,"nextCursor":null,"nvlink":{"records":[]}}`).exportLayer(context.Background(), LayerNVLink, 10)
	require.Nil(t, httpErr)
	require.Empty(t, records)
}

func TestExportLayerPaginationErrors(t *testing.T) {
	serve := func(t *testing.T, pages ...string) *Provider {
		var n int
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			page := pages[min(n, len(pages)-1)]
			n++
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(page))
		}))
		t.Cleanup(srv.Close)
		return newProvider(srv)
	}

	t.Run("incomplete page without a cursor", func(t *testing.T) {
		for _, cursor := range []string{`"nextCursor":null,`, ``} {
			_, httpErr := serve(t, `{`+cursor+`"complete":false,"eastWest":{"records":[]}}`).exportLayer(context.Background(), LayerEastWest, 10)
			require.NotNil(t, httpErr)
			require.Equal(t, http.StatusBadGateway, httpErr.Code())
			require.Contains(t, httpErr.Error(), "without a nextCursor")
		}
	})

	t.Run("traversal over the page limit", func(t *testing.T) {
		oldMax := maxPages
		maxPages = 3
		t.Cleanup(func() { maxPages = oldMax })

		var n int
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			n++
			w.Header().Set("Content-Type", "application/json")
			// a new cursor on every page, so only the page limit ends the traversal
			_, _ = fmt.Fprintf(w, `{"complete":false,"nextCursor":{"lastGroupPath":"p","lastDevice":"d%d"},"eastWest":{"records":[]}}`, n)
		}))
		t.Cleanup(srv.Close)

		_, httpErr := newProvider(srv).exportLayer(context.Background(), LayerEastWest, 10)
		require.NotNil(t, httpErr)
		require.Equal(t, http.StatusBadGateway, httpErr.Code())
		require.Contains(t, httpErr.Error(), "more than 3 pages")
		require.Equal(t, 3, n)
	})

	t.Run("cursor that does not advance", func(t *testing.T) {
		p := serve(t, `{"complete":false,"nextCursor":{"lastGroupPath":"a","lastDevice":"b"},"eastWest":{"records":[]}}`)
		_, httpErr := p.exportLayer(context.Background(), LayerEastWest, 10)
		require.NotNil(t, httpErr)
		require.Equal(t, http.StatusBadGateway, httpErr.Code())
		require.Contains(t, httpErr.Error(), "nextCursor repeated")
	})
}

// TestGetNetworkTreeErrors exercises the error paths of getNetworkTree that
// the happy-path fixtures don't reach.
func TestGetNetworkTreeErrors(t *testing.T) {
	cis := []topology.ComputeInstances{
		{Instances: map[string]string{"i-01": "node-01"}},
	}

	t.Run("error response", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"success": false, "error": "fabricType must be east-west or north-south"}`))
		}))
		defer srv.Close()

		_, httpErr := newProvider(srv).getNetworkTree(context.Background(), nil, cis)
		require.NotNil(t, httpErr)
		require.Equal(t, http.StatusBadGateway, httpErr.Code())
		require.Contains(t, httpErr.Error(), "status 400")
		require.Contains(t, httpErr.Error(), "fabricType must be")
	})

	t.Run("forbidden", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusForbidden)
		}))
		defer srv.Close()

		_, httpErr := newProvider(srv).getNetworkTree(context.Background(), nil, cis)
		require.NotNil(t, httpErr)
		require.Equal(t, http.StatusBadGateway, httpErr.Code())
		require.Contains(t, httpErr.Error(), "status 403")
	})

	t.Run("malformed JSON", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`not json`))
		}))
		defer srv.Close()

		_, httpErr := newProvider(srv).getNetworkTree(context.Background(), nil, cis)
		require.NotNil(t, httpErr)
		require.Equal(t, http.StatusBadGateway, httpErr.Code())
		require.Contains(t, httpErr.Error(), "minerva output read failed")
	})

	t.Run("untrusted certificate is rejected", func(t *testing.T) {
		var called bool
		srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			called = true
		}))
		defer srv.Close()

		_, httpErr := newProvider(srv).getNetworkTree(context.Background(), nil, cis)
		require.NotNil(t, httpErr)
		require.Contains(t, httpErr.Error(), "certificate")
		require.False(t, called, "the request, and the API key with it, must not reach an untrusted server")
	})

	t.Run("untrusted certificate is accepted when insecureSkipVerify is set", func(t *testing.T) {
		var key string
		srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key = r.Header.Get("X-Api-Key")
			_, _ = w.Write([]byte(`{"complete": true, "eastWest": {"records": []}}`))
		}))
		defer srv.Close()

		p := newProvider(srv)
		p.creds.InsecureSkipVerify = true
		_, httpErr := p.post(context.Background(), map[string]string{headerXApiKey: "test-key"}, []byte(`{}`))
		require.Nil(t, httpErr)
		require.Equal(t, "test-key", key)
	})

	t.Run("insecureSkipVerify does not make redirects followed", func(t *testing.T) {
		var targetCalled bool
		target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { targetCalled = true }))
		defer target.Close()
		srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
		}))
		defer srv.Close()

		p := newProvider(srv)
		p.creds.InsecureSkipVerify = true
		_, httpErr := p.post(context.Background(), map[string]string{headerXApiKey: "test-key"}, []byte(`{}`))
		require.NotNil(t, httpErr)
		require.Contains(t, httpErr.Error(), "refusing to follow a redirect")
		require.False(t, targetCalled)
	})

	t.Run("redirect is refused", func(t *testing.T) {
		for _, status := range []int{
			http.StatusMovedPermanently, http.StatusFound, http.StatusTemporaryRedirect, http.StatusPermanentRedirect,
		} {
			t.Run(http.StatusText(status), func(t *testing.T) {
				var targetCalled bool
				var targetKey string
				target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					targetCalled = true
					targetKey = r.Header.Get("X-Api-Key")
				}))
				defer target.Close()

				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					http.Redirect(w, r, target.URL, status)
				}))
				defer srv.Close()

				_, httpErr := newProvider(srv).getNetworkTree(context.Background(), nil, cis)
				require.NotNil(t, httpErr)
				require.Equal(t, http.StatusBadGateway, httpErr.Code())
				require.Contains(t, httpErr.Error(), "refusing to follow a redirect")
				require.False(t, targetCalled, "the redirect target must not be contacted")
				require.Empty(t, targetKey, "the API key must not reach the redirect target")
			})
		}
	})

	t.Run("canceled while the request is in flight", func(t *testing.T) {
		started := make(chan struct{})
		release := make(chan struct{})
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			close(started)
			select {
			case <-release:
			case <-r.Context().Done():
			}
		}))
		defer srv.Close()
		defer close(release)

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		go func() {
			<-started
			cancel()
		}()

		p := newProvider(srv)
		done := make(chan *httperr.Error, 1)
		go func() {
			_, httpErr := p.getNetworkTree(ctx, nil, cis)
			done <- httpErr
		}()

		select {
		case httpErr := <-done:
			require.NotNil(t, httpErr)
			require.Equal(t, http.StatusBadGateway, httpErr.Code())
			require.Contains(t, httpErr.Error(), "context canceled")
		case <-time.After(5 * time.Second):
			t.Fatal("getNetworkTree did not return after the context was canceled")
		}
	})

	t.Run("no nodes requested", func(t *testing.T) {
		srv := layerServer(t, map[string][]byte{LayerEastWest: readFixture(t, fixtureEastWest)})
		defer srv.Close()

		_, httpErr := newProvider(srv).getNetworkTree(context.Background(), nil, nil)
		require.NotNil(t, httpErr)
		require.Equal(t, http.StatusBadGateway, httpErr.Code())
		require.Contains(t, httpErr.Error(), "no topology available from Minerva")
	})
}

func TestDeviceLimit(t *testing.T) {
	n := func(v int) *int { return &v }
	require.Equal(t, maxDeviceLimit, deviceLimit(nil))
	require.Equal(t, maxDeviceLimit, deviceLimit(n(0)))
	require.Equal(t, maxDeviceLimit, deviceLimit(n(-3)))
	require.Equal(t, 50, deviceLimit(n(50)))
	require.Equal(t, maxDeviceLimit, deviceLimit(n(maxDeviceLimit)))
	require.Equal(t, maxDeviceLimit, deviceLimit(n(10000)))
}

// vertexCounts returns how many times each vertex ID appears in the tree.
func vertexCounts(treeRoot *topology.Vertex) map[string]int {
	counts := make(map[string]int)
	var walk func(v *topology.Vertex)
	walk = func(v *topology.Vertex) {
		counts[v.ID]++
		for _, c := range v.Vertices {
			walk(c)
		}
	}
	for _, v := range treeRoot.Vertices {
		walk(v)
	}
	return counts
}

// TestParseResponseMissingServers verifies that a requested server Minerva
// doesn't know about is kept under no-topology, as ClusterTopology.ToGraph
// does, rather than silently dropped.
func TestParseResponseMissingServers(t *testing.T) {
	devices := buildDevices(fixtureRecords(t, fixtureEastWest, LayerEastWest))

	treeRoot := newTreeRoot()
	parseResponse(treeRoot, devices, names("srv-03", "srv-missing"))
	require.Contains(t, treeRoot.Vertices, "super-01")
	require.Equal(t, &topology.Vertex{
		ID: topology.NoTopology,
		Vertices: map[string]*topology.Vertex{
			"srv-missing": {ID: "srv-missing", Name: "srv-missing"},
		},
	}, treeRoot.Vertices[topology.NoTopology])

	// a requested name that Minerva reports as a switch, not a server, is not
	// a node to place in the tree either
	treeRoot = newTreeRoot()
	parseResponse(treeRoot, devices, names("srv-03", "p1-leaf-01"))
	require.Contains(t, treeRoot.Vertices[topology.NoTopology].Vertices, "p1-leaf-01")
	require.Equal(t, 1, vertexCounts(treeRoot)["p1-leaf-02"])
	// it appears only as the childless placeholder, not as the switch
	require.Equal(t, 1, vertexCounts(treeRoot)["p1-leaf-01"])
	require.Empty(t, treeRoot.Vertices[topology.NoTopology].Vertices["p1-leaf-01"].Vertices)

	// when none of the requested nodes are known, they all land under
	// no-topology rather than failing the request
	treeRoot = newTreeRoot()
	parseResponse(treeRoot, devices, names("srv-missing"))
	require.Len(t, treeRoot.Vertices, 1)
	require.Contains(t, treeRoot.Vertices, topology.NoTopology)
}

// TestParseResponseSharedSwitches covers a partially connected fabric where
// two spines share one leaf, so merging identical child sets can't collapse
// them. Every vertex must still appear exactly once in the output.
func TestParseResponseSharedSwitches(t *testing.T) {
	devices := fabric(
		deviceRec("spine-a", RoleSpine), deviceRec("spine-b", RoleSpine),
		deviceRec("leaf-x", RoleLeaf), deviceRec("leaf-y", RoleLeaf), deviceRec("leaf-z", RoleLeaf),
		deviceRec("srv-x", RoleServer), deviceRec("srv-y", RoleServer), deviceRec("srv-z", RoleServer),
		linkRec("spine-a", "leaf-x"), linkRec("spine-a", "leaf-y"),
		linkRec("spine-b", "leaf-y"), linkRec("spine-b", "leaf-z"),
		linkRec("leaf-x", "srv-x"), linkRec("leaf-y", "srv-y"), linkRec("leaf-z", "srv-z"),
	)

	treeRoot := newTreeRoot()
	parseResponse(treeRoot, devices, names("srv-x", "srv-y", "srv-z"))

	seen := vertexCounts(treeRoot)
	for id, n := range seen {
		require.Equal(t, 1, n, "vertex %q appears %d times", id, n)
	}
	for _, id := range []string{"srv-x", "srv-y", "srv-z"} {
		require.Equal(t, 1, seen[id], "server %q must be present", id)
	}
}

// TestParseResponseKeepsRequestedServers verifies that a requested server is
// never lost: topology.Merger can drop a deeper branch under a switch whose
// children sit at different depths, and such servers must fall back to
// no-topology rather than vanishing from the output.
func TestParseResponseKeepsRequestedServers(t *testing.T) {
	devices := fabric(
		deviceRec("spine-01", RoleSpine), deviceRec("leaf-01", RoleLeaf),
		deviceRec("srv-a", RoleServer), deviceRec("srv-b", RoleServer),
		linkRec("spine-01", "leaf-01"), linkRec("spine-01", "srv-a"), linkRec("leaf-01", "srv-b"),
	)

	treeRoot := newTreeRoot()
	parseResponse(treeRoot, devices, names("srv-a", "srv-b"))
	counts := vertexCounts(treeRoot)
	require.Equal(t, 1, counts["srv-a"])
	require.Equal(t, 1, counts["srv-b"])
}

// TestParentLinksPrefersLowestTier verifies a server linked to both a leaf and
// a spine attaches to the leaf regardless of how the device names sort.
func TestParentLinksPrefersLowestTier(t *testing.T) {
	devices := fabric(
		deviceRec("a-spine", RoleSpine), deviceRec("z-leaf", RoleLeaf), deviceRec("srv", RoleServer),
		linkRec("a-spine", "srv"), linkRec("z-leaf", "srv"),
	)
	require.Equal(t, map[string]bool{"z-leaf": true}, parentLinks(devices, computeTiers(devices))["srv"])
}

// TestParentLinksSameTierPrefersFirstName verifies a multi-plane server keeps
// the leaf whose name sorts first.
func TestParentLinksSameTierPrefersFirstName(t *testing.T) {
	devices := fabric(
		deviceRec("p2-leaf", RoleLeaf), deviceRec("p1-leaf", RoleLeaf), deviceRec("srv", RoleServer),
		linkRec("p2-leaf", "srv"), linkRec("p1-leaf", "srv"),
	)
	require.Equal(t, map[string]bool{"p1-leaf": true}, parentLinks(devices, computeTiers(devices))["srv"])
}

func TestSingleParent(t *testing.T) {
	leaf := func(id string) *topology.Vertex { return &topology.Vertex{ID: id, Name: id} }
	sw := func(id string, children ...*topology.Vertex) *topology.Vertex {
		v := &topology.Vertex{ID: id, Name: id, Vertices: make(map[string]*topology.Vertex)}
		for _, c := range children {
			v.Vertices[c.ID] = c
		}
		return v
	}

	t.Run("shared vertex stays under the first parent in ID order", func(t *testing.T) {
		shared := leaf("y")
		a := sw("a", leaf("x"), shared)
		b := sw("b", shared, leaf("z"))

		tree := singleParent([]*topology.Vertex{b, a})
		require.Equal(t, []*topology.Vertex{a, b}, tree)
		require.Contains(t, a.Vertices, "y")
		require.NotContains(t, b.Vertices, "y")
		require.Contains(t, b.Vertices, "z")
	})

	t.Run("switch left without children is pruned", func(t *testing.T) {
		shared := leaf("y")
		a := sw("a", sw("l1", shared))
		b := sw("b", sw("l2", shared))

		tree := singleParent([]*topology.Vertex{a, b})
		require.Equal(t, []*topology.Vertex{a}, tree)
		require.Contains(t, a.Vertices, "l1")
	})

	t.Run("nil child is dropped", func(t *testing.T) {
		a := sw("a", leaf("x"))
		a.Vertices["ghost"] = nil

		tree := singleParent([]*topology.Vertex{a})
		require.Equal(t, []*topology.Vertex{a}, tree)
		require.NotContains(t, a.Vertices, "ghost")
	})

	t.Run("root already placed under another root is skipped", func(t *testing.T) {
		inner := sw("b", leaf("x"))
		a := sw("a", inner)

		tree := singleParent([]*topology.Vertex{inner, a})
		require.Equal(t, []*topology.Vertex{a}, tree)
	})
}

// placedIDs returns the ID of every vertex under v, including v.
func placedIDs(v *topology.Vertex) map[string]bool {
	ids := map[string]bool{}
	var walk func(v *topology.Vertex)
	walk = func(v *topology.Vertex) {
		ids[v.ID] = true
		for _, c := range v.Vertices {
			walk(c)
		}
	}
	walk(v)
	return ids
}

// Minerva exports a device record only for the devices it selected as hierarchy
// anchors; a compute node can be reported solely as the far end of its leaf's
// links. The caller names the compute nodes, so a requested node with no
// reported role is a server and is placed under its leaf.
func TestParseResponseRequestedNodesWithoutDeviceRecords(t *testing.T) {
	devices := fabric(
		deviceRec("leaf-1", RoleLeaf),
		deviceRec("leaf-2", RoleLeaf),
		deviceRec("spine-1", RoleSpine),
		linkRec("leaf-1", "srv-1"),
		linkRec("srv-2", "leaf-1"),
		linkRec("leaf-2", "srv-3"),
		linkRec("leaf-1", "spine-1"),
		linkRec("spine-1", "leaf-2"),
	)
	require.Empty(t, devices["srv-1"].Role)

	root := newTreeRoot()
	parseResponse(root, devices, names("srv-1", "srv-2", "srv-3"))
	require.NotContains(t, root.Vertices, topology.NoTopology)
	ids := placedIDs(root)
	for _, n := range []string{"srv-1", "srv-2", "srv-3", "leaf-1", "leaf-2", "spine-1"} {
		require.True(t, ids[n], n)
	}
	require.Equal(t, RoleServer, devices["srv-1"].Role)
}

// Only a requested node is a server: a link end that nobody requested stays out
// of the tree, and a role that Minerva reports is never overridden.
func TestParseResponseRequestedRoleRules(t *testing.T) {
	devices := fabric(
		deviceRec("leaf-1", RoleLeaf),
		deviceRec("srv-unmodeled", roleUnknown), // a role that is not modeled
		deviceRec("custom-1", "custom-role"),    // a deployment-specific role
		linkRec("leaf-1", "srv-unmodeled"),
		linkRec("leaf-1", "custom-1"),
		linkRec("leaf-1", "other-host"), // a link end nobody requested
	)

	root := newTreeRoot()
	parseResponse(root, devices, names("srv-unmodeled", "custom-1"))

	require.Equal(t, RoleServer, devices["srv-unmodeled"].Role)
	require.Equal(t, "custom-role", devices["custom-1"].Role)
	require.Empty(t, devices["other-host"].Role)

	ids := placedIDs(root)
	require.True(t, ids["srv-unmodeled"])
	require.False(t, ids["other-host"])
	// the custom role is kept, so that node has no tier and no place in the tree
	require.Contains(t, root.Vertices[topology.NoTopology].Vertices, "custom-1")
}

// The east-west layer also carries north-south devices, which are not part of
// the compute fabric.
func TestBuildDevicesIgnoresNorthSouthRoles(t *testing.T) {
	devices := buildDevices([]Record{
		deviceRec("leaf-1", RoleLeaf),
		{Type: recordTypeDevice, Name: "border-1", Role: RoleSpine, FabricType: "north-south"},
		linkRec("leaf-1", "border-1"),
	})
	require.Equal(t, RoleLeaf, devices["leaf-1"].Role)
	require.Empty(t, devices["border-1"].Role)
}

// Minerva scopes a group by its ancestry, so two racks may share a name.
func TestBuildDomainsRacksSharingAName(t *testing.T) {
	rack := func(parent, name string, devices ...string) Record {
		r := Record{Type: recordTypeRack, Name: name, Parent: parent}
		for _, d := range devices {
			r.Children = append(r.Children, Child{Type: recordTypeDevice, Name: d})
		}
		return r
	}
	dev := func(name, parent string) Record {
		return Record{Type: recordTypeDevice, Name: name, Role: RoleServer, Parent: parent}
	}

	t.Run("a shared name is qualified by the parent", func(t *testing.T) {
		domains := buildDomains([]Record{
			rack("SU-1", "Rack-1", "a"),
			rack("SU-2", "Rack-1", "b"),
			dev("a", "Rack-1"), dev("b", "Rack-1"),
			// the hierarchy context repeats across pages
			rack("SU-1", "Rack-1", "a"),
		}, names("a", "b"))
		require.Len(t, domains, 2)
		require.Contains(t, domains, "SU-1.Rack-1")
		require.Contains(t, domains, "SU-2.Rack-1")
	})

	t.Run("a qualified ID never collides with another rack's name", func(t *testing.T) {
		// "a"/"r1" qualifies to "a.r1", which is also the literal name of a rack
		records := []Record{
			rack("a", "r1", "x"),
			rack("b", "r1", "y"),
			rack("p", "a.r1", "z"),
			dev("x", "r1"), dev("y", "r1"), dev("z", "a.r1"),
		}
		domains := buildDomains(records, names("x", "y", "z"))
		require.Len(t, domains, 3, "each rack is its own domain")
		require.Contains(t, domains, "a.r1") // the rack literally named so keeps its name
		require.Len(t, domains["a.r1"], 1)
		require.Contains(t, domains["a.r1"], "z")
		require.Contains(t, domains, "b.r1")
		require.Contains(t, domains, "a.r1-2")
		require.Contains(t, domains["a.r1-2"], "x")

		// the IDs do not depend on the order of the records
		slices.Reverse(records)
		require.Equal(t, domains, buildDomains(records, names("x", "y", "z")))
	})

	t.Run("a unique name is kept as is", func(t *testing.T) {
		domains := buildDomains([]Record{rack("SU-1", "Rack-1", "a"), dev("a", "Rack-1")}, names("a"))
		require.Len(t, domains, 1)
		require.Contains(t, domains, "Rack-1")
	})

	t.Run("a device that no rack claims gets no domain", func(t *testing.T) {
		domains := buildDomains([]Record{
			rack("SU-1", "Rack-1", "a"),
			rack("SU-2", "Rack-1", "b"),
			dev("c", "Rack-1"),
		}, names("c"))
		require.Empty(t, domains)
	})

	t.Run("the rack field names a rack without a group record", func(t *testing.T) {
		domains := buildDomains([]Record{{Type: recordTypeDevice, Name: "a", Rack: "Rack-9"}}, names("a"))
		require.Contains(t, domains, "Rack-9")
	})
}

// Switches that are renamed because a requested instance ID takes their name
// must not be given the same ID: one would replace the other in the tree and its
// servers would fall back to no-topology despite having a known path.
func TestParseResponseRenamedSwitchIDsAreUnique(t *testing.T) {
	devices := fabric(
		deviceRec("leaf", RoleLeaf), deviceRec("switch-leaf", RoleLeaf),
		deviceRec("srv-a", RoleServer), deviceRec("srv-b", RoleServer),
		linkRec("leaf", "srv-a"), linkRec("switch-leaf", "srv-b"),
	)

	for range 20 { // the devices are visited in map order
		root := newTreeRoot()
		// both switch names are also requested instance IDs, so both are renamed
		parseResponse(root, devices, map[string]string{"srv-a": "leaf", "srv-b": "switch-leaf"})

		require.NotContains(t, root.Vertices, topology.NoTopology)
		require.Len(t, root.Vertices, 2)
		for id, n := range vertexCounts(root) {
			require.Equal(t, 1, n, "vertex %q", id)
		}
		ids := placedIDs(root)
		require.True(t, ids["leaf"] && ids["switch-leaf"], "both requested servers stay in the tree")
	}
}

// A north-south device is not a compute node even when it is requested and its
// role is not modeled or is a recognized switch role: it stays out of the tree.
func TestParseResponseNorthSouthDeviceStaysExcluded(t *testing.T) {
	ns := func(name, role string) Record {
		return Record{Type: recordTypeDevice, Name: name, Role: role, FabricType: fabricNorthSouth}
	}

	for _, role := range []string{"", roleUnknown, RoleLeaf, RoleServer} {
		t.Run("role "+role, func(t *testing.T) {
			devices := buildDevices([]Record{
				deviceRec("leaf-1", RoleLeaf), deviceRec("srv-1", RoleServer),
				ns("border-1", role),
				linkRec("leaf-1", "srv-1"), linkRec("leaf-1", "border-1"),
			})

			root := newTreeRoot()
			parseResponse(root, devices, names("srv-1", "border-1"))

			require.True(t, devices["border-1"].Excluded)
			require.Empty(t, devices["border-1"].Role, "never promoted to a server")
			leaf := root.Vertices["leaf-1"]
			require.NotNil(t, leaf)
			require.Contains(t, leaf.Vertices, "srv-1")
			require.NotContains(t, leaf.Vertices, "border-1", "not placed under the switch it is linked to")
			// it is reported like any requested node that is not a compute server
			require.Contains(t, root.Vertices[topology.NoTopology].Vertices, "border-1")
		})
	}

	t.Run("the exclusion does not depend on the order of the records", func(t *testing.T) {
		for _, records := range [][]Record{
			{ns("border-1", RoleLeaf), deviceRec("border-1", RoleLeaf)},
			{deviceRec("border-1", RoleLeaf), ns("border-1", RoleLeaf)},
		} {
			devices := buildDevices(records)
			require.True(t, devices["border-1"].Excluded)
			require.Empty(t, devices["border-1"].Role)
		}
	})
}

// A device's rack field can name a rack that has no group record. Its name must
// not be taken by a qualified ID of two racks that share a name.
func TestBuildDomainsReservesUngroupedRackNames(t *testing.T) {
	rack := func(parent, name string, devices ...string) Record {
		r := Record{Type: recordTypeRack, Name: name, Parent: parent}
		for _, d := range devices {
			r.Children = append(r.Children, Child{Type: recordTypeDevice, Name: d})
		}
		return r
	}
	records := []Record{
		rack("a", "r1", "x"),
		rack("b", "r1", "y"),
		{Type: recordTypeDevice, Name: "x", Parent: "r1"},
		{Type: recordTypeDevice, Name: "y", Parent: "r1"},
		// no group record names a.r1, which is also the qualified ID of rack a/r1
		{Type: recordTypeDevice, Name: "z", Rack: "a.r1"},
	}

	domains := buildDomains(records, names("x", "y", "z"))
	require.Len(t, domains, 3, "three racks, three domains")
	require.Equal(t, []string{"z"}, hostNames(domains["a.r1"]), "the ungrouped rack keeps the name it was given")
	require.Equal(t, []string{"x"}, hostNames(domains["a.r1-2"]))
	require.Equal(t, []string{"y"}, hostNames(domains["b.r1"]))

	// the IDs do not depend on the order of the records
	slices.Reverse(records)
	require.Equal(t, domains, buildDomains(records, names("x", "y", "z")))
}

// hostNames returns the sorted host names of a domain.
func hostNames(domain map[string]*topology.HostInfo) []string {
	var hosts []string
	for host := range domain {
		hosts = append(hosts, host)
	}
	slices.Sort(hosts)
	return hosts
}
