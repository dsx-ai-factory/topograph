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

package nautobot

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/stretchr/testify/require"
)

func decodeDevices(t *testing.T, payload string) []device {
	t.Helper()
	var devices []device
	require.NoError(t, json.Unmarshal([]byte(payload), &devices))
	return devices
}

func TestHierarchyStableAcrossOrderingAndRequests(t *testing.T) {
	p := loadProvider(t, "https://example.invalid")
	devices := decodeDevices(t, fabricJSON)
	requests := requestedNodes("node-a", "node-c")
	cluster, err := p.buildTopology(devices, requests)
	require.NoError(t, err)
	expected := cluster.ToGraph(NAME, requests, 0, false)
	for range 10 {
		slices.Reverse(devices)
		for i := range devices {
			slices.Reverse(devices[i].Interfaces)
		}
		cluster, err = p.buildTopology(devices, requests)
		require.NoError(t, err)
		require.Equal(t, expected, cluster.ToGraph(NAME, requests, 0, false))
	}
	cluster, err = p.buildTopology(devices, requestedNodes("node-a"))
	require.NoError(t, err)
	require.Equal(t, expected.Instances["node-a"].NetworkLayers, cluster.ToGraph(NAME, requestedNodes("node-a"), 0, false).Instances["node-a"].NetworkLayers)
}

func TestSharedChildrenAndLateralLinks(t *testing.T) {
	p := loadProvider(t, "https://example.invalid")
	devices := decodeDevices(t, `[
 {
  "id": "leaf-a",
  "name": "leaf-a",
  "interfaces": [
   {
    "cable_peer_interface": {
     "device": {
      "id": "host-a",
      "name": "node-a",
      "role": {
       "name": "GPU"
      }
     }
    }
   },
   {
    "cable_peer_interface": {
     "device": {
      "id": "leaf-c",
      "name": "leaf-c",
      "role": {
       "name": "Switch"
      }
     }
    }
   },
   {
    "cable_peer_interface": {
     "device": {
      "id": "spine-a",
      "name": "spine-a",
      "role": {
       "name": "Switch"
      }
     }
    }
   }
  ],
  "role": {
   "name": "Switch"
  }
 },
 {
  "id": "leaf-b",
  "name": "leaf-b",
  "interfaces": [
   {
    "cable_peer_interface": {
     "device": {
      "id": "host-a",
      "name": "node-a",
      "role": {
       "name": "GPU"
      }
     }
    }
   },
   {
    "cable_peer_interface": {
     "device": {
      "id": "host-b",
      "name": "node-b",
      "role": {
       "name": "GPU"
      }
     }
    }
   },
   {
    "cable_peer_interface": {
     "device": {
      "id": "spine-b",
      "name": "spine-b",
      "role": {
       "name": "Switch"
      }
     }
    }
   }
  ],
  "role": {
   "name": "Switch"
  }
 },
 {
  "id": "leaf-c",
  "name": "leaf-c",
  "interfaces": [
   {
    "cable_peer_interface": {
     "device": {
      "id": "host-b",
      "name": "node-b",
      "role": {
       "name": "GPU"
      }
     }
    }
   }
  ],
  "role": {
   "name": "Switch"
  }
 },
 {
  "id": "spine-a",
  "name": "spine-a",
  "role": {
   "name": "Switch"
  }
 },
 {
  "id": "spine-b",
  "name": "spine-b",
  "role": {
   "name": "Switch"
  }
 },
 {
  "id": "host-a",
  "name": "node-a",
  "role": {
   "name": "GPU"
  },
  "interfaces": [
   {
    "cable_peer_interface": {
     "device": {
      "id": "leaf-a",
      "name": "leaf-a",
      "role": {
       "name": "Switch"
      }
     }
    }
   },
   {
    "cable_peer_interface": {
     "device": {
      "id": "leaf-b",
      "name": "leaf-b",
      "role": {
       "name": "Switch"
      }
     }
    }
   }
  ]
 },
 {
  "id": "host-b",
  "name": "node-b",
  "role": {
   "name": "GPU"
  },
  "interfaces": [
   {
    "cable_peer_interface": {
     "device": {
      "id": "leaf-b",
      "name": "leaf-b",
      "role": {
       "name": "Switch"
      }
     }
    }
   },
   {
    "cable_peer_interface": {
     "device": {
      "id": "leaf-c",
      "name": "leaf-c",
      "role": {
       "name": "Switch"
      }
     }
    }
   }
  ]
 }
]`)
	requests := requestedNodes("node-a", "node-b")
	cluster, err := p.buildTopology(devices, requests)
	require.NoError(t, err)
	graph := cluster.ToGraph(NAME, requests, 0, false)
	for _, instance := range graph.Instances {
		require.Equal(t, []string{"leaf-a", "spine-a"}, instance.NetworkLayers)
	}
	require.Len(t, graph.Tiers.Vertices, 1)
	require.Len(t, graph.Tiers.Vertices["spine-a"].Vertices["leaf-a"].Vertices, 2)
}

func TestInventorySwitchNames(t *testing.T) {
	for _, tc := range []struct {
		name      string
		other     device
		wantError bool
	}{
		{"duplicate name", device{ID: "switch-b", Name: "leaf-01", Role: &namedObject{Name: "Switch"}}, true},
		{"unique name", device{ID: "switch-b", Name: "leaf-02", Role: &namedObject{Name: "Switch"}}, false},
		{"excluded duplicate", device{ID: "switch-b", Name: "leaf-01", Role: &namedObject{Name: "Switch"}, Status: &namedObject{Name: "Deleted"}}, false},
		{"compute name", device{ID: "host", Name: "leaf-01", Role: &namedObject{Name: "GPU"}}, false},
		{"unconfigured role", device{ID: "router", Name: "leaf-01", Role: &namedObject{Name: "Router"}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := loadProvider(t, "https://example.invalid")
			devices := []device{
				{ID: "switch-a", Name: "leaf-01", Role: &namedObject{Name: "Switch"}},
				tc.other,
			}
			for range 2 {
				inv, err := p.inventory(devices)
				if tc.wantError {
					require.Nil(t, inv)
					require.ErrorContains(t, err, `duplicate switch name "leaf-01"`)
					require.Contains(t, err.Error(), `"switch-a"`)
					require.Contains(t, err.Error(), `"switch-b"`)
				} else {
					require.NoError(t, err)
					require.Equal(t, "leaf-01", inv.switches["switch-a"].Name)
				}
				slices.Reverse(devices)
			}
		})
	}
}

func TestInventoryEdgeCases(t *testing.T) {
	for _, tc := range []struct {
		name, payload string
		matched       bool
		wantError     string
	}{
		{"empty", `[]`, false, ""},
		{"missing identity", `[{"name":"leaf","role":{"name":"Switch"}}]`, false, "has no ID or name"},
		{"duplicate switch ID", `[{"id":"s","name":"leaf-a","role":{"name":"Switch"}},{"id":"s","name":"leaf-b","role":{"name":"Switch"}}]`, false, `duplicate device ID "s"`},
		{"deleted switch", `[{"id":"s","name":"leaf","role":{"name":"Switch"},"status":{"name":"Deleted"}}]`, false, ""},
		{"unknown peer", `[{"id":"s","name":"leaf","role":{"name":"Switch"},"interfaces":[{"cable_peer_interface":{"device":{"id":"h","name":"node"}}}]}]`, false, ""},
		{"router peer", `[{"id":"s","name":"leaf","role":{"name":"Switch"},"interfaces":[{"cable_peer_interface":{"device":{"id":"h","name":"node","role":{"name":"Router"}}}}]},{"id":"h","name":"node","role":{"name":"Router"}}]`, false, ""},
		{"dangling and self links", `[{"id":"s","name":"leaf","role":{"name":"Switch"},"interfaces":[{}, {"cable_peer_interface":{}}, {"cable_peer_interface":{"device":{"id":"s","name":"leaf"}}}]}]`, false, ""},
		{"ambiguous alias", `[{"id":"s","name":"leaf","role":{"name":"Switch"},"interfaces":[{"cable_peer_interface":{"device":{"id":"h1","name":"node"}}},{"cable_peer_interface":{"device":{"id":"h2","name":"node"}}}]},{"id":"h1","name":"node","role":{"name":"GPU"}},{"id":"h2","name":"node","role":{"name":"GPU"}}]`, false, ""},
		{"single tier", `[{"id":"s","name":"leaf","role":{"name":"Switch"},"interfaces":[{"cable_peer_interface":{"device":{"id":"h","name":"node"}}}]},{"id":"h","name":"node","role":{"name":"GPU"}}]`, true, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := loadProvider(t, "https://example.invalid")
			cluster, err := p.buildTopology(decodeDevices(t, tc.payload), requestedNodes("node"))
			if tc.wantError != "" {
				require.ErrorContains(t, err, tc.wantError)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.matched, cluster.Len() == 1)
		})
	}
}
