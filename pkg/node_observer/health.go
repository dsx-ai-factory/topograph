/*
 * Copyright 2026 NVIDIA CORPORATION
 * SPDX-License-Identifier: Apache-2.0
 */

package node_observer

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"k8s.io/klog/v2"

	"github.com/dsx-ai-factory/topograph/internal/httpreq"
)

// healthCheckInterval is how long to wait between topograph health probes
// while the API is not yet ready.
const healthCheckInterval = 2 * time.Second

// healthCheckTimeout bounds how long to wait for the topograph API to become
// ready before giving up. When exceeded, waitForTopograph returns an error so
// the process exits non-zero and the pod restarts.
const healthCheckTimeout = 1 * time.Minute

// healthCheckURL derives the topograph health endpoint from the generate
// topology URL by replacing its path with /healthz.
func healthCheckURL(generateTopologyURL string) (string, error) {
	u, err := url.Parse(generateTopologyURL)
	if err != nil {
		return "", fmt.Errorf("failed to parse generateTopologyUrl %q: %w", generateTopologyURL, err)
	}
	u.Path = "/healthz"
	u.RawQuery = ""
	u.Fragment = ""
	return u.String(), nil
}

// waitForTopograph blocks until the topograph API health endpoint responds
// successfully, the context is cancelled, or timeout elapses. It replaces the
// chart's former `wait` init container. On timeout it returns an error so the
// caller can exit non-zero.
func waitForTopograph(ctx context.Context, healthURL string, interval, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	start := time.Now()
	f := httpreq.GetRequestFunc(ctx, http.MethodGet, nil, nil, nil, healthURL)
	timer := time.NewTimer(interval)
	if !timer.Stop() {
		<-timer.C
	}
	defer timer.Stop()

	for {
		_, _, err := httpreq.DoRequest(f, false)
		if err == nil {
			klog.Infof("Topograph API is ready at %s", healthURL)
			return nil
		}
		klog.Infof("Waiting for topograph to start at %s: %v", healthURL, err)

		timer.Reset(interval)
		select {
		case <-ctx.Done():
			// Report the actual elapsed time rather than the nominal timeout:
			// context.WithTimeout honours whichever deadline (this timeout or the
			// parent context's) fires first, so the two can differ.
			return fmt.Errorf("topograph API not ready at %s after %s: %w", healthURL, time.Since(start).Round(time.Second), ctx.Err())
		case <-timer.C:
		}
	}
}
