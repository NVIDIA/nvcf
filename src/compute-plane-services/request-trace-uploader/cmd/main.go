// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// request-trace-uploader validates and discovers Dynamo request-trace segments.
//
// Backends register themselves from an init function, so a build links only
// the backends it imports. This binary links the debug backend, which reports
// what it read and exports nothing. That makes the binary runnable against a
// real Dynamo with no credentials and no destination.
//
// It links no exporting backend. A distribution that needs one imports it in
// its own main and reuses the packages here.
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
)

func main() {
	cfg, warnings, err := config.LoadFromEnv()
	if err != nil {
		slog.Error("invalid request trace uploader configuration", "error", err)
		os.Exit(1)
	}
	for _, warning := range warnings {
		slog.Warn("request trace uploader configuration fallback", "setting", warning)
	}

	slog.Info("request trace uploader starting", "backend", cfg.Backend, "compiled_backends", backend.Registered())

	svc, err := service.New(cfg)
	if err != nil {
		slog.Error("create request trace uploader service", "error", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := svc.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		slog.Error("request trace uploader stopped", "error", err)
		os.Exit(1)
	}
}
