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

package netq

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/dsx-ai-factory/topograph/internal/httpauth"
	"github.com/dsx-ai-factory/topograph/internal/httperr"
	"github.com/dsx-ai-factory/topograph/internal/httpreq"
)

// acquireNetqToken delegates HTTP execution and access-token extraction to the
// shared helper. NetQ reads premises metadata and retains its transport settings.
func acquireNetqToken(factory httpreq.RequestFunc) (string, []Premises, *httperr.Error) {
	var metadata struct {
		Premises []Premises `json:"premises"`
	}
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}
	defer client.CloseIdleConnections()
	token, body, err := httpauth.AcquireBearerToken(factory, "/access_token", client)
	if err != nil {
		return "", nil, err
	}
	if err := json.Unmarshal(body, &metadata); err != nil {
		return "", nil, httperr.NewError(http.StatusBadGateway, fmt.Sprintf("failed to parse premises metadata: %v", err))
	}
	return token, metadata.Premises, nil
}
