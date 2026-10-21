/*
 * Copyright 2026 NVIDIA CORPORATION
 * SPDX-License-Identifier: Apache-2.0
 */

package nautobot

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"k8s.io/klog/v2"

	"github.com/dsx-ai-factory/topograph/internal/config"
	"github.com/dsx-ai-factory/topograph/internal/httperr"
	"github.com/dsx-ai-factory/topograph/pkg/providers"
	"github.com/dsx-ai-factory/topograph/pkg/topology"
)

const (
	NAME            = "nautobot"
	defaultPageSize = 100
)

type ProviderParams struct {
	Endpoint        string   `mapstructure:"endpoint"`
	Location        []string `mapstructure:"location"`
	Platform        []string `mapstructure:"platform"`
	DeviceRole      []string `mapstructure:"deviceRole"`
	SwitchRoles     []string `mapstructure:"switchRoles"`
	ExcludeStatuses []string `mapstructure:"excludeStatuses"`
	TrimTiers       int      `mapstructure:"trimTiers"`
}

type ProviderCreds struct {
	Token string `mapstructure:"token"`
}

type Provider struct {
	params ProviderParams
	creds  ProviderCreds
	client *http.Client
}

func NamedLoader() (string, providers.Loader) { return NAME, Loader }

func Loader(_ context.Context, cfg providers.Config) (providers.Provider, *httperr.Error) {
	params := ProviderParams{}
	if err := config.Decode(cfg.Params, &params); err != nil {
		return nil, httperr.NewError(http.StatusBadRequest, fmt.Sprintf("invalid Nautobot parameters: %v", err))
	}
	if _, configured := cfg.Params["excludeStatuses"]; !configured {
		params.ExcludeStatuses = []string{"Deleted"}
	}
	u, err := url.Parse(params.Endpoint)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, httperr.NewError(http.StatusBadRequest, "endpoint must be an absolute HTTP(S) URL without credentials, query, or fragment")
	}
	if !strings.HasSuffix(strings.TrimRight(u.Path, "/"), "/api/graphql") {
		u.Path = strings.TrimRight(u.Path, "/") + "/api/graphql/"
	} else {
		u.Path = strings.TrimRight(u.Path, "/") + "/"
	}
	params.Endpoint = u.String()
	if len(params.DeviceRole) == 0 {
		return nil, httperr.NewError(http.StatusBadRequest, "deviceRole must select at least one compute role")
	}
	if len(params.SwitchRoles) == 0 {
		return nil, httperr.NewError(http.StatusBadRequest, "switchRoles must select at least one switch role")
	}
	roles := make(map[string]bool)
	for _, role := range params.DeviceRole {
		if strings.TrimSpace(role) == "" {
			return nil, httperr.NewError(http.StatusBadRequest, "deviceRole names must not be blank")
		}
		roles[role] = true
	}
	for _, role := range params.SwitchRoles {
		if strings.TrimSpace(role) == "" || roles[role] {
			return nil, httperr.NewError(http.StatusBadRequest, "switchRoles must contain non-blank roles distinct from compute roles")
		}
	}
	if params.TrimTiers < 0 {
		return nil, httperr.NewError(http.StatusBadRequest, "trimTiers must be non-negative")
	}
	token, valid := cfg.Creds["token"].(string)
	if !valid || strings.TrimSpace(token) == "" || strings.ContainsAny(token, "\r\n") {
		return nil, httperr.NewError(http.StatusBadRequest, "creds.token must be a non-empty string without line breaks")
	}
	return &Provider{
		params: params, creds: ProviderCreds{Token: token},
		client: &http.Client{
			Timeout:       120 * time.Second,
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse },
		},
	}, nil
}

type device struct {
	ID         string       `json:"id"`
	Name       string       `json:"name"`
	Serial     string       `json:"serial"`
	Status     *namedObject `json:"status"`
	Role       *namedObject `json:"role"`
	Interfaces []struct {
		Peer *struct {
			Device *device `json:"device"`
		} `json:"cable_peer_interface"`
	} `json:"interfaces"`
}

type namedObject struct {
	Name string `json:"name"`
}

// deviceQuery selects switches by role/location/platform, or expands missing connected
// switches by UUID and role. Compute endpoints come from switch cable peers.
// JSON string encoding of filter values also produces valid GraphQL strings.
func (p *Provider) deviceQuery(ids []string, limit, offset int) string {
	locations := p.params.Location
	platforms := p.params.Platform
	if ids != nil {
		locations = nil
		platforms = nil
	}
	filters := []string{fmt.Sprintf("limit: %d", limit), fmt.Sprintf("offset: %d", offset)}
	for _, filter := range []struct {
		key    string
		values []string
	}{
		{"location", locations}, {"platform", platforms}, {"role", p.params.SwitchRoles}, {"status__n", p.params.ExcludeStatuses}, {"id", ids},
	} {
		if len(filter.values) != 0 {
			value, _ := json.Marshal(filter.values)
			filters = append(filters, filter.key+": "+string(value))
		}
	}
	return `query { devices(` + strings.Join(filters, ", ") + `) {
  id name serial status { name } role { name }
  interfaces(has_cable: true) {
    cable_peer_interface { device { id name serial status { name } role { name } } }
  }
} }`
}

// devices collects every page before discovery follows peers. Duplicate IDs
// fail rather than allowing ignored offsets to loop or silently truncate data.
func (p *Provider) devices(ctx context.Context, ids []string, limit int) ([]device, *httperr.Error) {
	devices := []device{}
	seen := map[string]bool{}
	for offset := 0; ; {
		page, httpErr := p.devicePage(ctx, ids, limit, offset)
		if httpErr != nil {
			return nil, httpErr
		}
		if len(page) > limit {
			klog.Warningf("Nautobot returned %d devices at offset %d, exceeding the requested page limit %d; continuing", len(page), offset, limit)
		}
		for _, d := range page {
			if d.ID == "" || d.Name == "" {
				klog.Warningf("Nautobot returned a device with an empty ID or name at offset %d (ID=%q, name=%q); skipping", offset, d.ID, d.Name)
				continue
			}
			if seen[d.ID] {
				return nil, httperr.NewError(http.StatusBadGateway, fmt.Sprintf("Nautobot returned duplicate device ID %q during pagination", d.ID))
			}
			seen[d.ID] = true
			devices = append(devices, d)
		}
		if len(page) < limit || (ids != nil && len(devices) >= len(ids)) {
			return devices, nil
		}
		offset += len(page)
	}
}

func (p *Provider) devicePage(ctx context.Context, ids []string, limit, offset int) ([]device, *httperr.Error) {
	payload, _ := json.Marshal(map[string]string{"query": p.deviceQuery(ids, limit, offset)})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.params.Endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, httperr.NewError(http.StatusBadGateway, "cannot create Nautobot request")
	}
	req.Header.Set("Authorization", "Token "+p.creds.Token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, httperr.NewError(http.StatusBadGateway, fmt.Sprintf("Nautobot GraphQL request failed: %v", err))
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, httperr.NewError(http.StatusBadGateway, fmt.Sprintf("Nautobot GraphQL returned HTTP %d", resp.StatusCode))
	}
	var result struct {
		Data *struct {
			Devices *[]device `json:"devices"`
		} `json:"data"`
		Errors []json.RawMessage `json:"errors"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, httperr.NewError(http.StatusBadGateway, "invalid Nautobot GraphQL response")
	}
	if len(result.Errors) != 0 {
		return nil, httperr.NewError(http.StatusBadGateway, "Nautobot GraphQL returned errors; check the query schema and token permissions")
	}
	if result.Data == nil || result.Data.Devices == nil {
		return nil, httperr.NewError(http.StatusBadGateway, "Nautobot GraphQL returned no devices data")
	}
	return *result.Data.Devices, nil
}

func (p *Provider) GenerateTopologyConfig(ctx context.Context, pageSize *int, instances []topology.ComputeInstances) (*topology.Graph, *httperr.Error) {
	devices, httpErr := p.discover(ctx, pageSize)
	if httpErr != nil {
		return nil, httpErr
	}
	cluster, err := p.buildTopology(devices, instances)
	if err != nil {
		return nil, httperr.NewError(http.StatusBadGateway, err.Error())
	}
	return cluster.ToGraph(NAME, instances, p.params.TrimTiers, false), nil
}

// Instances2NodeMap implements Slurm hostname-based discovery.
func (p *Provider) Instances2NodeMap(_ context.Context, nodes []string) (map[string]string, error) {
	result := make(map[string]string, len(nodes))
	for _, node := range nodes {
		result[node] = node
	}
	return result, nil
}

func (p *Provider) GetInstancesRegions(_ context.Context, nodes []string) (map[string]string, error) {
	result := make(map[string]string, len(nodes))
	for _, node := range nodes {
		result[node] = "local"
	}
	return result, nil
}
