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

package http

import (
	"context"
	nethttp "net/http"
	"net/url"
	"strings"
	"sync"

	"github.com/dsx-ai-factory/topograph/internal/httpauth"
	"github.com/dsx-ai-factory/topograph/internal/httperr"
)

const (
	authNone   = "none"
	authOAuth2 = "oauth2"
	authBearer = "bearer"
	authBasic  = "basic"
	authAPIKey = "apiKey"
)

// AuthParams is the provider.params.auth block. The oauth2 and bearer blocks
// reuse the shared httpauth configuration types.
type AuthParams struct {
	Type       string                      `mapstructure:"type"`
	OAuth2     *httpauth.OAuth2Config      `mapstructure:"oauth2"`
	Bearer     *httpauth.BearerLoginConfig `mapstructure:"bearer"`
	HeaderName string                      `mapstructure:"headerName"`
}

// authenticator supplies the authentication header for one outgoing request.
// A new authenticator is created for every discovery invocation, so tokens are
// reused across pages and retries but never across invocations.
type authenticator interface {
	header(ctx context.Context) (name, value string, err *httperr.Error)
}

// authFactory validates authentication configuration and credentials without
// network I/O. It returns the header name it sets and a constructor for
// per-invocation authenticators.
type authFactory func(params *AuthParams, creds httpauth.Credentials, client *nethttp.Client) (string, func() authenticator, *httperr.Error)

// authFactories maps auth.type to its factory. Adding an authentication
// mechanism means adding an entry here and rebuilding Topograph.
var authFactories = map[string]authFactory{
	authNone:   newNoneAuth,
	authOAuth2: newOAuth2Auth,
	authBearer: newBearerAuth,
	authBasic:  newBasicAuth,
	authAPIKey: newAPIKeyAuth,
}

// parseAuth decodes provider.params.auth. An absent or empty block selects none.
func parseAuth(raw map[string]any, creds httpauth.Credentials, client *nethttp.Client) (string, func() authenticator, *httperr.Error) {
	params := &AuthParams{Type: authNone}
	if len(raw) != 0 {
		params.Type = ""
		if err := decodeStrict(raw, params); err != nil {
			return "", nil, badRequest("invalid 'auth' parameters: %v", err)
		}
		if params.Type == "" {
			return "", nil, badRequest("'auth.type' is required when 'auth' is set")
		}
	}
	factory, ok := authFactories[params.Type]
	if !ok {
		return "", nil, badRequest("unsupported 'auth.type' %q", params.Type)
	}
	// Reject flow blocks that do not belong to the selected type.
	if params.OAuth2 != nil && params.Type != authOAuth2 {
		return "", nil, badRequest("'auth.oauth2' requires 'auth.type: oauth2'")
	}
	if params.Bearer != nil && params.Type != authBearer {
		return "", nil, badRequest("'auth.bearer' requires 'auth.type: bearer'")
	}
	if params.HeaderName != "" && params.Type != authAPIKey {
		return "", nil, badRequest("'auth.headerName' requires 'auth.type: apiKey'")
	}
	return factory(params, creds, client)
}

// credentialsFromConfig converts providers.Config.Creds, rejecting non-string values.
func credentialsFromConfig(raw map[string]any) (httpauth.Credentials, *httperr.Error) {
	creds := make(httpauth.Credentials, len(raw))
	for key, value := range raw {
		s, ok := value.(string)
		if !ok {
			// Name the key only; never format a credential value.
			return nil, badRequest("credential %q must be a string", key)
		}
		creds[key] = s
	}
	return creds, nil
}

func requireCreds(creds httpauth.Credentials, flow string, keys ...string) *httperr.Error {
	for _, key := range keys {
		if strings.TrimSpace(creds[key]) == "" {
			return badRequest("%s authentication requires a nonempty %q credential", flow, key)
		}
	}
	return nil
}

// staticAuth sets a fixed header value.
type staticAuth struct{ name, value string }

func (a *staticAuth) header(context.Context) (string, string, *httperr.Error) {
	return a.name, a.value, nil
}

func newNoneAuth(*AuthParams, httpauth.Credentials, *nethttp.Client) (string, func() authenticator, *httperr.Error) {
	return "", func() authenticator { return &staticAuth{} }, nil
}

func newBasicAuth(_ *AuthParams, creds httpauth.Credentials, _ *nethttp.Client) (string, func() authenticator, *httperr.Error) {
	if err := requireCreds(creds, "basic", "username", "password"); err != nil {
		return "", nil, err
	}
	value := httpauth.BasicHeader(creds["username"], creds["password"])
	return "Authorization", func() authenticator { return &staticAuth{name: "Authorization", value: value} }, nil
}

func newAPIKeyAuth(params *AuthParams, creds httpauth.Credentials, _ *nethttp.Client) (string, func() authenticator, *httperr.Error) {
	if !validHeaderName(params.HeaderName) {
		return "", nil, badRequest("'auth.headerName' must be a valid HTTP header name")
	}
	if err := requireCreds(creds, "apiKey", "apiKey"); err != nil {
		return "", nil, err
	}
	if !validHeaderValue(creds["apiKey"]) {
		return "", nil, badRequest("the \"apiKey\" credential contains invalid characters")
	}
	name, value := params.HeaderName, creds["apiKey"]
	return name, func() authenticator { return &staticAuth{name: name, value: value} }, nil
}

// oauth2Auth keeps one token source per invocation for reuse and renewal.
type oauth2Auth struct{ source *httpauth.TokenSource }

func (a *oauth2Auth) header(ctx context.Context) (string, string, *httperr.Error) {
	token, err := a.source.Token(ctx)
	if err != nil {
		return "", "", err
	}
	return "Authorization", httpauth.BearerHeader(token.AccessToken), nil
}

func newOAuth2Auth(params *AuthParams, creds httpauth.Credentials, client *nethttp.Client) (string, func() authenticator, *httperr.Error) {
	if params.OAuth2 == nil {
		return "", nil, badRequest("'auth.type: oauth2' requires the 'auth.oauth2' block")
	}
	cfg := httpauth.AuthConfig{Type: authOAuth2, OAuth2: params.OAuth2}
	// Validate configuration and credentials now; construction does no I/O.
	if _, err := httpauth.NewClientCredentialsSource(cfg, creds, client); err != nil {
		return "", nil, err
	}
	return "Authorization", func() authenticator {
		// Validated above, so construction cannot fail.
		source, _ := httpauth.NewClientCredentialsSource(cfg, creds, client)
		return &oauth2Auth{source: source}
	}, nil
}

// bearerAuth acquires a custom login token once per invocation and reuses it
// for every page and retry. Expiry is not inferred.
type bearerAuth struct {
	cfg    httpauth.AuthConfig
	creds  httpauth.Credentials
	client *nethttp.Client

	mu    sync.Mutex
	token string
}

func (a *bearerAuth) header(ctx context.Context) (string, string, *httperr.Error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.token == "" {
		token, err := httpauth.AcquireToken(ctx, a.cfg, a.creds, a.client)
		if err != nil {
			return "", "", err
		}
		a.token = token
	}
	return "Authorization", httpauth.BearerHeader(a.token), nil
}

func newBearerAuth(params *AuthParams, creds httpauth.Credentials, client *nethttp.Client) (string, func() authenticator, *httperr.Error) {
	b := params.Bearer
	if b == nil {
		return "", nil, badRequest("'auth.type: bearer' requires the 'auth.bearer' block")
	}
	u, err := url.Parse(b.URL)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Fragment != "" {
		return "", nil, badRequest("'auth.bearer.url' must be an absolute HTTPS URL without user information or fragment")
	}
	switch b.Method {
	case "", nethttp.MethodPost, nethttp.MethodPut:
	default:
		return "", nil, badRequest("'auth.bearer.method' must be POST or PUT")
	}
	switch b.Encoding {
	case "", "json", "form":
	default:
		return "", nil, badRequest("'auth.bearer.encoding' must be json or form")
	}
	if _, perr := parsePointer("auth.bearer.tokenPointer", b.TokenPointer, false); perr != nil {
		return "", nil, badRequest("'auth.bearer.tokenPointer' must be a nonempty JSON Pointer such as /access_token")
	}
	if len(creds) == 0 {
		return "", nil, badRequest("bearer authentication requires nonempty credentials (the login request body)")
	}
	cfg := httpauth.AuthConfig{Type: authBearer, Bearer: b}
	return "Authorization", func() authenticator {
		return &bearerAuth{cfg: cfg, creds: creds, client: client}
	}, nil
}
