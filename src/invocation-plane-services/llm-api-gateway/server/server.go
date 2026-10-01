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
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"time"

	echo "github.com/labstack/echo/v4"
	echoMiddleware "github.com/labstack/echo/v4/middleware"

	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/api"
	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/config"
	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/internal/tlsreload"
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

	authMiddleware, err := newAuthMiddleware(cfg, authClient)
	if err != nil {
		return nil, err
	}

	e := echo.New()
	e.HideBanner = true
	e.HidePort = true
	e.Use(echoMiddleware.Recover())
	e.Use(api.NewContextMiddleware(cfg))
	e.Use(authMiddleware)

	// Start and StartTLS replace e.Start and e.StartTLS, which would overwrite
	// this handler. Both serve on e.Server, so TLS gets the same handler and
	// limits.
	e.Server.Handler = api.WithFinalWriteDeadline(e, cfg.Server.InferenceWriteTimeout)
	e.Server.ErrorLog = e.StdLogger
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

// Start serves e on addr until the server is shut down. Use it instead of
// e.Start, which resets e.Server.Handler to e and drops the handler New
// installs around it.
func Start(e *echo.Echo, addr string) error {
	e.Server.Addr = addr
	return e.Server.ListenAndServe()
}

// StartTLS serves e over TLS on addr until the server is shut down. Use it
// instead of e.StartTLS, which serves from e.TLSServer with the handler reset
// to e, so the handler and limits New sets on e.Server would not apply.
//
// The pair in certFile and keyFile is loaded before listening; a pair that
// does not load is returned as an error. While serving, the files are checked
// every reloadInterval (non-positive means tlsreload.DefaultInterval) and a
// renewed pair is served to new connections without a restart. A pair that
// fails to load is rejected and the last good one stays in service. The
// checks stop when serving ends, including on e.Shutdown.
func StartTLS(e *echo.Echo, addr, certFile, keyFile string, reloadInterval time.Duration) error {
	certs, err := tlsreload.New(certFile, keyFile, tlsreload.WithInterval(reloadInterval))
	if err != nil {
		return err
	}

	ctx, cancel := context.WithCancel(context.Background())
	reloaderDone := make(chan struct{})
	go func() {
		defer close(reloaderDone)
		certs.Run(ctx)
	}()
	defer func() {
		cancel()
		<-reloaderDone
	}()

	e.Server.Addr = addr
	e.Server.TLSConfig = newTLSConfig(certs)
	return e.Server.ListenAndServeTLS("", "")
}

// newTLSConfig serves the loader's current pair on every handshake.
func newTLSConfig(certs *tlsreload.Loader) *tls.Config {
	return &tls.Config{
		GetCertificate: certs.GetCertificate,
		MinVersion:     tls.VersionTLS12,
		NextProtos:     []string{"h2", "http/1.1"},
	}
}

// newAuthMiddleware picks the caller authentication for the configured mode.
// Static key mode refuses to start without an authorizer; NVCF mode keeps
// its historical pass-through for a nil client, which the gateway binary only
// allows when ALLOW_ANONYMOUS is set. PUBLIC_READ_ENDPOINTS decides whether
// the discovery reads bypass either middleware.
func newAuthMiddleware(
	cfg *config.Config,
	authClient api.InvocationAuthClient,
) (echo.MiddlewareFunc, error) {
	publicReads := api.WithPublicReadEndpoints(cfg.PublicReadEndpointsEnabled())
	if !cfg.StaticAuthMode() {
		return api.NewNVCFAuthMiddleware(authClient, publicReads), nil
	}
	if authClient == nil {
		return nil, errors.New("static API key mode requires an authorizer")
	}
	return api.NewStaticKeyAuthMiddleware(authClient, cfg.Auth.StaticAllowedPaths, publicReads), nil
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
