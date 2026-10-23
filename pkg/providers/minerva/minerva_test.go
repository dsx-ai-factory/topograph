/*
 * Copyright 2026 NVIDIA CORPORATION
 * SPDX-License-Identifier: Apache-2.0
 */

package minerva

import (
	"context"
	"encoding/json"
	"fmt"
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

// TestParseResponse parses a fixture shaped like a real export-topology
// payload: numeric GPU componentId, an nvlink-switch with a null device_id,
// empty interface objects, an unrecognized-role switch, extra top-level
// fields, and servers cabled to a leaf in each of two planes.
func TestParseResponse(t *testing.T) {
	data, err := os.ReadFile("../../../tests/output/minerva/export-topology.json")
	require.NoError(t, err)

	var resp ExportTopologyResponse
	require.NoError(t, json.Unmarshal(data, &resp))
	require.True(t, resp.Success)
	require.Len(t, resp.Topology.Layer2, 13)
	require.Len(t, resp.Topology.Layer2["srv-01"].GPUs, 4)
	require.Equal(t, 0, resp.Topology.Layer2["srv-01"].GPUs[0].ComponentID)
	require.Empty(t, resp.Topology.Layer2["nvl-sw-01"].DeviceID)

	treeRoot := &topology.Vertex{Vertices: make(map[string]*topology.Vertex)}
	httpErr := parseResponse(treeRoot, &resp, map[string]bool{
		"srv-01": true,
		"srv-02": true,
		"srv-03": true,
	})
	require.Nil(t, httpErr)

	srv01 := &topology.Vertex{ID: "s1", Name: "srv-01"}
	srv02 := &topology.Vertex{ID: "s2", Name: "srv-02"}
	srv03 := &topology.Vertex{ID: "s3", Name: "srv-03"}

	// each server is cabled to a leaf in both planes; only one parent is kept
	// so the result is a tree, and the plane-2 switches never appear.
	leaf01 := &topology.Vertex{ID: "p1-l1", Name: "p1-leaf-01", Vertices: map[string]*topology.Vertex{
		"s1": srv01, "s2": srv02,
	}}
	leaf02 := &topology.Vertex{ID: "p1-l2", Name: "p1-leaf-02", Vertices: map[string]*topology.Vertex{
		"s3": srv03,
	}}
	spine01 := &topology.Vertex{ID: "p1-s1", Name: "p1-spine-01", Vertices: map[string]*topology.Vertex{
		"p1-l1": leaf01, "p1-l2": leaf02,
	}}
	super01 := &topology.Vertex{ID: "ss1", Name: "super-01", Vertices: map[string]*topology.Vertex{
		"p1-s1": spine01,
	}}

	expected := &topology.Vertex{Vertices: map[string]*topology.Vertex{"ss1": super01}}
	require.Equal(t, expected, treeRoot)

	// only a subset of nodes requested: p1-leaf-01 (whose servers srv-01 and
	// srv-02 are not requested) must not appear in the tree, and the
	// super-spine must remain the root.
	treeRoot = &topology.Vertex{Vertices: make(map[string]*topology.Vertex)}
	httpErr = parseResponse(treeRoot, &resp, map[string]bool{"srv-03": true})
	require.Nil(t, httpErr)

	expectedSubset := &topology.Vertex{Vertices: map[string]*topology.Vertex{
		"ss1": {ID: "ss1", Name: "super-01", Vertices: map[string]*topology.Vertex{
			"p1-s1": {ID: "p1-s1", Name: "p1-spine-01", Vertices: map[string]*topology.Vertex{
				"p1-l2": {ID: "p1-l2", Name: "p1-leaf-02", Vertices: map[string]*topology.Vertex{
					"s3": {ID: "s3", Name: "srv-03"},
				}},
			}},
		}},
	}}
	require.Equal(t, expectedSubset, treeRoot)

	// a link referencing an undeclared device must not panic; since the remote
	// device can't be resolved, the requested server has no switch parent and
	// is grouped under the shared no-topology switch instead of being dropped
	// or surfacing as a top-level switch itself.
	treeRoot = &topology.Vertex{Vertices: make(map[string]*topology.Vertex)}
	crasher := &ExportTopologyResponse{
		Success: true,
		Topology: Topology{
			Layer2: map[string]Device{
				"node-a": {
					DeviceName: "node-a",
					Role:       RoleServer,
					DeviceID:   "d-a",
					LocalInterface: map[string]InterfaceLink{
						"eth0": {RemoteInterface: "swp1", RemoteDevice: "sw-undeclared"},
					},
				},
			},
		},
	}
	httpErr = parseResponse(treeRoot, crasher, map[string]bool{"node-a": true})
	require.Nil(t, httpErr)
	require.Equal(t, map[string]*topology.Vertex{
		topology.NoTopology: {ID: topology.NoTopology, Vertices: map[string]*topology.Vertex{
			"d-a": {ID: "d-a", Name: "node-a"},
		}},
	}, treeRoot.Vertices)

	// two devices resolving to the same ID must error out rather than
	// silently overwrite each other in the ID-keyed adjacency/vertex maps.
	treeRoot = &topology.Vertex{Vertices: make(map[string]*topology.Vertex)}
	dup := &ExportTopologyResponse{
		Success: true,
		Topology: Topology{
			Layer2: map[string]Device{
				"node-b": {DeviceName: "node-b", Role: RoleServer, DeviceID: "d-dup"},
				"node-c": {DeviceName: "node-c", Role: RoleServer, DeviceID: "d-dup"},
			},
		},
	}
	httpErr = parseResponse(treeRoot, dup, map[string]bool{"node-b": true, "node-c": true})
	require.NotNil(t, httpErr)
	require.Equal(t, http.StatusBadGateway, httpErr.Code())
	require.Contains(t, httpErr.Error(), `duplicate device id "d-dup"`)
	require.Contains(t, httpErr.Error(), "node-b")
	require.Contains(t, httpErr.Error(), "node-c")

	// an empty set of requested nodes yields an empty tree, not an error
	treeRoot = &topology.Vertex{Vertices: make(map[string]*topology.Vertex)}
	httpErr = parseResponse(treeRoot, &resp, map[string]bool{})
	require.Nil(t, httpErr)
	require.Empty(t, treeRoot.Vertices)

	// a requested server with no resolvable switch parent must not surface as
	// a top-level root in the tree; it's grouped under no-topology instead
	treeRoot = &topology.Vertex{Vertices: make(map[string]*topology.Vertex)}
	orphan := &ExportTopologyResponse{
		Success: true,
		Topology: Topology{
			Layer2: map[string]Device{
				"node-orphan": {DeviceName: "node-orphan", Role: RoleServer, DeviceID: "d-orphan"},
			},
		},
	}
	httpErr = parseResponse(treeRoot, orphan, map[string]bool{"node-orphan": true})
	require.Nil(t, httpErr)
	require.Equal(t, map[string]*topology.Vertex{
		topology.NoTopology: {ID: topology.NoTopology, Vertices: map[string]*topology.Vertex{
			"d-orphan": {ID: "d-orphan", Name: "node-orphan"},
		}},
	}, treeRoot.Vertices)

	// a mixed request — one server resolves to a switch, the other has no
	// switch parent — must keep the resolved switch tree intact and group the
	// unresolved server separately, rather than losing either.
	treeRoot = &topology.Vertex{Vertices: make(map[string]*topology.Vertex)}
	mixed := &ExportTopologyResponse{
		Success: true,
		Topology: Topology{
			Layer2: map[string]Device{
				"leaf-01": {
					DeviceName: "leaf-01",
					Role:       "leaf",
					DeviceID:   "d-leaf",
					LocalInterface: map[string]InterfaceLink{
						"swp1": {RemoteInterface: "eth0", RemoteDevice: "node-ok"},
					},
				},
				"node-ok": {
					DeviceName: "node-ok",
					Role:       RoleServer,
					DeviceID:   "d-ok",
					LocalInterface: map[string]InterfaceLink{
						"eth0": {RemoteInterface: "swp1", RemoteDevice: "leaf-01"},
					},
				},
				"node-orphan": {DeviceName: "node-orphan", Role: RoleServer, DeviceID: "d-orphan2"},
			},
		},
	}
	httpErr = parseResponse(treeRoot, mixed, map[string]bool{"node-ok": true, "node-orphan": true})
	require.Nil(t, httpErr)
	require.Equal(t, map[string]*topology.Vertex{
		"d-leaf": {ID: "d-leaf", Name: "leaf-01", Vertices: map[string]*topology.Vertex{
			"d-ok": {ID: "d-ok", Name: "node-ok"},
		}},
		topology.NoTopology: {ID: topology.NoTopology, Vertices: map[string]*topology.Vertex{
			"d-orphan2": {ID: "d-orphan2", Name: "node-orphan"},
		}},
	}, treeRoot.Vertices)
}

// TestParseResponseRoleHierarchy covers non-uniform fabrics where a switch's
// distance to the nearest server (the old tiering heuristic) doesn't match
// its actual role-based hierarchy level.
//
// Note: a spine with both a directly-attached server and a leaf branch below
// it (i.e. a switch whose children sit at different depths) isn't covered
// here — that shape hits a separate, pre-existing bug in topology.Merger
// (traverse's per-layer, rather than per-vertex, leaf detection drops the
// deeper branch's children), independent of tier derivation.
func TestParseResponseRoleHierarchy(t *testing.T) {
	link := func(remote string) map[string]InterfaceLink {
		return map[string]InterfaceLink{"eth0": {RemoteDevice: remote}}
	}

	t.Run("leaf with no server of its own is not inverted above the spine", func(t *testing.T) {
		// A spine one hop from its server, plus a leaf with no server of its
		// own that only connects to the spine, used to end up farther from the
		// nearest server than the spine under distance-based tiering, wrongly
		// making the leaf the parent of the spine.
		resp := &ExportTopologyResponse{
			Success: true,
			Topology: Topology{
				Layer2: map[string]Device{
					"spine-01": {DeviceName: "spine-01", Role: RoleSpine, DeviceID: "d-sp",
						LocalInterface: map[string]InterfaceLink{
							"swp1": {RemoteDevice: "leaf-01"},
							"swp2": {RemoteDevice: "server-a"},
						}},
					"leaf-01":  {DeviceName: "leaf-01", Role: RoleLeaf, DeviceID: "d-lf", LocalInterface: link("spine-01")},
					"server-a": {DeviceName: "server-a", Role: RoleServer, DeviceID: "d-a", LocalInterface: link("spine-01")},
				},
			},
		}

		treeRoot := &topology.Vertex{Vertices: make(map[string]*topology.Vertex)}
		httpErr := parseResponse(treeRoot, resp, map[string]bool{"server-a": true})
		require.Nil(t, httpErr)
		require.Equal(t, map[string]*topology.Vertex{
			"d-sp": {ID: "d-sp", Name: "spine-01", Vertices: map[string]*topology.Vertex{
				"d-a": {ID: "d-a", Name: "server-a"},
			}},
		}, treeRoot.Vertices)
	})

	t.Run("super_spine sits above spine", func(t *testing.T) {
		resp := &ExportTopologyResponse{
			Success: true,
			Topology: Topology{
				Layer2: map[string]Device{
					"super-01": {DeviceName: "super-01", Role: RoleSuperSpine, DeviceID: "d-ss", LocalInterface: link("spine-01")},
					"spine-01": {DeviceName: "spine-01", Role: RoleSpine, DeviceID: "d-sp",
						LocalInterface: map[string]InterfaceLink{
							"swp1": {RemoteDevice: "super-01"},
							"swp2": {RemoteDevice: "leaf-01"},
						}},
					"leaf-01": {DeviceName: "leaf-01", Role: RoleLeaf, DeviceID: "d-lf",
						LocalInterface: map[string]InterfaceLink{
							"swp1": {RemoteDevice: "spine-01"},
							"swp2": {RemoteDevice: "node-x"},
						}},
					"node-x": {DeviceName: "node-x", Role: RoleServer, DeviceID: "d-x", LocalInterface: link("leaf-01")},
				},
			},
		}

		treeRoot := &topology.Vertex{Vertices: make(map[string]*topology.Vertex)}
		httpErr := parseResponse(treeRoot, resp, map[string]bool{"node-x": true})
		require.Nil(t, httpErr)
		require.Equal(t, map[string]*topology.Vertex{
			"d-ss": {ID: "d-ss", Name: "super-01", Vertices: map[string]*topology.Vertex{
				"d-sp": {ID: "d-sp", Name: "spine-01", Vertices: map[string]*topology.Vertex{
					"d-lf": {ID: "d-lf", Name: "leaf-01", Vertices: map[string]*topology.Vertex{
						"d-x": {ID: "d-x", Name: "node-x"},
					}},
				}},
			}},
		}, treeRoot.Vertices)
	})

	t.Run("unrecognized role is excluded and its server falls under no-topology", func(t *testing.T) {
		resp := &ExportTopologyResponse{
			Success: true,
			Topology: Topology{
				Layer2: map[string]Device{
					"custom-tor": {DeviceName: "custom-tor", Role: "custom-tor", DeviceID: "d-custom", LocalInterface: link("node-z")},
					"node-z":     {DeviceName: "node-z", Role: RoleServer, DeviceID: "d-z", LocalInterface: link("custom-tor")},
				},
			},
		}

		treeRoot := &topology.Vertex{Vertices: make(map[string]*topology.Vertex)}
		httpErr := parseResponse(treeRoot, resp, map[string]bool{"node-z": true})
		require.Nil(t, httpErr)
		require.Equal(t, map[string]*topology.Vertex{
			topology.NoTopology: {ID: topology.NoTopology, Vertices: map[string]*topology.Vertex{
				"d-z": {ID: "d-z", Name: "node-z"},
			}},
		}, treeRoot.Vertices)
	})
}

// TestGetNetworkTreeErrors exercises the error paths of getNetworkTree that
// TestGenerateTopologyConfig's happy-path fixture doesn't reach: an
// application-level failure reported via resp.Error, a malformed response
// body, and a response with no vertices for the requested nodes.
func TestGetNetworkTreeErrors(t *testing.T) {
	cis := []topology.ComputeInstances{
		{Instances: map[string]string{"i-01": "node-01"}},
	}

	// every handler that serves a response also checks the request it got; it
	// runs on the server's goroutine, so it reports with assert, not require
	assertExportRequest := func(t *testing.T, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/"+ExportTopologyURL, r.URL.Path)
		assert.Equal(t, "test-key", r.Header.Get("X-Api-Key"))
	}

	t.Run("unsuccessful response", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assertExportRequest(t, r)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"success": false, "error": "export failed"}`))
		}))
		defer srv.Close()

		p := &Provider{params: &ProviderParams{ApiURL: srv.URL}, creds: &Credentials{ApiKey: "test-key"}}
		_, httpErr := p.getNetworkTree(context.Background(), nil, cis)
		require.NotNil(t, httpErr)
		require.Equal(t, http.StatusBadGateway, httpErr.Code())
		require.Contains(t, httpErr.Error(), "export failed")
	})

	t.Run("unsuccessful response reports message when error is empty", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assertExportRequest(t, r)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"success": false, "message": "invalid limit"}`))
		}))
		defer srv.Close()

		p := &Provider{params: &ProviderParams{ApiURL: srv.URL}, creds: &Credentials{ApiKey: "test-key"}}
		_, httpErr := p.getNetworkTree(context.Background(), nil, cis)
		require.NotNil(t, httpErr)
		require.Contains(t, httpErr.Error(), "invalid limit")
	})

	t.Run("malformed JSON", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assertExportRequest(t, r)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`not json`))
		}))
		defer srv.Close()

		p := &Provider{params: &ProviderParams{ApiURL: srv.URL}, creds: &Credentials{ApiKey: "test-key"}}
		_, httpErr := p.getNetworkTree(context.Background(), nil, cis)
		require.NotNil(t, httpErr)
		require.Equal(t, http.StatusBadGateway, httpErr.Code())
		require.Contains(t, httpErr.Error(), "minerva output read failed")
	})

	t.Run("duplicate device id", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assertExportRequest(t, r)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"success": true, "topology": {"layer2": {
				"node-01": {"deviceName": "node-01", "role": "server", "device_id": "dup"},
				"node-02": {"deviceName": "node-02", "role": "server", "device_id": "dup"}}}}`))
		}))
		defer srv.Close()

		p := &Provider{params: &ProviderParams{ApiURL: srv.URL}, creds: &Credentials{ApiKey: "test-key"}}
		_, httpErr := p.getNetworkTree(context.Background(), nil, cis)
		require.NotNil(t, httpErr)
		require.Equal(t, http.StatusBadGateway, httpErr.Code())
		require.Contains(t, httpErr.Error(), "duplicate device id")
	})

	t.Run("untrusted certificate is rejected", func(t *testing.T) {
		var called bool
		srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			called = true
		}))
		defer srv.Close()

		p := &Provider{params: &ProviderParams{ApiURL: srv.URL}, creds: &Credentials{ApiKey: "test-key"}}
		_, httpErr := p.getNetworkTree(context.Background(), nil, cis)
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

				p := &Provider{params: &ProviderParams{ApiURL: srv.URL}, creds: &Credentials{ApiKey: "test-key"}}
				_, httpErr := p.getNetworkTree(context.Background(), nil, cis)
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

		p := &Provider{params: &ProviderParams{ApiURL: srv.URL}, creds: &Credentials{ApiKey: "test-key"}}
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
		fixture, err := os.ReadFile("../../../tests/output/minerva/export-topology.json")
		require.NoError(t, err)

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assertExportRequest(t, r)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(fixture)
		}))
		defer srv.Close()

		p := &Provider{params: &ProviderParams{ApiURL: srv.URL}, creds: &Credentials{ApiKey: "test-key"}}
		_, httpErr := p.getNetworkTree(context.Background(), nil, nil)
		require.NotNil(t, httpErr)
		require.Equal(t, http.StatusBadGateway, httpErr.Code())
		require.Contains(t, httpErr.Error(), "no topology available from Minerva")
	})

}

func TestDeviceID(t *testing.T) {
	require.Equal(t, "d-1", deviceID(Device{DeviceName: "node-01", DeviceID: "d-1"}))
	require.Equal(t, "node-01", deviceID(Device{DeviceName: "node-01"}))
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

// vertexNames returns how many times each vertex name appears in the tree.
func vertexNames(treeRoot *topology.Vertex) map[string]int {
	counts := make(map[string]int)
	var walk func(v *topology.Vertex)
	walk = func(v *topology.Vertex) {
		counts[v.Name]++
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
	data, err := os.ReadFile("../../../tests/output/minerva/export-topology.json")
	require.NoError(t, err)
	var resp ExportTopologyResponse
	require.NoError(t, json.Unmarshal(data, &resp))

	treeRoot := &topology.Vertex{Vertices: make(map[string]*topology.Vertex)}
	httpErr := parseResponse(treeRoot, &resp, map[string]bool{"srv-03": true, "srv-missing": true})
	require.Nil(t, httpErr)
	require.Contains(t, treeRoot.Vertices, "ss1")
	require.Equal(t, &topology.Vertex{
		ID: topology.NoTopology,
		Vertices: map[string]*topology.Vertex{
			"missing/srv-missing": {ID: "missing/srv-missing", Name: "srv-missing"},
		},
	}, treeRoot.Vertices[topology.NoTopology])

	// a requested name that equals the device ID of a reported server (srv-01
	// has device ID s1) is a different node: it must not shadow the server nor
	// be lost when the server is attached to its switch
	treeRoot = &topology.Vertex{Vertices: make(map[string]*topology.Vertex)}
	httpErr = parseResponse(treeRoot, &resp, map[string]bool{"srv-01": true, "s1": true})
	require.Nil(t, httpErr)
	names := vertexNames(treeRoot)
	require.Equal(t, 1, names["srv-01"])
	require.Equal(t, 1, names["s1"])
	require.Len(t, treeRoot.Vertices[topology.NoTopology].Vertices, 1)

	// a requested name that Minerva reports as a switch, not a server, is not
	// a node to place in the tree either
	treeRoot = &topology.Vertex{Vertices: make(map[string]*topology.Vertex)}
	httpErr = parseResponse(treeRoot, &resp, map[string]bool{"srv-03": true, "p1-leaf-01": true})
	require.Nil(t, httpErr)
	require.Contains(t, treeRoot.Vertices[topology.NoTopology].Vertices, "missing/p1-leaf-01")
	require.NotContains(t, vertexCounts(treeRoot), "p1-l1")

	// when none of the requested nodes are known, they all land under
	// no-topology rather than failing the request
	treeRoot = &topology.Vertex{Vertices: make(map[string]*topology.Vertex)}
	httpErr = parseResponse(treeRoot, &resp, map[string]bool{"srv-missing": true})
	require.Nil(t, httpErr)
	require.Len(t, treeRoot.Vertices, 1)
	require.Contains(t, treeRoot.Vertices, topology.NoTopology)
}

// TestParseResponseSharedSwitches covers a partially connected fabric where
// two spines share one leaf, so merging identical child sets can't collapse
// them. Every vertex must still appear exactly once in the output.
func TestParseResponseSharedSwitches(t *testing.T) {
	link := func(remotes ...string) map[string]InterfaceLink {
		m := make(map[string]InterfaceLink)
		for i, r := range remotes {
			m[fmt.Sprintf("swp%d", i)] = InterfaceLink{RemoteDevice: r}
		}
		return m
	}
	resp := &ExportTopologyResponse{
		Success: true,
		Topology: Topology{
			Layer2: map[string]Device{
				"spine-a": {DeviceName: "spine-a", Role: RoleSpine, DeviceID: "sa", LocalInterface: link("leaf-x", "leaf-y")},
				"spine-b": {DeviceName: "spine-b", Role: RoleSpine, DeviceID: "sb", LocalInterface: link("leaf-y", "leaf-z")},
				"leaf-x":  {DeviceName: "leaf-x", Role: RoleLeaf, DeviceID: "lx", LocalInterface: link("srv-x")},
				"leaf-y":  {DeviceName: "leaf-y", Role: RoleLeaf, DeviceID: "ly", LocalInterface: link("srv-y")},
				"leaf-z":  {DeviceName: "leaf-z", Role: RoleLeaf, DeviceID: "lz", LocalInterface: link("srv-z")},
				"srv-x":   {DeviceName: "srv-x", Role: RoleServer, DeviceID: "x", LocalInterface: link("leaf-x")},
				"srv-y":   {DeviceName: "srv-y", Role: RoleServer, DeviceID: "y", LocalInterface: link("leaf-y")},
				"srv-z":   {DeviceName: "srv-z", Role: RoleServer, DeviceID: "z", LocalInterface: link("leaf-z")},
			},
		},
	}

	treeRoot := &topology.Vertex{Vertices: make(map[string]*topology.Vertex)}
	httpErr := parseResponse(treeRoot, resp, map[string]bool{"srv-x": true, "srv-y": true, "srv-z": true})
	require.Nil(t, httpErr)

	seen := vertexCounts(treeRoot)
	for id, n := range seen {
		require.Equal(t, 1, n, "vertex %q appears %d times", id, n)
	}
	for _, id := range []string{"x", "y", "z"} {
		require.Equal(t, 1, seen[id], "server %q must be present", id)
	}
}

// TestParseResponseKeepsRequestedServers verifies that a requested server is
// never lost: topology.Merger can drop a deeper branch under a switch whose
// children sit at different depths, and such servers must fall back to
// no-topology rather than vanishing from the output.
func TestParseResponseKeepsRequestedServers(t *testing.T) {
	link := func(remotes ...string) map[string]InterfaceLink {
		m := make(map[string]InterfaceLink)
		for i, r := range remotes {
			m[fmt.Sprintf("swp%d", i)] = InterfaceLink{RemoteDevice: r}
		}
		return m
	}
	resp := &ExportTopologyResponse{
		Success: true,
		Topology: Topology{
			Layer2: map[string]Device{
				"spine-01": {DeviceName: "spine-01", Role: RoleSpine, DeviceID: "sp", LocalInterface: link("leaf-01", "srv-a")},
				"leaf-01":  {DeviceName: "leaf-01", Role: RoleLeaf, DeviceID: "lf", LocalInterface: link("srv-b")},
				"srv-a":    {DeviceName: "srv-a", Role: RoleServer, DeviceID: "a", LocalInterface: link("spine-01")},
				"srv-b":    {DeviceName: "srv-b", Role: RoleServer, DeviceID: "b", LocalInterface: link("leaf-01")},
			},
		},
	}

	treeRoot := &topology.Vertex{Vertices: make(map[string]*topology.Vertex)}
	httpErr := parseResponse(treeRoot, resp, map[string]bool{"srv-a": true, "srv-b": true})
	require.Nil(t, httpErr)
	counts := vertexCounts(treeRoot)
	require.Equal(t, 1, counts["a"])
	require.Equal(t, 1, counts["b"])
}

// TestParentLinksPrefersLowestTier verifies a server linked to both a leaf and
// a spine attaches to the leaf regardless of how the device names sort.
func TestParentLinksPrefersLowestTier(t *testing.T) {
	devices := map[string]Device{
		"a-spine": {DeviceName: "a-spine", Role: RoleSpine, DeviceID: "sp", LocalInterface: map[string]InterfaceLink{"swp1": {RemoteDevice: "srv"}}},
		"z-leaf":  {DeviceName: "z-leaf", Role: RoleLeaf, DeviceID: "lf", LocalInterface: map[string]InterfaceLink{"swp1": {RemoteDevice: "srv"}}},
		"srv":     {DeviceName: "srv", Role: RoleServer, DeviceID: "s"},
	}
	nameToID := map[string]string{"a-spine": "sp", "z-leaf": "lf", "srv": "s"}
	tiers := computeTiers(devices, nameToID)
	require.Equal(t, map[string]bool{"lf": true}, parentLinks(devices, nameToID, tiers)["s"])
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

// TestParseResponseMissingNodeIDsDoNotCollide verifies that the ID given to a
// requested node Minerva doesn't report never equals a reported device's ID,
// since both are keyed by ID under no-topology and in the switch tree.
func TestParseResponseMissingNodeIDsDoNotCollide(t *testing.T) {
	resp := &ExportTopologyResponse{
		Success: true,
		Topology: Topology{
			Layer2: map[string]Device{
				// a reported server whose ID is the one the fallback would pick first
				"node-a": {DeviceName: "node-a", Role: RoleServer, DeviceID: "missing/ghost"},
			},
		},
	}

	treeRoot := &topology.Vertex{Vertices: make(map[string]*topology.Vertex)}
	httpErr := parseResponse(treeRoot, resp, map[string]bool{"node-a": true, "ghost": true})
	require.Nil(t, httpErr)
	require.Equal(t, map[string]*topology.Vertex{
		"missing/ghost":  {ID: "missing/ghost", Name: "node-a"},
		"_missing/ghost": {ID: "_missing/ghost", Name: "ghost"},
	}, treeRoot.Vertices[topology.NoTopology].Vertices)
}
