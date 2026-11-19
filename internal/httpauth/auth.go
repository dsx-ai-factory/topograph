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

package httpauth

import (
	"context"
	"errors"
	"net/http"
	"net/url"

	"github.com/dsx-ai-factory/topograph/internal/httperr"
	"github.com/dsx-ai-factory/topograph/internal/httpreq"
	"golang.org/x/oauth2"
)

// AcquireToken returns an access-token string using OAuth2 client credentials
// (type oauth2) or a custom login request (type bearer). Configuration and
// credentials are validated before network I/O. Each call is independent.
// Bearer login uses the shared HTTP retry policy; OAuth2 uses its library's token
// exchange. HTTP errors are returned directly and malformed tokens return 502.
func AcquireToken(ctx context.Context, auth AuthConfig, creds Credentials, client *http.Client) (string, *httperr.Error) {
	switch auth.Type {
	case "oauth2":
		source, err := NewClientCredentialsSource(auth, creds, client)
		if err != nil {
			return "", err
		}
		token, err := source.Token(ctx)
		if err != nil {
			return "", err
		}
		return token.AccessToken, nil
	case "bearer":
		return acquireConfiguredBearerToken(ctx, auth, creds, client)
	default:
		return "", httperr.NewError(http.StatusBadRequest, "token acquisition requires auth type oauth2 or bearer")
	}
}

func validTokenURL(rawURL string) bool {
	u, err := url.Parse(rawURL)
	return err == nil && u.Scheme == "https" && u.Hostname() != "" && u.User == nil && u.Fragment == ""
}

func tokenHTTPClient(client *http.Client) *http.Client {
	c := *client
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &c
}

// tokenRequestError translates library failures without retaining their bodies,
// URLs, or credential-bearing messages. RetrieveError stays inside this helper.
func tokenRequestError(err error, kind string) *httperr.Error {
	switch code, _ := httpreq.CancellationStatus(err); code {
	case http.StatusRequestTimeout:
		return httperr.NewError(code, kind+" token request canceled")
	case http.StatusGatewayTimeout:
		return httperr.NewError(code, kind+" token request timed out")
	}
	code := http.StatusBadGateway
	var retrieve *oauth2.RetrieveError
	if errors.As(err, &retrieve) && retrieve.Response != nil &&
		retrieve.Response.StatusCode >= 400 && retrieve.Response.StatusCode <= 599 {
		code = retrieve.Response.StatusCode
	}
	return httperr.NewError(code, kind+" token acquisition failed")
}
