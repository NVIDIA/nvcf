// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Internal tests for unexported helpers in the cli package.
package cli

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/NVIDIA/nvcf/src/control-plane-services/helm-reval/pkg/authorizers"
	"github.com/NVIDIA/nvcf/src/control-plane-services/helm-reval/pkg/reval/config"
)

// ── getAddr ───────────────────────────────────────────────────────────────────

func TestGetAddr_LocalTrue(t *testing.T) {
	addr := getAddr(8080, true)
	assert.Equal(t, "127.0.0.1:8080", addr)
}

func TestGetAddr_LocalFalse(t *testing.T) {
	addr := getAddr(8080, false)
	assert.Equal(t, ":8080", addr)
}

func TestGetAddr_ZeroPort(t *testing.T) {
	addr := getAddr(0, false)
	assert.Equal(t, ":0", addr)
}

func TestGetAddr_HighPort(t *testing.T) {
	addr := getAddr(65535, true)
	assert.Equal(t, "127.0.0.1:65535", addr)
}

// ── serveManagementRoutes ─────────────────────────────────────────────────────

func TestServeManagementRoutes_Healthz(t *testing.T) {
	logger := zap.NewNop()
	atomicLevel := zap.NewAtomicLevel()
	cfg := config.HTTPConfig{
		ManagementPort: 0,
		Local:          true,
	}

	server := serveManagementRoutes(logger, &atomicLevel, cfg)
	require.NotNil(t, server)
	require.NotNil(t, server.Handler)

	// Test the /healthz route directly through the handler.
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	server.Handler.ServeHTTP(w, r)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "OK")
	assert.Equal(t, "text/plain", w.Header().Get("Content-Type"))
}

func TestServeManagementRoutes_LogLevelEndpoint(t *testing.T) {
	logger := zap.NewNop()
	atomicLevel := zap.NewAtomicLevel()
	cfg := config.HTTPConfig{ManagementPort: 0, Local: false}

	server := serveManagementRoutes(logger, &atomicLevel, cfg)
	require.NotNil(t, server)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/log_level", nil)
	server.Handler.ServeHTTP(w, r)
	// The log level endpoint returns 200 with the current level.
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestServeManagementRoutes_UnknownRoute(t *testing.T) {
	logger := zap.NewNop()
	atomicLevel := zap.NewAtomicLevel()
	cfg := config.HTTPConfig{ManagementPort: 0, Local: false}

	server := serveManagementRoutes(logger, &atomicLevel, cfg)
	require.NotNil(t, server)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/unknown", nil)
	server.Handler.ServeHTTP(w, r)
	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestServeManagementRoutes_ServerAddr_Local(t *testing.T) {
	logger := zap.NewNop()
	atomicLevel := zap.NewAtomicLevel()
	cfg := config.HTTPConfig{ManagementPort: 9999, Local: true}

	server := serveManagementRoutes(logger, &atomicLevel, cfg)
	require.NotNil(t, server)
	assert.Equal(t, fmt.Sprintf("127.0.0.1:%d", cfg.ManagementPort), server.Addr)
}

// ── gracefulHandle ────────────────────────────────────────────────────────────

func TestGracefulHandle_ShutdownOnContextCancel(t *testing.T) {
	logger := zap.NewNop()

	server := &http.Server{
		Addr:    "127.0.0.1:0",
		Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }),
	}

	ctx, cancel := context.WithCancel(context.Background())

	// ready is closed once the goroutine has entered gracefulHandle and is
	// blocking on <-ctx.Done(), ensuring cancel() races with nothing.
	ready := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		// Signal that we are about to block; closing before the call means
		// ctx.Done() is guaranteed to be reached (a cancelled ctx unblocks it
		// immediately even if cancel races ahead).
		close(ready)
		gracefulHandle(ctx, logger, server)
	}()

	<-ready
	cancel() // deterministic shutdown — no signals, no sleeps

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("gracefulHandle did not complete within 5 seconds")
	}
}

// ── runServer ─────────────────────────────────────────────────────────────────

func TestRunServer_FactoryReturnsError(t *testing.T) {
	expectedErr := errors.New("auth factory error")
	factory := AuthorizerFactory(func(_ context.Context, _ *viper.Viper, _ *config.RevalConfig, _ *zap.Logger) ([]authorizers.Authorizer, error) {
		return nil, expectedErr
	})

	cfg := &config.RevalConfig{
		HTTP:    config.HTTPConfig{Local: true, ApiPort: 0, MetricsPort: 0, ManagementPort: 0},
		Logging: config.LoggingConfig{ZapConfiguration: "production", Level: "info"},
	}
	err := runServer(cfg, viper.New(), factory)
	require.Error(t, err)
	assert.Equal(t, expectedErr, err)
}
