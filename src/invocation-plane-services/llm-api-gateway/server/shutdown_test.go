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
	"sync/atomic"
	"testing"
	"time"

	echo "github.com/labstack/echo/v4"

	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/config"
	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/nvcf"
	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/provider"
)

// TestShutdownStopsDependenciesAfterDrain checks that teardown steps run only
// once in-flight requests finish, and that Shutdown waits for them.
func TestShutdownStopsDependenciesAfterDrain(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name    string
		release bool
	}{
		{name: "request finishes during the drain", release: true},
		{name: "request outlives the drain"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			e := echo.New()
			teardown := newTeardown(e)
			var stopped atomic.Bool
			teardown.add(func() { stopped.Store(true) })

			started := make(chan struct{})
			release := make(chan struct{})
			t.Cleanup(func() { close(release) })
			stoppedWhileInFlight := make(chan bool, 1)
			e.GET("/slow", func(c echo.Context) error {
				close(started)
				select {
				case <-release:
				case <-c.Request().Context().Done():
				}
				stoppedWhileInFlight <- stopped.Load()
				return c.String(http.StatusOK, "done")
			})
			shutdownStarted := make(chan struct{})
			e.Server.RegisterOnShutdown(func() { close(shutdownStarted) })

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

			timeout := 20 * time.Second
			if !tc.release {
				timeout = 300 * time.Millisecond
			}
			ctx, cancel := context.WithTimeout(context.Background(), timeout)
			defer cancel()
			shutdownDone := make(chan error, 1)
			go func() { shutdownDone <- Shutdown(ctx, e) }()
			waitFor(t, shutdownStarted, "shutdown to start")

			if !tc.release {
				if err := waitFor(t, shutdownDone, "shutdown to return"); !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("Shutdown() error = %v, want context.DeadlineExceeded", err)
				}
				if stopped.Load() {
					t.Fatal("dependencies stopped while a request was still in flight")
				}
				return
			}

			release <- struct{}{}
			if waitFor(t, stoppedWhileInFlight, "handler to finish") {
				t.Fatal("dependencies stopped before the in-flight request finished")
			}
			if err := waitFor(t, shutdownDone, "shutdown to return"); err != nil {
				t.Fatalf("Shutdown() error = %v, want nil", err)
			}
			if !stopped.Load() {
				t.Fatal("Shutdown returned before dependencies stopped")
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
