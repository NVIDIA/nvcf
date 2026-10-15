/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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

package health

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hellofresh/health-go/v5"
	"github.com/stretchr/testify/require"
)

// fakeCheck builds a *health.Health whose single check returns whatever the
// supplied atomic error pointer currently holds. nil => StatusOK.
func fakeCheck(t *testing.T, errPtr *atomic.Pointer[error]) *health.Health {
	t.Helper()
	h, err := health.New()
	require.NoError(t, err)
	require.NoError(t, h.Register(health.Config{
		Name:      "fake",
		Timeout:   2 * time.Second,
		SkipOnErr: false,
		Check: func(context.Context) error {
			if p := errPtr.Load(); p != nil {
				return *p
			}
			return nil
		},
	}))
	return h
}

// splitHostPort extracts the bare host and integer port from an httptest URL.
func splitHostPort(t *testing.T, raw string) (string, int) {
	t.Helper()
	u, err := url.Parse(raw)
	require.NoError(t, err)
	port, err := strconv.Atoi(u.Port())
	require.NoError(t, err)
	return u.Hostname(), port
}

func setErr(p *atomic.Pointer[error], err error) {
	if err == nil {
		p.Store(nil)
		return
	}
	p.Store(&err)
}

func TestNewInferenceHealthStatus(t *testing.T) {
	s := NewInferenceHealthStatus()
	require.NotNil(t, s)
	require.False(t, s.isHealthy)
	require.NotNil(t, s.healthyCond)
}

func TestFindHealthTransitions(t *testing.T) {
	var errPtr atomic.Pointer[error]
	h := fakeCheck(t, &errPtr)
	s := NewInferenceHealthStatus()

	// Initially healthy.
	setErr(&errPtr, nil)
	s.findHealth(context.Background(), h)
	require.True(t, s.isHealthy)

	// Transition to unhealthy: the registered callback must fire.
	var cbCount atomic.Int32
	cb := func() { cbCount.Add(1) }
	s.CallBackFn.Store(&cb)
	setErr(&errPtr, errors.New("down"))
	s.findHealth(context.Background(), h)
	require.False(t, s.isHealthy)
	require.Equal(t, int32(1), cbCount.Load(), "callback fires on unhealthy")

	// Unhealthy with no callback registered must not panic.
	s.CallBackFn.Store(nil)
	s.findHealth(context.Background(), h)
	require.False(t, s.isHealthy)

	// Back to healthy.
	setErr(&errPtr, nil)
	s.findHealth(context.Background(), h)
	require.True(t, s.isHealthy)
}

func TestWaitForHealthyStateAlreadyHealthy(t *testing.T) {
	var errPtr atomic.Pointer[error]
	h := fakeCheck(t, &errPtr)
	s := NewInferenceHealthStatus()
	s.findHealth(context.Background(), h) // becomes healthy

	require.NoError(t, s.WaitForHealthyState(context.Background()))
}

func TestWaitForHealthyStateBecomesHealthy(t *testing.T) {
	var errPtr atomic.Pointer[error]
	setErr(&errPtr, errors.New("not ready"))
	h := fakeCheck(t, &errPtr)
	s := NewInferenceHealthStatus()
	s.findHealth(context.Background(), h) // unhealthy

	done := make(chan error, 1)
	go func() { done <- s.WaitForHealthyState(context.Background()) }()

	// Flip to healthy and trigger a re-measure which broadcasts the cond.
	setErr(&errPtr, nil)
	s.findHealth(context.Background(), h)

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("WaitForHealthyState did not return after becoming healthy")
	}
}

func TestWaitForHealthyStateContextCanceled(t *testing.T) {
	var errPtr atomic.Pointer[error]
	setErr(&errPtr, errors.New("never ready"))
	h := fakeCheck(t, &errPtr)
	s := NewInferenceHealthStatus()
	s.findHealth(context.Background(), h) // unhealthy

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.WaitForHealthyState(ctx) }()
	cancel()

	select {
	case err := <-done:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(5 * time.Second):
		t.Fatal("WaitForHealthyState did not return after context cancel")
	}
}

func TestRunHealthCheckRoutineExitsOnContextDone(t *testing.T) {
	var errPtr atomic.Pointer[error]
	h := fakeCheck(t, &errPtr)
	s := NewInferenceHealthStatus()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		s.RunHealthCheckRoutine(ctx, h) // runs initial findHealth, then ticks
		close(done)
	}()

	// The initial findHealth runs synchronously before the ticker loop, so the
	// state should reflect healthy shortly after start. Read under the mutex
	// since the routine goroutine writes isHealthy concurrently.
	isHealthy := func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.isHealthy
	}
	require.Eventually(t, isHealthy, 2*time.Second, 10*time.Millisecond)

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("RunHealthCheckRoutine did not exit on ctx.Done")
	}
}

func TestHealthCheckConfigHTTPDefaultsAndProbe(t *testing.T) {
	// httptest server returns 200 on the configured path.
	var path atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path.Store(r.URL.Path)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	host, port := splitHostPort(t, srv.URL)

	cfg, err := HealthCheckConfig(host, "" /*protocol default http*/, "/custom/health", 0 /*timeout default*/, 0 /*code default 200*/, port, 9999)
	require.NoError(t, err)
	require.Equal(t, "inference", cfg.Name)
	require.Equal(t, defaultInferenceHealthTimeout, cfg.Timeout)
	require.NotNil(t, cfg.Check)

	require.NoError(t, cfg.Check(context.Background()), "probe against 200 endpoint succeeds")
	require.Equal(t, "/custom/health", path.Load())
}

func TestHealthCheckConfigHTTPUnexpectedCode(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	host, port := splitHostPort(t, srv.URL)
	cfg, err := HealthCheckConfig(host, "http", "/health", time.Second, http.StatusOK, port, 0)
	require.NoError(t, err)

	err = cfg.Check(context.Background())
	require.Error(t, err, "permanent error on unexpected status code")
}

func TestHealthCheckConfigHTTPDefaultPathAndExpectedCode(t *testing.T) {
	// Server replies 200 only on the default inference path.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == defaultInferenceHealthEndpoint {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()

	host, port := splitHostPort(t, srv.URL)
	// Empty path => defaultInferenceHealthEndpoint; expected code defaulted to 200.
	cfg, err := HealthCheckConfig(host, "HTTP" /*uppercase exercises ToLower*/, "", time.Second, 0, port, 0)
	require.NoError(t, err)
	require.NoError(t, cfg.Check(context.Background()))
}

func TestHealthCheckConfigGRPC(t *testing.T) {
	// grpc path builds a check via the native grpc health checker. We do not
	// dial here; constructing the config exercises the grpc branch.
	cfg, err := HealthCheckConfig("127.0.0.1", "grpc", "", 0, 0, 0, 50051)
	require.NoError(t, err)
	require.Equal(t, "inference", cfg.Name)
	require.NotNil(t, cfg.Check)
}

func TestHealthCheckConfigUnknownProtocol(t *testing.T) {
	_, err := HealthCheckConfig("127.0.0.1", "ftp", "", 0, 0, 8080, 0)
	require.Error(t, err)
	require.Contains(t, err.Error(), "unknown health protocol")
}

func TestHealthCheckConfigFallbackPort(t *testing.T) {
	// port <= 0 must fall back to fallbackPort; the resulting check is built
	// against a server that is not running, but config construction succeeds.
	cfg, err := HealthCheckConfig("127.0.0.1", "http", "/x", time.Second, http.StatusOK, 0, 65000)
	require.NoError(t, err)
	require.NotNil(t, cfg.Check)
}
