// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package runner is the uploader's process startup, shared by every binary
// built on this module.
//
// A binary's main imports the backends it links, each of which registers
// itself from an init function, and calls Run. Binaries differ only in those
// imports and in the options they pass, so shutdown, logging, metrics export
// and configuration handling cannot drift between them.
package runner

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/backend"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/config"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/service"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/telemetry"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/telemetry/otlp"
)

// metricsFlushTimeout bounds the final metrics export after the drain.
const metricsFlushTimeout = 5 * time.Second

// Option changes how Run loads configuration.
type Option func(*options)

type options struct {
	defaultBackend config.Backend
	required       []string
}

// WithRequiredSettings makes Run refuse to start when any of names is absent
// from the environment, so a deployment that leaves one out cannot start on
// its default. An empty value counts as set.
func WithRequiredSettings(names ...string) Option {
	return func(o *options) { o.required = append(o.required, names...) }
}

// WithDefaultBackend selects backend when the environment names none or
// leaves it blank. Without it, a backend must be set.
func WithDefaultBackend(backend config.Backend) Option {
	return func(o *options) { o.defaultBackend = backend }
}

// Run loads configuration, starts the service, and blocks until SIGINT or
// SIGTERM. It then drains, sends the last metrics, and returns the process
// exit code, so a main can pass it to os.Exit.
//
// distribution names the build in the start log only. Which backends exist
// is decided by what the calling main linked.
func Run(distribution string, opts ...Option) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return run(ctx, os.LookupEnv, os.Environ(), distribution, collect(opts))
}

func collect(opts []Option) options {
	var o options
	for _, opt := range opts {
		opt(&o)
	}
	return o
}

// run is Run with its environment and shutdown signal passed in. It returns
// instead of exiting, so the final metrics export runs on every path.
//
// Metrics go out over OTLP when the metrics endpoint is set: the exporter is
// wrapped in telemetry.Safe, and after the drain a final export runs for up
// to metricsFlushTimeout.
func run(ctx context.Context, lookup config.LookupFunc, environ []string, distribution string, o options) int {
	cfg, err := loadConfig(lookup, environ, o)
	if err != nil {
		slog.Error("invalid request trace uploader configuration", "error", err)
		return 1
	}
	metricsCfg, exportMetrics, err := otlp.ConfigFromEnv(lookup)
	if err != nil {
		slog.Error("invalid request trace uploader configuration", "error", err)
		return 1
	}

	slog.Info("request trace uploader starting",
		"backend", cfg.Backend,
		"compiled_backends", backend.Registered(),
		"distribution", distribution)

	client, err := backend.New(cfg)
	if err != nil {
		slog.Error("create request trace uploader backend", "error", err)
		return 1
	}
	serviceOptions := service.Options{}
	if exportMetrics {
		exporter, err := otlp.New(context.Background(), metricsCfg, string(cfg.Backend))
		if err != nil {
			slog.Error("create request trace uploader metrics exporter", "error", err)
			return 1
		}
		defer flushMetrics(exporter)
		serviceOptions.Metrics = telemetry.Safe(exporter)
	}
	uploader, err := service.NewUploader(cfg, client, serviceOptions)
	if err != nil {
		slog.Error("create request trace uploader", "error", err)
		return 1
	}

	if err := uploader.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		slog.Error("request trace uploader stopped", "error", err)
		return 1
	}
	return 0
}

// flushMetrics sends what is left after the drain. A failed export is
// logged and never changes the exit code. The exporter's error names only a
// bounded reason, so it is safe to log.
func flushMetrics(exporter *otlp.Exporter) {
	ctx, cancel := context.WithTimeout(context.Background(), metricsFlushTimeout)
	defer cancel()
	if err := exporter.Shutdown(ctx); err != nil {
		slog.Warn("final metrics export failed", "error", err)
	}
}

// loadConfig reads configuration strictly, so an invalid value or an
// unknown uploader setting in environ stops startup, as does a required
// setting of o that is absent. A backend set in the environment wins over
// the default of o.
func loadConfig(lookup config.LookupFunc, environ []string, o options) (config.Config, error) {
	for _, name := range o.required {
		if _, ok := lookup(name); !ok {
			return config.Config{}, fmt.Errorf("%s must be set; an empty value is allowed", name)
		}
	}
	if o.defaultBackend != "" {
		base := lookup
		lookup = func(name string) (string, bool) {
			value, ok := base(name)
			if name == config.EnvBackend && (!ok || strings.TrimSpace(value) == "") {
				return string(o.defaultBackend), true
			}
			return value, ok
		}
	}
	return config.LoadStrict(lookup, environ)
}
