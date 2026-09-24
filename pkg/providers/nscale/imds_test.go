/*
 * Copyright 2026 NVIDIA CORPORATION
 * SPDX-License-Identifier: Apache-2.0
 */

package nscale

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/dsx-ai-factory/topograph/pkg/topology"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

// mockIMDSServer starts a real HTTP server returning body for every request,
// and redirects GetNodeAnnotations' hardcoded IMDSURL requests to it by
// swapping http.DefaultTransport for the duration of the test.
func mockIMDSServer(t *testing.T, body string) *int {
	t.Helper()

	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)

	target, err := url.Parse(server.URL)
	require.NoError(t, err)

	oldTransport := http.DefaultTransport
	http.DefaultTransport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		require.Equal(t, http.MethodGet, req.Method)
		require.Equal(t, IMDSURL, req.URL.String())

		redirected := req.Clone(req.Context())
		redirected.URL.Scheme = target.Scheme
		redirected.URL.Host = target.Host
		redirected.Host = target.Host

		return oldTransport.RoundTrip(redirected)
	})
	t.Cleanup(func() {
		http.DefaultTransport = oldTransport
	})

	return &calls
}

func TestGetNodeAnnotations(t *testing.T) {
	body := `{
		"uuid": "a839ab99-f2e2-4237-a822-688a47eca11d",
		"meta": {
			"regionID": "d0ba550d-1be4-4c4e-8ef9-3adb23b3fe3b",
			"serverID": "3fa74ca0-efab-4c5c-b926-aa98bb834dec-000000"
		}
	}`
	calls := mockIMDSServer(t, body)

	annotations, err := GetNodeAnnotations(context.Background(), nil)
	require.NoError(t, err)
	require.Equal(t, map[string]string{
		topology.KeyNodeInstance: "3fa74ca0-efab-4c5c-b926-aa98bb834dec-000000",
		topology.KeyNodeRegion:   "d0ba550d-1be4-4c4e-8ef9-3adb23b3fe3b",
	}, annotations)
	require.Equal(t, 1, *calls)
}

func TestGetNodeAnnotationsCustomIMDSURL(t *testing.T) {
	body := `{"meta": {"serverID": "srv-custom", "regionID": "region-custom"}}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NotEqual(t, IMDSURL, r.URL.String())
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)

	annotations, err := GetNodeAnnotations(context.Background(), map[string]any{"imdsUrl": server.URL})
	require.NoError(t, err)
	require.Equal(t, map[string]string{
		topology.KeyNodeInstance: "srv-custom",
		topology.KeyNodeRegion:   "region-custom",
	}, annotations)
}

func TestGetNodeAnnotationsInvalidIMDSUrlParam(t *testing.T) {
	_, err := GetNodeAnnotations(context.Background(), map[string]any{"imdsUrl": 1})
	require.ErrorContains(t, err, "invalid 'imdsUrl' value")
}

func TestGetNodeAnnotationsMissingServerID(t *testing.T) {
	body := `{"meta": {"regionID": "d0ba550d-1be4-4c4e-8ef9-3adb23b3fe3b"}}`
	mockIMDSServer(t, body)

	_, err := GetNodeAnnotations(context.Background(), nil)
	require.Error(t, err)
}

func TestGetNodeAnnotationsMalformedResponse(t *testing.T) {
	mockIMDSServer(t, `not valid json`)

	_, err := GetNodeAnnotations(context.Background(), nil)
	require.Error(t, err)
}

func TestGetNodeAnnotationsCanceledContext(t *testing.T) {
	started := make(chan struct{})
	unblock := make(chan struct{})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(started)
		<-unblock
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(func() {
		close(unblock)
		server.Close()
	})

	target, err := url.Parse(server.URL)
	require.NoError(t, err)

	oldTransport := http.DefaultTransport
	http.DefaultTransport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		redirected := req.Clone(req.Context())
		redirected.URL.Scheme = target.Scheme
		redirected.URL.Host = target.Host
		redirected.Host = target.Host
		return oldTransport.RoundTrip(redirected)
	})
	t.Cleanup(func() {
		http.DefaultTransport = oldTransport
	})

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		_, err := GetNodeAnnotations(ctx, nil)
		done <- err
	}()

	<-started
	cancel()

	select {
	case err := <-done:
		require.Error(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("GetNodeAnnotations did not return after context cancellation")
	}
}

func TestPdshCmd(t *testing.T) {
	expected := fmt.Sprintf("res=$(curl -fsS -- '%s') && echo \"$res\"", IMDSURL)
	require.Equal(t, expected, pdshCmd(IMDSURL))
}

func TestPdshCmdEscapesShellMetacharacters(t *testing.T) {
	malicious := `http://example.com/x'; rm -rf / #`
	cmd := pdshCmd(malicious)
	require.Equal(t, `res=$(curl -fsS -- 'http://example.com/x'\''; rm -rf / #') && echo "$res"`, cmd)
}

func TestParseIMDSOutput(t *testing.T) {
	input := `node1: {"meta": {"serverID": "psrv-1", "regionID": "region-a"}}
node2: {"meta": {"serverID": "psrv-2"}}
node3: not valid json
malformed line without separator
`
	res, err := parseIMDSOutput(bytes.NewBufferString(input))
	require.NoError(t, err)
	require.Equal(t, map[string]*imdsMetadata{
		"node1": {Meta: map[string]string{
			metaKeyServerID: "psrv-1",
			metaKeyRegionID: "region-a",
		}},
		"node2": {Meta: map[string]string{
			metaKeyServerID: "psrv-2",
		}},
	}, res)
}

func TestParseIMDSOutputOversizedLine(t *testing.T) {
	oversized := "node1: " + strings.Repeat("x", maxIMDSLineSize+1)
	_, err := parseIMDSOutput(bytes.NewBufferString(oversized))
	require.Error(t, err)
}

func TestInstanceToNodeMap(t *testing.T) {
	nodeMeta := map[string]*imdsMetadata{
		"node1": {Meta: map[string]string{
			metaKeyServerID: "psrv-1",
			metaKeyRegionID: "region-a",
		}},
		"node2": {Meta: map[string]string{
			metaKeyServerID: "psrv-2",
		}},
		"node3": {Meta: map[string]string{}},
	}

	require.Equal(t, map[string]string{
		"psrv-1": "node1",
		"psrv-2": "node2",
	}, instanceToNodeMap(nodeMeta))
}

func TestGetRegions(t *testing.T) {
	nodeMeta := map[string]*imdsMetadata{
		"node1": {Meta: map[string]string{
			metaKeyServerID: "psrv-1",
			metaKeyRegionID: "region-a",
		}},
		"node2": {Meta: map[string]string{
			metaKeyServerID: "psrv-2",
		}},
	}

	require.Equal(t, map[string]string{
		"node1": "region-a",
	}, getRegions(nodeMeta))
}

func TestFilterByRegion(t *testing.T) {
	nodeMeta := map[string]*imdsMetadata{
		"node1": {Meta: map[string]string{
			metaKeyServerID: "psrv-1",
			metaKeyRegionID: "region-a",
		}},
		"node2": {Meta: map[string]string{
			metaKeyServerID: "psrv-2",
			metaKeyRegionID: "region-b",
		}},
		"node3": {Meta: map[string]string{
			metaKeyServerID: "psrv-3",
		}},
	}

	t.Run("no expected region: no filtering", func(t *testing.T) {
		require.Equal(t, nodeMeta, filterByRegion(nodeMeta, ""))
	})

	t.Run("expected region: mismatched and missing regions excluded", func(t *testing.T) {
		require.Equal(t, map[string]*imdsMetadata{
			"node1": nodeMeta["node1"],
		}, filterByRegion(nodeMeta, "region-a"))
	})

	t.Run("expected region: no matches", func(t *testing.T) {
		require.Empty(t, filterByRegion(nodeMeta, "region-c"))
	})
}
