/*
 * Copyright (c) 2024, NVIDIA CORPORATION.  All rights reserved.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package server

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/dsx-ai-factory/topograph/pkg/config"
	"github.com/dsx-ai-factory/topograph/pkg/test"
)

const (
	simpleSlurmPayload = `
{
  "provider": {
    "name": "%s"
  },
  "engine": {
    "name": "slurm"
  }
}
`
	simpleSlurmConfig = `SwitchName=S1 Switches=S[2-3]
SwitchName=S2 Nodes=Node[201-202,205]
SwitchName=S3 Nodes=Node[304-306]
`

	slurmTreePayload = `
{
  "provider": {
    "name": "%s",
    "params": {
      "modelFileName": "../../tests/models/medium.yaml"
    }
  },
  "engine": {
    "name": "slurm"
  },
  "nodes": [
    {
      "region": "R1",
      "instances": {
        "1101": "n-1101",
        "1102": "n-1102",
        "1201": "n-1201",
        "1202": "n-1202",
        "1301": "n-1301",
        "1302": "n-1302",
        "1401": "n-1401",
        "1402": "n-1402",
        "1500": "n-CPU"
      }
    }
  ]
}
`
	slurmTreeConfig = `SwitchName=sw3 Switches=sw[21-22]
SwitchName=sw21 Switches=sw[11-12]
SwitchName=sw22 Switches=sw[13-14]
SwitchName=sw11 Nodes=n-[1101-1102]
SwitchName=sw12 Nodes=n-[1201-1202]
SwitchName=sw13 Nodes=n-[1301-1302]
SwitchName=sw14 Nodes=n-[1401-1402]
SwitchName=no-topology Nodes=n-CPU
`

	slurmTrimmedTreePayload = `
{
  "provider": {
    "name": "%s",
    "params": {
      "modelFileName": "../../tests/models/medium.yaml",
	  "trimTiers": 1
    }
  },
  "engine": {
    "name": "slurm"
  },
  "nodes": [
    {
      "region": "R1",
      "instances": {
        "1101": "n-1101",
        "1102": "n-1102",
        "1201": "n-1201",
        "1202": "n-1202",
        "1301": "n-1301",
        "1302": "n-1302",
        "1401": "n-1401",
        "1402": "n-1402",
        "1500": "n-CPU"
      }
    }
  ]
}
`
	slurmTrimmedTreeConfig = `SwitchName=sw21 Switches=sw[11-12]
SwitchName=sw22 Switches=sw[13-14]
SwitchName=sw11 Nodes=n-[1101-1102]
SwitchName=sw12 Nodes=n-[1201-1202]
SwitchName=sw13 Nodes=n-[1301-1302]
SwitchName=sw14 Nodes=n-[1401-1402]
SwitchName=no-topology Nodes=n-CPU
`

	slurmBlockPayload = `
{
  "provider": {
    "name": "%s",
    "params": {
      "modelFileName": "../../tests/models/large.yaml"
    }
  },
  "engine": {
    "name": "slurm",
	"params": {
      "plugin": "topology/block",
      "blockSizes": [8,16,32]
    }
  }
}
`
	slurmBlockConfig = `# block001=nvl-1-1
BlockName=block001 Nodes=srv[1101-1108]
# block002=nvl-1-2
BlockName=block002 Nodes=srv[1201-1208]
# block003=nvl-2-1
BlockName=block003 Nodes=srv[2101-2108]
# block004=nvl-2-2
BlockName=block004 Nodes=srv[2201-2208]
# block005=nvl-3-1
BlockName=block005 Nodes=srv[3101-3108]
# block006=nvl-3-2
BlockName=block006 Nodes=srv[3201-3208]
# block007=nvl-4-1
BlockName=block007 Nodes=srv[4101-4108]
# block008=nvl-4-2
BlockName=block008 Nodes=srv[4201-4208]
# block009=nvl-5-1
BlockName=block009 Nodes=srv[5101-5108]
# block010=nvl-5-2
BlockName=block010 Nodes=srv[5201-5208]
# block011=nvl-6-1
BlockName=block011 Nodes=srv[6101-6108]
# block012=nvl-6-2
BlockName=block012 Nodes=srv[6201-6208]
BlockSizes=8,16,32
`
	testPayload = `
{
  "provider": {
    "name": "test",
    "params": {
      "modelFileName": "large.yaml",
      "providerParam01": "test"
    }
  },
  "engine": {
    "name": "slurm",
    "params": {
      "plugin": "topology/block",
      "engineParam01": 2
    }
  }
}
`

	// graph engine returns compact JSON from instances embedded in the topology graph
	graphPayload = `
{
  "provider": {
    "name": "%s",
    "params": {
      "modelFileName": "small-tree.yaml"
    }
  },
  "engine": {
    "name": "graph"
  },
  "nodes": [
    {
      "region": "none",
      "instances": {
        "i-I21": "I21",
        "i-I22": "I22"
      }
    }
  ]
}
`
	graphInstancesJSON = `{
  "instances": [
    {
      "id": "i-I21",
      "labels": {
        "topology.kubernetes.io/region": "none"
      },
      "network_layers": [
        "S2",
        "S1"
      ]
    },
    {
      "id": "i-I22",
      "labels": {
        "topology.kubernetes.io/region": "none"
      },
      "network_layers": [
        "S2",
        "S1"
      ]
    }
  ]
}`
)

func TestServerLocal(t *testing.T) {
	port, err := test.GetAvailablePort()
	require.NoError(t, err)

	cfg := &config.Config{
		HTTP: config.Endpoint{
			Port: port,
		},
		RequestAggregationDelay: time.Second,
	}
	baseURL := fmt.Sprintf("http://localhost:%d", port)

	srv = initHttpServer(context.TODO(), cfg)
	defer srv.Stop(nil)
	go func() { _ = srv.Start() }()

	// let the server start
	time.Sleep(time.Second)

	testCases := []struct {
		name     string
		endpoint string
		provider string
		payload  string
		expected string
		metrics  []string
		jsonBody bool
	}{
		{
			name:     "Case 1: test invalid endpoint",
			endpoint: "invalid",
			expected: "404 page not found\n",
			metrics: []string{
				`topograph_http_request_duration_seconds_count\{from=".+",method="GET",path="/invalid",proto="HTTP/1\.1",status="404"\} 1`,
			},
		},
		{
			name:     "Case 2: test healthz endpoint",
			endpoint: "healthz",
			expected: "OK\n",
			metrics: []string{
				`topograph_http_request_duration_seconds_count\{from=".+",method="GET",path="/healthz",proto="HTTP/1\.1",status="200"\} 1`,
			},
		},
		{
			name:     "Case 3: send test request for tree topology",
			endpoint: "generate",
			provider: "test",
			payload:  simpleSlurmPayload,
			expected: simpleSlurmConfig,
			metrics: []string{
				`topograph_request_duration_seconds_count\{engine="slurm",provider="test",status="200"\} 1`,
				`topograph_http_request_duration_seconds_count\{from=".+",method="POST",path="/v1/generate",proto="HTTP/1\.1",status="202"\} 1`,
				`topograph_http_request_duration_seconds_count\{from=".+",method="GET",path="/v1/topology",proto="HTTP/1\.1",status="200"\} 1`,
				`topograph_http_request_duration_seconds_count\{from=".+",method="POST",path="/v1/lookup",proto="HTTP/1\.1",status="200"\} 1`,
			},
		},
		{
			name:     "Case 4: mock AWS request for tree topology",
			endpoint: "generate",
			provider: "aws-sim",
			payload:  slurmTreePayload,
			expected: slurmTreeConfig,
			metrics: []string{
				`topograph_request_duration_seconds_count\{engine="slurm",provider="aws-sim",status="200"\} 1`,
				`topograph_http_request_duration_seconds_count\{from=".+",method="POST",path="/v1/generate",proto="HTTP/1\.1",status="202"\} 2`,
				`topograph_http_request_duration_seconds_count\{from=".+",method="GET",path="/v1/topology",proto="HTTP/1\.1",status="200"\} 2`,
				`topograph_http_request_duration_seconds_count\{from=".+",method="POST",path="/v1/lookup",proto="HTTP/1\.1",status="200"\} 2`,
			},
		},
		{
			name:     "Case 5: mock AWS request for block topology",
			endpoint: "generate",
			provider: "aws-sim",
			payload:  slurmBlockPayload,
			expected: slurmBlockConfig,
		},
		{
			name:     "Case 6: mock GCP request for tree topology",
			endpoint: "generate",
			provider: "gcp-sim",
			payload:  slurmTreePayload,
			expected: slurmTreeConfig,
		},
		{
			name:     "Case 7: mock GCP request for trimmed tree topology",
			endpoint: "generate",
			provider: "gcp-sim",
			payload:  slurmTrimmedTreePayload,
			expected: slurmTrimmedTreeConfig,
		},
		{
			name:     "Case 8: mock Lambda request for tree topology",
			endpoint: "generate",
			provider: "lambdai-sim",
			payload:  slurmTreePayload,
			expected: slurmTreeConfig,
		},
		{
			name:     "Case 9: mock request for topology with invalid UID",
			endpoint: "topology",
			payload:  "invalid-uid",
			expected: "request ID invalid-uid not found\n",
			metrics: []string{
				`topograph_http_request_duration_seconds_count\{from=".+",method="GET",path="/v1/topology",proto="HTTP/1\.1",status="404"\} 1`,
			},
		},
		{
			name:     "Case 10: mock Dsx request for block topology",
			endpoint: "generate",
			provider: "dsx-sim",
			payload:  slurmBlockPayload,
			expected: slurmBlockConfig,
		},
		{
			name:     "Case 11: mock Dsx request for tree topology",
			endpoint: "generate",
			provider: "dsx-sim",
			payload:  slurmTreePayload,
			expected: slurmTreeConfig,
		},
		{
			name:     "Case 12: mock Dsx request for trimmed tree topology",
			endpoint: "generate",
			provider: "dsx-sim",
			payload:  slurmTrimmedTreePayload,
			expected: slurmTrimmedTreeConfig,
		},
		{
			name:     "Case 13: graph engine returns instances JSON",
			endpoint: "generate",
			provider: "test",
			payload:  graphPayload,
			expected: graphInstancesJSON,
			metrics: []string{
				// Cumulative HTTP metrics are omitted: they depend on how many prior generate cases
				// ran in the same TestServerLocal run (and subtests like -run Case_13 only see counts of 1).
				`topograph_request_duration_seconds_count\{engine="graph",provider="test",status="200"\} 1`,
			},
			jsonBody: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			switch tc.endpoint {
			case "invalid":
				testInvalid(t, baseURL, tc.expected, tc.metrics)
			case "healthz":
				testHealthz(t, baseURL, tc.expected, tc.metrics)
			case "generate":
				testGenerate(t, baseURL, fmt.Sprintf(tc.payload, tc.provider), tc.expected, tc.metrics, tc.jsonBody)
			case "topology":
				testTopology(t, baseURL, tc.payload, tc.expected, http.StatusNotFound, tc.metrics)
			default:
				t.Errorf("unsupported endpoint %s", tc.endpoint)
			}
		})
	}
}

func testInvalid(t *testing.T, baseURL, expected string, metrics []string) {
	resp, err := http.Get(baseURL + "/invalid")
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, http.StatusNotFound, resp.StatusCode)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, expected, string(body))

	checkMetrics(t, baseURL, metrics)
}

func testHealthz(t *testing.T, baseURL, expected string, metrics []string) {
	resp, err := http.Get(baseURL + "/healthz")
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, http.StatusOK, resp.StatusCode)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, expected, string(body))

	checkMetrics(t, baseURL, metrics)
}

func testGenerate(t *testing.T, baseURL, payload, expected string, metrics []string, jsonBody bool) {
	// send topology request
	resp, err := http.Post(baseURL+"/v1/generate", "application/json", bytes.NewBuffer([]byte(payload)))
	require.NoError(t, err)
	require.Equal(t, http.StatusAccepted, resp.StatusCode)

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	out := string(body)
	resp.Body.Close()

	// retrieve topology config
	params := url.Values{}
	params.Add("uid", out)
	fullURL := fmt.Sprintf("%s?%s", baseURL+"/v1/topology", params.Encode())

	for i := range 5 {
		time.Sleep(time.Second)
		resp, err = http.Get(fullURL)
		require.NoError(t, err)

		if resp.StatusCode == http.StatusOK {
			break
		}

		resp.Body.Close()
		if i == 4 {
			t.Errorf("timeout")
		}
	}

	require.NoError(t, err)
	defer resp.Body.Close()

	body, err = io.ReadAll(resp.Body)
	require.NoError(t, err)

	if jsonBody {
		require.JSONEq(t, expected, string(body))
	} else {
		require.Equal(t, stringToLineMap(expected), stringToLineMap(string(body)))
	}

	// Check lookup endpoint
	testLookup(t, baseURL, payload, expected, http.StatusOK, jsonBody)

	checkMetrics(t, baseURL, metrics)
}

func testTopology(t *testing.T, baseURL, uid, expected string, expectedResponse int, metrics []string) {
	resp, err := http.Get(baseURL + "/v1/topology?uid=" + uid)
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, expectedResponse, resp.StatusCode)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, expected, string(body))

	checkMetrics(t, baseURL, metrics)
}

func testLookup(t *testing.T, baseURL, payload, expected string, expectedResponse int, jsonBody bool) {
	resp, err := http.Post(baseURL+"/v1/lookup", "application/json", bytes.NewBuffer([]byte(payload)))
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, expectedResponse, resp.StatusCode)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	if jsonBody {
		require.JSONEq(t, expected, string(body))
	} else {
		require.Equal(t, stringToLineMap(expected), stringToLineMap(string(body)))
	}
}

func checkMetrics(t *testing.T, baseURL string, metrics []string) {
	resp, err := http.Get(baseURL + "/metrics")
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, http.StatusOK, resp.StatusCode)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	text := string(body)

	for _, metric := range metrics {
		matched, err := regexp.MatchString(metric, text)
		require.NoError(t, err)
		if !matched {
			t.Errorf("missing metrics %q", metric)
		}
	}
}

func stringToLineMap(str string) map[string]struct{} {
	m := make(map[string]struct{})
	for _, line := range strings.Split(str, "\n") {
		if len(line) != 0 {
			m[line] = struct{}{}
		}
	}

	return m
}

func readRequestTest(t *testing.T, payload string, provider, engine string, providerParams, engineParams map[string]any) {
	r := &http.Request{
		Method: http.MethodPost,
		Body:   io.NopCloser(bytes.NewBuffer([]byte(payload))),
	}

	w := httptest.NewRecorder()

	req := readRequest(w, r)
	require.NotNil(t, req)
	require.Equal(t, provider, req.Provider.Name)
	require.Equal(t, engine, req.Engine.Name)
	require.Equal(t, providerParams, req.Provider.Params)
	require.Equal(t, engineParams, req.Engine.Params)
}

func TestReadRequest(t *testing.T) {
	srv = &HttpServer{
		cfg: &config.Config{
			Provider: "test",
			ProviderParams: map[string]any{
				"providerParam01": 1,
			},
			Engine: "slurm",
			EngineParams: map[string]any{
				"engineParam01": "test",
			},
		},
	}
	testCases := []struct {
		name           string
		payload        string
		provider       string
		engine         string
		providerParams map[string]any
		engineParams   map[string]any
	}{
		{
			name:     "Test readRequest with an empty Payload",
			payload:  "",
			provider: "test",
			engine:   "slurm",
			providerParams: map[string]any{
				"providerParam01": 1,
			},
			engineParams: map[string]any{
				"engineParam01": "test",
			},
		},
		{
			name:     "Test readRequest with Simple Slurm Payload",
			payload:  fmt.Sprintf(simpleSlurmPayload, "test"),
			provider: "test",
			engine:   "slurm",
			providerParams: map[string]any{
				"providerParam01": 1,
			},
			engineParams: map[string]any{
				"engineParam01": "test",
			},
		},
		{
			// This test checks that the provider and engine params specified in the payload take precedence over the ones in the config
			// for overlapping keys, while non-overlapping keys from the config are still included in the final params.
			name:     "Test readRequest with Overlapping Keys in Payload and Config",
			payload:  testPayload,
			provider: "test",
			engine:   "slurm",
			providerParams: map[string]any{
				"modelFileName":   "large.yaml",
				"providerParam01": "test",
			},
			engineParams: map[string]any{
				"plugin":        "topology/block",
				"engineParam01": float64(2), // note that JSON unmarshaling converts numbers to float64
			},
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			readRequestTest(t, tc.payload, tc.provider, tc.engine, tc.providerParams, tc.engineParams)
		})
	}
}

func readInvalidRequest(t *testing.T, payload, msg string) {
	r := &http.Request{
		Method: http.MethodPost,
		Body:   io.NopCloser(bytes.NewBuffer([]byte(payload))),
	}
	w := httptest.NewRecorder()

	req := readRequest(w, r)
	require.Nil(t, req)
	require.Equal(t, http.StatusBadRequest, w.Result().StatusCode)
	require.Equal(t, msg, w.Body.String())
}

func TestReadInvalidRequest(t *testing.T) {

	srv = &HttpServer{
		cfg: &config.Config{},
	}
	testCases := []struct {
		name    string
		payload string
		message string
	}{
		{
			name:    "Test validate with an empty provider",
			payload: "",
			message: "no provider given for topology request\n",
		},
		{
			name: "Test validate with an empty engine",
			payload: `{
				"provider": {
					"name": "test"
				}
			}`,
			message: "no engine given for topology request\n",
		},
		{
			name: "Test validate with an invalid provider",
			payload: `{
				"provider": {
					"name": "mytestprovider"
				}
			}`,
			message: "unsupported provider mytestprovider\n",
		},
		{
			name: "Test validate with an invalid engine",
			payload: `{
				"provider": {
					"name": "test"
				},
				"engine": {
					"name": "mytestengine"
				}
			}`,
			message: "unsupported engine mytestengine\n",
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			readInvalidRequest(t, tc.payload, tc.message)
		})
	}
}

func TestHttpServerStopIdempotent(t *testing.T) {
	port, err := test.GetAvailablePort()
	require.NoError(t, err)
	s := initHttpServer(context.Background(), &config.Config{
		HTTP: config.Endpoint{Port: port},
	})

	// sequential repeated calls must not panic
	s.Stop(nil)
	s.Stop(nil)

	// concurrent calls must not panic
	var wg sync.WaitGroup
	for range 5 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.Stop(nil)
		}()
	}
	wg.Wait()
}
