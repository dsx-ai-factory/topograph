/*
 * Copyright 2026 LAMBDA
 * SPDX-License-Identifier: Apache-2.0
 */

package lambdai

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/dsx-ai-factory/topograph/internal/httperr"
)

// resetCredentialCache clears the process-level credential cache between tests.
// Test-only, so it lives here rather than in the production file.
func resetCredentialCache() {
	credMu.Lock()
	credCache = map[string]*workloadIdentityCredential{}
	credOrder = nil
	credMu.Unlock()
}

// writeToken writes contents to a fresh temp file and returns its path, standing
// in for the webhook-projected ServiceAccount token volume.
func writeToken(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "token")
	require.NoError(t, os.WriteFile(path, []byte(contents), 0o600))
	return path
}

// testCredential builds a workloadIdentityCredential with jitter disabled and an
// injected clock for deterministic refresh timing.
func testCredential(baseURL, tokenPath string, now func() time.Time) *workloadIdentityCredential {
	return &workloadIdentityCredential{
		baseURL:       baseURL,
		identityLRN:   "lrn:iam:identity:abc",
		tokenPath:     tokenPath,
		refreshWindow: defaultRefreshWin,
		jitterFrac:    0, // no jitter: deterministic refreshAt in tests
		now:           now,
	}
}

func TestStaticToken(t *testing.T) {
	tok, herr := staticToken("tok-abc").Token(context.Background())
	require.Nil(t, herr)
	require.Equal(t, "tok-abc", tok)
}

func TestWorkloadIdentityCredentialExchange(t *testing.T) {
	ctx := context.Background()

	const saJWT = "header.payload.signature"
	// Trailing whitespace must be trimmed before the JWT is sent.
	tokenPath := writeToken(t, saJWT+"\n")

	var hits int
	var gotMethod, gotPath string
	var gotBody exchangeRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		gotMethod = r.Method
		gotPath = r.URL.Path
		require.NoError(t, json.NewDecoder(r.Body).Decode(&gotBody))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"access_token":"minted-1","token_type":"Bearer","expires_in":3600}}`))
	}))
	defer server.Close()

	fixed := time.Unix(1_000_000, 0)
	c := testCredential(server.URL, tokenPath, func() time.Time { return fixed })

	tok, herr := c.Token(ctx)
	require.Nil(t, herr)
	require.Equal(t, "minted-1", tok)

	require.Equal(t, 1, hits)
	require.Equal(t, http.MethodPost, gotMethod)
	require.Equal(t, oidcTokenPath, gotPath)
	require.Equal(t, saJWT, gotBody.Token)
	require.Equal(t, "lrn:iam:identity:abc", gotBody.IdentityLRN)
}

func TestWorkloadIdentityCredentialCacheHit(t *testing.T) {
	ctx := context.Background()
	tokenPath := writeToken(t, "jwt")

	var hits int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		_, _ = w.Write([]byte(`{"data":{"access_token":"minted-1","expires_in":3600}}`))
	}))
	defer server.Close()

	fixed := time.Unix(1_000_000, 0)
	c := testCredential(server.URL, tokenPath, func() time.Time { return fixed })

	t1, herr := c.Token(ctx)
	require.Nil(t, herr)
	t2, herr := c.Token(ctx)
	require.Nil(t, herr)

	require.Equal(t, "minted-1", t1)
	require.Equal(t, t1, t2)
	require.Equal(t, 1, hits, "second call should be served from cache")
}

func TestWorkloadIdentityCredentialRefresh(t *testing.T) {
	ctx := context.Background()
	tokenPath := writeToken(t, "jwt")

	var hits int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		_, _ = fmt.Fprintf(w, `{"data":{"access_token":"minted-%d","expires_in":3600}}`, hits)
	}))
	defer server.Close()

	current := time.Unix(1_000_000, 0)
	c := testCredential(server.URL, tokenPath, func() time.Time { return current })

	t1, herr := c.Token(ctx)
	require.Nil(t, herr)
	require.Equal(t, "minted-1", t1)

	// Advance past refreshAt (3600s expiry - 300s window, jitter 0 => 3300s).
	current = current.Add(3400 * time.Second)

	t2, herr := c.Token(ctx)
	require.Nil(t, herr)
	require.Equal(t, "minted-2", t2)
	require.Equal(t, 2, hits, "expired key should be re-exchanged")
}

func TestWorkloadIdentityCredentialSingleFlight(t *testing.T) {
	ctx := context.Background()
	tokenPath := writeToken(t, "jwt")

	var mu sync.Mutex
	var hits int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits++
		mu.Unlock()
		// Hold the exchange open long enough for the fleet to pile up behind the
		// single-flight lock.
		time.Sleep(40 * time.Millisecond)
		_, _ = w.Write([]byte(`{"data":{"access_token":"minted-1","expires_in":3600}}`))
	}))
	defer server.Close()

	fixed := time.Unix(1_000_000, 0)
	c := testCredential(server.URL, tokenPath, func() time.Time { return fixed })

	const goroutines = 25
	var wg sync.WaitGroup
	results := make([]string, goroutines)
	errs := make([]*httperr.Error, goroutines)
	wg.Add(goroutines)
	for i := range goroutines {
		go func() {
			defer wg.Done()
			results[i], errs[i] = c.Token(ctx)
		}()
	}
	wg.Wait()

	for i := range goroutines {
		require.Nil(t, errs[i])
		require.Equal(t, "minted-1", results[i])
	}
	mu.Lock()
	require.Equal(t, 1, hits, "concurrent callers should share a single exchange")
	mu.Unlock()
}

func TestWorkloadIdentityCredentialGracefulDegradation(t *testing.T) {
	ctx := context.Background()
	tokenPath := writeToken(t, "jwt")

	var mu sync.Mutex
	var fail bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		f := fail
		mu.Unlock()
		if f {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":{"access_token":"minted-1","expires_in":3600}}`))
	}))
	defer server.Close()

	current := time.Unix(1_000_000, 0)
	c := testCredential(server.URL, tokenPath, func() time.Time { return current })

	// First mint succeeds.
	tok, herr := c.Token(ctx)
	require.Nil(t, herr)
	require.Equal(t, "minted-1", tok)

	// Past refreshAt (3300s) but before expiresAt (3600s): a failed refresh must
	// still serve the cached, still-valid key.
	current = current.Add(3400 * time.Second)
	mu.Lock()
	fail = true
	mu.Unlock()

	tok, herr = c.Token(ctx)
	require.Nil(t, herr, "a still-valid key survives a failed refresh")
	require.Equal(t, "minted-1", tok)

	// Past expiresAt (now 3800s): the dead key surfaces the error.
	current = current.Add(400 * time.Second)
	tok, herr = c.Token(ctx)
	require.NotNil(t, herr)
	require.Equal(t, http.StatusUnauthorized, herr.Code())
	require.Empty(t, tok)
}

func TestWorkloadIdentityCredentialExpiresAtPreferred(t *testing.T) {
	ctx := context.Background()
	tokenPath := writeToken(t, "jwt")

	fixed := time.Unix(1_000_000, 0)
	// Absolute expiry far in the future paired with a tiny expires_in: if
	// expires_in were preferred, the key would already be due for refresh.
	expiresAt := fixed.Add(10000 * time.Second).UTC().Format(time.RFC3339)

	var hits int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		_, _ = fmt.Fprintf(w, `{"data":{"access_token":"minted-%d","expires_in":1,"expires_at":%q}}`, hits, expiresAt)
	}))
	defer server.Close()

	current := fixed
	c := testCredential(server.URL, tokenPath, func() time.Time { return current })

	t1, herr := c.Token(ctx)
	require.Nil(t, herr)
	require.Equal(t, "minted-1", t1)

	// Advance well past what expires_in:1 would permit; expires_at keeps it fresh.
	current = current.Add(2 * time.Second)
	t2, herr := c.Token(ctx)
	require.Nil(t, herr)
	require.Equal(t, "minted-1", t2)
	require.Equal(t, 1, hits, "absolute expires_at should be preferred over expires_in")
}

func TestWorkloadIdentityCredentialEmptyTokenFile(t *testing.T) {
	ctx := context.Background()
	tokenPath := writeToken(t, "   \n") // whitespace only -> empty after trim

	var hits int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
	}))
	defer server.Close()

	c := testCredential(server.URL, tokenPath, func() time.Time { return time.Unix(1_000_000, 0) })

	tok, herr := c.Token(ctx)
	require.Empty(t, tok)
	require.NotNil(t, herr)
	require.Equal(t, http.StatusInternalServerError, herr.Code())
	require.Equal(t, 0, hits, "an empty token must not attempt an exchange")
}

func TestWorkloadIdentityCredentialMissingFile(t *testing.T) {
	ctx := context.Background()
	c := testCredential(
		"https://example.invalid",
		filepath.Join(t.TempDir(), "does-not-exist"),
		func() time.Time { return time.Unix(1_000_000, 0) },
	)

	tok, herr := c.Token(ctx)
	require.Empty(t, tok)
	require.NotNil(t, herr)
	require.Equal(t, http.StatusInternalServerError, herr.Code())
}

func TestWorkloadIdentityCredentialUnauthorizedNoLeak(t *testing.T) {
	ctx := context.Background()
	const saJWT = "super-secret-jwt-value"
	tokenPath := writeToken(t, saJWT)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"unauthorized-detail"}`))
	}))
	defer server.Close()

	c := testCredential(server.URL, tokenPath, func() time.Time { return time.Unix(1_000_000, 0) })

	tok, herr := c.Token(ctx)
	require.Empty(t, tok)
	require.NotNil(t, herr)
	require.Equal(t, http.StatusUnauthorized, herr.Code())
	// The error must leak neither the JWT nor the raw response body.
	require.NotContains(t, herr.Error(), saJWT)
	require.NotContains(t, herr.Error(), "unauthorized-detail")
}

func TestWorkloadIdentityCredentialInvalidateIfCurrent(t *testing.T) {
	ctx := context.Background()
	tokenPath := writeToken(t, "jwt")

	var hits int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		_, _ = fmt.Fprintf(w, `{"data":{"access_token":"minted-%d","expires_in":3600}}`, hits)
	}))
	defer server.Close()

	fixed := time.Unix(1_000_000, 0)
	c := testCredential(server.URL, tokenPath, func() time.Time { return fixed })

	t1, herr := c.Token(ctx)
	require.Nil(t, herr)
	require.Equal(t, "minted-1", t1)

	c.InvalidateIfCurrent(t1)

	t2, herr := c.Token(ctx)
	require.Nil(t, herr)
	require.Equal(t, "minted-2", t2)
	require.Equal(t, 2, hits, "invalidating the current key should force a re-exchange")
}

// TestWorkloadIdentityCredentialInvalidateIfCurrentIgnoresStale covers the race where
// a delayed 401 for an already-replaced key arrives after another request minted
// a newer one: the stale invalidation must not discard the valid replacement.
func TestWorkloadIdentityCredentialInvalidateIfCurrentIgnoresStale(t *testing.T) {
	ctx := context.Background()
	tokenPath := writeToken(t, "jwt")

	var hits int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		_, _ = fmt.Fprintf(w, `{"data":{"access_token":"minted-%d","expires_in":3600}}`, hits)
	}))
	defer server.Close()

	fixed := time.Unix(1_000_000, 0)
	c := testCredential(server.URL, tokenPath, func() time.Time { return fixed })

	old, herr := c.Token(ctx)
	require.Nil(t, herr)
	require.Equal(t, "minted-1", old)

	// Stand in for a concurrent refresh that installs a newer key.
	c.InvalidateIfCurrent(old)
	current, herr := c.Token(ctx)
	require.Nil(t, herr)
	require.Equal(t, "minted-2", current)
	require.Equal(t, 2, hits)

	// The delayed 401 for "minted-1" now lands; it must be a no-op.
	c.InvalidateIfCurrent(old)

	again, herr := c.Token(ctx)
	require.Nil(t, herr)
	require.Equal(t, current, again, "the newer key must survive a stale invalidation")
	require.Equal(t, 2, hits, "a stale invalidation must not trigger a re-exchange")
}

// TestWorkloadIdentityCredentialFailedRefreshIsShared covers the burst case: once
// a refresh fails while the cached key is still valid, later callers must reuse
// that outcome instead of each grinding through their own retry cycle against the
// failing endpoint.
func TestWorkloadIdentityCredentialFailedRefreshIsShared(t *testing.T) {
	ctx := context.Background()
	tokenPath := writeToken(t, "jwt")

	var mu sync.Mutex
	var hits int
	var fail bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits++
		f := fail
		mu.Unlock()
		if f {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"data":{"access_token":"minted-1","expires_in":3600}}`))
	}))
	defer server.Close()

	current := time.Unix(1_000_000, 0)
	c := testCredential(server.URL, tokenPath, func() time.Time { return current })

	tok, herr := c.Token(ctx)
	require.Nil(t, herr)
	require.Equal(t, "minted-1", tok)
	mu.Lock()
	require.Equal(t, 1, hits)
	mu.Unlock()

	// Past refreshAt (3300s) but before expiresAt (3600s), with the endpoint down.
	current = current.Add(3400 * time.Second)
	mu.Lock()
	fail = true
	mu.Unlock()

	tok, herr = c.Token(ctx)
	require.Nil(t, herr, "the still-valid key survives a failed refresh")
	require.Equal(t, "minted-1", tok)
	mu.Lock()
	require.Equal(t, 2, hits, "one attempt for the failed refresh")
	mu.Unlock()

	// Subsequent callers inside the backoff must not attempt another exchange.
	for range 5 {
		tok, herr = c.Token(ctx)
		require.Nil(t, herr)
		require.Equal(t, "minted-1", tok)
	}
	mu.Lock()
	require.Equal(t, 2, hits, "queued callers should share the failed refresh, not re-attempt")
	mu.Unlock()

	// The deferral never outlives the key: at expiry the exchange is retried.
	current = current.Add(300 * time.Second) // now == expiresAt
	_, _ = c.Token(ctx)
	mu.Lock()
	require.Greater(t, hits, 2, "an expired key must still trigger a fresh attempt")
	mu.Unlock()
}

// TestSharedCredentialCacheIsBounded guards against unbounded growth: baseURL is
// a caller-supplied provider parameter, so the cache key space is not closed.
func TestSharedCredentialCacheIsBounded(t *testing.T) {
	resetCredentialCache()
	defer resetCredentialCache()

	for i := range maxCachedCredentials + 20 {
		sharedCredential(fmt.Sprintf("https://api-%d.example.com", i), "lrn:1", "/path/token")
	}

	credMu.Lock()
	defer credMu.Unlock()
	require.LessOrEqual(t, len(credCache), maxCachedCredentials, "cache must stay bounded")
	require.Equal(t, len(credCache), len(credOrder), "eviction must keep map and order in step")
	// FIFO: the oldest entries are the ones evicted.
	_, oldestPresent := credCache["lrn:1|/path/token|https://api-0.example.com"]
	require.False(t, oldestPresent, "the first inserted entry should have been evicted")
	_, newestPresent := credCache[fmt.Sprintf("lrn:1|/path/token|https://api-%d.example.com", maxCachedCredentials+19)]
	require.True(t, newestPresent, "the most recent entry should be retained")
}

func TestSharedCredentialCaching(t *testing.T) {
	resetCredentialCache()

	a := sharedCredential("https://api", "lrn:1", "/path/token")
	b := sharedCredential("https://api", "lrn:1", "/path/token")
	require.Same(t, a, b, "the same key returns the cached credential")

	c := sharedCredential("https://api", "lrn:2", "/path/token")
	require.NotSame(t, a, c, "a different identity is a different credential")

	resetCredentialCache()
	d := sharedCredential("https://api", "lrn:1", "/path/token")
	require.NotSame(t, a, d, "reset clears the cache")
}
