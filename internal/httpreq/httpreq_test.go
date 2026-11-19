// SPDX-FileCopyrightText: Copyright (c) 2025 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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

package httpreq

import (
	"bytes"
	"context"
	"flag"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"k8s.io/klog/v2"

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

// connTracker counts the connections a test server has open.
type connTracker struct{ open atomic.Int32 }

func (c *connTracker) track(_ net.Conn, state http.ConnState) {
	switch state {
	case http.StateNew:
		c.open.Add(1)
	case http.StateClosed, http.StateHijacked:
		c.open.Add(-1)
	}
}

func (c *connTracker) requireClosed(t *testing.T) {
	t.Helper()
	require.Eventually(t, func() bool { return c.open.Load() == 0 }, 5*time.Second, 10*time.Millisecond,
		"connections left open: %d", c.open.Load())
}

func newTrackedTLSServer(t *testing.T, h http.HandlerFunc) (*httptest.Server, *connTracker) {
	t.Helper()
	tracker := &connTracker{}
	srv := httptest.NewUnstartedServer(h)
	srv.Config.ConnState = tracker.track
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return srv, tracker
}

// TestDoRequestClosesInsecureConnections verifies that the private transport
// used with insecureSkipVerify does not leave its keep-alive connection open
// once the request returns, however the request ends.
func TestDoRequestClosesInsecureConnections(t *testing.T) {
	request := func(ctx context.Context, url string) RequestFunc {
		return GetRequestFunc(ctx, http.MethodGet, nil, nil, nil, url)
	}

	t.Run("success", func(t *testing.T) {
		srv, tracker := newTrackedTLSServer(t, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("ok")) })
		for range 3 {
			_, _, err := DoRequestWithRedirects(request(context.Background(), srv.URL), true, false)
			require.Nil(t, err)
		}
		tracker.requireClosed(t)
	})

	t.Run("redirect refused", func(t *testing.T) {
		srv, tracker := newTrackedTLSServer(t, func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "http://u:pw@evil.example/x?token=SECRET", http.StatusTemporaryRedirect)
		})
		_, _, err := DoRequestWithRedirects(request(context.Background(), srv.URL), true, false)
		require.NotNil(t, err)
		require.Equal(t, http.StatusBadGateway, err.Code())
		require.Contains(t, err.Error(), "refusing to follow a redirect to http://evil.example")
		require.NotContains(t, err.Error(), "SECRET")
		require.NotContains(t, err.Error(), "pw")
		tracker.requireClosed(t)
	})

	t.Run("malformed redirect target", func(t *testing.T) {
		srv, tracker := newTrackedTLSServer(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Location", "http://u:pw@[bad/x?token=SECRET")
			w.WriteHeader(http.StatusTemporaryRedirect)
		})
		for _, allow := range []bool{false, true} {
			_, _, err := DoRequestWithRedirects(request(context.Background(), srv.URL), true, allow)
			require.NotNil(t, err)
			require.Equal(t, http.StatusBadGateway, err.Code())
			require.Contains(t, err.Error(), "failed to parse the redirect Location header")
			require.NotContains(t, err.Error(), "SECRET")
			require.NotContains(t, err.Error(), "pw")
		}
		tracker.requireClosed(t)
	})

	t.Run("truncated response", func(t *testing.T) {
		srv, tracker := newTrackedTLSServer(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Length", "100")
			_, _ = w.Write([]byte("short"))
		})
		_, _, err := DoRequestWithRedirects(request(context.Background(), srv.URL), true, false)
		require.NotNil(t, err)
		require.Equal(t, http.StatusInternalServerError, err.Code())
		require.Contains(t, err.Error(), "failed to read HTTP response")
		tracker.requireClosed(t)
	})

	t.Run("canceled while reading the body", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		const chunks = 5
		flushed := make(chan struct{})
		srv, tracker := newTrackedTLSServer(t, func(w http.ResponseWriter, r *http.Request) {
			// The status line, headers and then a trickle of the body: the client
			// has its response and is reading the body once the last chunk is out.
			w.Header().Set("Content-Length", "1000")
			for i := 0; i < chunks; i++ {
				_, _ = w.Write([]byte("chunk"))
				w.(http.Flusher).Flush()
				time.Sleep(10 * time.Millisecond)
			}
			close(flushed)
			<-r.Context().Done() // withhold the rest until the client gives up
		})
		go func() {
			<-flushed
			cancel()
		}()
		_, _, err := DoRequestWithRedirects(request(ctx, srv.URL), true, false)
		require.NotNil(t, err)
		// Only a failed body read reports 500; a cancel during setup would be a 502.
		require.Equal(t, http.StatusInternalServerError, err.Code(), err.Error())
		require.Contains(t, err.Error(), "failed to read HTTP response")
		tracker.requireClosed(t)
	})

	t.Run("overlapping calls do not interrupt each other", func(t *testing.T) {
		releaseCh := make(chan struct{})
		var releaseOnce sync.Once
		release := func() { releaseOnce.Do(func() { close(releaseCh) }) }
		// A failed assertion below must not leave the slow handler blocked, which
		// would hang the server's Close.
		defer release()
		started := make(chan struct{}, 2)
		srv, tracker := newTrackedTLSServer(t, func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/slow" {
				started <- struct{}{}
				<-releaseCh
				_, _ = w.Write([]byte("slow"))
				return
			}
			_, _ = w.Write([]byte("fast"))
		})

		type result struct {
			body string
			err  *httperr.Error
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		slow := make(chan result, 1)
		go func() {
			_, body, err := DoRequestWithRedirects(request(ctx, srv.URL+"/slow"), true, false)
			slow <- result{string(body), err}
		}()
		select {
		case <-started:
		case r := <-slow:
			t.Fatalf("slow request ended before it was in flight: %+v", r)
		case <-time.After(5 * time.Second):
			t.Fatal("slow request never reached the server")
		}

		// A call that finishes, and closes its own transport, while the slow one is
		// still in flight must not cut it short.
		_, body, err := DoRequestWithRedirects(request(ctx, srv.URL+"/fast"), true, false)
		require.Nil(t, err)
		require.Equal(t, "fast", string(body))

		release()
		select {
		case r := <-slow:
			require.Nil(t, r.err)
			require.Equal(t, "slow", r.body)
		case <-time.After(5 * time.Second):
			t.Fatal("slow request did not finish after release")
		}
		tracker.requireClosed(t)
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

func TestDoRequestLogCannotForgeLines(t *testing.T) {
	// DoRequest logs the URL at verbosity 4.
	var buf bytes.Buffer
	flags := flag.NewFlagSet("klog", flag.ContinueOnError)
	klog.InitFlags(flags)
	require.NoError(t, flags.Set("v", "4"))
	klog.LogToStderr(false)
	klog.SetOutput(&buf)
	t.Cleanup(func() {
		require.NoError(t, flags.Set("v", "0"))
		klog.SetOutput(os.Stderr)
		klog.LogToStderr(true)
	})

	// url.URL.String() escapes the path and host but writes RawQuery as is.
	f := func() (*http.Request, *httperr.Error) {
		req, e := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://127.0.0.1:1/topology", nil)
		if e != nil {
			return nil, httperr.NewError(http.StatusInternalServerError, e.Error())
		}
		req.URL.RawQuery = "page=1\nE1027 00:00:00.000000 1 forged.go:1] FORGED"
		return req, nil
	}
	_, _, _ = DoRequest(f, false)
	klog.Flush()

	require.Contains(t, buf.String(), "FORGED", "the URL is still logged")
	for _, line := range strings.Split(buf.String(), "\n") {
		require.False(t, strings.HasPrefix(line, "E1027"), "the URL started a log line: %q", line)
	}
}

func TestDoRequestWithRedirects(t *testing.T) {
	newServers := func(t *testing.T, status int) (redirecting *httptest.Server, targetKey *string, targetCalled *bool) {
		key, called := new(string), new(bool)
		target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			*called = true
			*key = r.Header.Get("X-Api-Key")
			_, _ = w.Write([]byte("target"))
		}))
		t.Cleanup(target.Close)

		redirecting = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, target.URL, status)
		}))
		t.Cleanup(redirecting.Close)
		return redirecting, key, called
	}
	request := func(url string) RequestFunc {
		return GetRequestFunc(context.Background(), http.MethodPost, map[string]string{"X-Api-Key": "secret"}, nil, []byte(`{}`), url)
	}

	for _, status := range []int{
		http.StatusMovedPermanently, http.StatusFound, http.StatusTemporaryRedirect, http.StatusPermanentRedirect,
	} {
		t.Run(http.StatusText(status)+" is refused when redirects are not allowed", func(t *testing.T) {
			srv, targetKey, targetCalled := newServers(t, status)

			_, body, httpErr := DoRequestWithRedirects(request(srv.URL), false, false)
			require.Nil(t, body)
			require.NotNil(t, httpErr)
			require.Equal(t, http.StatusBadGateway, httpErr.Code())
			require.Contains(t, httpErr.Error(), "refusing to follow a redirect")
			require.False(t, *targetCalled, "the redirect target must not be contacted")
			require.Empty(t, *targetKey)
		})
	}

	t.Run("redirects are followed when allowed", func(t *testing.T) {
		srv, targetKey, _ := newServers(t, http.StatusTemporaryRedirect)

		_, body, httpErr := DoRequestWithRedirects(request(srv.URL), false, true)
		require.Nil(t, httpErr)
		require.Equal(t, "target", string(body))
		require.Equal(t, "secret", *targetKey)
	})

	t.Run("DoRequest follows redirects", func(t *testing.T) {
		srv, _, _ := newServers(t, http.StatusTemporaryRedirect)

		_, body, httpErr := DoRequest(request(srv.URL), false)
		require.Nil(t, httpErr)
		require.Equal(t, "target", string(body))
	})

	t.Run("a direct answer is unaffected when redirects are not allowed", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte("direct"))
		}))
		defer srv.Close()

		_, body, httpErr := DoRequestWithRedirects(request(srv.URL), false, false)
		require.Nil(t, httpErr)
		require.Equal(t, "direct", string(body))
	})
}

func TestRequestOptionsPreserveClientAndRedactRetries(t *testing.T) {
	var logs bytes.Buffer
	flags := flag.NewFlagSet("redacted-http", flag.ContinueOnError)
	klog.InitFlags(flags)
	require.NoError(t, flags.Set("v", "4"))
	klog.LogToStderr(false)
	klog.SetOutput(&logs)
	t.Cleanup(func() {
		require.NoError(t, flags.Set("v", "0"))
		klog.SetOutput(os.Stderr)
		klog.LogToStderr(true)
	})
	calls := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		data, err := io.ReadAll(r.Body)
		if err != nil || string(data) != "fake-request-secret" {
			t.Errorf("request body was not replayed correctly")
		}
		w.Header().Set("Retry-After", "0")
		status := http.StatusServiceUnavailable
		if calls == 2 {
			status = http.StatusUnauthorized
		}
		w.WriteHeader(status)
		_, _ = io.WriteString(w, "fake-response-secret")
	}))
	defer server.Close()
	factory := func() (*http.Request, *httperr.Error) {
		req, err := http.NewRequest(http.MethodPost, server.URL+"?secret=fake-url-secret", strings.NewReader("fake-request-secret"))
		require.NoError(t, err)
		return req, nil
	}
	body, err := DoRequestWithRetriesAndOptions(factory, false, RequestOptions{Client: server.Client(), MaxResponseBytes: 4, Redact: true})
	require.NotNil(t, err)
	require.Equal(t, http.StatusUnauthorized, err.Code())
	require.EqualError(t, err, "HTTP request failed")
	require.Equal(t, "fake", string(body), "the body remains available within the configured limit")
	require.Equal(t, 2, calls)
	klog.Flush()
	require.Contains(t, logs.String(), "Retrying")
	for _, secret := range []string{server.URL, "fake-request-secret", "fake-response-secret", "fake-url-secret"} {
		require.NotContains(t, logs.String(), secret)
	}
}

func TestRequestOptionsRedactFactoryErrors(t *testing.T) {
	_, _, err := DoRequestWithOptions(func() (*http.Request, *httperr.Error) {
		return nil, httperr.NewError(http.StatusBadRequest, "fake-secret")
	}, false, RequestOptions{Redact: true})
	require.NotNil(t, err)
	require.Equal(t, http.StatusBadRequest, err.Code())
	require.EqualError(t, err, "HTTP request construction failed")
}
