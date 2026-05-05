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

package logging_test

import (
	"log"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/NVIDIA/nvcf/src/control-plane-services/helm-reval/pkg/reval/config"
	"github.com/NVIDIA/nvcf/src/control-plane-services/helm-reval/pkg/telemetry/logging"
)

func minimalConfig(zapCfg, level string) *config.RevalConfig {
	return &config.RevalConfig{
		Logging: config.LoggingConfig{
			ZapConfiguration: zapCfg,
			Level:            level,
		},
		Telemetry: config.TelemetryConfig{
			ServiceName:               "test-svc",
			ServiceVersion:            "0.0.0",
			DeploymentEnvironmentName: "test",
		},
	}
}

// ── InitializeLogger ──────────────────────────────────────────────────────────

func TestInitializeLogger_Production(t *testing.T) {
	logger, level, undo := logging.InitializeLogger(minimalConfig("production", "info"))
	defer undo()
	require.NotNil(t, logger)
	require.NotNil(t, level)
	assert.Equal(t, "info", level.String())
}

func TestInitializeLogger_Development(t *testing.T) {
	logger, level, undo := logging.InitializeLogger(minimalConfig("development", "debug"))
	defer undo()
	require.NotNil(t, logger)
	assert.Equal(t, "debug", level.String())
}

func TestInitializeLogger_InvalidLevel_DefaultsToInfo(t *testing.T) {
	logger, level, undo := logging.InitializeLogger(minimalConfig("production", "notavalidlevel"))
	defer undo()
	require.NotNil(t, logger)
	assert.Equal(t, "info", level.String())
}

func TestInitializeLogger_ReturnsUndoFunc(t *testing.T) {
	_, _, undo := logging.InitializeLogger(minimalConfig("production", "warn"))
	require.NotPanics(t, undo)
}

// ── SetupBootstrapLogger ──────────────────────────────────────────────────────

func TestSetupBootstrapLogger_ReturnsLogger(t *testing.T) {
	logger, undo := logging.SetupBootstrapLogger("v1.2.3")
	defer undo()
	require.NotNil(t, logger)
}

func TestSetupBootstrapLogger_EmptyVersion(t *testing.T) {
	logger, undo := logging.SetupBootstrapLogger("")
	defer undo()
	require.NotNil(t, logger)
}

// ── PanicHandler ─────────────────────────────────────────────────────────────

func TestPanicHandler_RecoversPanic(t *testing.T) {
	logger := zap.NewNop()
	handler := logging.PanicHandler(logger)
	assert.NotPanics(t, func() {
		defer handler()
		panic("test panic")
	})
}

func TestPanicHandler_NoOpWhenNoPanic(t *testing.T) {
	logger := zap.NewNop()
	handler := logging.PanicHandler(logger)
	assert.NotPanics(t, func() {
		defer handler()
	})
}

// ── ZapIoWriter / NewLoggerWithZapWriter ─────────────────────────────────────

func TestNewLoggerWithZapWriter_ReturnsStdlibLogger(t *testing.T) {
	zapLogger := zap.NewNop()
	stdLogger := logging.NewLoggerWithZapWriter(zapLogger)
	require.NotNil(t, stdLogger)
	assert.IsType(t, &log.Logger{}, stdLogger)
}

func TestZapIoWriter_Write_DoesNotError(t *testing.T) {
	// NewLoggerWithZapWriter wraps a ZapIoWriter; stdlib Output → Write.
	// We verify the write does not return an error (Write always returns len(p), nil).
	zapLogger := zap.NewNop()
	stdLogger := logging.NewLoggerWithZapWriter(zapLogger)
	err := stdLogger.Output(2, "hello from stdlib logger")
	assert.NoError(t, err)
}

// ── NewZapLoggerMiddleware ─────────────────────────────────────────────────────

func TestNewZapLoggerMiddleware_NilLogger_Passthrough(t *testing.T) {
	reached := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	})
	mw := logging.NewZapLoggerMiddleware(nil)
	r := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	w := httptest.NewRecorder()
	mw(next).ServeHTTP(w, r)
	assert.True(t, reached)
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestNewZapLoggerMiddleware_WithLogger_CallsNext(t *testing.T) {
	reached := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		w.WriteHeader(http.StatusAccepted)
	})
	logger := zap.NewNop()
	mw := logging.NewZapLoggerMiddleware(logger)
	r := httptest.NewRequest(http.MethodPost, "/v1/validate", nil)
	w := httptest.NewRecorder()
	mw(next).ServeHTTP(w, r)
	assert.True(t, reached)
	assert.Equal(t, http.StatusAccepted, w.Code)
}

func TestNewZapLoggerMiddleware_WithLogger_UserAgentHeader(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	logger := zap.NewNop()
	mw := logging.NewZapLoggerMiddleware(logger)
	r := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	r.Header.Set("User-Agent", "test-client/1.0")
	w := httptest.NewRecorder()
	// Should not panic even with a User-Agent header present.
	assert.NotPanics(t, func() { mw(next).ServeHTTP(w, r) })
}
