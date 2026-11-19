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
	"net/http"
	"strings"
	"time"

	"github.com/dsx-ai-factory/topograph/internal/httperr"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/clientcredentials"
)

// TokenSource acquires and reuses OAuth2 tokens within one discovery invocation.
// Construct a new source for every invocation/credential set. Calls are serialized
// and honor their own context, including while waiting for another acquisition.
// Tokens and upstream errors are never logged; callers receive sanitized errors.
type TokenSource struct {
	config clientcredentials.Config
	client *http.Client
	gate   chan struct{}
	token  *oauth2.Token
}

// NewClientCredentialsSource validates configuration without network I/O. The
// supplied client controls TLS, transport and timeout settings; a shallow copy
// disables redirects so credentials cannot be forwarded to another endpoint.
// Client and transport settings must not be mutated concurrently with requests.
// No local retries are added; any custom transport must follow the caller's policy.
// Invalid configuration returns an HTTP 400 error.
func NewClientCredentialsSource(auth AuthConfig, creds Credentials, client *http.Client) (*TokenSource, *httperr.Error) {
	if auth.Type != "oauth2" || auth.OAuth2 == nil || auth.Bearer != nil {
		return nil, httperr.NewError(http.StatusBadRequest, "OAuth2 requires type oauth2 and only the oauth2 configuration block")
	}
	config := *auth.OAuth2
	var authStyle oauth2.AuthStyle
	switch config.AuthStyle {
	case "", "basic":
		authStyle = oauth2.AuthStyleInHeader
	case "form":
		authStyle = oauth2.AuthStyleInParams
	default:
		return nil, httperr.NewError(http.StatusBadRequest, "OAuth2 authStyle must be basic or form")
	}
	if !validTokenURL(config.TokenURL) {
		return nil, httperr.NewError(http.StatusBadRequest, "OAuth2 token URL must be an absolute HTTPS URL without user information or fragment")
	}
	if strings.TrimSpace(creds["clientId"]) == "" {
		return nil, httperr.NewError(http.StatusBadRequest, "OAuth2 requires a nonempty clientId credential")
	}
	if strings.TrimSpace(creds["clientSecret"]) == "" {
		return nil, httperr.NewError(http.StatusBadRequest, "OAuth2 requires a nonempty clientSecret credential")
	}
	if client == nil {
		return nil, httperr.NewError(http.StatusBadRequest, "OAuth2 requires an explicit HTTP client")
	}
	for _, scope := range config.Scopes {
		if scope == "" {
			return nil, httperr.NewError(http.StatusBadRequest, "invalid OAuth2 scope")
		}
		for _, char := range scope {
			// RFC 6749 scope-token excludes whitespace, quotes and backslashes.
			if char < 0x21 || char > 0x7e || char == '"' || char == '\\' {
				return nil, httperr.NewError(http.StatusBadRequest, "invalid OAuth2 scope")
			}
		}
	}
	return &TokenSource{
		config: clientcredentials.Config{
			ClientID:     creds["clientId"],
			ClientSecret: creds["clientSecret"],
			TokenURL:     config.TokenURL,
			Scopes:       append([]string(nil), config.Scopes...),
			AuthStyle:    authStyle,
		},
		client: tokenHTTPClient(client),
		gate:   make(chan struct{}, 1),
	}, nil
}

// Token returns a copy of the cached token or acquires a new one. Tokens are
// renewed using client credentials before expiry (oauth2.Token.Valid's safety
// margin); refresh tokens are never used. An omitted expiry lasts for this source.
// Errors preserve upstream HTTP failure statuses, use 502 for protocol/transport
// failures, 408 for cancellation, and 504 for timeouts, without upstream details.
func (s *TokenSource) Token(ctx context.Context) (*oauth2.Token, *httperr.Error) {
	if err := ctx.Err(); err != nil {
		return nil, tokenRequestError(err, "OAuth2")
	}
	select {
	case s.gate <- struct{}{}:
		defer func() { <-s.gate }()
	case <-ctx.Done():
		return nil, tokenRequestError(ctx.Err(), "OAuth2")
	}
	if err := ctx.Err(); err != nil {
		return nil, tokenRequestError(err, "OAuth2")
	}
	if s.token.Valid() {
		token := *s.token
		return &token, nil
	}
	token, err := s.config.Token(context.WithValue(ctx, oauth2.HTTPClient, s.client))
	if err != nil {
		if ctx.Err() != nil {
			return nil, tokenRequestError(ctx.Err(), "OAuth2")
		}
		// oauth2 errors can contain response bodies, URLs, and credentials.
		return nil, tokenRequestError(err, "OAuth2")
	}
	if err := ValidateBearerToken(token.AccessToken, token.TokenType); err != nil ||
		(!token.Expiry.IsZero() && !token.Expiry.After(time.Now())) {
		return nil, httperr.NewError(http.StatusBadGateway, "invalid OAuth2 token response")
	}
	// Retain only the fields needed for bearer authentication. In particular,
	// discard refresh tokens and raw response extras before caching or returning.
	s.token = &oauth2.Token{AccessToken: token.AccessToken, TokenType: "Bearer", Expiry: token.Expiry}
	result := *s.token
	return &result, nil
}
