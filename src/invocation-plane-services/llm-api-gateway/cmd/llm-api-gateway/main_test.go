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

package main

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"
)

// TestRunGatewayDrainsInFlightRequests stops the gateway while a request is
// in flight and checks that shutdown waits for it, bounded by the timeout.
func TestRunGatewayDrainsInFlightRequests(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name    string
		release bool
		timeout time.Duration
	}{
		{name: "request finishes during the drain", release: true, timeout: 30 * time.Second},
		{name: "request outlives the drain", timeout: 500 * time.Millisecond},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			timeout := tc.timeout
			started := make(chan struct{})
			release := make(chan struct{})
			var releaseOnce sync.Once
			releaseHandler := func() { releaseOnce.Do(func() { close(release) }) }
			t.Cleanup(releaseHandler)
			server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				close(started)
				select {
				case <-release:
				case <-r.Context().Done():
					return
				}
				_, _ = io.WriteString(w, "done")
			})}
			t.Cleanup(func() { _ = server.Close() })
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}

			shutdownDeadline := make(chan time.Time, 1)
			shutdown := func(ctx context.Context) error {
				deadline, _ := ctx.Deadline()
				shutdownDeadline <- deadline
				return server.Shutdown(ctx)
			}

			ctx, stop := context.WithCancel(context.Background())
			defer stop()
			gatewayDone := make(chan error, 1)
			go func() {
				gatewayDone <- runGateway(
					ctx,
					listener.Addr().String(),
					timeout,
					func(string) error { return server.Serve(listener) },
					shutdown,
				)
			}()

			responseDone := make(chan string, 1)
			go func() {
				client := &http.Client{Timeout: 30 * time.Second}
				resp, err := client.Get("http://" + listener.Addr().String())
				if err != nil {
					responseDone <- "error: " + err.Error()
					return
				}
				defer resp.Body.Close()
				body, _ := io.ReadAll(resp.Body)
				responseDone <- string(body)
			}()

			waitFor(t, started, "request to start")
			stopped := time.Now()
			stop()
			// The drain gets the configured timeout, measured from shutdown.
			if got := waitFor(t, shutdownDeadline, "shutdown deadline").Sub(stopped); got < timeout || got > timeout+10*time.Second {
				t.Fatalf("shutdown deadline is %s after stop, want about %s", got, timeout)
			}

			if tc.release {
				releaseHandler()
				if err := waitFor(t, gatewayDone, "gateway to stop"); err != nil {
					t.Fatalf("runGateway() error = %v, want nil", err)
				}
				if body := waitFor(t, responseDone, "response"); body != "done" {
					t.Fatalf("in-flight response = %q, want done", body)
				}
				return
			}
			if err := waitFor(t, gatewayDone, "gateway to stop"); !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("runGateway() error = %v, want context.DeadlineExceeded", err)
			}
		})
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

func TestShutdownTimeoutFallsBackToDefault(t *testing.T) {
	t.Parallel()

	if got := shutdownTimeout(0); got != defaultGatewayShutdownTimeout {
		t.Fatalf("shutdownTimeout(0) = %s, want %s", got, defaultGatewayShutdownTimeout)
	}
	if got := shutdownTimeout(time.Minute); got != time.Minute {
		t.Fatalf("shutdownTimeout(1m) = %s, want 1m", got)
	}
}
