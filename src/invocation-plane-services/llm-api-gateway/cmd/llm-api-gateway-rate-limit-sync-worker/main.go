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
	"net/http"
	"os"
	"os/signal"
	"syscall"

	echo "github.com/labstack/echo/v4"
	zlog "github.com/rs/zerolog/log"

	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/config"
	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/ratelimitsync"
	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/telemetry"
)

const defaultWorkerServiceName = "llm-api-gateway-rate-limit-sync-worker"

func main() {
	cfg, err := config.LoadFromEnv()
	if err != nil {
		zlog.Fatal().Err(err).Msg("failed to load configuration")
	}
	if os.Getenv("OTEL_SERVICE_NAME") == "" {
		telemetry.SetServiceName(defaultWorkerServiceName)
	} else {
		telemetry.SetServiceName(cfg.Telemetry.ServiceName)
	}

	observability, err := telemetry.Init(context.Background(), telemetry.RuntimeConfig{
		MetricsPort: cfg.Telemetry.MetricsPort,
	})
	if err != nil {
		zlog.Fatal().Err(err).Msg("failed to initialize open telemetry")
	}
	defer func() {
		if shutdownErr := observability.Shutdown(context.Background()); shutdownErr != nil {
			zlog.Error().Err(shutdownErr).Msg("failed to shutdown open telemetry")
		}
	}()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	e := newHealthServer()
	go func() {
		if err := e.Start(cfg.Server.Addr); err != nil && !errors.Is(err, http.ErrServerClosed) {
			zlog.Fatal().Err(err).Msg("rate limit sync worker health server exited unexpectedly")
		}
	}()

	if err := ratelimitsync.RunWorker(ctx, cfg); err != nil && !errors.Is(err, context.Canceled) {
		zlog.Fatal().Err(err).Msg("rate limit sync worker exited unexpectedly")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.Server.ReadTimeout)
	defer cancel()
	if err := e.Shutdown(shutdownCtx); err != nil {
		zlog.Error().Err(err).Msg("failed to shutdown rate limit sync worker health server")
	}
}

func newHealthServer() *echo.Echo {
	e := echo.New()
	e.HideBanner = true
	e.HidePort = true
	e.GET("/healthz", func(c echo.Context) error {
		return c.NoContent(http.StatusOK)
	})
	e.GET("/readyz", func(c echo.Context) error {
		return c.NoContent(http.StatusOK)
	})
	return e
}
