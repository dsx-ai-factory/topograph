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

package firebird

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/dsx-ai-factory/topograph/internal/httperr"
	"github.com/dsx-ai-factory/topograph/internal/httpreq"
)

const authTokenPath = "/auth/token"

type tokenRequest struct {
	GrantType    string `json:"grantType"`
	ClientID     string `json:"clientId"`
	ClientSecret string `json:"clientSecret"`
}

// Only the access token is retained for the duration of topology generation.
// ID and refresh tokens are not used.
type tokenResponse struct {
	AccessToken string `json:"accessToken"`
	ExpiresIn   int64  `json:"expiresIn"`
	TokenType   string `json:"tokenType"`
}

// accessToken obtains a fresh token for one topology generation request.
func (p *Provider) accessToken(ctx context.Context) (string, *httperr.Error) {
	payload, err := json.Marshal(tokenRequest{
		GrantType:    "client_credentials",
		ClientID:     p.creds.ClientID,
		ClientSecret: p.creds.ClientSecret,
	})
	if err != nil {
		return "", httperr.NewError(http.StatusBadRequest, "invalid Firebird client credentials")
	}
	headers := map[string]string{"Accept": "application/json", "Content-Type": "application/json"}
	f := httpreq.GetRequestFunc(ctx, http.MethodPost, headers, nil, payload, p.params.APIURL, authTokenPath)
	body, httpErr := httpreq.DoRequestWithRetries(f, false)
	if httpErr != nil {
		if ctx.Err() != nil {
			return "", httperr.NewError(http.StatusBadGateway, "Firebird authentication request canceled")
		}
		return "", firebirdAPIError(httpErr.Code(), body, "authentication")
	}
	var response tokenResponse
	if err := json.Unmarshal(body, &response); err != nil || strings.TrimSpace(response.AccessToken) == "" ||
		strings.ContainsAny(response.AccessToken, "\r\n") || response.ExpiresIn < 0 ||
		(response.TokenType != "" && !strings.EqualFold(response.TokenType, "Bearer")) {
		return "", httperr.NewError(http.StatusBadGateway, "invalid Firebird authentication response")
	}
	return response.AccessToken, nil
}
