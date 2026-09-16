/*
 * Copyright 2025 NVIDIA CORPORATION
 * SPDX-License-Identifier: Apache-2.0
 */

package httpreq

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/dsx-ai-factory/topograph/internal/httperr"
)

func TestShouldRetry(t *testing.T) {
	testCases := []struct {
		name   string
		status int
		retry  bool
	}{
		{
			name:   "request timeout",
			status: http.StatusRequestTimeout, // 408
			retry:  true,
		},
		{
			name:   "too many requests",
			status: http.StatusTooManyRequests, // 429
			retry:  true,
		},
		{
			name:   "internal server error",
			status: http.StatusInternalServerError, // 500
			retry:  true,
		},
		{
			name:   "bad gateway",
			status: http.StatusBadGateway, // 502
			retry:  true,
		},
		{
			name:   "service unavailable",
			status: http.StatusServiceUnavailable, // 503
			retry:  true,
		},
		{
			name:   "gateway timeout",
			status: http.StatusGatewayTimeout, // 504
			retry:  true,
		},
		{
			name:   "ok",
			status: http.StatusOK, // 200
			retry:  false,
		},
		{
			name:   "bad request",
			status: http.StatusBadRequest, // 400
			retry:  false,
		},
		{
			name:   "unauthorized",
			status: http.StatusUnauthorized, // 401
			retry:  false,
		},
		{
			name:   "not found",
			status: http.StatusNotFound, // 404
			retry:  false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			retry := ShouldRetry(tc.status)
			require.Equal(t, tc.retry, retry)
		})
	}
}

type callback struct{ status, attempts int }

func (c *callback) Inc() (*http.Request, *httperr.Error) {
	c.attempts++
	return nil, httperr.NewError(c.status, "error")
}

func TestDoRequestWithRetries(t *testing.T) {
	testCases := []struct {
		name     string
		status   int
		attempts int
	}{
		{
			name:     "gateway timeout",
			status:   http.StatusGatewayTimeout, // 504
			attempts: maxRetries,
		},
		{
			name:     "unauthorized",
			status:   http.StatusUnauthorized, // 401
			attempts: 1,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			c := &callback{status: tc.status}
			_, err := DoRequestWithRetries(c.Inc, false)
			require.Equal(t, tc.status, err.Code())
			require.Equal(t, tc.attempts, c.attempts)
		})
	}
}

func TestDoRequestWithRetriesStopsWhenRequestContextIsCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	requestAttempted := make(chan struct{}, 1)
	attempts := 0
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://topograph.invalid", nil)
	require.NoError(t, err)
	f := func() (*http.Request, *httperr.Error) {
		attempts++
		requestAttempted <- struct{}{}
		return req, httperr.NewError(http.StatusServiceUnavailable, "retry")
	}

	done := make(chan *httperr.Error, 1)
	go func() {
		_, err := DoRequestWithRetries(f, false)
		done <- err
	}()

	select {
	case <-requestAttempted:
	case <-time.After(time.Second):
		t.Fatal("request was not attempted")
	}
	cancel()

	select {
	case err := <-done:
		require.EqualError(t, err, "retry")
		require.Equal(t, 1, attempts)
	case <-time.After(time.Second):
		t.Fatal("retry did not stop after request context cancellation")
	}
}

func TestGetURL(t *testing.T) {
	testCases := []struct {
		name    string
		baseURL string
		paths   []string
		query   map[string]string
		url     string
		err     string
	}{
		{
			name:    "Case 1: bad base URL",
			baseURL: "123:",
			err:     `parse "123:": first path segment in URL cannot contain colon`,
		},
		{
			name:    "Case 2: single base URL",
			baseURL: "http://localhost",
			url:     "http://localhost",
		},
		{
			name:    "Case 3: base URL with path",
			baseURL: "http://localhost/",
			paths:   []string{"a", "b/", "/c", "d/"},
			url:     "http://localhost/a/b/c/d",
		},
		{
			name:    "Case 4: base URL with path and query",
			baseURL: "http://localhost/",
			paths:   []string{"a", "b/", "/c", "d/"},
			query:   map[string]string{"key1": "val1", "key2": "val2"},
			url:     "http://localhost/a/b/c/d?key1=val1&key2=val2",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			u, err := GetURL(tc.baseURL, tc.query, tc.paths...)
			if len(tc.err) != 0 {
				require.EqualError(t, err, tc.err)
			} else {

				require.Nil(t, err)
				require.Equal(t, tc.url, u)
			}
		})
	}
}

func TestDoRequest(t *testing.T) {
	t.Run("request func returns error", func(t *testing.T) {
		f := func() (*http.Request, *httperr.Error) {
			return nil, httperr.NewError(http.StatusBadRequest, "bad input")
		}
		resp, body, err := DoRequest(f, false)
		require.Nil(t, resp)
		require.Nil(t, body)
		require.NotNil(t, err)
		require.Equal(t, http.StatusBadRequest, err.Code())
	})

	t.Run("2xx response returns body and no error", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("ok"))
		}))
		defer srv.Close()

		f := func() (*http.Request, *httperr.Error) {
			req, e := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL, nil)
			if e != nil {
				return nil, httperr.NewError(http.StatusInternalServerError, e.Error())
			}
			return req, nil
		}
		resp, body, err := DoRequest(f, false)
		require.Nil(t, err)
		require.NotNil(t, resp)
		require.Equal(t, []byte("ok"), body)
	})

	t.Run("non-2xx response returns body and error with that status", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte("not found"))
		}))
		defer srv.Close()

		f := func() (*http.Request, *httperr.Error) {
			req, e := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL, nil)
			if e != nil {
				return nil, httperr.NewError(http.StatusInternalServerError, e.Error())
			}
			return req, nil
		}
		resp, body, err := DoRequest(f, false)
		require.NotNil(t, err)
		require.Equal(t, http.StatusNotFound, err.Code())
		require.Equal(t, "not found", err.Error())
		require.NotNil(t, resp)
		require.Equal(t, []byte("not found"), body)
	})

	t.Run("transport error returns 502", func(t *testing.T) {
		f := func() (*http.Request, *httperr.Error) {
			req, e := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://127.0.0.1:1", nil)
			if e != nil {
				return nil, httperr.NewError(http.StatusInternalServerError, e.Error())
			}
			return req, nil
		}
		_, _, err := DoRequest(f, false)
		require.NotNil(t, err)
		require.Equal(t, http.StatusBadGateway, err.Code())
	})

	t.Run("insecureSkipVerify works against TLS server", func(t *testing.T) {
		srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("tls-ok"))
		}))
		defer srv.Close()

		f := func() (*http.Request, *httperr.Error) {
			req, e := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL, nil)
			if e != nil {
				return nil, httperr.NewError(http.StatusInternalServerError, e.Error())
			}
			return req, nil
		}
		resp, body, err := DoRequest(f, true)
		require.Nil(t, err)
		require.NotNil(t, resp)
		require.Equal(t, []byte("tls-ok"), body)
	})
}

func TestGetRequestFunc(t *testing.T) {
	ctx := context.Background()
	f := GetRequestFunc(ctx, http.MethodGet,
		map[string]string{"Authorization": "Bearer token"},
		map[string]string{"page": "1"},
		nil,
		"http://localhost",
		"v1", "resource",
	)
	req, err := f()
	require.Nil(t, err)
	require.NotNil(t, req)
	require.Equal(t, http.MethodGet, req.Method)
	require.Equal(t, "Bearer token", req.Header.Get("Authorization"))
	require.Equal(t, "1", req.URL.Query().Get("page"))
	require.Equal(t, "/v1/resource", req.URL.Path)
}

func TestGetNextBackoff(t *testing.T) {
	testCases := []struct {
		name  string
		resp  *http.Response
		iter  int
		check func(time.Duration) bool
	}{
		{
			name: "Case 1.1: valid Retry-After header (seconds)",
			resp: &http.Response{
				Header: http.Header{
					"Retry-After": []string{"5"},
				},
			},
			iter:  0,
			check: func(wait time.Duration) bool { return wait == 5*time.Second },
		},
		{
			name: "Case 1.2: exceeded Retry-After header (seconds)",
			resp: &http.Response{
				Header: http.Header{
					"Retry-After": []string{"1000"},
				},
			},
			iter:  0,
			check: func(wait time.Duration) bool { return wait == maxRetryAfter },
		},
		{
			name: "Case 2.1: valid Retry-After header (date)",
			resp: &http.Response{
				Header: http.Header{
					"Retry-After": []string{time.Now().Add(10 * time.Second).Format(time.RFC850)},
				},
			},
			iter:  3,
			check: func(wait time.Duration) bool { return wait > 8*time.Second && wait < 12*time.Second },
		},
		{
			name: "Case 2.2: exceeded Retry-After header (date)",
			resp: &http.Response{
				Header: http.Header{
					"Retry-After": []string{time.Now().Add(10 * time.Minute).Format(time.RFC850)},
				},
			},
			iter:  3,
			check: func(wait time.Duration) bool { return wait == maxRetryAfter },
		},
		{
			name: "Case 3.1: no Retry-After header",
			resp: &http.Response{
				Header: http.Header{},
			},
			iter:  0,
			check: func(wait time.Duration) bool { return wait == 500*time.Millisecond },
		},
		{
			name:  "Case 3.2: no response",
			iter:  1,
			check: func(wait time.Duration) bool { return wait == time.Second },
		},
		{
			name: "Case 4: invalid Retry-After header",
			resp: &http.Response{
				Header: http.Header{
					"Retry-After": []string{"invalid"},
				},
			},
			iter:  2,
			check: func(wait time.Duration) bool { return wait == 2*time.Second },
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			wait := GetNextBackoff(tc.resp, backOff, tc.iter)
			correct := tc.check(wait)
			require.True(t, correct)
		})
	}
}
