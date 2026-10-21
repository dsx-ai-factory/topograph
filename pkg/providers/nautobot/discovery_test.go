/*
 * Copyright 2026 NVIDIA CORPORATION
 * SPDX-License-Identifier: Apache-2.0
 */

package nautobot

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/dsx-ai-factory/topograph/pkg/providers"
)

func queryFilter(t *testing.T, query, key string) []string {
	t.Helper()
	match := regexp.MustCompile(key + `: (\[[^\]]*\])`).FindStringSubmatch(query)
	if match == nil {
		return nil
	}
	var values []string
	require.NoError(t, json.Unmarshal([]byte(match[1]), &values))
	return values
}

func queryNumber(t *testing.T, query, key string) int {
	t.Helper()
	match := regexp.MustCompile(key + `: (\d+)`).FindStringSubmatch(query)
	require.Len(t, match, 2)
	value, err := strconv.Atoi(match[1])
	require.NoError(t, err)
	return value
}

func devicePageForQuery(t *testing.T, query string, devices []device) []device {
	t.Helper()
	limit, offset := queryNumber(t, query, "limit"), queryNumber(t, query, "offset")
	require.Positive(t, limit)
	start := min(offset, len(devices))
	return devices[start:min(start+limit, len(devices))]
}

func fabricServer(t *testing.T, inventory []device, observe ...func([]string)) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		require.Equal(t, "/api/graphql/", r.URL.Path)
		require.Equal(t, "Token fake-test-token", r.Header.Get("Authorization"))
		require.Equal(t, "application/json", r.Header.Get("Content-Type"))
		var payload map[string]string
		require.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
		query := payload["query"]
		require.Equal(t, 2, strings.Count(query, "role { name }"))
		require.NotContains(t, query, "platform")
		ids := queryFilter(t, query, "id")
		roles := queryFilter(t, query, "role")
		if ids == nil {
			require.Equal(t, []string{"Switch"}, roles)
		} else {
			require.Equal(t, []string{"Switch"}, roles)
			require.NotContains(t, query, "location:")
		}
		for _, f := range observe {
			f(ids)
		}
		result := []device{}
		for _, d := range inventory {
			if hasRole(d, roles) && (ids == nil || slices.Contains(ids, d.ID)) &&
				(d.Status == nil || !slices.Contains(queryFilter(t, query, "status__n"), d.Status.Name)) {
				result = append(result, d)
			}
		}
		require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"devices": devicePageForQuery(t, query, result)}}))
	}))
}

func TestDiscoverConnectedSwitches(t *testing.T) {
	var mu sync.Mutex
	calls, fetched := 0, map[string]int{}
	server := fabricServer(t, decodeDevices(t, fabricJSON), func(ids []string) {
		mu.Lock()
		defer mu.Unlock()
		calls++
		require.LessOrEqual(t, len(ids), 2)
		for _, id := range ids {
			fetched[id]++
		}
	})
	defer server.Close()
	p := loadProvider(t, server.URL)
	pageSize := 2
	devices, err := p.discover(context.Background(), &pageSize)
	require.Nil(t, err)
	ids := []string{}
	for _, d := range devices {
		ids = append(ids, d.ID)
	}
	require.ElementsMatch(t, []string{"host-a", "host-c", "leaf-a", "leaf-b", "spine-a", "spine-b", "core", "isolated"}, ids)
	mu.Lock()
	defer mu.Unlock()
	require.Equal(t, 4, calls, "six switches require three pages and a final empty page, without switch expansion")
	require.Empty(t, fetched)
}

func TestPaginationPartialFinalPage(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	server := fabricServer(t, decodeDevices(t, fabricJSON), func(ids []string) {
		mu.Lock()
		defer mu.Unlock()
		calls++
		require.Nil(t, ids, "collect every initial page before expanding switch peers")
	})
	defer server.Close()
	pageSize := 4
	graph, err := loadProvider(t, server.URL).GenerateTopologyConfig(context.Background(), &pageSize, requestedNodes("node-a", "node-c"))
	require.Nil(t, err)
	require.Equal(t, []string{"leaf-a", "spine-a", "core"}, graph.Instances["node-a"].NetworkLayers)
	require.Equal(t, []string{"leaf-b", "spine-a", "core"}, graph.Instances["node-c"].NetworkLayers)
	mu.Lock()
	defer mu.Unlock()
	require.Equal(t, 2, calls)
}

func TestPaginationDefaultPageSize(t *testing.T) {
	zero, negative := 0, -1
	for _, pageSize := range []*int{nil, &zero, &negative} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var payload map[string]string
			require.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
			require.Equal(t, 100, queryNumber(t, payload["query"], "limit"))
			require.Zero(t, queryNumber(t, payload["query"], "offset"))
			_, _ = w.Write([]byte(`{"data":{"devices":[]}}`))
		}))
		p := loadProvider(t, server.URL)
		devices, err := p.discover(context.Background(), pageSize)
		server.Close()
		require.Nil(t, err)
		require.Empty(t, devices)
	}
}

func TestPaginationRejectsIncompleteInventory(t *testing.T) {
	firstPage := `{"data":{"devices":[{"id":"a","name":"a","role":{"name":"Switch"}},{"id":"b","name":"b","role":{"name":"Switch"}}]}}`
	for _, tc := range []struct {
		name        string
		body        string
		duplicateID string
	}{
		{"ignored offset", firstPage, "a"},
		{"overlapping pages", `{"data":{"devices":[{"id":"b","name":"b","role":{"name":"Switch"}}]}}`, "b"},
		{"duplicate within page", `{"data":{"devices":[{"id":"c","name":"c","role":{"name":"Switch"}},{"id":"c","name":"c","role":{"name":"Switch"}}]}}`, "c"},
		{"GraphQL error on later page", `{"errors":[{"message":"failure"}],"data":{"devices":[]}}`, ""},
		{"malformed later page", `{`, ""},
		{"null later page", `{"data":{"devices":null}}`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var payload map[string]string
				require.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
				query := payload["query"]
				require.Equal(t, 2, queryNumber(t, query, "limit"))
				offset := queryNumber(t, query, "offset")
				if offset == 0 {
					_, _ = w.Write([]byte(firstPage))
					return
				}
				require.Equal(t, 2, offset)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			pageSize := 2
			graph, err := loadProvider(t, server.URL).GenerateTopologyConfig(context.Background(), &pageSize, nil)
			require.Nil(t, graph)
			require.NotNil(t, err)
			require.Equal(t, http.StatusBadGateway, err.Code())
			if tc.duplicateID != "" {
				require.Contains(t, err.Error(), `duplicate device ID "`+tc.duplicateID+`"`)
			}
		})
	}
}

func TestPaginationSkipsInvalidIdentities(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]string
		require.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
		query := payload["query"]
		require.Equal(t, 2, queryNumber(t, query, "limit"))
		switch offset := queryNumber(t, query, "offset"); offset {
		case 0:
			_, _ = w.Write([]byte(`{"data":{"devices":[{"name":"no-id"},{"id":"a"}]}}`))
		case 2:
			_, _ = w.Write([]byte(`{"data":{"devices":[{"id":"a","name":"a","role":{"name":"Switch"}},{"id":"b","name":"b","role":{"name":"Switch"}}]}}`))
		case 4:
			_, _ = w.Write([]byte(`{"data":{"devices":[{"id":"c","name":"c","role":{"name":"Switch"}}]}}`))
		default:
			t.Errorf("unexpected offset %d", offset)
			_, _ = w.Write([]byte(`{"data":{"devices":[]}}`))
		}
	}))
	defer server.Close()
	pageSize := 2
	devices, err := loadProvider(t, server.URL).discover(context.Background(), &pageSize)
	require.Nil(t, err)
	ids := make([]string, 0, len(devices))
	for _, d := range devices {
		ids = append(ids, d.ID)
	}
	require.Equal(t, []string{"a", "b", "c"}, ids)
}

func TestPaginationContinuesAfterOversizedPage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]string
		require.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
		query := payload["query"]
		require.Equal(t, 2, queryNumber(t, query, "limit"))
		offset := queryNumber(t, query, "offset")
		if offset == 0 {
			_, _ = w.Write([]byte(`{"data":{"devices":[
 {"id":"a","name":"a","role":{"name":"Switch"}},
 {"id":"b","name":"b","role":{"name":"Switch"}},
 {"id":"c","name":"c","role":{"name":"Switch"}}
]}}`))
			return
		}
		require.Equal(t, 3, offset, "skip every device already received, including the oversized part")
		_, _ = w.Write([]byte(`{"data":{"devices":[{"id":"d","name":"d","role":{"name":"Switch"}}]}}`))
	}))
	defer server.Close()
	pageSize := 2
	devices, err := loadProvider(t, server.URL).discover(context.Background(), &pageSize)
	require.Nil(t, err)
	ids := make([]string, 0, len(devices))
	for _, d := range devices {
		ids = append(ids, d.ID)
	}
	require.Equal(t, []string{"a", "b", "c", "d"}, ids)
}

func TestDiscoveryClassificationAndCycles(t *testing.T) {
	inventory := decodeDevices(t, `[
 {"id":"host","name":"node","role":{"name":"GPU"},"interfaces":[
  {"cable_peer_interface":{"device":{"id":"leaf","name":"leaf","role":{"name":"Switch"}}}},
  {"cable_peer_interface":{"device":{"id":"unknown","name":"unknown"}}},
  {"cable_peer_interface":{"device":{"id":"deleted","name":"deleted","role":{"name":"Switch"},"status":{"name":"Deleted"}}}}
 ]},
 {"id":"leaf","name":"leaf","role":{"name":"Switch"},"interfaces":[
  {"cable_peer_interface":{"device":{"id":"host","name":"node","role":{"name":"GPU"}}}},
  {"cable_peer_interface":{"device":{"id":"spine","name":"spine","role":{"name":"Switch"}}}},
  {"cable_peer_interface":{"device":{"id":"router","name":"router","role":{"name":"Router"}}}},
  {"cable_peer_interface":{"device":{"id":"other-compute","name":"other-compute","role":{"name":"CPU"}}}}
 ]},
 {"id":"spine","name":"spine","role":{"name":"Switch"},"interfaces":[
  {"cable_peer_interface":{"device":{"id":"leaf","name":"leaf","role":{"name":"Switch"}}}}
 ]},
 {"id":"other-compute","name":"other-compute","role":{"name":"CPU"}},
 {"id":"router","name":"router","role":{"name":"Router"}},
 {"id":"unknown","name":"unknown"}
]`)
	server := fabricServer(t, inventory)
	defer server.Close()
	p := loadProvider(t, server.URL)
	graph, err := p.GenerateTopologyConfig(context.Background(), nil, requestedNodes("node", "other-compute", "router", "unknown"))
	require.Nil(t, err)
	require.Len(t, graph.Instances, 1)
	require.Equal(t, []string{"leaf", "spine"}, graph.Instances["node"].NetworkLayers)
}

func TestDiscoveryExpansionFailures(t *testing.T) {
	seed := `{"id":"seed","name":"seed","role":{"name":"Switch"},"interfaces":[{"cable_peer_interface":{"device":{"id":"leaf","name":"leaf","role":{"name":"Switch"}}}}]}`
	for _, tc := range []struct {
		name, response, wantError string
	}{
		{"missing switch", `{"data":{"devices":[]}}`, "Nautobot omitted connected switches"},
		{"GraphQL error", `{"errors":[{"message":"failure"}]}`, "Nautobot GraphQL returned errors"},
		{"unexpected switch", `{"data":{"devices":[{"id":"other","name":"other","role":{"name":"Switch"}}]}}`, "Nautobot returned an unexpected, duplicate, or invalid switch"},
		{"wrong role", `{"data":{"devices":[{"id":"leaf","name":"leaf","role":{"name":"Router"}}]}}`, "Nautobot returned an unexpected, duplicate, or invalid switch"},
		{"duplicate switch", `{"data":{"devices":[{"id":"leaf","name":"leaf","role":{"name":"Switch"}},{"id":"leaf","name":"leaf","role":{"name":"Switch"}}]}}`, `Nautobot returned duplicate device ID "leaf" during pagination`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var payload map[string]string
				require.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
				if queryFilter(t, payload["query"], "id") == nil {
					_, _ = w.Write([]byte(`{"data":{"devices":[` + seed + `]}}`))
				} else {
					_, _ = w.Write([]byte(tc.response))
				}
			}))
			defer server.Close()
			_, err := loadProvider(t, server.URL).discover(context.Background(), nil)
			require.NotNil(t, err)
			require.Equal(t, http.StatusBadGateway, err.Code())
			require.Contains(t, err.Error(), tc.wantError)
		})
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type cancelOnCloseBody struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (b *cancelOnCloseBody) Close() error {
	err := b.ReadCloser.Close()
	b.cancel()
	return err
}

func TestDiscoveryCanceledBetweenBatches(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p := loadProvider(t, "https://example.invalid")
	requests := 0
	p.client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		requests++
		body := `{"data":{"devices":[{"id":"seed","name":"seed","role":{"name":"Switch"},"interfaces":[{"cable_peer_interface":{"device":{"id":"leaf","name":"leaf","role":{"name":"Switch"}}}}]}]}}`
		return &http.Response{
			StatusCode: http.StatusOK,
			Request:    r,
			// devicePage closes the body after decoding the seed successfully.
			// Cancel here so the next expansion sees cancellation without racing HTTP reads.
			Body: &cancelOnCloseBody{ReadCloser: io.NopCloser(strings.NewReader(body)), cancel: cancel},
		}, nil
	})
	devices, err := p.discover(ctx, nil)
	require.Nil(t, devices)
	require.NotNil(t, err)
	require.Equal(t, http.StatusBadGateway, err.Code())
	require.Contains(t, err.Error(), "Nautobot discovery canceled")
	require.Equal(t, 1, requests, "cancellation must stop discovery before an expansion request")
}

func TestSwitchFirstExpansionBatches(t *testing.T) {
	inventory := decodeDevices(t, fabricJSON)
	var mu sync.Mutex
	fetched := map[string]int{}
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]string
		require.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
		query := payload["query"]
		require.Equal(t, []string{"Switch"}, queryFilter(t, query, "role"))
		ids := queryFilter(t, query, "id")
		mu.Lock()
		calls++
		for _, id := range ids {
			fetched[id]++
		}
		mu.Unlock()
		if ids == nil {
			require.Equal(t, []string{"lab"}, queryFilter(t, query, "location"))
			require.Equal(t, []string{"SONiC", `test "platform"`}, queryFilter(t, query, "platform"))
			// Only leaf-a matches the initial location/platform; upper switches do not.
			ids = []string{"leaf-a"}
		} else {
			require.Len(t, ids, 1)
			require.NotContains(t, query, "location:")
			require.NotContains(t, query, "platform:")
		}
		result := []device{}
		for _, d := range inventory {
			if slices.Contains(ids, d.ID) {
				result = append(result, d)
			}
		}
		require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"devices": devicePageForQuery(t, query, result)}}))
	}))
	defer server.Close()
	prv, loadErr := Loader(context.Background(), providers.Config{
		Params: map[string]any{
			"endpoint": server.URL, "location": []string{"lab"}, "platform": []string{"SONiC", `test "platform"`},
			"deviceRole": []string{"GPU"}, "switchRoles": []string{"Switch"},
		},
		Creds: map[string]any{"token": "fake-test-token"},
	})
	require.Nil(t, loadErr)
	p := prv.(*Provider)
	pageSize := 1
	graph, err := p.GenerateTopologyConfig(context.Background(), &pageSize, requestedNodes("serial-a"))
	require.Nil(t, err)
	require.Equal(t, []string{"leaf-a", "spine-a", "core"}, graph.Instances["serial-a"].NetworkLayers)
	mu.Lock()
	defer mu.Unlock()
	require.Equal(t, 5, calls)
	require.Equal(t, map[string]int{"spine-a": 1, "spine-b": 1, "core": 1}, fetched)
}

func TestGenerateTopologyRejectsDuplicateSwitchNames(t *testing.T) {
	devices := decodeDevices(t, `[
		{"id":"local-leaf","name":"leaf-01","role":{"name":"Switch"},"interfaces":[
			{"cable_peer_interface":{"device":{"id":"remote-leaf","name":"leaf-01","role":{"name":"Switch"}}}},
			{"cable_peer_interface":{"device":{"id":"host","name":"node","role":{"name":"GPU"}}}}
		]},
		{"id":"remote-leaf","name":"leaf-01","role":{"name":"Switch"}}
	]`)
	for _, expand := range []bool{false, true} {
		name := "initial inventory"
		if expand {
			name = "connected switch outside location"
		}
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var payload map[string]string
				require.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
				query := payload["query"]
				result := devices
				if ids := queryFilter(t, query, "id"); ids != nil {
					require.True(t, expand)
					require.Equal(t, []string{"remote-leaf"}, ids)
					require.NotContains(t, query, "location:")
					result = devices[1:]
				} else {
					require.Equal(t, []string{"lab"}, queryFilter(t, query, "location"))
					if expand {
						result = devices[:1]
					}
				}
				require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"devices": result}}))
			}))
			defer server.Close()
			p := loadProvider(t, server.URL)
			p.params.Location = []string{"lab"}
			graph, err := p.GenerateTopologyConfig(context.Background(), nil, requestedNodes("node"))
			require.Nil(t, graph)
			require.NotNil(t, err)
			require.Equal(t, http.StatusBadGateway, err.Code())
			require.Contains(t, err.Error(), `duplicate switch name "leaf-01"`)
			require.Contains(t, err.Error(), `"local-leaf"`)
			require.Contains(t, err.Error(), `"remote-leaf"`)
		})
	}
}

func TestSwitchFirstComputePeers(t *testing.T) {
	for _, tc := range []struct {
		name       string
		secondName string
		serial     string
		wantError  bool
	}{
		{"dual-homed host", "node", "serial", false},
		{"missing serial on one peer", "node", "", false},
		{"conflicting names", "other", "serial", true},
		{"conflicting serials", "node", "different", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			inventory := decodeDevices(t, `[
 {"id":"a","name":"leaf-a","role":{"name":"Switch"},"interfaces":[
  {"cable_peer_interface":{"device":{"id":"host","name":"node","serial":"serial","role":{"name":"GPU"}}}}
 ]},
 {"id":"b","name":"leaf-b","role":{"name":"Switch"},"interfaces":[
  {"cable_peer_interface":{"device":{"id":"host","name":"node","role":{"name":"GPU"}}}}
 ]}
]`)
			peer := inventory[1].Interfaces[0].Peer.Device
			peer.Name, peer.Serial = tc.secondName, tc.serial
			server := fabricServer(t, inventory)
			defer server.Close()
			p := loadProvider(t, server.URL)
			graph, err := p.GenerateTopologyConfig(context.Background(), nil, requestedNodes("serial"))
			if tc.wantError {
				require.NotNil(t, err)
				require.Equal(t, http.StatusBadGateway, err.Code())
				return
			}
			require.Nil(t, err)
			require.Equal(t, []string{"leaf-a"}, graph.Instances["serial"].NetworkLayers)
		})
	}
}

func TestRoleConfiguration(t *testing.T) {
	for _, params := range []map[string]any{
		{"deviceRole": []string{"GPU"}},
		{"deviceRole": []string{"GPU"}, "switchRoles": []string{}},
		{"deviceRole": []string{"GPU"}, "switchRoles": []string{"GPU"}},
		{"deviceRole": []string{" "}, "switchRoles": []string{"Switch"}},
		{"deviceRole": []string{"GPU"}, "switchRoles": []string{" "}},
	} {
		params["endpoint"] = "https://example.invalid"
		_, err := Loader(context.Background(), providers.Config{Params: params, Creds: map[string]any{"token": "fake-test-token"}})
		require.NotNil(t, err)
		require.Equal(t, http.StatusBadRequest, err.Code())
	}
}
