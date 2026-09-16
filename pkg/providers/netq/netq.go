/*
 * Copyright 2025-2026 NVIDIA CORPORATION
 * SPDX-License-Identifier: Apache-2.0
 */

package netq

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"k8s.io/klog/v2"

	"github.com/dsx-ai-factory/topograph/internal/httperr"
	"github.com/dsx-ai-factory/topograph/internal/httpreq"
	"github.com/dsx-ai-factory/topograph/pkg/topology"
)

const (
	LoginURL    = "api/netq/auth/v1/login"
	OpIdURL     = "api/netq/auth/v1/select/opid"
	TopologyURL = "api/netq/telemetry/v1/object/topologygraph/fetch-topology"
)

type NetqResponse struct {
	Links []Links `json:"links"`
	Nodes []Nodes `json:"nodes"`
}

type Nodes struct {
	Cnode []CNode `json:"compounded_nodes"`
}

type CNode struct {
	Id   string `json:"id"`
	Name string `json:"name"`
	Tier int    `json:"tier"`
}

type Links struct {
	Id string `json:"id"`
}

type AuthOutput struct {
	AccessToken string     `json:"access_token"`
	Premises    []Premises `json:"premises"`
}

type Premises struct {
	ConfigKeyViewed bool   `json:"config_key_viewed"`
	Name            string `json:"name"`
	Namespace       string `json:"namespace"`
	OPID            int    `json:"opid"`
}

func (p *Provider) getNetworkTree(ctx context.Context, cis []topology.ComputeInstances) (*topology.Vertex, *httperr.Error) {
	// login to NetQ server
	payload := fmt.Appendf(nil, `{"username":%q, "password":%q}`, p.creds.Username, p.creds.Password)
	headers := map[string]string{
		"Content-Type": "application/json",
		"Accept":       "application/json",
	}
	f := httpreq.GetRequestFunc(ctx, http.MethodPost, headers, nil, payload, p.params.ApiURL, LoginURL)
	_, data, httpErr := httpreq.DoRequest(f, true)
	if httpErr != nil {
		return nil, httpErr
	}

	if len(data) == 0 {
		return nil, httperr.NewError(http.StatusUnauthorized, "failed to login to NetQ server")
	}

	// get access token and premises
	var authOutput AuthOutput
	if err := json.Unmarshal(data, &authOutput); err != nil {
		return nil, httperr.NewError(http.StatusBadGateway, fmt.Sprintf("failed to parse access token: %v", err))
	}

	treeRoot := &topology.Vertex{Vertices: make(map[string]*topology.Vertex)}
	for _, premises := range authOutput.Premises {
		if premises.ConfigKeyViewed {
			klog.InfoS("Getting topology graph for premises", "name", premises.Name, "OPID", premises.OPID)
			if httpErr = p.getPremisesTopology(ctx, cis, treeRoot, authOutput.AccessToken, fmt.Sprintf("%d", premises.OPID)); httpErr != nil {
				return nil, httpErr
			}
		}
	}

	if len(treeRoot.Vertices) == 0 {
		return nil, httperr.NewError(http.StatusBadGateway, "no topology available from the provided premises")
	}

	return treeRoot, nil
}

func (p *Provider) getPremisesTopology(ctx context.Context, cis []topology.ComputeInstances, treeRoot *topology.Vertex, token, opid string) *httperr.Error {
	// set OpID
	headers := map[string]string{
		"Authorization": "Bearer " + token,
	}
	f := httpreq.GetRequestFunc(ctx, http.MethodGet, headers, nil, nil, p.params.ApiURL, OpIdURL, opid)
	_, data, httpErr := httpreq.DoRequest(f, true)
	if httpErr != nil {
		return httpErr
	}

	if len(data) == 0 {
		return httperr.NewError(http.StatusBadGateway, "failed to set NetQ OpID")
	}

	// get access token
	var authOutput AuthOutput
	if err := json.Unmarshal(data, &authOutput); err != nil {
		return httperr.NewError(http.StatusBadGateway, fmt.Sprintf("failed to parse access token: %v", err))
	}

	// get topology graph
	payload := []byte(`{"filters": [], "subgroupNestingDepth":2}`)
	headers = map[string]string{
		"Content-Type":  "application/json",
		"Authorization": "Bearer " + authOutput.AccessToken,
	}
	query := map[string]string{"timestamp": "0"}
	f = httpreq.GetRequestFunc(ctx, http.MethodPost, headers, query, payload, p.params.ApiURL, TopologyURL)
	_, data, httpErr = httpreq.DoRequest(f, true)
	if httpErr != nil {
		return httpErr
	}

	return parseNetq(treeRoot, data, topology.GetNodeNameMap(cis))
}

// parseNetq parses Netq topology output
func parseNetq(treeRoot *topology.Vertex, data []byte, inputNodes map[string]bool) *httperr.Error {
	var resp []NetqResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return httperr.NewError(http.StatusBadGateway, fmt.Sprintf("netq output read failed: %v", err))
	}

	if len(resp) != 1 {
		return httperr.NewError(http.StatusBadGateway, "invalid NetQ response: multiple entries")
	}

	layer := make(map[string]*topology.Vertex)   // current layer starting from leaves (nodeId : Vertex)
	nodeMap := make(map[string]*topology.Vertex) // nodeId : Vertex
	tierMap := make(map[string]int)              // nodeId : tier
	nameMap := make(map[string]string)           // nodeId : nodeName

	// split nodes between leaves and switches
	for _, nodelist := range resp[0].Nodes {
		for _, cnode := range nodelist.Cnode {
			klog.V(4).InfoS("NetQ node", "tier", cnode.Tier, "name", cnode.Name, "id", cnode.Id)
			v := &topology.Vertex{
				ID:   cnode.Id,
				Name: cnode.Name,
			}
			if cnode.Tier == -1 { // leaf
				if inputNodes[cnode.Name] {
					layer[cnode.Id] = v
				}
			} else { // switch
				nodeMap[cnode.Id] = v
			}
			tierMap[cnode.Id] = cnode.Tier
			nameMap[cnode.Id] = cnode.Name
		}
	}

	// create map of link IDs [lower node : upper nodes]
	linksUp := make(map[string]map[string]bool)
	for _, link := range resp[0].Links {
		nodeIDs := strings.Split(link.Id, "-*-")
		if len(nodeIDs) != 2 {
			klog.Warningf("invalid link ID %q", link.Id)
			continue
		}
		nodeLow, nodeHigh := nodeIDs[0], nodeIDs[1]

		if tierMap[nodeLow] == tierMap[nodeHigh] {
			klog.Warningf("invalid link ID %q: nodes belong to the same tier %d", link.Id, tierMap[nodeLow])
			continue
		}

		if tierMap[nodeLow] > tierMap[nodeHigh] {
			nodeLow, nodeHigh = nodeHigh, nodeLow
		}

		up, ok := linksUp[nodeLow]
		if !ok {
			up = make(map[string]bool)
			linksUp[nodeLow] = up
		}
		up[nodeHigh] = true
	}

	for {
		count := len(nodeMap)
		nextLayer := make(map[string]*topology.Vertex)
		for id, w := range layer {
			for up := range linksUp[id] {
				v, ok := nextLayer[up]
				if !ok {
					v = nodeMap[up]
					if v == nil {
						klog.Warningf("node ID %q not found", up)
						continue
					}
					v.Vertices = make(map[string]*topology.Vertex)
					nextLayer[up] = v
					delete(nodeMap, up)
				}
				v.Vertices[id] = w
			}
		}

		if count == len(nodeMap) {
			break
		}
		layer = nextLayer
	}

	var top []*topology.Vertex
	for _, node := range layer {
		top = append(top, node)
	}

	// Ethernet Spectrum-X may have CLOS network and may require merging of switches to a tree format
	merger := topology.NewMerger(top)
	merger.Merge()
	top = merger.TopTier()

	for _, node := range top {
		treeRoot.Vertices[node.ID] = node
	}

	return nil
}
