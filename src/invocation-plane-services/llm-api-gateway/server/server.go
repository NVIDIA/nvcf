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
	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/lastcluster"
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

	// Start replaces e.Start, which would overwrite this handler.
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

	// context.Background for startup: Echo gives us no hook-scoped ctx, and a
	// startup failure cannot be undone by callers. The telemetry logger is
	// initialised per-call, so ctx only affects tracing and logs.
	ctx := context.Background()
	olric, err := startOlric(ctx, cfg)
	if err != nil {
		return nil, err
	}

	limiter, stopLimiter, err := newRateLimiter(cfg, olric.node)
	if err != nil {
		olric.stop(ctx)
		return nil, err
	}

	tracker, err := newLastClusterTracker(ctx, cfg, olric.node)
	if err != nil {
		stopLimiter()
		olric.stop(ctx)
		return nil, err
	}
	if aware, ok := inferenceProvider.(provider.LastClusterAware); ok && tracker != nil {
		aware.SetLastClusterTracker(tracker)
	}

	if olric.node != nil || tracker != nil {
		// One hook keeps the order fixed: stop the limiter, wait briefly for
		// pending last-cluster writes, then leave the Olric cluster. This is
		// best effort: http.Server.Shutdown does not wait for its hooks, so
		// writes still pending when the process exits are dropped. A lost
		// hint only costs that session one affinity miss.
		e.Server.RegisterOnShutdown(func() {
			stopLimiter()
			if tracker != nil {
				drainCtx, cancel := contextWithOptionalTimeout(cfg.Olric.ShutdownTimeout)
				tracker.Drain(drainCtx)
				cancel()
			}
			olric.stop(context.Background())
		})
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

// olricRuntime is the embedded Olric node shared by the rate limiter and the
// last-cluster store. node is nil when Olric is disabled.
type olricRuntime struct {
	node      *util.OlricNode
	collector *telemetry.OlricCollector
	timeout   time.Duration
}

// startOlric starts the embedded Olric node whenever Olric is enabled, even
// when the rate limiter is off, so other gateway state can use it.
func startOlric(ctx context.Context, cfg *config.Config) (*olricRuntime, error) {
	runtime := &olricRuntime{timeout: cfg.Olric.ShutdownTimeout}
	if !cfg.Olric.Enabled {
		return runtime, nil
	}
	node, err := util.NewOlricNode(ctx, cfg.Olric)
	if err != nil {
		return nil, fmt.Errorf("start olric node: %w", err)
	}
	collector, err := telemetry.NewOlricCollector(node.Client, node.SelfAddr)
	if err != nil {
		util.ShutdownOlricNode(ctx, node, cfg.Olric.ShutdownTimeout)
		return nil, fmt.Errorf("start olric metrics collector: %w", err)
	}
	runtime.node = node
	runtime.collector = collector
	return runtime, nil
}

func (r *olricRuntime) stop(ctx context.Context) {
	if r == nil || r.node == nil {
		return
	}
	r.collector.Stop()
	util.ShutdownOlricNode(ctx, r.node, r.timeout)
}

// newRateLimiter builds the limiter on the shared Olric node. The returned
// stop function is never nil.
func newRateLimiter(cfg *config.Config, node *util.OlricNode) (ratelimit.RateLimiter, func(), error) {
	noop := func() {}
	if !cfg.RateLimiter.Enabled {
		return ratelimit.AllowAll, noop, nil
	}

	if node == nil {
		if cfg.RateLimiter.FailOpen {
			return ratelimit.AllowAll, noop, nil
		}
		return ratelimit.RejectAll, noop, nil
	}

	syncRuntime, err := ratelimitsync.NewPublisherRuntime(cfg)
	if err != nil {
		return nil, nil, err
	}

	limiter, err := ratelimit.NewRateLimiter(
		ratelimit.NewOlricStore(node.DMap),
		ratelimit.WithFailOpen(cfg.RateLimiter.FailOpen),
		ratelimit.WithSynchronizer(syncRuntime.Synchronizer),
	)
	if err != nil {
		syncRuntime.Stop()
		return nil, nil, err
	}

	if err := syncRuntime.Start(); err != nil {
		syncRuntime.Stop()
		return nil, nil, err
	}

	stop := func() {
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
	}
	return limiter, stop, nil
}

// newLastClusterTracker returns nil when last-cluster hints are disabled. It
// uses the shared Olric DMap when Olric is enabled, and an in-process LRU
// otherwise.
func newLastClusterTracker(
	ctx context.Context,
	cfg *config.Config,
	node *util.OlricNode,
) (*lastcluster.Tracker, error) {
	lcCfg := cfg.Stargate.LastCluster
	if !lcCfg.Enabled {
		return nil, nil
	}

	var (
		store     lastcluster.Store
		storeKind string
	)
	if node != nil {
		dm, err := node.Client.NewDMap(lastcluster.DMapName)
		if err != nil {
			return nil, fmt.Errorf("create olric dmap %q: %w", lastcluster.DMapName, err)
		}
		store, storeKind = lastcluster.NewOlricStore(dm), "olric"
	} else {
		store, storeKind = lastcluster.NewLocalStore(lcCfg.LocalMaxEntries), "local"
	}

	telemetry.Logger(ctx).Info().
		Str("store", storeKind).
		Dur("ttl", lcCfg.TTL).
		Dur("lookup_timeout", lcCfg.LookupTimeout).
		Int("local_max_entries", lcCfg.LocalMaxEntries).
		Msg("stargate last-cluster hints enabled")

	return lastcluster.NewTracker(store, lastcluster.Options{
		TTL:           lcCfg.TTL,
		LookupTimeout: lcCfg.LookupTimeout,
	}), nil
}

// contextWithOptionalTimeout returns a context without a deadline when
// timeout <= 0.
func contextWithOptionalTimeout(timeout time.Duration) (context.Context, context.CancelFunc) {
	if timeout <= 0 {
		return context.WithCancel(context.Background())
	}
	return context.WithTimeout(context.Background(), timeout)
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
