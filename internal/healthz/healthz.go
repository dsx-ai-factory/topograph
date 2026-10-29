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

// Package healthz serves a /healthz endpoint that answers 200 "ok" for as long
// as the process runs.
package healthz

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"k8s.io/klog/v2"
)

// Variables so tests can shorten them.
var (
	readHeaderTimeout = 5 * time.Second
	readTimeout       = 5 * time.Second
	idleTimeout       = 5 * time.Second
	shutdownTimeout   = 5 * time.Second
)

// Serve runs a minimal HTTP server exposing /healthz on port. It blocks until
// ctx is cancelled, then shuts down gracefully, or returns early if the server
// fails, for example because the port is taken.
func Serve(ctx context.Context, port int) error {
	srv := &http.Server{
		Addr:              fmt.Sprintf(":%d", port),
		Handler:           Handler(),
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		IdleTimeout:       idleTimeout,
	}

	errCh := make(chan error, 1)
	go func() {
		klog.Infof("Serving health endpoint on %s", srv.Addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case <-ctx.Done():
		klog.Info("Shutting down health endpoint")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("shutting down health endpoint on %s: %w", srv.Addr, err)
		}
		return nil
	case err := <-errCh:
		return fmt.Errorf("serving health endpoint on %s: %w", srv.Addr, err)
	}
}

// Handler answers GET /healthz with 200 "ok".
func Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	return mux
}
