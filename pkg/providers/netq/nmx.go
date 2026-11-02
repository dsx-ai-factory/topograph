// SPDX-FileCopyrightText: Copyright (c) 2025 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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

package netq

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"

	"k8s.io/klog/v2"

	"github.com/dsx-ai-factory/topograph/internal/httperr"
	"github.com/dsx-ai-factory/topograph/internal/httpreq"
	"github.com/dsx-ai-factory/topograph/pkg/topology"
)

const (
	ComputeURL = "nmx/v1/compute-nodes"
)

type ComputeNode struct {
	Id         string `json:"ID"`
	Name       string `json:"Name"`
	DomainUUID string `json:"DomainUUID"`
}

func (p *Provider) getNvlDomains(ctx context.Context) (topology.DomainMap, *httperr.Error) {
	auth := p.creds.Username + ":" + p.creds.Password
	authHeader := "Basic " + base64.StdEncoding.EncodeToString([]byte(auth))
	headers := map[string]string{"Authorization": authHeader}

	f := httpreq.GetRequestFunc(ctx, http.MethodGet, headers, nil, nil, p.params.ApiURL, ComputeURL)
	_, data, err := httpreq.DoRequest(f, true)
	if err != nil {
		return nil, err
	}

	return parseComputeNodes(data)
}

func parseComputeNodes(data []byte) (topology.DomainMap, *httperr.Error) {
	var computeNodes []ComputeNode
	err := json.Unmarshal(data, &computeNodes)
	if err != nil {
		return nil, httperr.NewError(http.StatusBadGateway, fmt.Sprintf("nmx output read failed: %v", err))
	}

	domainMap := topology.NewDomainMap()
	for _, node := range computeNodes {
		klog.V(4).Infof("Add NVL domain %q for node %q", node.DomainUUID, node.Name)
		domainMap.AddHost(node.DomainUUID, node.Name, node.Name)
	}

	return domainMap, nil
}
