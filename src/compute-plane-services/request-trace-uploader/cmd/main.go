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

	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/backend"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/config"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/service"

	_ "github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/backend/debug"
	_ "github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/backend/objectstore"
)

func main() {
	cfg, warnings, err := loadConfig(os.LookupEnv)
	if err != nil {
		slog.Error("invalid request trace uploader configuration", "error", err)
		os.Exit(1)
	}
	for _, warning := range warnings {
		slog.Warn("request trace uploader configuration fallback", "setting", warning)
	}

	slog.Info("request trace uploader starting", "backend", cfg.Backend, "compiled_backends", backend.Registered())

	client, err := backend.New(cfg)
	if err != nil {
		slog.Error("create request trace uploader backend", "error", err)
		os.Exit(1)
	}
	uploader, err := service.NewUploader(cfg, client, service.Options{})
	if err != nil {
		slog.Error("create request trace uploader", "error", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := uploader.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		slog.Error("request trace uploader stopped", "error", err)
		os.Exit(1)
	}
}

// loadConfig reads configuration through lookup and the env aliases the
// linked packages registered, so the names a deployment already sets work.
func loadConfig(lookup config.LookupFunc) (config.Config, []string, error) {
	aliased, err := config.WithAliases(lookup)
	if err != nil {
		return config.Config{}, nil, err
	}
	return config.Load(aliased)
}
