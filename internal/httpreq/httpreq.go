// SPDX-FileCopyrightText: Copyright (c) 2024-2025 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"k8s.io/klog/v2"

	"github.com/dsx-ai-factory/topograph/internal/httperr"
)

const (
	// maxRetries is the maximum number of retry attempts
	maxRetries = 5

	// backOff is the initial delay used for retry backoff
	backOff = 500 * time.Millisecond

	// maxRetryAfter is the maximum delay allowed when honoring a Retry-After header
	maxRetryAfter = 5 * time.Minute
)

// ShouldRetry returns true if the given HTTP status code is retryable
func ShouldRetry(status int) bool {
	switch status {
	case
		http.StatusRequestTimeout,      // 408
		http.StatusTooManyRequests,     // 429
		http.StatusInternalServerError, // 500
		http.StatusBadGateway,          // 502
		http.StatusServiceUnavailable,  // 503
		http.StatusGatewayTimeout:      // 504
		return true
	default:
		return false
	}
}

func ParseRetryAfter(resp *http.Response) (time.Duration, bool) {
	if resp == nil {
		return 0, false
	}

	value := resp.Header.Get("Retry-After")
	if len(value) == 0 {
		return 0, false
	}

	// check if Retry-After is seconds
	if seconds, err := strconv.Atoi(value); err == nil && seconds >= 0 {
		if seconds > int(maxRetryAfter/time.Second) {
			return maxRetryAfter, true
		}
		return time.Duration(seconds) * time.Second, true
	}

	// check if Retry-After is an HTTP date
	if t, err := http.ParseTime(value); err == nil {
		if delay := time.Until(t); delay > 0 {
			if delay > maxRetryAfter {
				delay = maxRetryAfter
			}
			return delay, true
		}
	}

	return 0, false
}

type RequestFunc func() (*http.Request, *httperr.Error)

// RequestOptions customizes HTTP execution without changing existing callers.
// Client supplies transport and timeout policies. MaxResponseBytes bounds reads
// when positive. Redact omits URLs and raw upstream details from logs and errors;
// the response body is still returned to trusted callers for API error mapping.
type RequestOptions struct {
	Client           *http.Client
	MaxResponseBytes int64
	Redact           bool
}

var errRedirectNotAllowed = errors.New("redirects are not allowed")

// DoRequest sends an HTTP request with the existing default policies.
func DoRequest(f RequestFunc, insecureSkipVerify bool) (*http.Response, []byte, *httperr.Error) {
	return DoRequestWithOptions(f, insecureSkipVerify, RequestOptions{})
}

// DoRequestWithOptions sends an HTTP request using explicit execution options.
func DoRequestWithOptions(f RequestFunc, insecureSkipVerify bool, opts RequestOptions) (*http.Response, []byte, *httperr.Error) {
	return doRequest(f, insecureSkipVerify, true, opts)
}

// DoRequestWithRedirects sends HTTP requests and returns HTTP response. When
// allowRedirects is false, a redirect is not followed but reported as a 502
// error, so request headers such as an API key are only ever sent to the
// requested URL.
func DoRequestWithRedirects(f RequestFunc, insecureSkipVerify, allowRedirects bool) (*http.Response, []byte, *httperr.Error) {
	return doRequest(f, insecureSkipVerify, allowRedirects, RequestOptions{})
}

func doRequest(f RequestFunc, insecureSkipVerify, allowRedirects bool, opts RequestOptions) (*http.Response, []byte, *httperr.Error) {
	req, httpErr := f()
	if httpErr != nil {
		if opts.Redact {
			return nil, nil, httperr.NewError(httpErr.Code(), "HTTP request construction failed")
		}
		return nil, nil, httpErr
	}
	if opts.Redact {
		klog.V(4).Info("Sending HTTP request")
	} else {
		// Quote URLs so line breaks cannot forge log lines.
		klog.V(4).Infof("Sending HTTP request %q", req.URL.String())
	}
	client := opts.Client
	if client == nil {
		client = &http.Client{}
		if insecureSkipVerify {
			transport := &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}
			client.Transport = transport
			// This transport is private to the call and cannot reuse connections.
			defer transport.CloseIdleConnections()
		}
	}
	if !allowRedirects {
		client.CheckRedirect = func(req *http.Request, _ []*http.Request) error {
			// Name only the scheme and host: the Location is server-controlled and its
			// userinfo, path or query could carry secrets or control characters.
			return fmt.Errorf("%w: refusing to follow a redirect to %s://%s", errRedirectNotAllowed, req.URL.Scheme, req.URL.Host)
		}
	}
	resp, err := client.Do(req)
	if err != nil {
		if opts.Redact {
			return resp, nil, redactedRequestError(err)
		}
		code := http.StatusBadGateway
		msg := err.Error()
		var urlErr *url.Error
		isURLErr := errors.As(err, &urlErr)
		switch {
		case errors.Is(err, errRedirectNotAllowed):
			// The *url.Error wrapping the refusal repeats the raw Location; report
			// only the sanitized refusal from CheckRedirect.
			if isURLErr {
				msg = urlErr.Err.Error()
			}
		case isURLErr && strings.Contains(urlErr.Err.Error(), "Location header"):
			// net/http rejects an unparsable Location before CheckRedirect runs and
			// quotes it, userinfo and query included, in the error.
			msg = fmt.Sprintf("%s %q: failed to parse the redirect Location header", urlErr.Op, urlErr.URL)
		case resp != nil:
			code = resp.StatusCode
		}
		return resp, nil, httperr.NewError(code, msg)
	}
	defer func() { _ = resp.Body.Close() }()
	var reader io.Reader = resp.Body
	if opts.MaxResponseBytes > 0 {
		reader = io.LimitReader(reader, opts.MaxResponseBytes)
	}
	body, err := io.ReadAll(reader)
	if err != nil {
		if opts.Redact {
			return resp, nil, redactedRequestError(err)
		}
		return resp, nil, httperr.NewError(http.StatusInternalServerError, fmt.Sprintf("failed to read HTTP response: %v", err))
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return resp, body, nil
	}
	message := string(body)
	if opts.Redact {
		message = "HTTP request failed"
	}
	return resp, body, httperr.NewError(resp.StatusCode, message)
}

// CancellationStatus maps a canceled request to 408 and a deadline or network
// timeout to 504. The bool is false for any other error.
func CancellationStatus(err error) (int, bool) {
	if errors.Is(err, context.Canceled) {
		return http.StatusRequestTimeout, true
	}
	var timeout net.Error
	if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &timeout) && timeout.Timeout()) {
		return http.StatusGatewayTimeout, true
	}
	return 0, false
}

func redactedRequestError(err error) *httperr.Error {
	switch code, _ := CancellationStatus(err); code {
	case http.StatusRequestTimeout:
		return httperr.NewError(code, "HTTP request canceled")
	case http.StatusGatewayTimeout:
		return httperr.NewError(code, "HTTP request timed out")
	}
	return httperr.NewError(http.StatusBadGateway, "HTTP request failed")
}

// DoRequestWithRetries sends HTTP requests and returns HTTP response; retries if needed
func DoRequestWithRetries(f RequestFunc, insecureSkipVerify bool) ([]byte, *httperr.Error) {
	return DoRequestWithRetriesAndOptions(f, insecureSkipVerify, RequestOptions{})
}

// DoRequestWithRetriesAndOptions uses the shared retry policy with explicit HTTP options.
func DoRequestWithRetriesAndOptions(f RequestFunc, insecureSkipVerify bool, opts RequestOptions) ([]byte, *httperr.Error) {
	klog.V(4).Infof("Sending HTTP request with retries")
	attempt := 0
	for {
		attempt++
		var requestCtx context.Context
		resp, body, err := DoRequestWithOptions(func() (*http.Request, *httperr.Error) {
			req, httpErr := f()
			if req != nil {
				requestCtx = req.Context()
			}
			return req, httpErr
		}, insecureSkipVerify, opts)
		if requestCtx != nil && requestCtx.Err() != nil {
			return body, err
		}
		if err == nil || attempt == maxRetries || !ShouldRetry(err.Code()) {
			return body, err
		}
		wait := GetNextBackoff(resp, backOff, attempt-1)
		klog.Infof("Attempt %d failed with error: %v. Retrying in %s", attempt, err, wait.String())
		if !waitForRetry(requestCtx, wait) {
			return body, err
		}
	}
}

func waitForRetry(ctx context.Context, delay time.Duration) bool {
	if ctx == nil {
		time.Sleep(delay)
		return true
	}

	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}

// GetURL builds a fully-qualified URL from a base URL, optional path segments,
// and optional query parameters.
func GetURL(baseURL string, query map[string]string, paths ...string) (string, *httperr.Error) {
	u, err := url.Parse(baseURL)
	if err != nil {
		return "", httperr.NewError(http.StatusBadRequest, err.Error())
	}

	u.Path = path.Join(append([]string{u.Path}, paths...)...)

	if len(query) != 0 {
		q := u.Query()
		for key, val := range query {
			q.Set(key, val)
		}
		u.RawQuery = q.Encode()
	}

	return u.String(), nil
}

func GetRequestFunc(ctx context.Context, method string, headers, query map[string]string, payload []byte, baseUrl string, paths ...string) RequestFunc {
	return func() (*http.Request, *httperr.Error) {
		u, httpErr := GetURL(baseUrl, query, paths...)
		if httpErr != nil {
			return nil, httpErr
		}
		klog.V(4).Infof("Fetching %s", u)
		req, err := http.NewRequestWithContext(ctx, method, u, bytes.NewBuffer(payload))
		if err != nil {
			return nil, httperr.NewError(http.StatusInternalServerError, fmt.Sprintf("failed to create HTTP request: %v", err))
		}
		for key, val := range headers {
			req.Header.Add(key, val)
		}
		return req, nil
	}
}

// GetNextBackoff determines the retry delay from Retry-After header or exponential backoff
func GetNextBackoff(resp *http.Response, initialBackoff time.Duration, attempt int) time.Duration {
	wait, valid := ParseRetryAfter(resp)
	if !valid {
		wait = initialBackoff * time.Duration(int(math.Pow(2, float64(attempt))))
	}
	return wait
}
