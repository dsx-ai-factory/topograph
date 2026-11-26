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
	"math/rand/v2"
	nethttp "net/http"
	"strconv"
	"time"

	"k8s.io/klog/v2"

	"github.com/dsx-ai-factory/topograph/internal/httperr"
	"github.com/dsx-ai-factory/topograph/internal/httpreq"
)

const (
	maxAttempts    = 5
	initialBackoff = 500 * time.Millisecond
	maxBackoff     = 30 * time.Second
)

// fetchAll collects every page before returning. Any failure discards all
// pages, so a partial inventory never reaches graph conversion.
func (p *Provider) fetchAll(ctx context.Context, auth authenticator, pageSize int) ([]*record, *httperr.Error) {
	pc := p.cfg.pagination
	var records []*record
	pageNum, offset, cursor := pc.initialPage, pc.initialOffset, ""

	for pageIndex := 0; ; pageIndex++ {
		if pageIndex >= pc.maxPages {
			return nil, badGateway("pagination exceeded 'maxPages' (%d) without reaching the last page", pc.maxPages)
		}

		query := cloneValues(p.cfg.query)
		if pc.sizeParam != "" {
			query.Set(pc.sizeParam, strconv.Itoa(pageSize))
		}
		switch pc.mode {
		case paginationPage:
			query.Set(pc.pageParam, strconv.Itoa(pageNum))
		case paginationOffset:
			query.Set(pc.offsetParam, strconv.Itoa(offset))
		case paginationCursor:
			if cursor != "" {
				query.Set(pc.cursorParam, cursor)
			}
		}

		body, err := p.get(ctx, auth, query.Encode())
		if err != nil {
			return nil, err
		}
		pg, err := p.cfg.parser.parse(body, pageIndex)
		if err != nil {
			return nil, err
		}
		records = append(records, pg.records...)
		klog.V(4).Infof("http provider: page %d returned %d items", pageIndex, len(pg.records))

		switch pc.mode {
		case paginationNone:
			return records, nil
		case paginationPage, paginationOffset:
			if len(pg.records) == 0 || (pc.completion == completionShort && len(pg.records) < pageSize) {
				return records, nil
			}
			pageNum++
			offset += len(pg.records)
		case paginationCursor:
			if pg.nextCursor == "" {
				return records, nil
			}
			if pg.nextCursor == cursor {
				return nil, badGateway("page %d: upstream returned the same cursor again", pageIndex)
			}
			cursor = pg.nextCursor
		}
	}
}

// get performs one topology GET with authentication applied to every attempt
// and bounded retries. Errors never contain URLs, query values, or bodies.
func (p *Provider) get(ctx context.Context, auth authenticator, rawQuery string) ([]byte, *httperr.Error) {
	u := *p.cfg.url
	u.RawQuery = rawQuery
	target := u.String()

	opts := httpreq.RequestOptions{Client: p.client, MaxResponseBytes: p.cfg.maxResponseBytes, Redact: true}

	for attempt := 1; ; attempt++ {
		// Authentication errors are returned directly, preserving their status.
		name, value, authErr := auth.header(ctx)
		if authErr != nil {
			return nil, authErr
		}

		resp, body, err := httpreq.DoRequestWithOptions(func() (*nethttp.Request, *httperr.Error) {
			req, reqErr := nethttp.NewRequestWithContext(ctx, nethttp.MethodGet, target, nil)
			if reqErr != nil {
				return nil, httperr.NewError(nethttp.StatusBadRequest, "invalid topology request")
			}
			req.Header = p.cfg.headers.Clone()
			if name != "" {
				req.Header.Set(name, value)
			}
			return req, nil
		}, false, opts)

		if ctxErr := ctx.Err(); ctxErr != nil {
			code, _ := httpreq.CancellationStatus(ctxErr)
			return nil, httperr.NewError(code, "topology request canceled or timed out")
		}
		if err == nil {
			return body, nil
		}
		if httpreq.IsResponseTooLarge(err) {
			return nil, badGateway("topology response exceeds 'maxResponseBytes' (%d)", p.cfg.maxResponseBytes)
		}

		status := 0
		if resp != nil {
			status = resp.StatusCode
		}
		var final *httperr.Error
		switch {
		case status == 0 && (err.Code() == nethttp.StatusRequestTimeout || err.Code() == nethttp.StatusGatewayTimeout):
			// Client-side timeout of a single attempt; treat as transient.
			final = httperr.NewError(nethttp.StatusGatewayTimeout, "topology request timed out")
		case status == 0:
			final = badGateway("topology request failed")
		case status >= 300 && status < 400:
			return nil, badGateway("topology request was redirected (HTTP %d); redirects are not followed", status)
		default:
			final = badGateway("topology request failed with upstream HTTP %d", status)
		}

		if attempt >= maxAttempts || !retryable(status) {
			return nil, final
		}
		wait := p.backoff(resp, attempt)
		klog.Infof("http provider: topology request attempt %d failed (%s); retrying in %s", attempt, final.Error(), wait)
		timer := time.NewTimer(wait)
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			code, _ := httpreq.CancellationStatus(ctx.Err())
			return nil, httperr.NewError(code, "topology request canceled or timed out")
		}
	}
}

// retryable reports transient failures: transport errors (status 0) and
// 429/502/503/504 responses.
func retryable(status int) bool {
	switch status {
	case 0, nethttp.StatusTooManyRequests, nethttp.StatusBadGateway,
		nethttp.StatusServiceUnavailable, nethttp.StatusGatewayTimeout:
		return true
	}
	return false
}

// defaultBackoff honors Retry-After (capped by httpreq) and otherwise uses
// exponential backoff with jitter in [d/2, d].
func defaultBackoff(resp *nethttp.Response, attempt int) time.Duration {
	if wait, ok := httpreq.ParseRetryAfter(resp); ok {
		return wait
	}
	d := initialBackoff << (attempt - 1)
	if d > maxBackoff || d <= 0 {
		d = maxBackoff
	}
	half := d / 2
	return half + rand.N(half+1) //nolint:gosec // jitter does not need a CSPRNG
}
