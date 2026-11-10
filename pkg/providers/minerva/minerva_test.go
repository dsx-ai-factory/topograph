/*
 * Copyright 2026 NVIDIA CORPORATION
 * SPDX-License-Identifier: Apache-2.0
 */

package minerva

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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
	require.Equal(t, RoleServer, devices["srv-01"].Role)
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
	require.Nil(t, parseResponse(treeRoot, devices, map[string]bool{"srv-01": true, "srv-02": true, "srv-03": true}))

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
	require.Nil(t, parseResponse(treeRoot, devices, map[string]bool{"srv-03": true}))
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
	require.Nil(t, parseResponse(treeRoot, devices, map[string]bool{}))
	require.Empty(t, treeRoot.Vertices)

	// a link to a device without a record is not a switch: the requested
	// server has no parent and is grouped under no-topology
	treeRoot = newTreeRoot()
	require.Nil(t, parseResponse(treeRoot, fabric(deviceRec("node-a", RoleServer), linkRec("node-a", "sw-undeclared")),
		map[string]bool{"node-a": true}))
	require.Equal(t, map[string]*topology.Vertex{
		topology.NoTopology: {ID: topology.NoTopology, Vertices: map[string]*topology.Vertex{
			"node-a": {ID: "node-a", Name: "node-a"},
		}},
	}, treeRoot.Vertices)

	// a mixed request keeps the resolved tree intact and groups the other server separately
	treeRoot = newTreeRoot()
	require.Nil(t, parseResponse(treeRoot, fabric(
		deviceRec("leaf-01", RoleLeaf), deviceRec("node-ok", RoleServer), deviceRec("node-orphan", RoleServer),
		linkRec("leaf-01", "node-ok"),
	), map[string]bool{"node-ok": true, "node-orphan": true}))
	require.Equal(t, map[string]*topology.Vertex{
		"leaf-01": {ID: "leaf-01", Name: "leaf-01", Vertices: map[string]*topology.Vertex{
			"node-ok": {ID: "node-ok", Name: "node-ok"},
		}},
		topology.NoTopology: {ID: topology.NoTopology, Vertices: map[string]*topology.Vertex{
			"node-orphan": {ID: "node-orphan", Name: "node-orphan"},
		}},
	}, treeRoot.Vertices)
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
		require.Nil(t, parseResponse(treeRoot, devices, map[string]bool{"server-a": true}))
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
		require.Nil(t, parseResponse(treeRoot, devices, map[string]bool{"node-x": true}))
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
		require.Nil(t, parseResponse(treeRoot, devices, map[string]bool{"node-z": true}))
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
		var buf [4096]byte
		n, _ := r.Body.Read(buf[:])
		raw = append(raw, string(buf[:n]))
		var body ExportTopologyRequest
		assert.NoError(t, json.Unmarshal(buf[:n], &body))
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
	require.Nil(t, parseResponse(treeRoot, devices, map[string]bool{"srv-03": true, "srv-missing": true}))
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
	require.Nil(t, parseResponse(treeRoot, devices, map[string]bool{"srv-03": true, "p1-leaf-01": true}))
	require.Contains(t, treeRoot.Vertices[topology.NoTopology].Vertices, "p1-leaf-01")
	require.Equal(t, 1, vertexCounts(treeRoot)["p1-leaf-02"])
	// it appears only as the childless placeholder, not as the switch
	require.Equal(t, 1, vertexCounts(treeRoot)["p1-leaf-01"])
	require.Empty(t, treeRoot.Vertices[topology.NoTopology].Vertices["p1-leaf-01"].Vertices)

	// when none of the requested nodes are known, they all land under
	// no-topology rather than failing the request
	treeRoot = newTreeRoot()
	require.Nil(t, parseResponse(treeRoot, devices, map[string]bool{"srv-missing": true}))
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
	require.Nil(t, parseResponse(treeRoot, devices, map[string]bool{"srv-x": true, "srv-y": true, "srv-z": true}))

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
	require.Nil(t, parseResponse(treeRoot, devices, map[string]bool{"srv-a": true, "srv-b": true}))
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
