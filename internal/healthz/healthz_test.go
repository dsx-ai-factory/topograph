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

package healthz

import (
	"bufio"
	"context"
	"errors"
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

// The handler never reads a request body, so without ReadTimeout and
// IdleTimeout a client could hold a connection open indefinitely, either with
// a body it never sends or by keeping an idle connection alive.
func TestServeClosesStalledConnections(t *testing.T) {
	saved := []time.Duration{readTimeout, idleTimeout}
	readTimeout, idleTimeout = 200*time.Millisecond, 200*time.Millisecond
	t.Cleanup(func() { readTimeout, idleTimeout = saved[0], saved[1] })

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := ln.Addr().(*net.TCPAddr).Port
	require.NoError(t, ln.Close())

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, port) }()
	t.Cleanup(func() { cancel(); <-done })

	addr := fmt.Sprintf("127.0.0.1:%d", port)
	require.Eventually(t, func() bool {
		c, err := net.Dial("tcp", addr)
		if err == nil {
			_ = c.Close()
		}
		return err == nil
	}, 5*time.Second, 10*time.Millisecond)

	for name, req := range map[string]string{
		"idle keep-alive": "GET /healthz HTTP/1.1\r\nHost: x\r\n\r\n",
		"unsent body":     "POST /healthz HTTP/1.1\r\nHost: x\r\nContent-Length: 1000\r\n\r\n",
	} {
		t.Run(name, func(t *testing.T) {
			conn, err := net.Dial("tcp", addr)
			require.NoError(t, err)
			defer func() { _ = conn.Close() }()

			// The server must answer and close the connection well before the
			// client gives up. For a body it never gets, Go answers only once
			// its attempt to drain the body times out.
			require.NoError(t, conn.SetDeadline(time.Now().Add(3*time.Second)))
			_, err = conn.Write([]byte(req))
			require.NoError(t, err)
			r := bufio.NewReader(conn)
			resp, err := http.ReadResponse(r, nil)
			require.NoError(t, err)
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			require.Equal(t, http.StatusOK, resp.StatusCode)

			_, err = r.ReadByte()
			require.True(t, errors.Is(err, io.EOF), "server kept the connection open: %v", err)
		})
	}
}
