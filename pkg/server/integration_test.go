/*
 * Copyright 2026, NVIDIA CORPORATION
 * SPDX-License-Identifier: Apache-2.0
 */

package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/dsx-ai-factory/topograph/pkg/config"
	"github.com/dsx-ai-factory/topograph/pkg/test"
	"github.com/stretchr/testify/require"
)

const (
	slurmSmallConfig = `
SwitchName=S1 Switches=S[2-3]
SwitchName=S2 Nodes=I[21-22,25]
SwitchName=S3 Nodes=I[34-36]
`
)

func TestServerIntegration(t *testing.T) {
	backOff = 100 * time.Millisecond
	defer func() { backOff = defaultBackOff }()

	// testIntegration and topologyRequestWithRetries use http.DefaultClient / http.Get,
	// which pool keep-alive connections in http.DefaultTransport. Close idle connections
	// after the test so goroutine-leak detectors do not flag the transport's readLoop /
	// writeLoop goroutines. See https://github.com/dsx-ai-factory/topograph/issues/493.
	t.Cleanup(func() { http.DefaultClient.CloseIdleConnections() })

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
		filename       string
		expected       string
		generateMethod string
		timeout        time.Duration
	}{
		{
			filename: "../../tests/integration/payload-error-500-after-retries.json",
			timeout:  time.Minute,
		},
		{
			filename:       "../../tests/integration/payload-invalid-http-method.json",
			generateMethod: "GET",
		},
		{
			filename: "../../tests/integration/payload-invalid-user-input.json",
		},
		{
			filename: "../../tests/integration/payload-repeated-202.json",
		},
		{
			filename: "../../tests/integration/payload-request-id-not-found.json",
		},
		{
			filename: "../../tests/integration/payload-valid-topology.json",
			expected: slurmSmallConfig,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.filename, func(t *testing.T) {
			payloadFile, err := os.Open(tc.filename)
			require.NoError(t, err)
			defer payloadFile.Close()

			payload, err := io.ReadAll(payloadFile)
			require.NoError(t, err)
			if tc.timeout <= 0 {
				tc.timeout = 5 * time.Second
			}

			testIntegration(t, baseURL, string(payload), tc.expected, tc.generateMethod, tc.timeout)
		})
	}
}

func testIntegration(t *testing.T, baseURL, payload, expected, generateMethod string, timeout time.Duration) {

	// parse payload to get the request details
	tp, err := ParseTestPayload([]byte(payload))
	require.NoError(t, err)

	//set generate method if not set
	if len(generateMethod) == 0 {
		generateMethod = "POST"
	}

	//validate input parameters
	require.Equal(t, "test", tp.Provider.Name)

	//construct generate request
	req, err := http.NewRequest(generateMethod, baseURL+"/v1/generate", bytes.NewBuffer([]byte(payload)))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")

	// send generate request and validate response code
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, tp.Provider.Params.GenerateResponseCode, resp.StatusCode)
	if resp.StatusCode != http.StatusAccepted {
		return
	}

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	out := string(body)

	// retrieve topology config
	params := url.Values{}
	params.Add("uid", out)
	fullURL := fmt.Sprintf("%s?%s", baseURL+"/v1/topology", params.Encode())

	//invoke topology endpoint with retries and validate response code and body
	code, body, err := topologyRequestWithRetries(fullURL, timeout)
	require.NoError(t, err)
	require.Equal(t, tp.Provider.Params.TopologyResponseCode, code)
	if code == http.StatusOK {
		require.Equal(t, stringToLineMap(expected), stringToLineMap(string(body)))
	}
}

func topologyRequestWithRetries(url string, timeout time.Duration) (int, []byte, error) {

	start, delay := time.Now(), time.Second

	var resp *http.Response
	var code int
	var err error
	var body []byte

	for time.Since(start) < timeout {
		time.Sleep(delay)
		resp, err = http.Get(url)
		if err != nil {
			return 0, nil, err
		}

		code = resp.StatusCode
		if code == http.StatusAccepted {
			resp.Body.Close()
			continue
		}

		if code == http.StatusOK {
			body, err = io.ReadAll(resp.Body)
		}
		resp.Body.Close()
		break
	}

	return code, body, err
}

func ParseTestPayload(data []byte) (*TestPayload, error) {
	var tp TestPayload
	if err := json.Unmarshal(data, &tp); err != nil {
		return nil, fmt.Errorf("failed to parse test payload: %v", err)
	}

	return &tp, nil
}

type TestPayload struct {
	Provider TestProvider `json:"provider"`
}

type TestProvider struct {
	Name   string `json:"name"`
	Params struct {
		TestcaseName         string `json:"testcaseName,omitempty"`
		Description          string `json:"description,omitempty"`
		GenerateResponseCode int    `json:"generateResponseCode,omitempty"`
		TopologyResponseCode int    `json:"topologyResponseCode,omitempty"`
		ModelFileName        string `json:"modelFileName,omitempty"`
		ErrorMessage         string `json:"errorMessage,omitempty"`
	} `json:"params"`
}
