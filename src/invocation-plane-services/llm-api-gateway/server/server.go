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
	"fmt"
	"io"
	"time"

	echo "github.com/labstack/echo/v4"
	echoMiddleware "github.com/labstack/echo/v4/middleware"

	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/api"
	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/config"
	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/provider"
	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/ratelimit"
	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/ratelimitsync"
	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/telemetry"
	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/util"
)

func New(
	cfg *config.Config,
	inferenceProvider provider.InferenceProvider,
	authClient api.InvocationAuthClient,
) (*echo.Echo, error) {
	if cfg != nil && cfg.Telemetry.ServiceName != "" {
		telemetry.SetServiceName(cfg.Telemetry.ServiceName)
	}

	e := echo.New()
	e.HideBanner = true
	e.HidePort = true
	e.Use(echoMiddleware.Recover())
	e.Use(api.NewContextMiddleware(cfg))
	e.Use(api.NewNVCFAuthMiddleware(authClient))

	e.Server.ReadHeaderTimeout = cfg.Server.ReadHeaderTimeout
	e.Server.ReadTimeout = cfg.Server.ReadTimeout
	e.Server.WriteTimeout = cfg.Server.WriteTimeout
	e.Server.IdleTimeout = cfg.Server.IdleTimeout

	if closer, ok := authClient.(io.Closer); ok {
		e.Server.RegisterOnShutdown(func() {
			_ = closer.Close()
		})
	}

	limiter, err := newRateLimiter(cfg, e)
	if err != nil {
		return nil, err
	}

	handlers := api.NewHandlers(
		cfg,
		inferenceProvider,
		limiter,
	)
	api.RegisterRoutes(e, handlers)

	return e, nil
}

func newRateLimiter(cfg *config.Config, e *echo.Echo) (ratelimit.RateLimiter, error) {
	if !cfg.RateLimiter.Enabled {
		return ratelimit.AllowAll, nil
	}

	if !cfg.Olric.Enabled {
		if cfg.RateLimiter.FailOpen {
			return ratelimit.AllowAll, nil
		}
		return ratelimit.RejectAll, nil
	}

	// context.Background for startup: Echo gives us no hook-scoped ctx, and a
	// rate-limiter startup failure cannot be undone by callers. The telemetry
	// logger is initialised per-call, so ctx only affects tracing and logs.
	ctx := context.Background()
	node, err := util.NewOlricNode(ctx, cfg.Olric)
	if err != nil {
		return nil, fmt.Errorf("start olric node: %w", err)
	}

	olricCollector, err := telemetry.NewOlricCollector(node.Client, node.SelfAddr)
	if err != nil {
		util.ShutdownOlricNode(ctx, node, cfg.Olric.ShutdownTimeout)
		return nil, fmt.Errorf("start olric metrics collector: %w", err)
	}

	syncRuntime, err := ratelimitsync.NewPublisherRuntime(cfg)
	if err != nil {
		olricCollector.Stop()
		util.ShutdownOlricNode(ctx, node, cfg.Olric.ShutdownTimeout)
		return nil, err
	}

	limiter, err := ratelimit.NewRateLimiter(
		ratelimit.NewOlricStore(node.DMap),
		ratelimit.WithFailOpen(cfg.RateLimiter.FailOpen),
		ratelimit.WithSynchronizer(syncRuntime.Synchronizer),
	)
	if err != nil {
		olricCollector.Stop()
		syncRuntime.Stop()
		util.ShutdownOlricNode(ctx, node, cfg.Olric.ShutdownTimeout)
		return nil, err
	}

	if err := syncRuntime.Start(); err != nil {
		olricCollector.Stop()
		syncRuntime.Stop()
		util.ShutdownOlricNode(ctx, node, cfg.Olric.ShutdownTimeout)
		return nil, err
	}

	e.Server.RegisterOnShutdown(func() {
		olricCollector.Stop()

		// The sync synchronizer's Stop() blocks on the publisher loop draining
		// its in-flight publish goroutines; bound it so a stuck remote cannot
		// delay the gateway's shutdown indefinitely. We reuse the Olric
		// shutdown timeout as a single "infra goodbye budget" knob.
		stopped := make(chan struct{})
		go func() {
			defer close(stopped)
			syncRuntime.Stop()
		}()
		select {
		case <-stopped:
		case <-timeAfterOrForever(cfg.Olric.ShutdownTimeout):
			telemetry.Logger(context.Background()).
				Warn().
				Dur("timeout", cfg.Olric.ShutdownTimeout).
				Msg("rate limit sync runtime did not stop within shutdown timeout")
		}
		util.ShutdownOlricNode(context.Background(), node, cfg.Olric.ShutdownTimeout)
	})

	return limiter, nil
}

// timeAfterOrForever returns a channel that never fires when timeout <= 0,
// and a time.After channel otherwise. It lets the shutdown hook stay in a
// single select regardless of whether the user configured a timeout.
func timeAfterOrForever(timeout time.Duration) <-chan time.Time {
	if timeout <= 0 {
		return nil
	}
	return time.After(timeout)
}
