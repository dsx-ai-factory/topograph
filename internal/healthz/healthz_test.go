/*
 * Copyright 2026 NVIDIA CORPORATION
 * SPDX-License-Identifier: Apache-2.0
 */

package healthz

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestHandler(t *testing.T) {
	srv := httptest.NewServer(Handler())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/healthz")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusOK, resp.StatusCode)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, "ok", string(body))

	other, err := http.Get(srv.URL + "/other")
	require.NoError(t, err)
	defer func() { _ = other.Body.Close() }()
	require.Equal(t, http.StatusNotFound, other.StatusCode)
}

func TestServeAnswersUntilContextCancel(t *testing.T) {
	// Reserve a free port, then release it for Serve.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := ln.Addr().(*net.TCPAddr).Port
	require.NoError(t, ln.Close())

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, port) }()

	url := fmt.Sprintf("http://127.0.0.1:%d/healthz", port)
	require.Eventually(t, func() bool {
		resp, err := http.Get(url)
		if err != nil {
			return false
		}
		defer func() { _ = resp.Body.Close() }()
		body, err := io.ReadAll(resp.Body)
		return err == nil && resp.StatusCode == http.StatusOK && string(body) == "ok"
	}, 5*time.Second, 10*time.Millisecond)

	cancel()

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not return after context cancellation")
	}
}

func TestServeReportsListenError(t *testing.T) {
	ln, err := net.Listen("tcp", ":0")
	require.NoError(t, err)
	defer func() { _ = ln.Close() }()
	port := ln.Addr().(*net.TCPAddr).Port

	// The port is taken, so Serve must return the listen error rather than block.
	select {
	case err := <-serveAsync(port):
		require.ErrorContains(t, err, fmt.Sprintf("health endpoint on :%d", port))
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not report the listen error")
	}
}

func serveAsync(port int) <-chan error {
	done := make(chan error, 1)
	go func() { done <- Serve(context.Background(), port) }()
	return done
}
