/*
SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

	http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/
package server

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	echo "github.com/labstack/echo/v4"

	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/config"
	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/nvcf"
	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/provider"
)

// TestShutdownStopsDependenciesAfterDrain checks that teardown steps run only
// after in-flight requests end: after they finish when the drain succeeds,
// and after they are canceled when it times out.
func TestShutdownStopsDependenciesAfterDrain(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name    string
		release bool
		timeout time.Duration
		wantErr error
	}{
		{name: "request finishes during the drain", release: true, timeout: 30 * time.Second},
		{name: "request outlives the drain", timeout: 300 * time.Millisecond, wantErr: context.DeadlineExceeded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			e := echo.New()
			teardown := newTeardown(e)
			var stops atomic.Int32
			teardown.add(func() { stops.Add(1) })

			started := make(chan struct{})
			release := make(chan struct{})
			var releaseOnce sync.Once
			releaseHandler := func() { releaseOnce.Do(func() { close(release) }) }
			t.Cleanup(releaseHandler)
			stoppedDuringRequest := make(chan bool, 1)
			e.GET("/slow", func(c echo.Context) error {
				close(started)
				select {
				case <-release:
				case <-c.Request().Context().Done():
				}
				stoppedDuringRequest <- stops.Load() > 0
				return c.String(http.StatusOK, "done")
			})
			shutdownStarted := make(chan struct{})
			var shutdownOnce sync.Once
			// http.Server reruns this hook on every Shutdown call.
			e.Server.RegisterOnShutdown(func() { shutdownOnce.Do(func() { close(shutdownStarted) }) })

			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			go func() { _ = e.Server.Serve(listener) }()
			t.Cleanup(func() { _ = e.Close() })

			go func() {
				client := &http.Client{Timeout: 30 * time.Second}
				resp, err := client.Get("http://" + listener.Addr().String() + "/slow")
				if err == nil {
					_, _ = io.Copy(io.Discard, resp.Body)
					resp.Body.Close()
				}
			}()
			waitFor(t, started, "request to start")

			ctx, cancel := context.WithTimeout(context.Background(), tc.timeout)
			defer cancel()
			shutdownDone := make(chan error, 1)
			go func() { shutdownDone <- Shutdown(ctx, e) }()
			waitFor(t, shutdownStarted, "shutdown to start")
			if tc.release {
				releaseHandler()
			}

			// Timing out cancels the request instead of leaving it running.
			if waitFor(t, stoppedDuringRequest, "request to end") {
				t.Fatal("dependencies stopped while the request was still in flight")
			}
			if err := waitFor(t, shutdownDone, "shutdown to return"); !errors.Is(err, tc.wantErr) {
				t.Fatalf("Shutdown() error = %v, want %v", err, tc.wantErr)
			}
			if got := stops.Load(); got != 1 {
				t.Fatalf("teardown ran %d times, want 1", got)
			}

			// A repeated Shutdown must not run teardown again.
			if err := Shutdown(context.Background(), e); err != nil && !errors.Is(err, http.ErrServerClosed) {
				t.Fatalf("second Shutdown() error = %v", err)
			}
			if got := stops.Load(); got != 1 {
				t.Fatalf("teardown ran %d times after a second Shutdown, want 1", got)
			}
		})
	}
}

type closingAuthClient struct {
	closed atomic.Bool
}

func (c *closingAuthClient) AuthorizeInvocation(context.Context, string, string) (*nvcf.InvocationAuthResponse, error) {
	return &nvcf.InvocationAuthResponse{}, nil
}

func (c *closingAuthClient) Close() error {
	c.closed.Store(true)
	return nil
}

func TestShutdownClosesAuthClient(t *testing.T) {
	t.Parallel()

	cfg := config.Default()
	cfg.RateLimiter.Enabled = false
	inferenceProvider, err := provider.NewStargateProvider(config.StargateConfig{URL: "http://127.0.0.1:1"})
	if err != nil {
		t.Fatal(err)
	}
	auth := &closingAuthClient{}
	e, err := New(cfg, inferenceProvider, auth)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = e.Server.Serve(listener) }()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := Shutdown(ctx, e); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
	if !auth.closed.Load() {
		t.Fatal("auth client was not closed")
	}
}

func waitFor[T any](t *testing.T, ch <-chan T, what string) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(20 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
		panic("unreachable")
	}
}
