/*
 * Copyright 2026 LAMBDA
 * SPDX-License-Identifier: Apache-2.0
 */

package lambdai

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"k8s.io/klog/v2"

	"github.com/dsx-ai-factory/topograph/internal/httperr"
	"github.com/dsx-ai-factory/topograph/internal/httpreq"
)

const (
	// Env vars injected by lambda-pod-identity-webhook into pods whose
	// ServiceAccount carries the lambda.ai/identity-lrn annotation.
	envIdentityLRN = "LAMBDA_IDENTITY_LRN"
	envTokenFile   = "LAMBDA_WORKLOAD_IDENTITY_TOKEN_FILE"

	// envRoleLRN is the webhook's previous name for envIdentityLRN. The webhook
	// injects both while operators migrate their ServiceAccount annotations, so
	// this is read only when the current name is absent.
	//
	// Deprecated: read envIdentityLRN; this exists so pods admitted by an older
	// webhook keep authenticating.
	envRoleLRN = "LAMBDA_ROLE_LRN"

	// defaultSATokenPath matches where the webhook mounts the projected token;
	// used only if envTokenFile is unset.
	defaultSATokenPath = "/var/run/secrets/lambda.ai/serviceaccount/token"

	oidcTokenPath = "/api/v1/oidc/token"

	defaultTokenTTL   = 60 * time.Minute
	defaultRefreshWin = 5 * time.Minute
	defaultJitterFrac = 0.2

	// failedRefreshBackoff defers the next exchange attempt after a failed
	// refresh, so a burst of callers shares one outcome instead of each grinding
	// through its own retry cycle while a usable cached key is still in hand.
	failedRefreshBackoff = 30 * time.Second
)

// injectedIdentityLRN returns the workload identity the webhook injected,
// preferring the current env var over the deprecated one it still sets alongside
// it. An empty result means no identity was injected at all.
func injectedIdentityLRN() string {
	if lrn := os.Getenv(envIdentityLRN); lrn != "" {
		return lrn
	}

	return os.Getenv(envRoleLRN)
}

// tokenProvider yields a Lambda API bearer token. *httperr.Error preserves the
// provider-boundary contract so the correct HTTP status propagates.
type tokenProvider interface {
	Token(ctx context.Context) (string, *httperr.Error)
}

// staticToken preserves the original static-credential behavior.
type staticToken string

func (s staticToken) Token(context.Context) (string, *httperr.Error) { return string(s), nil }

// The provider Loader runs per request, but a workload-identity credential must
// persist across requests so its token cache, single-flight refresh, and
// graceful degradation take effect. Cache one credential per
// (identityLRN, tokenPath, baseURL).
//
// identityLRN and tokenPath come from the pod environment and are therefore
// fixed for the process, but baseURL is a per-request provider parameter, so the
// key space is caller-controlled and must be bounded. Eviction is by insertion
// order: dropping an entry costs one extra token exchange, whereas unbounded
// growth is a slow leak. Real deployments hold exactly one entry.
const maxCachedCredentials = 32

var (
	credMu    sync.Mutex
	credCache = map[string]*workloadIdentityCredential{}
	credOrder []string
)

func sharedCredential(baseURL, identityLRN, tokenPath string) *workloadIdentityCredential {
	key := identityLRN + "|" + tokenPath + "|" + baseURL
	credMu.Lock()
	defer credMu.Unlock()
	if c, ok := credCache[key]; ok {
		return c
	}
	for len(credOrder) >= maxCachedCredentials {
		delete(credCache, credOrder[0])
		credOrder = credOrder[1:]
	}
	c := &workloadIdentityCredential{
		baseURL:       baseURL,
		identityLRN:   identityLRN,
		tokenPath:     tokenPath,
		refreshWindow: defaultRefreshWin,
		jitterFrac:    defaultJitterFrac,
		now:           time.Now,
	}
	credCache[key] = c
	credOrder = append(credOrder, key)
	return c
}

type cachedToken struct {
	accessToken string
	expiresAt   time.Time // hard deadline from the server
	refreshAt   time.Time // when we begin refreshing, a little before expiry
}

// workloadIdentityCredential mints, caches, and refreshes a short-lived Lambda
// API key by exchanging the pod's projected ServiceAccount JWT. Safe for
// concurrent use. Ported from lambdal/ll-sdk-go PR #38, reimplemented over
// internal/httpreq (no SDK dependency).
type workloadIdentityCredential struct {
	baseURL       string
	identityLRN   string
	tokenPath     string
	refreshWindow time.Duration
	jitterFrac    float64
	now           func() time.Time // test seam; defaults to time.Now

	mu        sync.RWMutex // guards cur
	cur       *cachedToken
	refreshMu sync.Mutex // one refresh at a time
}

func (c *workloadIdentityCredential) Token(ctx context.Context) (string, *httperr.Error) {
	// Fast path: hand back the cached key if it isn't due for refresh.
	c.mu.RLock()
	cur := c.cur
	c.mu.RUnlock()
	if cur != nil && c.now().Before(cur.refreshAt) {
		return cur.accessToken, nil
	}

	// One refresh at a time; everyone else reuses its result.
	c.refreshMu.Lock()
	defer c.refreshMu.Unlock()
	c.mu.RLock()
	cur = c.cur
	c.mu.RUnlock()
	if cur != nil && c.now().Before(cur.refreshAt) {
		return cur.accessToken, nil
	}

	fresh, herr := c.exchangeOnce(ctx)
	if herr != nil {
		// A still-valid key survives a failed refresh; only a dead one errors.
		if cur != nil && c.now().Before(cur.expiresAt) {
			c.deferRefresh(cur, failedRefreshBackoff)
			return cur.accessToken, nil
		}
		return "", herr
	}
	c.mu.Lock()
	c.cur = &fresh
	c.mu.Unlock()
	return fresh.accessToken, nil
}

// deferRefresh pushes the next refresh attempt out after a failed exchange, so
// callers queued behind refreshMu reuse the still-valid cached key instead of
// each running its own retry cycle against a failing endpoint. It never defers
// past the hard expiry, since the key must still be re-minted before it dies.
//
// A copy is republished rather than the cached token mutated in place: Token
// reads a published cachedToken after releasing the lock, so published values
// must stay immutable.
func (c *workloadIdentityCredential) deferRefresh(stale *cachedToken, d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cur == nil || c.cur != stale {
		return // someone installed a fresh key meanwhile; leave theirs alone
	}

	next := c.now().Add(d)
	if next.After(c.cur.expiresAt) {
		next = c.cur.expiresAt
	}
	if !next.After(c.cur.refreshAt) {
		return
	}

	updated := *c.cur
	updated.refreshAt = next
	c.cur = &updated
}

// InvalidateIfCurrent drops the cached key only when it still matches the token
// that triggered the 401, preventing a concurrent refresh from being wiped by a
// delayed response for an older key. The lock is held across both the comparison
// and the clear, so a Token call that just installed a fresh key either happens
// entirely before (and is seen as a mismatch) or entirely after.
func (c *workloadIdentityCredential) InvalidateIfCurrent(token string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cur != nil && c.cur.accessToken == token {
		c.cur = nil
	}
}

func (c *workloadIdentityCredential) exchangeOnce(ctx context.Context) (cachedToken, *httperr.Error) {
	// Re-read the file every mint: the kubelet rotates the projected token.
	raw, err := os.ReadFile(c.tokenPath)
	if err != nil {
		return cachedToken{}, httperr.NewError(http.StatusInternalServerError,
			fmt.Sprintf("failed to read service-account token %s: %v", c.tokenPath, err))
	}
	saToken := strings.TrimSpace(string(raw))
	if saToken == "" {
		return cachedToken{}, httperr.NewError(http.StatusInternalServerError,
			fmt.Sprintf("service-account token %s is empty", c.tokenPath))
	}

	accessToken, expiresAt, herr := c.exchange(ctx, saToken)
	if herr != nil {
		return cachedToken{}, herr
	}

	// Refresh a bit before expiry (jittered) so a request never rides an
	// expiring key and a fleet doesn't refresh in lockstep.
	now := c.now()
	window := c.refreshWindow
	if life := expiresAt.Sub(now); window >= life {
		window = life / 2
	}
	jitter := time.Duration(cryptoFloat64() * c.jitterFrac * float64(window))
	refreshAt := expiresAt.Add(-(window - jitter))
	if !refreshAt.After(now) {
		refreshAt = now.Add(expiresAt.Sub(now) / 2)
	}
	return cachedToken{accessToken: accessToken, expiresAt: expiresAt, refreshAt: refreshAt}, nil
}

type exchangeRequest struct {
	Token       string `json:"token"`
	IdentityLRN string `json:"identity_lrn"`
}

type exchangeResponse struct {
	Data struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
		ExpiresAt   string `json:"expires_at"`
	} `json:"data"`
}

// exchange posts the ServiceAccount JWT to Lambda's OIDC endpoint. On failure it
// returns an opaque error (Lambda returns a uniform 401 — "no oracle") and never
// logs the JWT, the minted key, or the raw response body.
func (c *workloadIdentityCredential) exchange(ctx context.Context, saToken string) (string, time.Time, *httperr.Error) {
	payload, err := json.Marshal(exchangeRequest{Token: saToken, IdentityLRN: c.identityLRN})
	if err != nil {
		return "", time.Time{}, httperr.NewError(http.StatusInternalServerError, err.Error())
	}
	headers := map[string]string{"Content-Type": "application/json"}
	f := httpreq.GetRequestFunc(ctx, http.MethodPost, headers, nil, payload, c.baseURL, oidcTokenPath)
	body, herr := httpreq.DoRequestWithRetries(f, false)
	if herr != nil {
		klog.Warningf("lambda workload-identity token exchange failed (status %d) for identity %s", herr.Code(), c.identityLRN)
		return "", time.Time{}, httperr.NewError(herr.Code(), "workload-identity token exchange failed")
	}
	var resp exchangeResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return "", time.Time{}, httperr.NewError(http.StatusBadGateway, err.Error())
	}
	if resp.Data.AccessToken == "" {
		return "", time.Time{}, httperr.NewError(http.StatusBadGateway, "token exchange returned empty access_token")
	}
	return resp.Data.AccessToken, expiryOf(resp, c.now()), nil
}

// expiryOf prefers the absolute expires_at, falls back to expires_in, then a default.
func expiryOf(resp exchangeResponse, now time.Time) time.Time {
	if resp.Data.ExpiresAt != "" {
		for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
			if t, err := time.Parse(layout, resp.Data.ExpiresAt); err == nil {
				return t
			}
		}
	}
	if resp.Data.ExpiresIn > 0 {
		return now.Add(time.Duration(resp.Data.ExpiresIn) * time.Second)
	}
	return now.Add(defaultTokenTTL)
}

// cryptoFloat64 returns a random float in [0,1) for refresh jitter only.
func cryptoFloat64() float64 {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return 0 // no randomness: skip jitter rather than fail
	}
	return float64(binary.BigEndian.Uint64(b[:])>>11) / float64(1<<53)
}
