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

package middleware

import (
	"context"
	"net/http"
	"net/http/httptrace"
	"sync"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/httptrace/otelhttptrace"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.uber.org/zap"

	"github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/internal/config"
)

var (
	// Shared HTTP client for JWKS fetching to avoid connection pool exhaustion
	sharedHTTPClient *http.Client
	clientOnce       sync.Once
)

// GetSharedHTTPClient returns a shared HTTP client configured for JWKS fetching
func GetSharedHTTPClient(cfg *config.HTTPClientConfig) *http.Client {
	clientOnce.Do(func() {
		// Use default config if none provided
		if cfg == nil {
			defaultCfg := config.DefaultHTTPConfig()
			cfg = &defaultCfg
		}

		// Log the HTTP configuration being used
		zap.L().Info("creating shared jwks http client with configuration",
			zap.Int("max_idle_conns", cfg.MaxIdleConns),
			zap.Int("max_idle_conns_per_host", cfg.MaxIdleConnsPerHost),
			zap.Int("idle_conn_timeout_sec", cfg.IdleConnTimeoutSec),
			zap.Int("tls_handshake_timeout_sec", cfg.TLSHandshakeTimeoutSec),
			zap.Int("expect_continue_timeout_sec", cfg.ExpectContinueTimeoutSec),
		)

		// Create a custom transport with configured connection pool settings
		transport := cfg.NewHTTPTransport()

		sharedHTTPClient = &http.Client{
			Transport: otelhttp.NewTransport(
				transport,
				// https://github.com/open-telemetry/opentelemetry-go-contrib/issues/399
				// Nests spans properly within otelhttp.NewTransport
				otelhttp.WithClientTrace(func(ctx context.Context) *httptrace.ClientTrace {
					return otelhttptrace.NewClientTrace(ctx)
				}),
				otelhttp.WithSpanNameFormatter(func(operation string, r *http.Request) string {
					return "shared http client"
				}),
			),
			Timeout: 10 * time.Second,
		}
	})
	return sharedHTTPClient
}
