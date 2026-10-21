/*
 * Copyright 2026 NVIDIA CORPORATION
 * SPDX-License-Identifier: Apache-2.0
 */

package nautobot

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/dsx-ai-factory/topograph/pkg/engines/slurm"
	"github.com/dsx-ai-factory/topograph/pkg/providers"
	"github.com/dsx-ai-factory/topograph/pkg/topology"
)

func loadProvider(t *testing.T, endpoint string) *Provider {
	t.Helper()
	prv, err := Loader(context.Background(), providers.Config{
		Params: map[string]any{"endpoint": endpoint, "deviceRole": []string{"GPU"}, "switchRoles": []string{"Switch"}}, Creds: map[string]any{"token": "fake-test-token"},
	})
	require.Nil(t, err)
	return prv.(*Provider)
}

func TestLoader(t *testing.T) {
	// An environment token must not satisfy missing or invalid credentials.
	t.Setenv("NAUTOBOT_TOKEN", "fake-env-token")
	const endpointError = "endpoint must be an absolute HTTP(S) URL without credentials, query, or fragment"
	const tokenError = "creds.token must be a non-empty string without line breaks"
	validCreds := map[string]any{"token": "fake-test-token"}
	for _, tc := range []struct {
		name      string
		params    map[string]any
		creds     map[string]any
		wantError string
	}{
		{"missing URL", map[string]any{"endpoint": nil}, validCreds, endpointError},
		{"relative URL", map[string]any{"endpoint": "/api/graphql/"}, validCreds, endpointError},
		{"wrong scheme", map[string]any{"endpoint": "ftp://example.com"}, validCreds, endpointError},
		{"URL credentials", map[string]any{"endpoint": "https://user:fake@example.com"}, validCreds, endpointError},
		{"URL query", map[string]any{"endpoint": "https://example.com?token=fake"}, validCreds, endpointError},
		{"URL fragment", map[string]any{"endpoint": "https://example.com#fragment"}, validCreds, endpointError},
		{"missing deviceRole", map[string]any{"deviceRole": nil}, validCreds, "deviceRole must select at least one compute role"},
		{"no deviceRole", map[string]any{"deviceRole": []string{}}, validCreds, "deviceRole must select at least one compute role"},
		{"negative trim", map[string]any{"trimTiers": -1}, validCreds, "trimTiers must be non-negative"},
		{"invalid params", map[string]any{"trimTiers": "invalid"}, validCreds, "invalid Nautobot parameters:"},
		{"missing token", nil, nil, tokenError},
		{"token in params", map[string]any{"token": "fake-param-token"}, nil, tokenError},
		{"null token", nil, map[string]any{"token": nil}, tokenError},
		{"invalid token", nil, map[string]any{"token": 42}, tokenError},
		{"empty token", nil, map[string]any{"token": ""}, tokenError},
		{"blank token", nil, map[string]any{"token": " "}, tokenError},
		{"header injection", nil, map[string]any{"token": "fake\nvalue"}, tokenError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			params := map[string]any{
				"endpoint": "https://example.com", "deviceRole": []string{"GPU"}, "switchRoles": []string{"Switch"},
			}
			// Override only the input under test; nil removes a required parameter.
			for key, value := range tc.params {
				if value == nil {
					delete(params, key)
				} else {
					params[key] = value
				}
			}
			prv, err := Loader(context.Background(), providers.Config{Params: params, Creds: tc.creds})
			require.Nil(t, prv)
			require.NotNil(t, err)
			require.Equal(t, http.StatusBadRequest, err.Code())
			require.Contains(t, err.Error(), tc.wantError)
		})
	}
	for _, endpoint := range []string{"https://example.com", "https://example.com/api/graphql", "https://example.com/api/graphql/"} {
		prv, err := Loader(context.Background(), providers.Config{
			Params: map[string]any{"endpoint": endpoint, "deviceRole": []string{"GPU"}, "switchRoles": []string{"Switch"}},
			Creds:  map[string]any{"token": "fake-test-token"},
		})
		require.Nil(t, err)
		require.Equal(t, "https://example.com/api/graphql/", prv.(*Provider).params.Endpoint)
		require.Equal(t, ProviderCreds{Token: "fake-test-token"}, prv.(*Provider).creds)
	}
	name, loader := NamedLoader()
	require.Equal(t, "nautobot", name)
	require.NotNil(t, loader)
}

func TestExcludeStatusesConfiguration(t *testing.T) {
	for _, tc := range []struct {
		name   string
		params map[string]any
		want   []string
	}{
		{"default", nil, []string{"Deleted"}},
		{"empty", map[string]any{"excludeStatuses": []string{}}, []string{}},
		{"custom", map[string]any{"excludeStatuses": []string{"Planned"}}, []string{"Planned"}},
		{"null", map[string]any{"excludeStatuses": nil}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			params := map[string]any{
				"endpoint": "https://example.invalid", "deviceRole": []string{"GPU"}, "switchRoles": []string{"Switch"},
			}
			for key, value := range tc.params {
				params[key] = value
			}
			prv, err := Loader(context.Background(), providers.Config{Params: params, Creds: map[string]any{"token": "fake-test-token"}})
			require.Nil(t, err)
			p := prv.(*Provider)
			require.Equal(t, tc.want, p.params.ExcludeStatuses)
			require.Equal(t, len(tc.want) != 0, strings.Contains(p.deviceQuery(nil, 100, 0), "status__n:"))
			if len(tc.want) != 0 {
				require.Equal(t, tc.want, queryFilter(t, p.deviceQuery(nil, 100, 0), "status__n"))
			}
			for _, status := range []string{"Deleted", "Planned", "Active"} {
				require.Equal(t, slices.Contains(tc.want, status), p.excluded(device{Status: &namedObject{Name: status}}))
			}
		})
	}
}

const fabricJSON = `[
 {
  "id": "leaf-a",
  "name": "leaf-a",
  "interfaces": [
   {
    "cable_peer_interface": {
     "device": {
      "id": "host-a",
      "name": "node-a",
      "serial": "serial-a",
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
      "status": {
       "name": "Deleted"
      },
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
   },
   {
    "cable_peer_interface": null
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
      "id": "host-c",
      "name": "node-c",
      "role": {
       "name": "GPU"
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
  "id": "spine-a",
  "name": "spine-a",
  "interfaces": [
   {
    "cable_peer_interface": {
     "device": {
      "id": "core",
      "name": "core",
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
  "id": "spine-b",
  "name": "spine-b",
  "role": {
   "name": "Switch"
  }
 },
 {
  "id": "core",
  "name": "core",
  "role": {
   "name": "Switch"
  }
 },
 {
  "id": "isolated",
  "name": "isolated",
  "role": {
   "name": "Switch"
  }
 },
 {
  "id": "host-a",
  "name": "node-a",
  "serial": "serial-a",
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
   }
  ]
 },
 {
  "id": "host-b",
  "name": "node-b",
  "status": {
   "name": "Deleted"
  },
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
   }
  ]
 },
 {
  "id": "host-c",
  "name": "node-c",
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
   }
  ]
 }
]`

func requestedNodes(ids ...string) []topology.ComputeInstances {
	instances := map[string]string{}
	for _, id := range ids {
		instances[id] = "scheduler-" + id
	}
	return []topology.ComputeInstances{{Region: "local", Instances: instances}}
}

func TestGenerateTopologyConfig(t *testing.T) {
	server := fabricServer(t, decodeDevices(t, fabricJSON))
	defer server.Close()
	p := loadProvider(t, server.URL)
	p.params.Location = []string{"lab"}
	graph, err := p.GenerateTopologyConfig(context.Background(), nil, requestedNodes("node-a", "host-c", "missing", "node-b"))
	require.Nil(t, err)
	require.Nil(t, graph.Domains)
	require.Equal(t, []string{"leaf-a", "spine-a", "core"}, graph.Instances["node-a"].NetworkLayers)
	require.Equal(t, []string{"leaf-b", "spine-a", "core"}, graph.Instances["host-c"].NetworkLayers)
	require.Len(t, graph.Instances, 2)
	require.Contains(t, graph.Tiers.Vertices, "core")
	require.Len(t, graph.Tiers.Vertices[topology.NoTopology].Vertices, 2)
	output, outputErr := slurm.GenerateOutputParams(context.Background(), graph, &slurm.Params{
		BaseParams: slurm.BaseParams{Plugin: topology.TopologyTree},
	})
	require.Nil(t, outputErr)
	require.Contains(t, string(output), "SwitchName=core Switches=spine-a")
	require.Contains(t, string(output), "SwitchName=spine-a Switches=leaf-a,leaf-b")
	require.Contains(t, string(output), "SwitchName=leaf-a Nodes=scheduler-node-a")
	require.Contains(t, string(output), "SwitchName=leaf-b Nodes=scheduler-host-c")
	p.params.TrimTiers = 1
	graph, err = p.GenerateTopologyConfig(context.Background(), nil, requestedNodes("serial-a"))
	require.Nil(t, err)
	require.Equal(t, []string{"leaf-a", "spine-a"}, graph.Instances["serial-a"].NetworkLayers)
}

func TestGraphQLErrors(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{"unauthorized", 401, "sensitive response"},
		{"upstream unavailable", 503, ""},
		{"redirect", 302, ""},
		{"bad JSON", 200, "{"},
		{"GraphQL error", 200, `{"errors":[{"message":"sensitive response"}],"data":{"devices":[]}}`},
		{"null data", 200, `{"data":null}`},
		{"missing devices", 200, `{"data":{}}`},
		{"null devices", 200, `{"data":{"devices":null}}`},
		{"wrong shape", 200, `{"data":{"devices":{}}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				w.Header().Set("Location", "/must-not-follow")
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			p := loadProvider(t, server.URL)
			graph, err := p.GenerateTopologyConfig(context.Background(), nil, nil)
			require.Nil(t, graph)
			require.NotNil(t, err)
			require.Equal(t, http.StatusBadGateway, err.Code())
			require.EqualValues(t, 1, requests.Load())
			require.NotContains(t, err.Error(), "sensitive response")
		})
	}
}

func TestCanceledRequest(t *testing.T) {
	p := loadProvider(t, "https://example.invalid")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := p.GenerateTopologyConfig(ctx, nil, nil)
	require.NotNil(t, err)
	require.Equal(t, http.StatusBadGateway, err.Code())
	require.Contains(t, err.Error(), "Nautobot GraphQL request failed:")
	require.Contains(t, err.Error(), context.Canceled.Error())
}

func TestQueryEscapesFilters(t *testing.T) {
	p := loadProvider(t, "https://example.invalid")
	p.params.Location = []string{"lab\" ) { secrets } #\n"}
	query := p.deviceQuery(nil, defaultPageSize, 0)
	require.Contains(t, query, `location: ["lab\" ) { secrets } #\n"]`)
	require.False(t, strings.Contains(query, "#\n"))
}

func TestInstanceMapping(t *testing.T) {
	p := loadProvider(t, "https://example.invalid")
	mapping, err := p.Instances2NodeMap(context.Background(), []string{"node-a"})
	require.NoError(t, err)
	require.Equal(t, map[string]string{"node-a": "node-a"}, mapping)
	regions, err := p.GetInstancesRegions(context.Background(), []string{"node-a"})
	require.NoError(t, err)
	require.Equal(t, map[string]string{"node-a": "local"}, regions)
}
