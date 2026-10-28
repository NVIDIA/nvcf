// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// request-trace-uploader validates and discovers Dynamo request-trace segments.
//
// Backends register themselves from an init function, so a build links only
// the backends it imports. This binary links debug, which reports what it
// read and exports nothing, and objectstore, a generic S3-compatible export
// backend with no NVIDIA-internal dependencies. That makes the binary
// runnable against a real Dynamo with no credentials and no destination, and
// able to export without any additional wiring.
//
// It links no backend that carries NVIDIA-internal dependencies. A
// distribution that needs one, such as Kratos Bulk Upload, imports it in its
// own main and reuses the packages here.
package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/backend"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/config"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/service"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/telemetry"

	_ "github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/backend/debug"
	_ "github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/backend/objectstore"
)

// metricsFlushTimeout bounds the final metrics export after the drain.
const metricsFlushTimeout = 5 * time.Second

func main() {
	os.Exit(run())
}

// run starts the uploader and returns the process exit code. It returns
// instead of exiting, so the final metrics export runs on every path.
func run() int {
	cfg, _, err := loadConfig(os.LookupEnv, os.Environ()...)
	if err != nil {
		slog.Error("invalid request trace uploader configuration", "error", err)
		return 1
	}
	metricsCfg, exportMetrics, err := telemetry.OTLPConfigFromEnv(os.LookupEnv)
	if err != nil {
		slog.Error("invalid request trace uploader configuration", "error", err)
		return 1
	}

	slog.Info("request trace uploader starting", "backend", cfg.Backend, "compiled_backends", backend.Registered())

	client, err := backend.New(cfg)
	if err != nil {
		slog.Error("create request trace uploader backend", "error", err)
		return 1
	}
	options := service.Options{}
	if exportMetrics {
		exporter, err := telemetry.NewOTLP(context.Background(), metricsCfg, string(cfg.Backend))
		if err != nil {
			slog.Error("create request trace uploader metrics exporter", "error", err)
			return 1
		}
		defer flushMetrics(exporter)
		options.Metrics = telemetry.Safe(exporter)
	}
	uploader, err := service.NewUploader(cfg, client, options)
	if err != nil {
		slog.Error("create request trace uploader", "error", err)
		return 1
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := uploader.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		slog.Error("request trace uploader stopped", "error", err)
		return 1
	}
	return 0
}

// flushMetrics sends what is left after the drain. A failed export is
// logged and never changes the exit code.
func flushMetrics(exporter *telemetry.OTLP) {
	ctx, cancel := context.WithTimeout(context.Background(), metricsFlushTimeout)
	defer cancel()
	if err := exporter.Shutdown(ctx); err != nil {
		slog.Warn("final metrics export failed", "error", err)
	}
}

// loadConfig reads configuration strictly from the uploader's own setting
// names, so any invalid value or unknown setting in environ stops startup.
// Strict loading leaves no warnings to return.
func loadConfig(lookup config.LookupFunc, environ ...string) (config.Config, []string, error) {
	cfg, err := config.LoadStrict(lookup, environ)
	return cfg, nil, err
}
