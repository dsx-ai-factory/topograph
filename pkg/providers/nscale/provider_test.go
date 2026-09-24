/*
 * Copyright 2026 NVIDIA CORPORATION
 * SPDX-License-Identifier: Apache-2.0
 */

package nscale

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dsx-ai-factory/topograph/internal/httperr"
	"github.com/dsx-ai-factory/topograph/pkg/providers"
	"github.com/dsx-ai-factory/topograph/pkg/topology"
	"github.com/stretchr/testify/require"
)

type fakeClient struct {
	instances []InstanceTopology
	err       *httperr.Error
	regions   []string
}

func (c *fakeClient) Topology(_ context.Context, region string, _, offset int) ([]InstanceTopology, *httperr.Error) {
	c.regions = append(c.regions, region)
	if c.err != nil {
		return nil, c.err
	}
	if offset >= len(c.instances) {
		return nil, nil
	}
	return c.instances[offset:], nil
}

func TestLoader(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name   string
		config providers.Config
		err    string
	}{
		{
			name: "Case 1: success",
			config: providers.Config{
				Params: map[string]any{
					"radarApiUrl": "https://radar.test.com",
				},
				Creds: map[string]any{
					"org":    "org",
					"token":  "token",
					"region": "region",
				},
			},
		},
		{
			name: "Case 1b: success with custom imdsUrl",
			config: providers.Config{
				Params: map[string]any{
					"radarApiUrl": "https://radar.test.com",
					"imdsUrl":     "http://custom-imds.example.com/meta_data.json",
				},
				Creds: map[string]any{
					"org":    "org",
					"token":  "token",
					"region": "region",
				},
			},
		},
		{
			name: "Case 2: missing radarApiUrl",
			config: providers.Config{
				Params: map[string]any{},
				Creds: map[string]any{
					"org":   "org",
					"token": "token",
				},
			},
			err: "missing 'radarApiUrl'",
		},
		{
			name: "Case 3: missing org",
			config: providers.Config{
				Params: map[string]any{
					"radarApiUrl": "https://radar.test.com",
				},
				Creds: map[string]any{
					"token": "token",
				},
			},
			err: "missing 'org'",
		},
		{
			name: "Case 4: missing token",
			config: providers.Config{
				Params: map[string]any{
					"radarApiUrl": "https://radar.test.com",
				},
				Creds: map[string]any{
					"org": "org",
				},
			},
			err: "missing 'token'",
		},
		{
			name: "Case 5: invalid imdsUrl type",
			config: providers.Config{
				Params: map[string]any{
					"radarApiUrl": "https://radar.test.com",
					"imdsUrl":     1,
				},
				Creds: map[string]any{
					"org":   "org",
					"token": "token",
				},
			},
			err: "invalid 'imdsUrl' value '1': unsupported type int",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider, err := Loader(ctx, tt.config)

			if len(tt.err) != 0 {
				require.Nil(t, provider)
				require.NotNil(t, err)
				require.Equal(t, http.StatusBadRequest, err.Code())
				require.Equal(t, err.Error(), tt.err)
			} else {
				require.NotNil(t, provider)
				require.Nil(t, err)
			}
		})
	}
}

func TestGetParamsIMDSUrl(t *testing.T) {
	p, err := getParams(map[string]any{
		"radarApiUrl": "https://radar.test.com",
		"imdsUrl":     "http://custom-imds.example.com/meta_data.json",
	})
	require.NoError(t, err)
	require.Equal(t, "http://custom-imds.example.com/meta_data.json", p.IMDSUrl)

	p, err = getParams(map[string]any{
		"radarApiUrl": "https://radar.test.com",
	})
	require.NoError(t, err)
	require.Empty(t, p.IMDSUrl)
}

func TestIMDSURL(t *testing.T) {
	tests := []struct {
		name     string
		params   *ProviderParams
		expected string
	}{
		{
			name:     "Case 1: unset falls back to default",
			params:   &ProviderParams{},
			expected: IMDSURL,
		},
		{
			name:     "Case 2: custom URL overrides default",
			params:   &ProviderParams{IMDSUrl: "http://custom-imds.example.com/meta_data.json"},
			expected: "http://custom-imds.example.com/meta_data.json",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &baseProvider{params: tt.params}
			require.Equal(t, tt.expected, p.imdsURL())
		})
	}
}

func TestFetchIMDSMetadataNilParams(t *testing.T) {
	var gotURL string
	p := &baseProvider{
		imdsFetch: func(_ context.Context, _ []string, imdsURL string) (map[string]*imdsMetadata, error) {
			gotURL = imdsURL
			return fakeMetadata(), nil
		},
	}

	data, err := p.fetchIMDSMetadata(context.Background(), []string{"node-1"})
	require.NoError(t, err)
	require.Equal(t, fakeMetadata(), data)
	require.Equal(t, IMDSURL, gotURL)
}

func TestGenerateTopologyConfig(t *testing.T) {
	blockID := "block-1"

	tests := []struct {
		name      string
		client    Client
		instances []topology.ComputeInstances
		trimTiers int
		err       string
	}{
		{
			name: "Case 1: success",
			client: &fakeClient{instances: []InstanceTopology{
				{ServerID: "srv-1", NetworkPath: []string{"core-1", "spine-1", "leaf-1"}},
				{ServerID: "srv-2", NetworkPath: []string{"core-1", "spine-1", "leaf-2"}, BlockID: &blockID},
			}},
			instances: []topology.ComputeInstances{
				{
					Region:    "region-a",
					Instances: map[string]string{"srv-1": "node1", "srv-2": "node2"},
				},
			},
		},
		{
			name:      "Case 2: no compute instances",
			client:    &fakeClient{},
			instances: nil,
		},
		{
			name:   "Case 3: missing region",
			client: &fakeClient{},
			instances: []topology.ComputeInstances{
				{Instances: map[string]string{"srv-1": "node1"}},
			},
			err: "must specify region",
		},
		{
			name:   "Case 4: Radar API error",
			client: &fakeClient{err: httperr.NewError(http.StatusBadGateway, "boom")},
			instances: []topology.ComputeInstances{
				{
					Region:    "region-a",
					Instances: map[string]string{"srv-1": "node1"},
				},
			},
			err: "boom",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &baseProvider{
				client: tt.client,
				params: &ProviderParams{TrimTiers: tt.trimTiers},
			}

			graph, err := p.GenerateTopologyConfig(context.Background(), nil, tt.instances)

			if len(tt.err) != 0 {
				require.Nil(t, graph)
				require.NotNil(t, err)
				require.Contains(t, err.Error(), tt.err)
			} else {
				require.Nil(t, err)
				require.NotNil(t, graph)

				if tt.name == "Case 1: success" {
					leaves := make(map[string]string)
					collectLeafVertices(graph.Tiers, leaves)
					require.Equal(t, map[string]string{"srv-1": "node1", "srv-2": "node2"}, leaves)

					fc := tt.client.(*fakeClient)
					require.NotEmpty(t, fc.regions)
					for _, r := range fc.regions {
						require.Equal(t, "region-a", r)
					}
				}
			}
		})
	}
}

// collectLeafVertices walks the graph tree, collecting compute-node vertices
// (those without children) into leaves, keyed by instance ID.
func collectLeafVertices(v *topology.Vertex, leaves map[string]string) {
	if v == nil {
		return
	}
	if len(v.Vertices) == 0 {
		if len(v.ID) != 0 {
			leaves[v.ID] = v.Name
		}
		return
	}
	for _, child := range v.Vertices {
		collectLeafVertices(child, leaves)
	}
}

func TestNscaleClientTopology(t *testing.T) {
	blockID := "block-1"

	tests := []struct {
		name     string
		region   string
		pageSize int
		offset   int
		status   int
		body     string
		expected []InstanceTopology
		err      string
	}{
		{
			name:     "Case 1: success",
			region:   "region-a",
			pageSize: 50,
			offset:   100,
			status:   http.StatusOK,
			body: `{"results": [
				{"server_id": "srv-1", "network_node_path": ["core-1", "spine-1", "leaf-1"]},
				{"server_id": "srv-2", "network_node_path": ["core-1", "spine-1", "leaf-2"], "block_id": "block-1"}
			]}`,
			expected: []InstanceTopology{
				{ServerID: "srv-1", NetworkPath: []string{"core-1", "spine-1", "leaf-1"}},
				{ServerID: "srv-2", NetworkPath: []string{"core-1", "spine-1", "leaf-2"}, BlockID: &blockID},
			},
		},
		{
			name:     "Case 2: empty page",
			region:   "region-a",
			pageSize: 50,
			offset:   0,
			status:   http.StatusOK,
			body:     `{"results": []}`,
			expected: []InstanceTopology{},
		},
		{
			name:     "Case 3: API error",
			region:   "region-a",
			pageSize: 50,
			offset:   0,
			status:   http.StatusBadRequest,
			body:     `invalid request`,
			err:      "invalid request",
		},
		{
			name:     "Case 4: malformed JSON",
			region:   "region-a",
			pageSize: 50,
			offset:   0,
			status:   http.StatusOK,
			body:     `not valid json`,
			err:      "invalid character",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotMethod, gotPath, gotAuth, gotOrg, gotRegion string
			var gotLimit, gotOffset string

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotMethod = r.Method
				gotPath = r.URL.Path
				gotAuth = r.Header.Get("Authorization")
				gotOrg = r.Header.Get("X-Organization")
				gotRegion = r.Header.Get("X-Region")
				gotLimit = r.URL.Query().Get("limit")
				gotOffset = r.URL.Query().Get("offset")

				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			}))
			t.Cleanup(server.Close)

			c := &nscaleClient{radarAPIURL: server.URL, org: "org1", token: "token1"}
			resp, err := c.Topology(context.Background(), tt.region, tt.pageSize, tt.offset)

			if len(tt.err) != 0 {
				require.Error(t, err)
				require.Contains(t, err.Error(), tt.err)
			} else {
				require.Nil(t, err)
				require.Equal(t, tt.expected, resp)
			}

			require.Equal(t, http.MethodGet, gotMethod)
			require.Equal(t, urlTopologyPath, gotPath)
			require.Equal(t, "Bearer token1", gotAuth)
			require.Equal(t, "org1", gotOrg)
			require.Equal(t, tt.region, gotRegion)
			require.Equal(t, strconv.Itoa(tt.pageSize), gotLimit)
			require.Equal(t, strconv.Itoa(tt.offset), gotOffset)
		})
	}
}

func TestNscaleClientTopologyCanceledContext(t *testing.T) {
	started := make(chan struct{})
	unblock := make(chan struct{})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(started)
		<-unblock
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"results": []}`))
	}))
	t.Cleanup(func() {
		close(unblock)
		server.Close()
	})

	c := &nscaleClient{radarAPIURL: server.URL, org: "org1", token: "token1"}

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		_, err := c.Topology(ctx, "region-a", 50, 0)
		done <- err
	}()

	<-started
	cancel()

	select {
	case err := <-done:
		require.Error(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("Topology did not return after context cancellation")
	}
}

func fakeMetadata() map[string]*imdsMetadata {
	return map[string]*imdsMetadata{
		"node-1": {Meta: map[string]string{"serverID": "srv-1"}},
	}
}

func TestFetchIMDSMetadataCachesRepeatedIdenticalNodes(t *testing.T) {
	var calls int32
	p := &baseProvider{
		params: &ProviderParams{},
		imdsFetch: func(_ context.Context, _ []string, _ string) (map[string]*imdsMetadata, error) {
			atomic.AddInt32(&calls, 1)
			return fakeMetadata(), nil
		},
	}
	nodes := []string{"node-1"}

	data1, err := p.fetchIMDSMetadata(context.Background(), nodes)
	require.NoError(t, err)
	data2, err := p.fetchIMDSMetadata(context.Background(), nodes)
	require.NoError(t, err)

	require.Equal(t, data1, data2)
	require.Equal(t, int32(1), atomic.LoadInt32(&calls))

	_, err = p.fetchIMDSMetadata(context.Background(), []string{"node-2"})
	require.NoError(t, err)
	require.Equal(t, int32(2), atomic.LoadInt32(&calls))
}

func TestFetchIMDSMetadataConcurrentIdenticalNodesDedup(t *testing.T) {
	var calls int32
	started := make(chan struct{})
	unblock := make(chan struct{})

	p := &baseProvider{
		params: &ProviderParams{},
		imdsFetch: func(_ context.Context, _ []string, _ string) (map[string]*imdsMetadata, error) {
			atomic.AddInt32(&calls, 1)
			close(started)
			<-unblock
			return fakeMetadata(), nil
		},
	}
	nodes := []string{"node-1"}

	type result struct {
		data map[string]*imdsMetadata
		err  error
	}
	results := make(chan result, 2)

	go func() {
		data, err := p.fetchIMDSMetadata(context.Background(), nodes)
		results <- result{data, err}
	}()

	<-started // owner is in-flight, holding no lock, blocked on unblock

	go func() {
		data, err := p.fetchIMDSMetadata(context.Background(), nodes)
		results <- result{data, err}
	}()

	close(unblock)

	for i := 0; i < 2; i++ {
		select {
		case r := <-results:
			require.NoError(t, r.err)
			require.Equal(t, fakeMetadata(), r.data)
		case <-time.After(5 * time.Second):
			t.Fatal("fetchIMDSMetadata did not return")
		}
	}

	require.Equal(t, int32(1), atomic.LoadInt32(&calls))
}

func TestFetchIMDSMetadataWaiterCanceledContext(t *testing.T) {
	var calls int32
	started := make(chan struct{})
	unblock := make(chan struct{})

	p := &baseProvider{
		params: &ProviderParams{},
		imdsFetch: func(_ context.Context, _ []string, _ string) (map[string]*imdsMetadata, error) {
			atomic.AddInt32(&calls, 1)
			close(started)
			<-unblock
			return fakeMetadata(), nil
		},
	}
	nodes := []string{"node-1"}

	ownerDone := make(chan struct{})
	go func() {
		_, _ = p.fetchIMDSMetadata(context.Background(), nodes)
		close(ownerDone)
	}()

	<-started // owner is in-flight, blocked on unblock

	waiterCtx, cancel := context.WithCancel(context.Background())
	waiterErrCh := make(chan error, 1)
	go func() {
		_, err := p.fetchIMDSMetadata(waiterCtx, nodes)
		waiterErrCh <- err
	}()

	cancel()

	select {
	case err := <-waiterErrCh:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(5 * time.Second):
		t.Fatal("waiter did not return promptly after ctx cancellation")
	}

	// The canceled waiter must not have disturbed the owner's in-flight load.
	require.Equal(t, int32(1), atomic.LoadInt32(&calls))

	close(unblock)
	select {
	case <-ownerDone:
	case <-time.After(5 * time.Second):
		t.Fatal("owner fetch did not complete")
	}

	data, err := p.fetchIMDSMetadata(context.Background(), nodes)
	require.NoError(t, err)
	require.Equal(t, fakeMetadata(), data)
	require.Equal(t, int32(1), atomic.LoadInt32(&calls))
}

func TestFetchIMDSMetadataFailureClearsInFlight(t *testing.T) {
	var calls int32
	wantErr := errors.New("pdsh failed")

	p := &baseProvider{
		params: &ProviderParams{},
		imdsFetch: func(_ context.Context, _ []string, _ string) (map[string]*imdsMetadata, error) {
			if atomic.AddInt32(&calls, 1) == 1 {
				return nil, wantErr
			}
			return fakeMetadata(), nil
		},
	}
	nodes := []string{"node-1"}

	_, err := p.fetchIMDSMetadata(context.Background(), nodes)
	require.ErrorIs(t, err, wantErr)
	require.Nil(t, p.imdsInFlight)
	require.Nil(t, p.imdsData)

	data, err := p.fetchIMDSMetadata(context.Background(), nodes)
	require.NoError(t, err)
	require.Equal(t, fakeMetadata(), data)
	require.Equal(t, int32(2), atomic.LoadInt32(&calls))
}

func TestProviderInstanceAndRegionMapsFilterByRegion(t *testing.T) {
	p := &Provider{
		baseProvider: baseProvider{
			params: &ProviderParams{},
			creds:  &Credentials{Region: "region-a"},
			imdsFetch: func(_ context.Context, _ []string, _ string) (map[string]*imdsMetadata, error) {
				return map[string]*imdsMetadata{
					"node-1": {Meta: map[string]string{"serverID": "srv-1", "regionID": "region-a"}},
					"node-2": {Meta: map[string]string{"serverID": "srv-2", "regionID": "region-b"}},
				}, nil
			},
		},
	}
	nodes := []string{"node-1", "node-2"}

	i2n, err := p.Instances2NodeMap(context.Background(), nodes)
	require.NoError(t, err)
	require.Equal(t, map[string]string{"srv-1": "node-1"}, i2n)

	nodeRegions, err := p.GetInstancesRegions(context.Background(), nodes)
	require.NoError(t, err)
	require.Equal(t, map[string]string{"node-1": "region-a"}, nodeRegions)
}
