/*
 * Copyright 2026 NVIDIA CORPORATION
 * SPDX-License-Identifier: Apache-2.0
 */

package firebird

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/dsx-ai-factory/topograph/internal/httperr"
	"github.com/dsx-ai-factory/topograph/internal/httpreq"
)

// Only the access token is needed. ID and refresh tokens are not retained;
// renewal uses the client credentials exchange again.
type tokenResponse struct {
	AccessToken string `json:"accessToken"`
	ExpiresIn   int64  `json:"expiresIn"`
	TokenType   string `json:"tokenType"`
}

// lockAuth serializes token exchanges and cache access while allowing callers
// waiting on another exchange to cancel their own request.
func (p *Provider) lockAuth(ctx context.Context) *httperr.Error {
	select {
	case p.authGate <- struct{}{}:
		if ctx.Err() == nil {
			return nil
		}
		<-p.authGate
	case <-ctx.Done():
	}
	return httperr.NewError(http.StatusBadGateway, "Firebird authentication request canceled")
}

func (p *Provider) accessToken(ctx context.Context) (string, *httperr.Error) {
	if err := p.lockAuth(ctx); err != nil {
		return "", err
	}
	defer func() { <-p.authGate }()
	if p.token != "" && time.Now().Before(p.tokenExpiresAt) {
		return p.token, nil
	}
	payload, err := json.Marshal(p.creds)
	if err != nil {
		return "", httperr.NewError(http.StatusBadRequest, "invalid Firebird client credentials")
	}
	headers := map[string]string{"Accept": "application/json", "Content-Type": "application/json"}
	started := time.Now()
	f := httpreq.GetRequestFunc(ctx, http.MethodPost, headers, nil, payload, p.params.APIURL, "/auth/token")
	body, httpErr := httpreq.DoRequestWithRetries(f, false)
	if httpErr != nil {
		if ctx.Err() != nil {
			return "", httperr.NewError(http.StatusBadGateway, "Firebird authentication request canceled")
		}
		return "", firebirdAPIError(httpErr.Code(), body, "authentication")
	}
	var response tokenResponse
	if err := json.Unmarshal(body, &response); err != nil || strings.TrimSpace(response.AccessToken) == "" ||
		strings.ContainsAny(response.AccessToken, "\r\n") || response.ExpiresIn < 0 || response.ExpiresIn > int64((1<<63-1)/time.Second) ||
		(response.TokenType != "" && !strings.EqualFold(response.TokenType, "Bearer")) {
		return "", httperr.NewError(http.StatusBadGateway, "invalid Firebird authentication response")
	}
	lifetime := time.Duration(response.ExpiresIn) * time.Second
	// Renew slightly early to account for request latency and clock differences.
	p.tokenExpiresAt = started.Add(lifetime - min(30*time.Second, lifetime/10))
	p.token = response.AccessToken
	return p.token, nil
}

func (p *Provider) invalidateToken(ctx context.Context, rejected string) *httperr.Error {
	if err := p.lockAuth(ctx); err != nil {
		return err
	}
	defer func() { <-p.authGate }()
	// Another request may already have obtained a replacement token.
	if p.token == rejected {
		p.token = ""
	}
	return nil
}
