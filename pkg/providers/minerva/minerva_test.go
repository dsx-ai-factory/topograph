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

	"github.com/stretchr/testify/require"

	"github.com/dsx-ai-factory/topograph/pkg/topology"
)

func TestParseResponse(t *testing.T) {
	data, err := os.ReadFile("../../../tests/output/minerva/export-topology.json")
	require.NoError(t, err)

	var resp ExportTopologyResponse
	require.NoError(t, json.Unmarshal(data, &resp))

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

	t.Run("unsuccessful response", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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

	t.Run("malformed JSON", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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

	t.Run("no vertices for requested nodes", func(t *testing.T) {
		fixture, err := os.ReadFile("../../../tests/output/minerva/export-topology.json")
		require.NoError(t, err)

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(fixture)
		}))
		defer srv.Close()

		p := &Provider{params: &ProviderParams{ApiURL: srv.URL}, creds: &Credentials{ApiKey: "test-key"}}
		_, httpErr := p.getNetworkTree(context.Background(), nil, []topology.ComputeInstances{
			{Instances: map[string]string{"i-99": "node-99"}},
		})
		require.NotNil(t, httpErr)
		require.Equal(t, http.StatusBadGateway, httpErr.Code())
		require.Contains(t, httpErr.Error(), "no topology available from Minerva")
	})
}

func TestDeviceID(t *testing.T) {
	require.Equal(t, "d-1", deviceID(Device{DeviceName: "node-01", DeviceID: "d-1"}))
	require.Equal(t, "node-01", deviceID(Device{DeviceName: "node-01"}))
}

// TestParseLiveShapeResponse covers the quirks of a real export-topology
// payload that the minimal fixture does not: numeric GPU componentId,
// nvlink-switch devices with a null device_id, empty interface objects, an
// unrecognized-role switch, extra top-level fields, and servers dual-homed to
// a leaf in each of two planes.
func TestParseLiveShapeResponse(t *testing.T) {
	data, err := os.ReadFile("../../../tests/output/minerva/export-topology.json")
	require.NoError(t, err)

	var resp ExportTopologyResponse
	require.NoError(t, json.Unmarshal(data, &resp))
	require.True(t, resp.Success)
	require.Len(t, resp.Topology.Layer2, 13)
	require.Len(t, resp.Topology.Layer2["srv-01"].GPUs, 4)
	require.Equal(t, 0, resp.Topology.Layer2["srv-01"].GPUs[0].ComponentID)

	treeRoot := &topology.Vertex{Vertices: make(map[string]*topology.Vertex)}
	httpErr := parseResponse(treeRoot, &resp, map[string]bool{"srv-01": true, "srv-02": true})
	require.Nil(t, httpErr)

	// every requested server is placed under a switch, none are dropped or
	// parked under the no-topology switch
	require.NotContains(t, treeRoot.Vertices, topology.NoTopology)
	servers := map[string]bool{}
	names := map[string]bool{}
	var walk func(v *topology.Vertex)
	walk = func(v *topology.Vertex) {
		names[v.Name] = true
		if len(v.Vertices) == 0 {
			servers[v.Name] = true
		}
		for _, c := range v.Vertices {
			walk(c)
		}
	}
	for _, v := range treeRoot.Vertices {
		walk(v)
	}
	require.Equal(t, map[string]bool{"srv-01": true, "srv-02": true}, servers)

	// the nvlink-switch (null device_id) and the unknown-role switch take no
	// part in the switch tree
	require.NotContains(t, names, "nvl-sw-01")
	require.NotContains(t, names, "p1-leaf-09")
	require.NotContains(t, names, "p2-leaf-09")

	// a dual-homed server has exactly one parent, so the result is a tree
	seen := map[string]int{}
	var count func(v *topology.Vertex)
	count = func(v *topology.Vertex) {
		seen[v.ID]++
		for _, c := range v.Vertices {
			count(c)
		}
	}
	for _, v := range treeRoot.Vertices {
		count(v)
	}
	require.Equal(t, 1, seen["s1"])
	require.Equal(t, 1, seen["s2"])

	// the super-spine is the single root above both planes
	require.Len(t, treeRoot.Vertices, 1)
	require.Contains(t, treeRoot.Vertices, "ss1")
}

func TestGPUComponentIDNumeric(t *testing.T) {
	var g GPU
	require.NoError(t, json.Unmarshal([]byte(`{"name":"gpu","componentId":3}`), &g))
	require.Equal(t, 3, g.ComponentID)

	require.Error(t, json.Unmarshal([]byte(`{"name":"gpu","componentId":"c-1"}`), &g))
}
