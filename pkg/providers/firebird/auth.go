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
	"net/http"

	"github.com/dsx-ai-factory/topograph/internal/httpauth"
	"github.com/dsx-ai-factory/topograph/internal/httperr"
	"github.com/dsx-ai-factory/topograph/internal/httpreq"
)

const authTokenPath = "/auth/token"

// accessToken configures shared acquisition for Firebird's custom login format.
// The shared helper owns request execution, retries and token validation.
func (p *Provider) accessToken(ctx context.Context) (string, *httperr.Error) {
	endpoint, err := httpreq.GetURL(p.params.APIURL, nil, authTokenPath)
	if err != nil {
		return "", err
	}
	credentials := p.creds.ToMap()
	credentials["grantType"] = "client_credentials"
	auth := httpauth.AuthConfig{Type: "bearer", TokenRequest: &httpauth.TokenRequestConfig{
		URL:          endpoint,
		TokenPointer: "/accessToken",
	}}
	return httpauth.AcquireToken(ctx, auth, credentials, http.DefaultClient)
}
