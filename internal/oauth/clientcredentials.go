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

// Package oauth implements the OAuth2 client-credentials flow with OIDC
// discovery, for providers that call APIs behind an OIDC issuer.
package oauth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/mitchellh/mapstructure"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/clientcredentials"
)

// TokenFunc returns a Bearer access token. Implementations cache the token and
// refresh it as it expires, so it is cheap to call once per request.
type TokenFunc func(ctx context.Context) (string, error)

// authTimeout bounds each OIDC discovery request and each token request. The
// token request runs on a context detached from the caller's, so without this
// bound an unresponsive token endpoint would block every caller indefinitely.
const authTimeout = 30 * time.Second

// Config configures the client-credentials flow. The mapstructure tags are the
// credential keys, so a provider's credentials map (from the credentials file or
// the request payload) decodes into it directly with DecodeConfig.
type Config struct {
	ClientID     string `mapstructure:"client_id"`
	ClientSecret string `mapstructure:"client_secret"`
	// IssuerURL is the OIDC issuer. The token endpoint is read from its
	// discovery document. It is used only when TokenURL is empty.
	IssuerURL string `mapstructure:"issuer_url"`
	// TokenURL is the token endpoint. When set, discovery is skipped, which suits
	// identity providers that publish no discovery document.
	TokenURL string   `mapstructure:"token_url"`
	Scopes   []string `mapstructure:"scopes"`
}

// DecodeConfig decodes the client-credentials settings from a credentials map.
// Keys it does not know, such as a provider's own "token", are ignored. Scopes
// may be a list or a single space-separated string.
func DecodeConfig(creds map[string]any) (Config, error) {
	var cfg Config
	dec, err := mapstructure.NewDecoder(&mapstructure.DecoderConfig{
		Result:     &cfg,
		DecodeHook: splitFields,
	})
	if err != nil {
		return cfg, err
	}
	if err := dec.Decode(creds); err != nil {
		return cfg, err
	}
	return cfg, nil
}

// splitFields turns a string into a list of its whitespace-separated fields, so
// scopes may be written as "a b c" as well as a YAML list.
func splitFields(from, to reflect.Type, data any) (any, error) {
	if from.Kind() == reflect.String && to == reflect.TypeOf([]string{}) {
		return strings.Fields(data.(string)), nil
	}
	return data, nil
}

// Configured reports whether any client-credentials setting is present.
func (c Config) Configured() bool {
	return c.ClientID != "" || c.ClientSecret != "" || c.IssuerURL != "" || c.TokenURL != "" || len(c.Scopes) > 0
}

// Validate checks that the secrets are paired and that the endpoint settings
// are usable, so misconfiguration is reported at load time, not on first use.
func (c Config) Validate() error {
	if c.ClientID == "" || c.ClientSecret == "" {
		return fmt.Errorf("client ID and client secret are required")
	}
	if c.TokenURL == "" && c.IssuerURL == "" {
		return fmt.Errorf("an issuer URL or a token URL is required")
	}
	if c.TokenURL != "" {
		if err := ValidateURL(c.TokenURL); err != nil {
			return fmt.Errorf("token URL: %w", err)
		}
	} else if err := ValidateURL(c.IssuerURL); err != nil {
		return fmt.Errorf("issuer URL: %w", err)
	}
	return nil
}

// NewClientCredentialsSource returns a TokenFunc that performs the OAuth2
// client-credentials grant. The token endpoint is cfg.TokenURL when set,
// otherwise the token_endpoint found by OIDC discovery at cfg.IssuerURL. The
// endpoint is resolved once and the token is cached and refreshed by oauth2
// until it expires. It returns an error if cfg is invalid.
func NewClientCredentialsSource(cfg Config) (TokenFunc, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	var (
		mu  sync.Mutex
		src oauth2.TokenSource
	)

	// source returns the cached token source, resolving the endpoint on first
	// use. The lock is held only for discovery (bounded by authTimeout and the
	// caller's ctx), never for the token request itself.
	source := func(ctx context.Context) (oauth2.TokenSource, error) {
		mu.Lock()
		defer mu.Unlock()
		if src != nil {
			return src, nil
		}
		endpoint := cfg.TokenURL
		if endpoint == "" {
			var err error
			if endpoint, err = discoverTokenEndpoint(ctx, cfg.IssuerURL); err != nil {
				return nil, err
			}
		}
		cc := clientcredentials.Config{ClientID: cfg.ClientID, ClientSecret: cfg.ClientSecret, TokenURL: endpoint, Scopes: cfg.Scopes}
		// The source outlives one request, so it uses a detached context carrying
		// an HTTP client with a timeout.
		hctx := context.WithValue(context.Background(), oauth2.HTTPClient, &http.Client{Timeout: authTimeout})
		src = oauth2.ReuseTokenSource(nil, cc.TokenSource(hctx))
		return src, nil
	}

	return func(ctx context.Context) (string, error) {
		ts, err := source(ctx)
		if err != nil {
			return "", err
		}
		// ReuseTokenSource is safe for concurrent use.
		tok, err := ts.Token()
		if err != nil {
			return "", err
		}
		return tok.AccessToken, nil
	}, nil
}

// ValidateURL requires an https URL, so credentials are never sent in
// cleartext to a remote host. Plain http is accepted only for loopback hosts,
// which lets tests use httptest servers.
func ValidateURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return fmt.Errorf("invalid URL %q", raw)
	}
	switch u.Scheme {
	case "https":
		return nil
	case "http":
		h := u.Hostname()
		if ip := net.ParseIP(h); h == "localhost" || (ip != nil && ip.IsLoopback()) {
			return nil
		}
	}
	return fmt.Errorf("URL %q must use https", raw)
}

// discoverTokenEndpoint reads the token_endpoint from the OIDC discovery document
// of issuerURL. Both URLs must use https (loopback hosts may use http).
func discoverTokenEndpoint(ctx context.Context, issuerURL string) (string, error) {
	if err := ValidateURL(issuerURL); err != nil {
		return "", fmt.Errorf("issuer: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, authTimeout)
	defer cancel()
	u := strings.TrimRight(issuerURL, "/") + "/.well-known/openid-configuration"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("OIDC discovery: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("OIDC discovery %s returned status %d", u, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	var doc struct {
		TokenEndpoint string `json:"token_endpoint"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return "", fmt.Errorf("OIDC discovery: %w", err)
	}
	if doc.TokenEndpoint == "" {
		return "", fmt.Errorf("OIDC discovery document has no token_endpoint")
	}
	if err := ValidateURL(doc.TokenEndpoint); err != nil {
		return "", fmt.Errorf("token_endpoint: %w", err)
	}
	return doc.TokenEndpoint, nil
}
